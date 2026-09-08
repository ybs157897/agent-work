// Package fsjail resolves and reads paths under a single Workspace Root.
// Algorithm matches docs/architecture/workspace-fs.md.
package fsjail

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalidPath = errors.New("invalid path")
	ErrForbidden   = errors.New("path outside workspace root")
	ErrNotFound    = errors.New("not found")
	ErrNotFile     = errors.New("not a file")
	ErrNotDir      = errors.New("not a directory")
	ErrTooLarge    = errors.New("file too large")
	ErrNotText     = errors.New("not a text preview")
)

const (
	DefaultMaxFileBytes = 2 << 20 // 2 MiB
	DefaultMaxEntries   = 2000
	MaxDepth            = 3
)

// Jail binds reads to an absolute workspace root.
type Jail struct {
	Root         string
	MaxFileBytes int64
	MaxEntries   int
}

func New(root string) (*Jail, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%w: root is not a directory", ErrInvalidPath)
	}
	// Resolve root symlinks so jail boundary is the real directory.
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	return &Jail{
		Root:         resolved,
		MaxFileBytes: DefaultMaxFileBytes,
		MaxEntries:   DefaultMaxEntries,
	}, nil
}

func (j *Jail) rootWithSep() string {
	return j.Root + string(os.PathSeparator)
}

func (j *Jail) inside(abs string) bool {
	return abs == j.Root || strings.HasPrefix(abs, j.rootWithSep())
}

// Resolve maps a client-relative POSIX path to an absolute path inside the jail.
// Empty path means the root itself. Symlinks are evaluated; targets outside root are forbidden.
func (j *Jail) Resolve(rel string) (string, error) {
	if err := validateRelative(rel); err != nil {
		return "", err
	}
	clean := path.Clean("/" + rel)
	trimmed := strings.TrimPrefix(clean, "/")
	joined := filepath.Join(j.Root, filepath.FromSlash(trimmed))
	abs, err := filepath.Abs(joined)
	if err != nil {
		return "", err
	}
	if !j.inside(abs) {
		return "", ErrForbidden
	}

	// Walk components with Lstat so a symlink escape is caught before open.
	cur := j.Root
	if trimmed != "" {
		for _, seg := range strings.Split(trimmed, "/") {
			next := filepath.Join(cur, seg)
			fi, err := os.Lstat(next)
			if err != nil {
				if os.IsNotExist(err) {
					return "", ErrNotFound
				}
				return "", err
			}
			if fi.Mode()&os.ModeSymlink != 0 {
				target, err := filepath.EvalSymlinks(next)
				if err != nil {
					if os.IsNotExist(err) {
						return "", ErrNotFound
					}
					return "", err
				}
				if !j.inside(target) {
					return "", ErrForbidden
				}
				cur = target
				continue
			}
			cur = next
		}
	}

	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", err
	}
	if !j.inside(resolved) {
		return "", ErrForbidden
	}
	return resolved, nil
}

func validateRelative(rel string) error {
	if strings.ContainsRune(rel, 0) {
		return ErrInvalidPath
	}
	if strings.Contains(rel, `\`) {
		return ErrInvalidPath
	}
	if strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "~") {
		return ErrInvalidPath
	}
	if len(rel) >= 2 && ((rel[0] >= 'A' && rel[0] <= 'Z') || (rel[0] >= 'a' && rel[0] <= 'z')) && rel[1] == ':' {
		return ErrInvalidPath
	}
	if rel == "" {
		return nil
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return ErrInvalidPath
		}
	}
	return nil
}

// DirEntry is one tree listing row.
type DirEntry struct {
	Name       string     `json:"name"`
	Type       string     `json:"type"` // file | dir
	Size       *int64     `json:"size,omitempty"`
	ModifiedAt *time.Time `json:"modified_at,omitempty"`
}

// TreeResponse is the fs/tree payload.
type TreeResponse struct {
	Path      string     `json:"path"`
	Truncated bool       `json:"truncated,omitempty"`
	Entries   []DirEntry `json:"entries"`
}

// Stat is the fs/stat payload.
type Stat struct {
	Path       string     `json:"path"`
	Type       string     `json:"type"`
	Size       *int64     `json:"size,omitempty"`
	ModifiedAt *time.Time `json:"modified_at,omitempty"`
}

// Tree lists directory entries under rel with depth 1..MaxDepth.
func (j *Jail) Tree(rel string, depth int) (*TreeResponse, error) {
	if depth < 1 {
		depth = 1
	}
	if depth > MaxDepth {
		depth = MaxDepth
	}
	abs, err := j.Resolve(rel)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if !fi.IsDir() {
		return nil, ErrNotDir
	}

	max := j.MaxEntries
	if max <= 0 {
		max = DefaultMaxEntries
	}
	out := &TreeResponse{Path: rel, Entries: make([]DirEntry, 0)}
	var count int
	var walk func(dirAbs, dirRel string, remaining int) error
	walk = func(dirAbs, dirRel string, remaining int) error {
		entries, err := os.ReadDir(dirAbs)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if count >= max {
				out.Truncated = true
				return nil
			}
			name := e.Name()
			childRel := name
			if dirRel != "" {
				childRel = dirRel + "/" + name
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			typ := "file"
			if e.IsDir() {
				typ = "dir"
			} else if info.Mode()&os.ModeSymlink != 0 {
				target, err := filepath.EvalSymlinks(filepath.Join(dirAbs, name))
				if err != nil || !j.inside(target) {
					continue
				}
				sti, err := os.Stat(target)
				if err != nil {
					continue
				}
				if sti.IsDir() {
					typ = "dir"
				}
			}
			displayName := name
			if dirRel != "" {
				displayName = childRel
			}
			de := DirEntry{Name: displayName, Type: typ}
			if typ == "file" {
				sz := info.Size()
				de.Size = &sz
			}
			mt := info.ModTime().UTC()
			de.ModifiedAt = &mt
			out.Entries = append(out.Entries, de)
			count++
			if typ == "dir" && remaining > 1 && !out.Truncated && name != "node_modules" && name != ".git" {
				childAbs := filepath.Join(dirAbs, name)
				if err := walk(childAbs, childRel, remaining-1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(abs, "", depth); err != nil {
		return nil, err
	}
	return out, nil
}

// StatPath returns metadata for a relative path.
func (j *Jail) StatPath(rel string) (*Stat, error) {
	abs, err := j.Resolve(rel)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	st := &Stat{Path: rel}
	mt := fi.ModTime().UTC()
	st.ModifiedAt = &mt
	if fi.IsDir() {
		st.Type = "dir"
	} else {
		st.Type = "file"
		sz := fi.Size()
		st.Size = &sz
	}
	return st, nil
}

// ReadFile returns UTF-8 text file contents under the jail.
func (j *Jail) ReadFile(rel string) ([]byte, error) {
	abs, err := j.Resolve(rel)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if fi.IsDir() {
		return nil, ErrNotFile
	}
	max := j.MaxFileBytes
	if max <= 0 {
		max = DefaultMaxFileBytes
	}
	if fi.Size() > max {
		return nil, ErrTooLarge
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	limited := io.LimitReader(f, max+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, ErrTooLarge
	}
	if !isText(data) {
		return nil, ErrNotText
	}
	return data, nil
}

// WriteFile writes UTF-8 text under the jail. Parent directory must already exist.
// Does not create intermediate directories (avoids accidental tree expansion).
func (j *Jail) WriteFile(rel string, data []byte) error {
	abs, err := j.resolveWriteTarget(rel)
	if err != nil {
		return err
	}
	max := j.MaxFileBytes
	if max <= 0 {
		max = DefaultMaxFileBytes
	}
	if int64(len(data)) > max {
		return ErrTooLarge
	}
	if !isText(data) {
		return ErrNotText
	}
	parent := filepath.Dir(abs)
	tmp, err := os.CreateTemp(parent, ".webidea-write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, abs)
}

// resolveWriteTarget maps rel to an absolute path for writing.
// All parent segments must exist and stay inside the jail; the leaf may be new.
func (j *Jail) resolveWriteTarget(rel string) (string, error) {
	if err := validateRelative(rel); err != nil {
		return "", err
	}
	if rel == "" {
		return "", ErrNotFile
	}
	clean := path.Clean("/" + rel)
	trimmed := strings.TrimPrefix(clean, "/")
	segs := strings.Split(trimmed, "/")
	cur := j.Root
	for i, seg := range segs {
		next := filepath.Join(cur, seg)
		isLast := i == len(segs)-1
		fi, err := os.Lstat(next)
		if err != nil {
			if os.IsNotExist(err) {
				if !isLast {
					return "", ErrNotFound
				}
				// New file: ensure join stays inside jail without following a missing leaf.
				abs, err := filepath.Abs(next)
				if err != nil {
					return "", err
				}
				if !j.inside(abs) {
					return "", ErrForbidden
				}
				return abs, nil
			}
			return "", err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(next)
			if err != nil {
				return "", err
			}
			if !j.inside(target) {
				return "", ErrForbidden
			}
			if isLast {
				// Refuse writing through a leaf symlink.
				return "", ErrForbidden
			}
			cur = target
			continue
		}
		if isLast {
			if fi.IsDir() {
				return "", ErrNotFile
			}
			abs, err := filepath.Abs(next)
			if err != nil {
				return "", err
			}
			if !j.inside(abs) {
				return "", ErrForbidden
			}
			return abs, nil
		}
		if !fi.IsDir() {
			return "", ErrNotDir
		}
		cur = next
	}
	return "", ErrNotFile
}

func isText(data []byte) bool {
	if strings.ContainsRune(string(data), 0) {
		return false
	}
	return utf8.Valid(data)
}

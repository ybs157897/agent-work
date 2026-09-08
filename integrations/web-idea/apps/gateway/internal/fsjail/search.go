package fsjail

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	DefaultMaxSearchHits    = 200
	DefaultMaxSearchFiles   = 5000
	DefaultMaxSearchBytes   = 512 << 10 // per-file scan cap
	DefaultMaxLinePreview   = 240
	DefaultMaxFilePaths     = 60
	MaxFilePaths            = 200
	DefaultMaxSearchEntries = 10000
)

// SearchHit is one Find-in-Files match.
type SearchHit struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Preview string `json:"preview"`
}

// SearchResponse is the fs/search payload.
type SearchResponse struct {
	Query     string      `json:"query"`
	Hits      []SearchHit `json:"hits"`
	Truncated bool        `json:"truncated,omitempty"`
}

// FilesResponse is the fs/files payload. Paths are relative to the workspace
// root and are limited to regular files.
type FilesResponse struct {
	Query     string   `json:"query"`
	Paths     []string `json:"paths"`
	Truncated bool     `json:"truncated,omitempty"`
}

var skipSearchDirs = map[string]struct{}{
	".git":         {},
	"node_modules": {},
	"target":       {},
	"build":        {},
	".idea":        {},
	".gradle":      {},
	"out":          {},
	"dist":         {},
	"__pycache__":  {},
}

// Search walks the jail for case-insensitive substring matches in text files.
func (j *Jail) Search(query string, maxHits int) (*SearchResponse, error) {
	return j.SearchContext(context.Background(), query, maxHits)
}

// SearchContext walks the jail for case-insensitive substring matches in text
// files. Cancellation stops the walk before another file is opened.
func (j *Jail) SearchContext(ctx context.Context, query string, maxHits int) (*SearchResponse, error) {
	q := strings.TrimSpace(query)
	out := &SearchResponse{Query: q, Hits: make([]SearchHit, 0)}
	if q == "" {
		return out, nil
	}
	if maxHits <= 0 {
		maxHits = DefaultMaxSearchHits
	}
	needle := strings.ToLower(q)
	var filesScanned int
	err := filepath.WalkDir(j.Root, func(abs string, d os.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if _, skip := skipSearchDirs[name]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		if filesScanned >= DefaultMaxSearchFiles {
			out.Truncated = true
			return filepath.SkipAll
		}
		filesScanned++
		if !j.inside(abs) {
			return nil
		}
		rel, err := filepath.Rel(j.Root, abs)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if err := validateRelative(rel); err != nil {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			return nil
		}
		maxBytes := j.MaxFileBytes
		if maxBytes <= 0 {
			maxBytes = DefaultMaxFileBytes
		}
		scanCap := int64(DefaultMaxSearchBytes)
		if scanCap > maxBytes {
			scanCap = maxBytes
		}
		if fi.Size() > maxBytes {
			return nil
		}
		f, err := os.Open(abs)
		if err != nil {
			return nil
		}
		defer f.Close()

		limited := bufio.NewReader(f)
		var read int64
		lineNo := 0
		for {
			if ctxErr := ctx.Err(); ctxErr != nil {
				_ = f.Close()
				return ctxErr
			}
			if out.Truncated || len(out.Hits) >= maxHits {
				out.Truncated = true
				return filepath.SkipAll
			}
			line, err := limited.ReadBytes('\n')
			if len(line) > 0 {
				read += int64(len(line))
				lineNo++
				if read > scanCap {
					break
				}
				// Drop trailing newline for match/preview
				raw := line
				if n := len(raw); n > 0 && raw[n-1] == '\n' {
					raw = raw[:n-1]
					if n = len(raw); n > 0 && raw[n-1] == '\r' {
						raw = raw[:n-1]
					}
				}
				if bytes.IndexByte(raw, 0) >= 0 || !utf8.Valid(raw) {
					break // binary-ish file
				}
				lower := strings.ToLower(string(raw))
				idx := strings.Index(lower, needle)
				if idx >= 0 {
					preview := string(raw)
					if len(preview) > DefaultMaxLinePreview {
						preview = preview[:DefaultMaxLinePreview] + "…"
					}
					out.Hits = append(out.Hits, SearchHit{
						Path:    path.Clean(rel),
						Line:    lineNo,
						Column:  utf8.RuneCountInString(string(raw[:idx])) + 1,
						Preview: strings.TrimSpace(preview),
					})
				}
			}
			if err != nil {
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

type filePathMatch struct {
	path  string
	score int
}

// SearchFiles finds regular file paths by case-insensitive basename/path
// substring or subsequence matching. It never reads file contents or follows
// symlinks. The walk and result set are both bounded.
func (j *Jail) SearchFiles(ctx context.Context, query string, maxPaths int) (*FilesResponse, error) {
	q := strings.TrimSpace(query)
	out := &FilesResponse{Query: q, Paths: make([]string, 0)}
	if q == "" {
		return out, nil
	}
	if maxPaths <= 0 {
		maxPaths = DefaultMaxFilePaths
	}
	if maxPaths > MaxFilePaths {
		maxPaths = MaxFilePaths
	}

	var matches []filePathMatch
	matchesSeen := 0
	filesScanned := 0
	entriesVisited := 0
	err := filepath.WalkDir(j.Root, func(abs string, d os.DirEntry, walkErr error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if walkErr != nil {
			return nil
		}
		entriesVisited++
		if entriesVisited > DefaultMaxSearchEntries {
			out.Truncated = true
			return filepath.SkipAll
		}
		// WalkDir does not follow directory symlinks, but it still visits the
		// symlink entry. Skip both directory and file links explicitly so a
		// link cannot expose an outside name or target.
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if _, skip := skipSearchDirs[name]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		if filesScanned >= DefaultMaxSearchFiles {
			out.Truncated = true
			return filepath.SkipAll
		}
		filesScanned++
		info, err := d.Info()
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(j.Root, abs)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if err := validateRelative(rel); err != nil {
			return nil
		}
		if score, ok := filePathScore(rel, q); ok {
			matchesSeen++
			if matchesSeen > maxPaths {
				out.Truncated = true
			}
			candidate := filePathMatch{path: path.Clean(rel), score: score}
			if len(matches) < maxPaths {
				matches = append(matches, candidate)
			} else if filePathMatchLess(candidate, matches[len(matches)-1]) {
				matches[len(matches)-1] = candidate
			}
			if len(matches) > 1 {
				sort.Slice(matches, func(i, k int) bool { return filePathMatchLess(matches[i], matches[k]) })
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, match := range matches {
		out.Paths = append(out.Paths, match.path)
	}
	return out, nil
}

func filePathMatchLess(a, b filePathMatch) bool {
	if a.score != b.score {
		return a.score < b.score
	}
	return strings.ToLower(a.path) < strings.ToLower(b.path) ||
		(strings.EqualFold(a.path, b.path) && a.path < b.path)
}

func filePathScore(rel, query string) (int, bool) {
	lowerPath := strings.ToLower(rel)
	lowerBase := strings.ToLower(path.Base(rel))
	lowerQuery := strings.ToLower(query)
	if lowerBase == lowerQuery {
		return 0, true
	}
	if idx := strings.Index(lowerBase, lowerQuery); idx >= 0 {
		return 10 + idx, true
	}
	if idx := strings.Index(lowerPath, lowerQuery); idx >= 0 {
		return 30 + idx, true
	}
	if ok, gap := runeSubsequence(lowerBase, lowerQuery); ok {
		return 50 + gap, true
	}
	if ok, gap := runeSubsequence(lowerPath, lowerQuery); ok {
		return 80 + gap, true
	}
	return 0, false
}

// runeSubsequence reports whether query's runes occur in order in text. The
// gap score makes tighter matches sort before widely separated matches.
func runeSubsequence(text, query string) (bool, int) {
	textRunes := []rune(text)
	queryRunes := []rune(query)
	if len(queryRunes) == 0 {
		return true, 0
	}
	qi := 0
	first := -1
	last := -1
	for i, r := range textRunes {
		if r != queryRunes[qi] {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
		qi++
		if qi == len(queryRunes) {
			return true, (last - first + 1) - len(queryRunes)
		}
	}
	return false, 0
}

// Package chatsources stores original Chat attachments without interpreting
// their contents. Source ownership and Run admission remain in the application.
package chatsources

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const MaxFileBytes = 10 << 20

var (
	ErrUnsafe        = errors.New("invalid attachment storage scope")
	ErrTooLarge      = errors.New("attachment exceeds 10 MiB")
	ErrEmpty         = errors.New("attachment is empty")
	ErrUnavailable   = errors.New("attachment original is unavailable")
	ErrChanged       = errors.New("attachment original differs from its recorded digest")
	ErrConflict      = errors.New("attachment identity already contains different original data")
	componentPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)
	extensionPattern = regexp.MustCompile(`^\.[A-Za-z0-9]{1,12}$`)
	leafPattern      = regexp.MustCompile(`^content\.[a-z0-9]{1,12}$`)
)

type Blob struct {
	Key    string
	SHA256 string
	Size   int64
}

type Location struct {
	WorkspaceID string
	ChatID      string
	SourceID    string
	Key         string
	SHA256      string
}

type Store struct {
	root string
	mu   sync.Mutex
}

func NewStore(root string) *Store { return &Store{root: root} }

func validScope(workspaceID, chatID, sourceID string) bool {
	return componentPattern.MatchString(workspaceID) && componentPattern.MatchString(chatID) && componentPattern.MatchString(sourceID)
}

func validateLocation(loc Location) error {
	if !validScope(loc.WorkspaceID, loc.ChatID, loc.SourceID) {
		return ErrUnsafe
	}
	parts := strings.Split(loc.Key, "/")
	if len(parts) != 4 || parts[0] != loc.WorkspaceID || parts[1] != loc.ChatID || parts[2] != loc.SourceID || !leafPattern.MatchString(parts[3]) {
		return ErrUnsafe
	}
	digest, err := hex.DecodeString(loc.SHA256)
	if err != nil || len(digest) != sha256.Size {
		return ErrUnsafe
	}
	return nil
}

func (s *Store) openRoot(create bool) (*os.Root, string, error) {
	if strings.TrimSpace(s.root) == "" {
		return nil, "", ErrUnavailable
	}
	if create {
		if err := os.MkdirAll(s.root, 0o700); err != nil {
			return nil, "", ErrUnavailable
		}
	}
	info, err := os.Lstat(s.root)
	if err != nil {
		return nil, "", ErrUnavailable
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, "", ErrUnsafe
	}
	root, err := filepath.EvalSymlinks(s.root)
	if err != nil {
		return nil, "", ErrUnavailable
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, "", ErrUnavailable
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, "", ErrUnavailable
	}
	return r, root, nil
}

func checkComponents(root *os.Root, key string, allowMissing bool) error {
	parts := strings.Split(key, "/")
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if errors.Is(err, os.ErrNotExist) && allowMissing {
			return nil
		}
		if err != nil {
			return ErrUnavailable
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrUnsafe
		}
		if i < len(parts)-1 && !info.IsDir() {
			return ErrUnsafe
		}
	}
	return nil
}

func syncDirectories(root *os.Root, directory string) error {
	parts := strings.Split(directory, "/")
	for n := len(parts); n >= 0; n-- {
		path := "."
		if n > 0 {
			path = strings.Join(parts[:n], "/")
		}
		dir, err := root.Open(path)
		if err != nil {
			return ErrUnavailable
		}
		err = dir.Sync()
		closeErr := dir.Close()
		if err != nil || closeErr != nil {
			return ErrUnavailable
		}
	}
	return nil
}

// Put atomically makes a complete original visible. A hard link publishes
// the temporary inode without replacing another upload's completed file.
func (s *Store) Put(ctx context.Context, workspaceID, chatID, sourceID, filename string, data []byte) (Blob, error) {
	if err := ctx.Err(); err != nil {
		return Blob{}, err
	}
	if !validScope(workspaceID, chatID, sourceID) {
		return Blob{}, ErrUnsafe
	}
	if len(data) == 0 {
		return Blob{}, ErrEmpty
	}
	if len(data) > MaxFileBytes {
		return Blob{}, ErrTooLarge
	}
	extension := strings.ToLower(filepath.Ext(filename))
	if !extensionPattern.MatchString(extension) {
		extension = ".bin"
	}
	directory := strings.Join([]string{workspaceID, chatID, sourceID}, "/")
	key := directory + "/content" + extension
	sum := sha256.Sum256(data)
	blob := Blob{Key: key, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
	loc := Location{WorkspaceID: workspaceID, ChatID: chatID, SourceID: sourceID, Key: key, SHA256: blob.SHA256}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Blob{}, err
	}
	root, _, err := s.openRoot(true)
	if err != nil {
		return Blob{}, err
	}
	defer root.Close()
	if err := checkComponents(root, key, true); err != nil {
		return Blob{}, err
	}
	if err := root.MkdirAll(directory, 0o700); err != nil {
		return Blob{}, ErrUnavailable
	}
	if err := checkComponents(root, key, true); err != nil {
		return Blob{}, err
	}
	dir, err := root.Open(directory)
	if err != nil {
		return Blob{}, ErrUnavailable
	}
	entries, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		return Blob{}, ErrUnavailable
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "content.") && entry.Name() != filepath.Base(key) {
			return Blob{}, ErrConflict
		}
	}
	if _, err := root.Lstat(key); err == nil {
		existing, err := openVerified(ctx, root, loc)
		if err != nil {
			if errors.Is(err, ErrChanged) {
				return Blob{}, ErrConflict
			}
			return Blob{}, err
		}
		_ = existing.Close()
		return blob, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Blob{}, ErrUnavailable
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Blob{}, ErrUnavailable
	}
	temporary := directory + "/.upload-" + hex.EncodeToString(nonce[:])
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Blob{}, ErrUnavailable
	}
	defer root.Remove(temporary)
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		return Blob{}, ErrUnavailable
	}
	if err = ctx.Err(); err != nil {
		_ = file.Close()
		return Blob{}, err
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return Blob{}, ErrUnavailable
	}
	if err = file.Close(); err != nil {
		return Blob{}, ErrUnavailable
	}
	if err = checkComponents(root, key, true); err != nil {
		return Blob{}, err
	}
	if err = root.Link(temporary, key); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return Blob{}, ErrUnavailable
		}
		other, verifyErr := openVerified(ctx, root, loc)
		if verifyErr != nil {
			return Blob{}, ErrConflict
		}
		_ = other.Close()
	}
	if err = syncDirectories(root, directory); err != nil {
		return Blob{}, err
	}
	return blob, nil
}

func openVerified(ctx context.Context, root *os.Root, loc Location) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateLocation(loc); err != nil {
		return nil, err
	}
	if err := checkComponents(root, loc.Key, false); err != nil {
		return nil, err
	}
	file, err := root.Open(loc.Key)
	if err != nil {
		return nil, ErrUnavailable
	}
	failed := true
	defer func() {
		if failed {
			_ = file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrUnavailable
	}
	if info.Size() <= 0 || info.Size() > MaxFileBytes {
		return nil, ErrChanged
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, MaxFileBytes+1))
	if err != nil {
		return nil, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if n != info.Size() || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), loc.SHA256) {
		return nil, ErrChanged
	}
	after, err := file.Stat()
	if err != nil || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, ErrChanged
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, ErrUnavailable
	}
	failed = false
	return file, nil
}

func (s *Store) Open(ctx context.Context, loc Location) (*os.File, error) {
	if err := validateLocation(loc); err != nil {
		return nil, err
	}
	root, _, err := s.openRoot(false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return openVerified(ctx, root, loc)
}

// Resolve returns a path only after ownership-key and content verification.
// It is for the trusted Run context, not for accepting a browser path.
func (s *Store) Resolve(ctx context.Context, loc Location) (string, error) {
	if err := validateLocation(loc); err != nil {
		return "", err
	}
	root, path, err := s.openRoot(false)
	if err != nil {
		return "", err
	}
	defer root.Close()
	file, err := openVerified(ctx, root, loc)
	if err != nil {
		return "", err
	}
	defer file.Close()
	full := filepath.Join(path, filepath.FromSlash(loc.Key))
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", ErrUnavailable
	}
	if resolved != full {
		return "", ErrUnsafe
	}
	expected, err := file.Stat()
	if err != nil {
		return "", ErrUnavailable
	}
	actual, err := os.Lstat(full)
	if err != nil {
		return "", ErrUnavailable
	}
	if !actual.Mode().IsRegular() || !os.SameFile(expected, actual) {
		return "", ErrChanged
	}
	return full, nil
}

// Remove is only for an application-confirmed unreferenced upload. The full
// scope and digest must match, and no directory tree is recursively removed.
func (s *Store) Remove(ctx context.Context, loc Location) error {
	if err := validateLocation(loc); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Lstat(s.root); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	root, _, err := s.openRoot(false)
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err := root.Lstat(loc.Key); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return ErrUnavailable
	}
	file, err := openVerified(ctx, root, loc)
	if err != nil {
		return err
	}
	_ = file.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := root.Remove(loc.Key); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	return syncDirectories(root, strings.Join(strings.Split(loc.Key, "/")[:3], "/"))
}

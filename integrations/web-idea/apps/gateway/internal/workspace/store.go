package workspace

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ybs/web-idea/apps/gateway/internal/fsjail"
)

var (
	ErrNotFound          = errors.New("workspace not found")
	ErrInvalidRoot       = errors.New("invalid root")
	ErrRootNotDir        = errors.New("root is not a directory")
	ErrInvalidClientKey  = errors.New("invalid client key")
	ErrClientKeyConflict = errors.New("client key belongs to another workspace")
	ErrInvalidTTL        = errors.New("invalid workspace ttl")
	ErrLimitReached      = errors.New("workspace limit reached")
)

const (
	// DefaultReadOnlyTTL bounds an embedded/read-only browser session when the
	// caller omits ttl_seconds. Writable standalone workspaces retain the
	// historical no-expiry behavior unless a TTL is explicitly supplied.
	DefaultReadOnlyTTL = 30 * time.Minute
	// DefaultMaxTTL is a safety ceiling for all in-memory workspace leases.
	DefaultMaxTTL = 24 * time.Hour
	// DefaultMaxWorkspaces bounds uncertain creates when the caller loses the
	// response before learning the workspace ID.
	DefaultMaxWorkspaces = 32
)

type Status string

const (
	StatusBrowsing Status = "browsing"
)

// Workspace is an in-memory workspace session (P1: browsing only).
type Workspace struct {
	ID        string
	Root      string
	Status    Status
	ReadOnly  bool
	ClientKey string
	ExpiresAt time.Time
	Jail      *fsjail.Jail
}

// Options controls the in-memory lease policy. It deliberately contains no
// persistence or Run fields: a Gateway workspace is a process-local session.
type Options struct {
	MaxWorkspaces      int
	DefaultReadOnlyTTL time.Duration
	MaxTTL             time.Duration
}

func DefaultOptions() Options {
	return Options{
		MaxWorkspaces:      DefaultMaxWorkspaces,
		DefaultReadOnlyTTL: DefaultReadOnlyTTL,
		MaxTTL:             DefaultMaxTTL,
	}
}

func normalizeOptions(options Options) Options {
	defaults := DefaultOptions()
	if options.MaxWorkspaces <= 0 {
		options.MaxWorkspaces = defaults.MaxWorkspaces
	}
	if options.MaxTTL <= 0 {
		options.MaxTTL = defaults.MaxTTL
	}
	if options.DefaultReadOnlyTTL <= 0 {
		options.DefaultReadOnlyTTL = defaults.DefaultReadOnlyTTL
	}
	if options.DefaultReadOnlyTTL > options.MaxTTL {
		options.DefaultReadOnlyTTL = options.MaxTTL
	}
	return options
}

type CreateOptions struct {
	ClientKey string
	TTL       time.Duration
}

// Store tracks workspace sessions and client-key idempotency in memory.
type Store struct {
	mu          sync.Mutex
	byID        map[string]*Workspace
	byClientKey map[string]string
	options     Options
	onExpired   func([]string)
}

// NewStore preserves the original no-argument constructor while allowing the
// Gateway to inject its configured lease policy.
func NewStore(options ...Options) *Store {
	policy := DefaultOptions()
	if len(options) > 0 {
		policy = normalizeOptions(options[0])
	}
	return &Store{
		byID:        make(map[string]*Workspace),
		byClientKey: make(map[string]string),
		options:     policy,
	}
}

// SetExpirationHook installs the resource cleanup callback used when a lease
// expires during a store operation. The callback runs after the store lock is
// released and may stop jdtls or remove its per-workspace index.
func (s *Store) SetExpirationHook(hook func([]string)) {
	s.mu.Lock()
	s.onExpired = hook
	s.mu.Unlock()
}

// Create is the Gateway creation primitive. A non-empty client key
// is an entity-level idempotency key: the same root/read_only pair returns the
// original workspace, while a payload conflict is rejected.
func (s *Store) Create(root string, maxFileBytes int64, readOnly bool, options ...CreateOptions) (*Workspace, bool, error) {
	var option CreateOptions
	if len(options) > 0 {
		option = options[0]
	}
	option.ClientKey = strings.TrimSpace(option.ClientKey)
	if option.ClientKey != "" && (len(option.ClientKey) > 256 || strings.ContainsRune(option.ClientKey, 0)) {
		return nil, false, ErrInvalidClientKey
	}
	if option.TTL < 0 {
		return nil, false, ErrInvalidTTL
	}

	if root == "" || !filepath.IsAbs(root) {
		return nil, false, ErrInvalidRoot
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, false, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, false, err
	}
	if !fi.IsDir() {
		return nil, false, ErrRootNotDir
	}
	jail, err := fsjail.New(abs)
	if err != nil {
		return nil, false, err
	}
	if maxFileBytes > 0 {
		jail.MaxFileBytes = maxFileBytes
	}

	now := time.Now()
	ttl := option.TTL
	if readOnly && ttl == 0 {
		ttl = s.options.DefaultReadOnlyTTL
	}
	if ttl > s.options.MaxTTL {
		return nil, false, ErrInvalidTTL
	}
	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = now.Add(ttl)
	}

	s.mu.Lock()
	expired := s.expireLocked(now)
	if option.ClientKey != "" {
		if existingID, ok := s.byClientKey[option.ClientKey]; ok {
			existing := s.byID[existingID]
			if existing != nil && existing.Root == jail.Root && existing.ReadOnly == readOnly {
				s.mu.Unlock()
				s.notifyExpired(expired)
				return existing, true, nil
			}
			s.mu.Unlock()
			s.notifyExpired(expired)
			return nil, false, ErrClientKeyConflict
		}
	}
	if len(s.byID) >= s.options.MaxWorkspaces {
		s.mu.Unlock()
		s.notifyExpired(expired)
		return nil, false, ErrLimitReached
	}
	id, err := newID()
	if err != nil {
		s.mu.Unlock()
		s.notifyExpired(expired)
		return nil, false, err
	}
	ws := &Workspace{
		ID:        id,
		Root:      jail.Root,
		Status:    StatusBrowsing,
		ReadOnly:  readOnly,
		ClientKey: option.ClientKey,
		ExpiresAt: expiresAt,
		Jail:      jail,
	}
	s.byID[id] = ws
	if option.ClientKey != "" {
		s.byClientKey[option.ClientKey] = id
	}
	s.mu.Unlock()
	s.notifyExpired(expired)
	return ws, false, nil
}

func (s *Store) Get(id string) (*Workspace, error) {
	// Expire all entries first so an expired client key cannot reserve a slot
	// forever. The second check is under the store lock to make the fixed
	// deadline strict even when it passes between the sweep and lookup.
	s.Expire(time.Now())
	s.mu.Lock()
	now := time.Now()
	ws, ok := s.byID[id]
	var expired []string
	if ok && ws != nil && !ws.ExpiresAt.IsZero() && !now.Before(ws.ExpiresAt) {
		delete(s.byID, id)
		if ws.ClientKey != "" {
			delete(s.byClientKey, ws.ClientKey)
		}
		expired = []string{id}
		ok = false
	}
	s.mu.Unlock()
	s.notifyExpired(expired)
	if !ok {
		return nil, ErrNotFound
	}
	return ws, nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[id]; !ok {
		return ErrNotFound
	}
	if ws := s.byID[id]; ws != nil && ws.ClientKey != "" {
		delete(s.byClientKey, ws.ClientKey)
	}
	delete(s.byID, id)
	return nil
}

// Expire removes expired workspaces and returns their IDs so the API layer can
// stop jdtls and remove each matching data directory.
func (s *Store) Expire(now time.Time) []string {
	s.mu.Lock()
	expired := s.expireLocked(now)
	s.mu.Unlock()
	s.notifyExpired(expired)
	return expired
}

// Clear removes every in-memory workspace during Gateway shutdown. The caller
// remains responsible for stopping any associated external process first.
func (s *Store) Clear() []string {
	s.mu.Lock()
	ids := make([]string, 0, len(s.byID))
	for id := range s.byID {
		ids = append(ids, id)
		delete(s.byID, id)
	}
	clear(s.byClientKey)
	s.mu.Unlock()
	return ids
}

func (s *Store) expireLocked(now time.Time) []string {
	var expired []string
	for id, ws := range s.byID {
		if ws == nil || ws.ExpiresAt.IsZero() || now.Before(ws.ExpiresAt) {
			continue
		}
		delete(s.byID, id)
		if ws.ClientKey != "" {
			delete(s.byClientKey, ws.ClientKey)
		}
		expired = append(expired, id)
	}
	return expired
}

func (s *Store) notifyExpired(ids []string) {
	if len(ids) == 0 {
		return
	}
	s.mu.Lock()
	hook := s.onExpired
	s.mu.Unlock()
	if hook != nil {
		hook(ids)
	}
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

package jdtls

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

var (
	ErrNotConfigured = errors.New("jdtls not configured")
	ErrNotReady      = errors.New("jdtls not ready")
)

type Status string

const (
	StatusOff      Status = "off"
	StatusStarting Status = "starting"
	StatusReady    Status = "ready"
	StatusFailed   Status = "failed"
)

type Config struct {
	// LaunchScript is path to apps/jdtls-sidecar/bin/launch.sh (or empty to disable).
	LaunchScript string
	// DataRoot holds per-workspace jdtls -data directories.
	DataRoot string
}

type Session struct {
	Status Status
	Cmd    *exec.Cmd
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	Root   string
	Err    string
	done   chan struct{}
}

// Done is closed after the jdtls process has exited and its stdio pipes are
// released. The proxy uses it to close any WebSocket attached to this session.
func (s *Session) Done() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.done
}

type Manager struct {
	cfg Config
	mu  sync.Mutex
	by  map[string]*Session
}

func NewManager(cfg Config) *Manager {
	return &Manager{cfg: cfg, by: make(map[string]*Session)}
}

func (m *Manager) Configured() bool {
	return m.cfg.LaunchScript != ""
}

func (m *Manager) Status(workspaceID string) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.by[workspaceID]
	if !ok {
		if !m.Configured() {
			return StatusOff
		}
		return StatusOff
	}
	return s.Status
}

func (m *Manager) Session(workspaceID string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.by[workspaceID]
	if !ok {
		return nil, ErrNotReady
	}
	if s.Status != StatusReady && s.Status != StatusStarting {
		return nil, ErrNotReady
	}
	return s, nil
}

// Ensure starts jdtls for the workspace if needed. Returns ErrNotConfigured when disabled.
// The optional readOnly flag is used by the sidecar launcher to keep Eclipse
// project metadata in the jdtls data area instead of the checkout.
func (m *Manager) Ensure(workspaceID, workspaceRoot string, readOnly ...bool) (*Session, error) {
	if !m.Configured() {
		return nil, ErrNotConfigured
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.by[workspaceID]; ok {
		if s.Status == StatusReady || s.Status == StatusStarting {
			return s, nil
		}
		_ = m.stopLocked(workspaceID, false)
	}

	dataDir, err := m.workspaceDataDir(workspaceID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}

	cmd := exec.Command(m.cfg.LaunchScript, dataDir, workspaceRoot)
	cmd.Dir = workspaceRoot
	cmd.Env = append(os.Environ(), "WORKSPACE_ROOT="+workspaceRoot)
	if len(readOnly) > 0 && readOnly[0] {
		cmd.Env = append(cmd.Env, "WEBIDEA_READ_ONLY=true")
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	cmd.Stderr = os.Stderr

	s := &Session{
		Status: StatusStarting,
		Cmd:    cmd,
		Stdin:  stdin,
		Stdout: stdout,
		Root:   workspaceRoot,
		done:   make(chan struct{}),
	}
	if err := cmd.Start(); err != nil {
		s.Status = StatusFailed
		s.Err = err.Error()
		close(s.done)
		m.by[workspaceID] = s
		return nil, fmt.Errorf("start jdtls: %w", err)
	}
	s.Status = StatusReady
	m.by[workspaceID] = s

	go func() {
		err := cmd.Wait()
		close(s.done)
		m.mu.Lock()
		defer m.mu.Unlock()
		cur := m.by[workspaceID]
		if cur == nil || cur.Cmd != cmd {
			return
		}
		cur.Status = StatusFailed
		if err != nil {
			cur.Err = err.Error()
		} else {
			cur.Err = "jdtls exited"
		}
	}()

	return s, nil
}

func (m *Manager) Stop(workspaceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_ = m.stopLocked(workspaceID, false)
}

// StopAndCleanup releases the session and removes only its workspace-owned
// data directory. A transient WebSocket disconnect uses Stop so a reconnect
// can reuse the index; DELETE uses this method.
func (m *Manager) StopAndCleanup(workspaceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_ = m.stopLocked(workspaceID, true)
}

// StopAll releases every workspace session owned by this manager. It is used
// during Gateway shutdown so a supervisor never leaves a jdtls child behind.
func (m *Manager) StopAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.by))
	for id := range m.by {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.StopAndCleanup(id)
	}

	// Clean up data for sessions from a previous Gateway process. DataRoot is a
	// dedicated parent; only safe generated workspace-id children are removed.
	if m.cfg.DataRoot == "" {
		return
	}
	root, err := filepath.Abs(m.cfg.DataRoot)
	if err != nil {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !isGeneratedWorkspaceID(entry.Name()) {
			continue
		}
		m.StopAndCleanup(entry.Name())
	}
}

func (m *Manager) stopLocked(workspaceID string, cleanup bool) error {
	s, ok := m.by[workspaceID]
	if ok {
		delete(m.by, workspaceID)
		if s.Stdin != nil {
			_ = s.Stdin.Close()
		}
		if s.Cmd != nil && s.Cmd.Process != nil {
			_ = s.Cmd.Process.Kill()
			if s.done != nil {
				<-s.done
			} else {
				_, _ = s.Cmd.Process.Wait()
			}
		}
	}
	if cleanup {
		dataDir, err := m.workspaceDataDir(workspaceID)
		if err != nil {
			return err
		}
		if dataDir != "" {
			return os.RemoveAll(dataDir)
		}
	}
	return nil
}

func (m *Manager) workspaceDataDir(workspaceID string) (string, error) {
	if m.cfg.DataRoot == "" {
		return "", nil
	}
	if workspaceID == "" || filepath.Base(workspaceID) != workspaceID || strings.ContainsRune(workspaceID, 0) {
		return "", fmt.Errorf("invalid workspace id")
	}
	root, err := filepath.Abs(m.cfg.DataRoot)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(root, workspaceID)
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("workspace data path escapes data root")
	}
	return candidate, nil
}

func isGeneratedWorkspaceID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

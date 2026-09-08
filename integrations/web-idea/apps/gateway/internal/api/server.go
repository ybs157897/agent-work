package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ybs/web-idea/apps/gateway/internal/auth"
	"github.com/ybs/web-idea/apps/gateway/internal/config"
	"github.com/ybs/web-idea/apps/gateway/internal/fsjail"
	"github.com/ybs/web-idea/apps/gateway/internal/jdtls"
	"github.com/ybs/web-idea/apps/gateway/internal/lspproxy"
	"github.com/ybs/web-idea/apps/gateway/internal/projectmodel"
	"github.com/ybs/web-idea/apps/gateway/internal/workspace"
)

type Server struct {
	cfg       config.Config
	store     *workspace.Store
	jdtls     *jdtls.Manager
	lspMu     sync.Mutex
	lspActive map[string]struct{}
	sweepMu   sync.Mutex
	sweepStop chan struct{}
	sweepDone chan struct{}
}

func New(cfg config.Config, store *workspace.Store, jdtlsMgr *jdtls.Manager) *Server {
	s := &Server{cfg: cfg, store: store, jdtls: jdtlsMgr, lspActive: make(map[string]struct{})}
	if store != nil {
		store.SetExpirationHook(s.cleanupExpired)
	}
	return s
}

// Start starts the bounded in-memory lease reaper. It is explicit so tests and
// embedders can own the goroutine lifecycle; main starts it with its signal
// context and calls Close on shutdown.
func (s *Server) Start(ctx context.Context) {
	if s == nil || s.store == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.sweepMu.Lock()
	if s.sweepDone != nil {
		s.sweepMu.Unlock()
		return
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	s.sweepStop, s.sweepDone = stop, done
	interval := s.cfg.WorkspaceSweepInterval
	if interval <= 0 {
		interval = time.Minute
	}
	s.sweepMu.Unlock()
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.reapExpired()
			case <-stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
}

// Close stops the lease sweeper and releases all Gateway-owned jdtls/index
// resources. It is idempotent and intentionally has no database side effect.
func (s *Server) Close() {
	if s == nil {
		return
	}
	s.sweepMu.Lock()
	stop, done := s.sweepStop, s.sweepDone
	s.sweepStop, s.sweepDone = nil, nil
	if stop != nil {
		close(stop)
	}
	s.sweepMu.Unlock()
	if done != nil {
		<-done
	}
	if s.store != nil {
		s.reapExpired()
		for _, id := range s.store.Clear() {
			s.cleanupExpired([]string{id})
		}
	}
	if s.jdtls != nil {
		s.jdtls.StopAll()
	}
}

func (s *Server) reapExpired() {
	if s == nil || s.store == nil {
		return
	}
	_ = s.store.Expire(time.Now())
}

func (s *Server) cleanupExpired(ids []string) {
	for _, id := range ids {
		s.releaseLSP(id)
		if s.jdtls != nil {
			s.jdtls.StopAndCleanup(id)
		}
	}
}

func (s *Server) getWorkspace(id string) (*workspace.Workspace, error) {
	s.reapExpired()
	return s.store.Get(id)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	api := http.NewServeMux()
	api.HandleFunc("POST /api/v1/workspaces", s.handleCreateWorkspace)
	api.HandleFunc("GET /api/v1/workspaces/{id}", s.handleGetWorkspace)
	api.HandleFunc("DELETE /api/v1/workspaces/{id}", s.handleDeleteWorkspace)
	api.HandleFunc("GET /api/v1/workspaces/{id}/fs/tree", s.handleTree)
	api.HandleFunc("GET /api/v1/workspaces/{id}/fs/file", s.handleFile)
	api.HandleFunc("PUT /api/v1/workspaces/{id}/fs/file", s.handlePutFile)
	api.HandleFunc("GET /api/v1/workspaces/{id}/fs/stat", s.handleStat)
	api.HandleFunc("GET /api/v1/workspaces/{id}/fs/search", s.handleSearch)
	api.HandleFunc("GET /api/v1/workspaces/{id}/fs/files", s.handleFiles)
	api.HandleFunc("GET /api/v1/workspaces/{id}/project", s.handleProject)
	api.HandleFunc("GET /api/v1/workspaces/{id}/lsp", s.handleLSP)

	protected := auth.DevBearer{Token: s.cfg.DevToken}.Middleware(api)
	mux.Handle("/api/", protected)
	if s.cfg.StaticDir != "" {
		mux.Handle("/", staticFiles(s.cfg.StaticDir))
	}

	return withCORS(s.cfg.CORSOrigins, mux)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

type createReq struct {
	Root       string `json:"root"`
	ReadOnly   bool   `json:"read_only"`
	ClientKey  string `json:"client_key"`
	TTLSeconds *int64 `json:"ttl_seconds"`
}

type workspaceResp struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	RootDisplay string `json:"root_display,omitempty"`
	LSPStatus   string `json:"lsp_status,omitempty"`
	LSPEnabled  bool   `json:"lsp_enabled"`
	ReadOnly    bool   `json:"read_only"`
	ExpiresAt   string `json:"expires_at,omitempty"`
}

func (s *Server) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	var req createReq
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	headerKey := strings.TrimSpace(r.Header.Get("X-ATW-Code-Workspace-Create-ID"))
	bodyKey := strings.TrimSpace(req.ClientKey)
	if headerKey != "" && bodyKey != "" && headerKey != bodyKey {
		writeErr(w, http.StatusBadRequest, "client key header does not match body")
		return
	}
	clientKey := bodyKey
	if clientKey == "" {
		clientKey = headerKey
	}
	createOptions := workspace.CreateOptions{ClientKey: clientKey}
	if req.TTLSeconds != nil {
		if *req.TTLSeconds < 0 {
			writeErr(w, http.StatusBadRequest, workspace.ErrInvalidTTL.Error())
			return
		}
		maxTTL := s.cfg.WorkspaceMaxTTL
		if maxTTL <= 0 {
			maxTTL = workspace.DefaultMaxTTL
		}
		if *req.TTLSeconds > int64(maxTTL/time.Second) {
			writeErr(w, http.StatusBadRequest, workspace.ErrInvalidTTL.Error())
			return
		}
		createOptions.TTL = time.Duration(*req.TTLSeconds) * time.Second
	}
	ws, replayed, err := s.store.Create(req.Root, s.cfg.MaxFileBytes, req.ReadOnly, createOptions)
	if err != nil {
		switch {
		case errors.Is(err, workspace.ErrInvalidRoot), errors.Is(err, workspace.ErrRootNotDir):
			writeErr(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, workspace.ErrClientKeyConflict):
			writeErr(w, http.StatusConflict, err.Error())
		case errors.Is(err, workspace.ErrInvalidClientKey), errors.Is(err, workspace.ErrInvalidTTL):
			writeErr(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, workspace.ErrLimitReached):
			writeErr(w, http.StatusTooManyRequests, err.Error())
		default:
			if osIsNotExist(err) {
				writeErr(w, http.StatusBadRequest, "root not found")
				return
			}
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, s.workspaceJSON(ws))
}

func (s *Server) handleGetWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, err := s.getWorkspace(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "workspace not found")
		return
	}
	writeJSON(w, http.StatusOK, s.workspaceJSON(ws))
}

func (s *Server) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.getWorkspace(id); err != nil {
		writeErr(w, http.StatusNotFound, "workspace not found")
		return
	}
	if s.jdtls != nil {
		s.jdtls.StopAndCleanup(id)
	}
	if err := s.store.Delete(id); err != nil {
		writeErr(w, http.StatusNotFound, "workspace not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) workspaceJSON(ws *workspace.Workspace) workspaceResp {
	resp := workspaceResp{
		ID:          ws.ID,
		Status:      string(ws.Status),
		RootDisplay: path.Base(ws.Root),
		LSPEnabled:  s.jdtls != nil && s.jdtls.Configured(),
		ReadOnly:    ws.ReadOnly,
	}
	if s.jdtls != nil {
		resp.LSPStatus = string(s.jdtls.Status(ws.ID))
	} else {
		resp.LSPStatus = "off"
	}
	if !ws.ExpiresAt.IsZero() {
		resp.ExpiresAt = ws.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	return resp
}

func (s *Server) handleLSP(w http.ResponseWriter, r *http.Request) {
	if s.jdtls == nil || !s.jdtls.Configured() {
		writeErr(w, http.StatusServiceUnavailable, "jdtls not configured (set WEBIDEA_JDTLS_LAUNCH)")
		return
	}
	id := r.PathValue("id")
	ws, err := s.getWorkspace(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "workspace not found")
		return
	}
	sess, err := s.jdtls.Ensure(id, ws.Root, ws.ReadOnly)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if !s.acquireLSP(id) {
		writeErr(w, http.StatusConflict, "workspace already has an active lsp connection")
		return
	}
	defer func() {
		s.jdtls.Stop(id)
		s.releaseLSP(id)
	}()
	lspproxy.Handle(w, r, sess, id, ws.Root, ws.ReadOnly)
}

func (s *Server) acquireLSP(workspaceID string) bool {
	s.lspMu.Lock()
	defer s.lspMu.Unlock()
	if s.lspActive == nil {
		s.lspActive = make(map[string]struct{})
	}
	if _, active := s.lspActive[workspaceID]; active {
		return false
	}
	s.lspActive[workspaceID] = struct{}{}
	return true
}

func (s *Server) releaseLSP(workspaceID string) {
	s.lspMu.Lock()
	delete(s.lspActive, workspaceID)
	s.lspMu.Unlock()
}

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	ws, err := s.getWorkspace(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "workspace not found")
		return
	}
	rel := r.URL.Query().Get("path")
	depth := 1
	if d := r.URL.Query().Get("depth"); d != "" {
		n, err := strconv.Atoi(d)
		if err != nil || n < 1 {
			writeErr(w, http.StatusBadRequest, "invalid depth")
			return
		}
		depth = n
	}
	tr, err := ws.Jail.Tree(rel, depth)
	if err != nil {
		writeFSErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tr)
}

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	ws, err := s.getWorkspace(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "workspace not found")
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeErr(w, http.StatusBadRequest, "path required")
		return
	}
	data, err := ws.Jail.ReadFile(rel)
	if err != nil {
		writeFSErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) handlePutFile(w http.ResponseWriter, r *http.Request) {
	ws, err := s.getWorkspace(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "workspace not found")
		return
	}
	if ws.ReadOnly {
		writeErr(w, http.StatusForbidden, "workspace is read-only")
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeErr(w, http.StatusBadRequest, "path required")
		return
	}
	max := ws.Jail.MaxFileBytes
	if max <= 0 {
		max = fsjail.DefaultMaxFileBytes
	}
	limited := io.LimitReader(r.Body, max+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "failed to read body")
		return
	}
	if int64(len(data)) > max {
		writeErr(w, http.StatusRequestEntityTooLarge, "file too large")
		return
	}
	if err := ws.Jail.WriteFile(rel, data); err != nil {
		writeFSErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStat(w http.ResponseWriter, r *http.Request) {
	ws, err := s.getWorkspace(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "workspace not found")
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeErr(w, http.StatusBadRequest, "path required")
		return
	}
	st, err := ws.Jail.StatPath(rel)
	if err != nil {
		writeFSErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	ws, err := s.getWorkspace(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "workspace not found")
		return
	}
	q := r.URL.Query().Get("q")
	if strings.TrimSpace(q) == "" {
		writeErr(w, http.StatusBadRequest, "q required")
		return
	}
	maxHits := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeErr(w, http.StatusBadRequest, "invalid limit")
			return
		}
		maxHits = n
	}
	res, err := ws.Jail.SearchContext(r.Context(), q, maxHits)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		writeFSErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	ws, err := s.getWorkspace(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "workspace not found")
		return
	}
	q := r.URL.Query().Get("q")
	if strings.TrimSpace(q) == "" {
		writeErr(w, http.StatusBadRequest, "q required")
		return
	}
	maxPaths := fsjail.DefaultMaxFilePaths
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > fsjail.MaxFilePaths {
			writeErr(w, http.StatusBadRequest, "invalid limit")
			return
		}
		maxPaths = n
	}
	res, err := ws.Jail.SearchFiles(r.Context(), q, maxPaths)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		writeFSErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleProject(w http.ResponseWriter, r *http.Request) {
	ws, err := s.getWorkspace(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "workspace not found")
		return
	}
	info := projectmodel.Inspect(ws.Root)
	writeJSON(w, http.StatusOK, info)
}

func writeFSErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, fsjail.ErrInvalidPath):
		writeErr(w, http.StatusBadRequest, "invalid path")
	case errors.Is(err, fsjail.ErrForbidden):
		writeErr(w, http.StatusForbidden, "path outside workspace")
	case errors.Is(err, fsjail.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, fsjail.ErrNotFile), errors.Is(err, fsjail.ErrNotDir):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, fsjail.ErrTooLarge):
		writeErr(w, http.StatusRequestEntityTooLarge, "file too large")
	case errors.Is(err, fsjail.ErrNotText):
		writeErr(w, http.StatusUnsupportedMediaType, "not a text preview")
	default:
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func osIsNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist) || os.IsNotExist(err)
}

func withCORS(origins []string, next http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		allowed[o] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if originOK(origin, allowed) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-ATW-Code-Workspace-Create-ID")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originOK allows exact configured origins, plus any localhost / 127.0.0.1 port
// so Vite can hop 5173→5174 during local dev without CORS breakage.
func originOK(origin string, allowed map[string]struct{}) bool {
	if origin == "" {
		return false
	}
	if _, ok := allowed[origin]; ok {
		return true
	}
	return strings.HasPrefix(origin, "http://127.0.0.1:") ||
		strings.HasPrefix(origin, "http://localhost:") ||
		origin == "http://127.0.0.1" ||
		origin == "http://localhost"
}

// staticFiles serves a trusted, build-time directory without allowing the
// request path (or a symlink below it) to escape that directory. Unknown
// extensionless paths fall back to index.html for the embedded SPA; API paths
// are matched by the longer /api/ handler before this catch-all.
func staticFiles(root string) http.Handler {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "static files unavailable", http.StatusInternalServerError)
		})
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		rootReal = rootAbs
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel, ok := safeStaticRelativePath(r.URL.EscapedPath())
		if !ok {
			http.NotFound(w, r)
			return
		}
		target := filepath.Join(rootReal, filepath.FromSlash(rel))
		if !staticPathInside(rootReal, target) {
			http.NotFound(w, r)
			return
		}

		info, statErr := os.Stat(target)
		if statErr != nil || info.IsDir() {
			if rel != "" && path.Ext(rel) != "" {
				http.NotFound(w, r)
				return
			}
			rel = "index.html"
			target = filepath.Join(rootReal, rel)
			if !staticPathInside(rootReal, target) {
				http.NotFound(w, r)
				return
			}
			info, statErr = os.Stat(target)
			if statErr != nil || info.IsDir() {
				http.NotFound(w, r)
				return
			}
		}

		request := r.Clone(r.Context())
		request.URL.Path = "/" + filepath.ToSlash(rel)
		request.URL.RawPath = ""
		file, err := os.Open(target)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		http.ServeContent(w, request, filepath.Base(target), info.ModTime(), file)
	})
}

func safeStaticRelativePath(escaped string) (string, bool) {
	decoded, err := url.PathUnescape(escaped)
	if err != nil || !strings.HasPrefix(decoded, "/") {
		return "", false
	}
	rel := strings.TrimPrefix(decoded, "/")
	if strings.ContainsAny(rel, "\\\x00") {
		return "", false
	}
	for _, segment := range strings.Split(rel, "/") {
		if segment == "" || segment == "." || segment == ".." {
			if rel == "" && segment == "" {
				continue
			}
			return "", false
		}
	}
	return rel, true
}

func staticPathInside(root, candidate string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return false
	}
	probe, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	for {
		resolved, resolveErr := filepath.EvalSymlinks(probe)
		if resolveErr == nil {
			return pathWithin(rootReal, resolved)
		}
		if !os.IsNotExist(resolveErr) {
			return false
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return pathWithin(rootReal, probe)
		}
		probe = parent
	}
}

func pathWithin(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

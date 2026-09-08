package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/security"
)

type codeWorkspaceCreateRequest struct {
	ConversationID string `json:"conversation_id,omitempty"`
	RunID          string `json:"run_id,omitempty"`
}

type codeWorkspaceResponse struct {
	ID                 string `json:"id"`
	FrameURL           string `json:"frame_url"`
	RepositoryIdentity string `json:"repository_identity"`
	Branch             string `json:"branch,omitempty"`
	WorktreeRef        string `json:"worktree_ref,omitempty"`
	ReadOnly           bool   `json:"read_only"`
	ExpiresAt          string `json:"expires_at"`
}

func (s *Server) registerCodeWorkspaceRoutes(mux *http.ServeMux) {
	read := func(next http.HandlerFunc) http.HandlerFunc {
		return s.guard(security.PermRead, func(w http.ResponseWriter, r *http.Request) {
			if !sameCodeWorkspaceOrigin(r) {
				writeCodeWorkspaceProblem(w, r, fmt.Errorf("%w: code workspace requires a same-origin request", application.ErrCodeWorkspaceProxy))
				return
			}
			next(w, r)
		})
	}
	mux.HandleFunc("POST /api/v1/workspaces/{workspace_id}/agent-profiles/{agent_id}/code-workspaces", read(s.handleCreateCodeWorkspace))
	mux.HandleFunc("DELETE /api/v1/code-workspaces/{session_id}", read(s.handleDeleteCodeWorkspace))
	mux.HandleFunc("GET /api/v1/code-workspaces/{session_id}/bootstrap", read(s.handleCodeWorkspaceBootstrap))
	mux.HandleFunc("GET /api/v1/code-workspaces/{session_id}/view/{asset_path...}", read(s.handleCodeWorkspaceView))
	mux.HandleFunc("GET /api/v1/code-workspaces/{session_id}/gateway/{gateway_path...}", read(s.handleCodeWorkspaceGateway))
}

func (s *Server) handleCreateCodeWorkspace(w http.ResponseWriter, r *http.Request) {
	if s.codeWorkspaces == nil {
		writeCodeWorkspaceProblem(w, r, fmt.Errorf("%w: code gateway is not configured", application.ErrCodeWorkspaceUnavailable))
		return
	}
	workspaceID, agentID := r.PathValue("workspace_id"), r.PathValue("agent_id")
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if status, body, forbidden := codeWorkspacePathForbiddenBytes(r); forbidden {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	var req codeWorkspaceCreateRequest
	if err := decodeBody(r, &req); err != nil {
		writeCodeWorkspaceProblem(w, r, fmt.Errorf("%w: %v", domain.ErrValidation, err))
		return
	}
	ws, err := s.codeWorkspaces.Create(r.Context(), workspaceID, agentID, application.CodeWorkspaceCreateParams{
		ConversationID: req.ConversationID, RunID: req.RunID,
	})
	if err != nil {
		writeCodeWorkspaceProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, codeWorkspaceResponse{
		ID:                 ws.ID,
		FrameURL:           "/api/v1/code-workspaces/" + url.PathEscape(ws.ID) + "/view/",
		RepositoryIdentity: ws.RepositoryIdentity, Branch: ws.Branch,
		WorktreeRef: ws.WorktreeRef, ReadOnly: true, ExpiresAt: ws.ExpiresAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
	})
}

func codeWorkspacePathForbiddenBytes(r *http.Request) (int, []byte, bool) {
	if status, body, forbidden := pathForbiddenBytes(r); forbidden {
		return status, body, true
	}
	body, err := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		status, raw := renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		return status, raw, true
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) == nil {
		if _, ok := raw["root"]; ok {
			status, response := renderProblem(http.StatusForbidden, "workspace_path_forbidden", "Workspace path forbidden", "请求不接受 root 字段")
			return status, response, true
		}
	}
	return 0, nil, false
}

func (s *Server) handleDeleteCodeWorkspace(w http.ResponseWriter, r *http.Request) {
	if s.codeWorkspaces == nil {
		writeCodeWorkspaceProblem(w, r, application.ErrCodeWorkspaceExpired)
		return
	}
	if err := s.codeWorkspaces.CloseSession(r.Context(), r.PathValue("session_id")); err != nil {
		writeCodeWorkspaceProblem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type codeWorkspaceBootstrapResponse struct {
	GatewayBaseURL string `json:"gateway_base_url"`
	Workspace      any    `json:"workspace"`
	ReadOnly       bool   `json:"read_only"`
	Embedded       bool   `json:"embedded"`
}

func (s *Server) handleCodeWorkspaceBootstrap(w http.ResponseWriter, r *http.Request) {
	if s.codeWorkspaces == nil {
		writeCodeWorkspaceProblem(w, r, application.ErrCodeWorkspaceExpired)
		return
	}
	id := r.PathValue("session_id")
	ws, err := s.codeWorkspaces.Get(r.Context(), id)
	if err != nil {
		writeCodeWorkspaceProblem(w, r, err)
		return
	}
	setCodeWorkspaceResponseHeaders(w.Header())
	writeJSON(w, http.StatusOK, codeWorkspaceBootstrapResponse{
		GatewayBaseURL: codeWorkspaceGatewayBaseURL(r, id),
		Workspace: map[string]any{
			"id": ws.GatewayWorkspaceID, "repository_identity": ws.RepositoryIdentity,
			"branch": nullableString(ws.Branch), "worktree_ref": nullableString(ws.WorktreeRef),
			"ref_kind": string(ws.RefKind), "status": ws.GatewayStatus,
			"root_display": ws.RootDisplay, "lsp_enabled": ws.LSPEnabled, "lsp_status": ws.LSPStatus,
		},
		ReadOnly: true, Embedded: true,
	})
}

func (s *Server) handleCodeWorkspaceView(w http.ResponseWriter, r *http.Request) {
	if s.codeWorkspaces == nil {
		writeCodeWorkspaceProblem(w, r, application.ErrCodeWorkspaceExpired)
		return
	}
	if r.Method != http.MethodGet {
		writeCodeWorkspaceProblem(w, r, fmt.Errorf("%w: static view is read-only", application.ErrCodeWorkspaceProxy))
		return
	}
	setCodeWorkspaceResponseHeaders(w.Header())
	proxy, err := s.codeWorkspaces.Proxy(r.Context(), r.PathValue("session_id"))
	if err != nil {
		writeCodeWorkspaceProblem(w, r, err)
		return
	}
	base := "/api/v1/code-workspaces/" + url.PathEscape(r.PathValue("session_id")) + "/view"
	rest := strings.TrimPrefix(r.URL.Path, base)
	if rest == "" || rest == "/" {
		rest = "/"
	}
	if !safeProxyPath(rest) || strings.HasPrefix(rest, "/api/") {
		writeCodeWorkspaceProblem(w, r, fmt.Errorf("%w: invalid static asset path", application.ErrCodeWorkspaceProxy))
		return
	}
	s.proxyCodeWorkspace(w, r, proxy, rest)
}

func (s *Server) handleCodeWorkspaceGateway(w http.ResponseWriter, r *http.Request) {
	if s.codeWorkspaces == nil {
		writeCodeWorkspaceProblem(w, r, application.ErrCodeWorkspaceExpired)
		return
	}
	if r.Method != http.MethodGet {
		writeCodeWorkspaceProblem(w, r, fmt.Errorf("%w: code workspace Gateway is read-only", application.ErrCodeWorkspaceProxy))
		return
	}
	setCodeWorkspaceResponseHeaders(w.Header())
	sessionID := r.PathValue("session_id")
	proxy, err := s.codeWorkspaces.Proxy(r.Context(), sessionID)
	if err != nil {
		writeCodeWorkspaceProblem(w, r, err)
		return
	}
	prefix := "/api/v1/code-workspaces/" + url.PathEscape(sessionID) + "/gateway"
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	if rest == "" || !safeProxyPath(rest) || !allowedCodeWorkspaceGatewayPath(rest, proxy.GatewayWorkspaceID) {
		writeCodeWorkspaceProblem(w, r, fmt.Errorf("%w: endpoint is outside the read-only workspace surface", application.ErrCodeWorkspaceProxy))
		return
	}
	targetPath := "/api/v1/workspaces/" + url.PathEscape(proxy.GatewayWorkspaceID) + strings.TrimPrefix(rest, "/api/v1/workspaces/"+url.PathEscape(proxy.GatewayWorkspaceID))
	if strings.HasSuffix(rest, "/lsp") {
		s.proxyCodeWorkspaceLSP(w, r, sessionID, proxy, targetPath)
		return
	}
	s.proxyCodeWorkspace(w, r, proxy, targetPath)
}

const codeWorkspaceWSAuthRecheckInterval = 2 * time.Second

const codeWorkspaceWSMaxMessageBytes = 16 << 20

func (s *Server) proxyCodeWorkspaceLSP(w http.ResponseWriter, r *http.Request, sessionID string, proxy *application.CodeWorkspaceProxy, targetPath string) {
	target, err := url.Parse(proxy.BaseURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		writeCodeWorkspaceProblem(w, r, fmt.Errorf("%w: invalid Gateway endpoint", application.ErrCodeWorkspaceProxy))
		return
	}
	if target.Scheme == "http" {
		target.Scheme = "ws"
	} else if target.Scheme == "https" {
		target.Scheme = "wss"
	} else {
		writeCodeWorkspaceProblem(w, r, fmt.Errorf("%w: invalid Gateway scheme", application.ErrCodeWorkspaceProxy))
		return
	}
	target.Path = joinProxyPath(target.Path, targetPath)
	target.RawQuery = removeProxyToken(r.URL.RawQuery)
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+proxy.Token)
	upstream, _, err := websocket.DefaultDialer.DialContext(r.Context(), target.String(), headers)
	if err != nil {
		writeCodeWorkspaceProblem(w, r, fmt.Errorf("%w: Gateway LSP unavailable", application.ErrCodeWorkspaceUpstream))
		return
	}
	upgrader := websocket.Upgrader{
		ReadBufferSize:  32 * 1024,
		WriteBufferSize: 32 * 1024,
		CheckOrigin:     sameCodeWorkspaceOrigin,
	}
	browser, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		_ = upstream.Close()
		return
	}
	browser.SetReadLimit(codeWorkspaceWSMaxMessageBytes)
	upstream.SetReadLimit(codeWorkspaceWSMaxMessageBytes)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer browser.Close()
	defer upstream.Close()

	var closeOnce sync.Once
	closeConnections := func() {
		closeOnce.Do(func() {
			_ = browser.Close()
			_ = upstream.Close()
		})
	}
	pumpDone := make(chan struct{}, 2)
	limitExceeded := make(chan struct{}, 1)
	pump := func(dst, src *websocket.Conn) {
		defer func() { pumpDone <- struct{}{} }()
		for {
			messageType, payload, readErr := src.ReadMessage()
			if readErr != nil {
				if errors.Is(readErr, websocket.ErrReadLimit) {
					select {
					case limitExceeded <- struct{}{}:
					default:
					}
				}
				return
			}
			if writeErr := dst.WriteMessage(messageType, payload); writeErr != nil {
				return
			}
		}
	}
	go pump(upstream, browser)
	go pump(browser, upstream)
	go func() {
		ticker := time.NewTicker(codeWorkspaceWSAuthRecheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.codeWorkspaces.Recheck(ctx, sessionID); err != nil {
					closeConnections()
					_ = s.codeWorkspaces.CloseSession(context.WithoutCancel(r.Context()), sessionID)
					return
				}
			}
		}
	}()
	select {
	case <-limitExceeded:
		_ = s.codeWorkspaces.CloseSession(context.WithoutCancel(r.Context()), sessionID)
		cancel()
	case <-pumpDone:
		cancel()
	case <-r.Context().Done():
		cancel()
	}
	closeConnections()
}

func sameCodeWorkspaceOrigin(r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host && (u.Scheme == "http" || u.Scheme == "https")
}

func (s *Server) proxyCodeWorkspace(w http.ResponseWriter, r *http.Request, proxy *application.CodeWorkspaceProxy, targetPath string) {
	target, err := url.Parse(proxy.BaseURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		writeCodeWorkspaceProblem(w, r, fmt.Errorf("%w: invalid Gateway endpoint", application.ErrCodeWorkspaceProxy))
		return
	}
	target.Path = joinProxyPath(target.Path, targetPath)
	query := removeProxyToken(r.URL.RawQuery)
	reverse := &httputil.ReverseProxy{}
	reverse.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.URL.Path = target.Path
		req.URL.RawPath = ""
		req.URL.RawQuery = query
		req.Host = target.Host
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")
		req.Header.Del("Origin")
		req.Header.Set("Authorization", "Bearer "+proxy.Token)
	}
	reverse.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		writeCodeWorkspaceProblem(w, r, fmt.Errorf("%w: Gateway unavailable", application.ErrCodeWorkspaceUpstream))
	}
	reverse.ModifyResponse = func(resp *http.Response) error {
		setCodeWorkspaceResponseHeaders(resp.Header)
		return nil
	}
	reverse.ServeHTTP(w, r.Clone(r.Context()))
}

func setCodeWorkspaceResponseHeaders(headers http.Header) {
	headers.Set("Cache-Control", "no-store")
	headers.Set("Referrer-Policy", "no-referrer")
}

func codeWorkspaceGatewayBaseURL(_ *http.Request, sessionID string) string {
	return "/api/v1/code-workspaces/" + url.PathEscape(sessionID) + "/gateway"
}

func safeProxyPath(value string) bool {
	if value == "" || strings.ContainsRune(value, '\x00') || strings.Contains(value, "\\") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." || segment == "." {
			return false
		}
	}
	return strings.HasPrefix(value, "/")
}

func allowedCodeWorkspaceGatewayPath(rest, workspaceID string) bool {
	prefix := "/api/v1/workspaces/" + url.PathEscape(workspaceID)
	if rest == prefix || rest == prefix+"/" {
		return true
	}
	for _, suffix := range []string{"/fs/tree", "/fs/file", "/fs/stat", "/fs/search", "/fs/files", "/project", "/lsp"} {
		if rest == prefix+suffix {
			return true
		}
	}
	return false
}

func joinProxyPath(base, suffix string) string {
	base = strings.TrimRight(base, "/")
	if base == "" {
		base = "/"
	}
	return path.Join(base, "/"+strings.TrimLeft(suffix, "/"))
}

func removeProxyToken(rawQuery string) string {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return ""
	}
	values.Del("token")
	return values.Encode()
}

func writeCodeWorkspaceProblem(w http.ResponseWriter, r *http.Request, err error) {
	status, body := codeWorkspaceProblemBytes(err)
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func codeWorkspaceProblemBytes(err error) (int, []byte) {
	if errors.Is(err, application.ErrCodeWorkspaceForbidden) || errors.Is(err, application.ErrCodeWorkspaceProxy) {
		return renderProblem(http.StatusForbidden, "code_workspace_forbidden", "Code workspace forbidden", "代码工作台请求未通过 Agent、会话或目录边界校验")
	}
	if errors.Is(err, application.ErrCodeWorkspaceExpired) {
		return renderProblem(http.StatusGone, "code_workspace_expired", "Code workspace expired", "代码阅读会话已过期或 Gateway 已重启，请重新打开")
	}
	if errors.Is(err, application.ErrCodeWorkspaceUnavailable) {
		return renderProblem(http.StatusServiceUnavailable, "code_workspace_unavailable", "Code workspace unavailable", err.Error())
	}
	if errors.Is(err, application.ErrCodeWorkspaceUpstream) {
		return renderProblem(http.StatusBadGateway, "code_workspace_upstream_unavailable", "Code workspace upstream unavailable", "代码 Gateway 已退出或当前不可达，请重新打开")
	}
	return problemBytes(err)
}

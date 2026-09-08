package application

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/hostregistry"
)

const (
	// CodeWorkspaceTTL is deliberately finite. A browser that disappears without
	// sending DELETE must still release the Gateway workspace and its jdtls.
	CodeWorkspaceTTL           = 30 * time.Minute
	codeWorkspaceSweepInterval = time.Minute
	maxCodeWorkspaceSessions   = 32
)

var (
	// Access/session errors intentionally do not reveal whether the requested
	// Agent, conversation, or Run exists. Availability errors are mapped to a
	// retryable 503/502 by the HTTP layer.
	ErrCodeWorkspaceForbidden   = errors.New("code workspace access denied")
	ErrCodeWorkspaceExpired     = errors.New("code workspace session expired")
	ErrCodeWorkspaceProxy       = errors.New("code workspace proxy rejected")
	ErrCodeWorkspaceUnavailable = errors.New("code workspace gateway unavailable")
	ErrCodeWorkspaceUpstream    = errors.New("code workspace upstream unavailable")
)

// CodeWorkspaceGateway starts (or reuses) the local web-idea Gateway. The
// returned token is server-only and must never enter a browser response.
type CodeWorkspaceGateway func(context.Context) (baseURL, token string, err error)

// CodeWorkspaceCreateParams identifies the existing conversation/Run context.
// Root/path/cwd are intentionally absent: the HostRegistry is the sole source
// of the host-local directory.
type CodeWorkspaceCreateParams struct {
	ConversationID string `json:"conversation_id,omitempty"`
	RunID          string `json:"run_id,omitempty"`
}

type CodeWorkspace struct {
	ID                 string
	WorkspaceID        string
	AgentID            string
	ConversationID     string
	RunID              string
	GatewayWorkspaceID string
	RepositoryIdentity string
	Branch             string
	WorktreeRef        string
	RefKind            domain.RefKind
	GatewayStatus      string
	RootDisplay        string
	LSPEnabled         bool
	LSPStatus          string
	ReadOnly           bool
	ExpiresAt          time.Time
}

// CodeWorkspaceProxy is an internal server-side target. Token is deliberately
// kept out of CodeWorkspace and out of every public DTO.
type CodeWorkspaceProxy struct {
	BaseURL            string
	Token              string
	GatewayWorkspaceID string
}

type codeWorkspaceSession struct {
	CodeWorkspace
	Snapshot domain.ExecutionContextSnapshot
	BaseURL  string
	Token    string
	closing  bool
}

type CodeWorkspaceService struct {
	store    Store
	svc      *Service
	endpoint CodeWorkspaceGateway
	registry *hostregistry.Registry
	client   *http.Client

	mu          sync.Mutex
	sessions    map[string]*codeWorkspaceSession
	stop        chan struct{}
	done        chan struct{}
	closed      bool
	inflight    int
	createsDone chan struct{}
}

func NewCodeWorkspaceService(svc *Service, store Store, endpoint CodeWorkspaceGateway, registry *hostregistry.Registry) *CodeWorkspaceService {
	s := &CodeWorkspaceService{
		store: store, svc: svc, endpoint: endpoint, registry: registry,
		client: &http.Client{Timeout: 20 * time.Second}, sessions: make(map[string]*codeWorkspaceSession),
		stop: make(chan struct{}), done: make(chan struct{}),
		createsDone: closedSignal(),
	}
	go s.sweepLoop()
	return s
}

// Close releases every locally tracked Gateway workspace. It is safe to call
// during shutdown more than once.
func (s *CodeWorkspaceService) Close(ctx context.Context) {
	s.mu.Lock()
	s.closed = true
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	createsDone := s.createsDone
	s.mu.Unlock()
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	select {
	case <-createsDone:
	case <-shutdownCtx.Done():
	}
	s.mu.Lock()
	sessions := make([]*codeWorkspaceSession, 0, len(s.sessions))
	for id, session := range s.sessions {
		sessions = append(sessions, session)
		delete(s.sessions, id)
	}
	s.mu.Unlock()
	select {
	case <-s.done:
	case <-shutdownCtx.Done():
	}
	var wg sync.WaitGroup
	for _, session := range sessions {
		wg.Add(1)
		go func(session *codeWorkspaceSession) {
			defer wg.Done()
			_ = s.deleteGatewayWorkspace(shutdownCtx, session)
		}(session)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-shutdownCtx.Done():
	}
}

func closedSignal() chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}

func (s *CodeWorkspaceService) beginCreate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("%w: code workspace service is closed", ErrCodeWorkspaceExpired)
	}
	if s.inflight == 0 {
		s.createsDone = make(chan struct{})
	}
	s.inflight++
	return nil
}

func (s *CodeWorkspaceService) finishCreate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight > 0 {
		s.inflight--
		if s.inflight == 0 {
			close(s.createsDone)
		}
	}
}

func (s *CodeWorkspaceService) sweepLoop() {
	ticker := time.NewTicker(codeWorkspaceSweepInterval)
	defer ticker.Stop()
	defer close(s.done)
	for {
		select {
		case <-ticker.C:
			sweepCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			s.sweepExpired(sweepCtx)
			cancel()
		case <-s.stop:
			return
		}
	}
}

func (s *CodeWorkspaceService) sweepExpired(ctx context.Context) {
	now := time.Now().UTC()
	var expired []*codeWorkspaceSession
	s.mu.Lock()
	if len(s.sessions) == 0 {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	endpointReady := s.endpoint == nil
	currentBase, currentToken := "", ""
	if s.endpoint != nil {
		baseURL, token, err := s.endpoint(ctx)
		if err == nil {
			endpointReady = true
			currentBase, currentToken = strings.TrimRight(strings.TrimSpace(baseURL), "/"), token
		}
	}
	s.mu.Lock()
	for id, session := range s.sessions {
		if endpointReady && s.endpoint != nil &&
			(currentBase != session.BaseURL || currentToken != session.Token) {
			// The local codegateway Manager guarantees that a successful new
			// Endpoint means the old process group has already been reaped. Do
			// not send DELETE to the dead port, and free the stale local handle.
			delete(s.sessions, id)
			continue
		}
		if session.closing || !now.Before(session.ExpiresAt) {
			session.closing = true
			expired = append(expired, session)
		}
	}
	s.mu.Unlock()
	if !endpointReady {
		return
	}
	for _, session := range expired {
		if err := s.deleteGatewayWorkspace(ctx, session); err == nil {
			s.mu.Lock()
			if current := s.sessions[session.ID]; current == session {
				delete(s.sessions, session.ID)
			}
			s.mu.Unlock()
		}
	}
}

func (s *CodeWorkspaceService) Create(ctx context.Context, workspaceID, agentID string, p CodeWorkspaceCreateParams) (*CodeWorkspace, error) {
	if s == nil || s.endpoint == nil || s.registry == nil || s.store == nil || s.svc == nil {
		return nil, fmt.Errorf("%w: code gateway is not configured", domain.ErrCapabilityMissing)
	}
	if err := s.beginCreate(); err != nil {
		return nil, err
	}
	defer s.finishCreate()
	if workspaceID == "" || agentID == "" {
		return nil, fmt.Errorf("%w: workspace and agent are required", ErrCodeWorkspaceForbidden)
	}
	agent, err := s.authorizeAgent(ctx, workspaceID, agentID)
	if err != nil {
		return nil, err
	}
	snapshot, resolved, err := s.resolveContext(ctx, workspaceID, agentID, p)
	if err != nil {
		return nil, err
	}
	baseURL, token, err := s.endpoint(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCodeWorkspaceUnavailable, err)
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" || token == "" {
		return nil, fmt.Errorf("%w: gateway endpoint is incomplete", ErrCodeWorkspaceUnavailable)
	}
	// A successful new Endpoint proves the old local Gateway generation has
	// been reaped. Drop only handles whose base/token pair is confirmed stale;
	// an Endpoint error keeps old handles for retry instead.
	s.dropStaleSessions(baseURL, token)
	s.mu.Lock()
	capacityReached := len(s.sessions) >= maxCodeWorkspaceSessions
	s.mu.Unlock()
	if capacityReached {
		return nil, fmt.Errorf("%w: code workspace session limit reached", domain.ErrCapabilityMissing)
	}
	gateway, err := s.createGatewayWorkspace(ctx, baseURL, token, resolved.CWD)
	if err != nil {
		return nil, fmt.Errorf("%w: gateway workspace unavailable", ErrCodeWorkspaceUnavailable)
	}
	gatewayID := gateway.ID
	id, err := randomCodeWorkspaceID()
	if err != nil {
		_ = s.deleteGatewayWorkspace(ctx, &codeWorkspaceSession{BaseURL: baseURL, Token: token, CodeWorkspace: CodeWorkspace{GatewayWorkspaceID: gatewayID}})
		return nil, err
	}
	now := time.Now().UTC()
	session := &codeWorkspaceSession{
		CodeWorkspace: CodeWorkspace{
			ID: id, WorkspaceID: workspaceID, AgentID: agentID,
			ConversationID: p.ConversationID, RunID: p.RunID,
			GatewayWorkspaceID: gatewayID, RepositoryIdentity: snapshot.RepositoryIdentity,
			Branch: snapshot.BranchName, WorktreeRef: snapshot.WorktreeRef,
			RefKind: snapshot.RefKind, GatewayStatus: gateway.Status, RootDisplay: gateway.RootDisplay,
			LSPEnabled: gateway.LSPEnabled, LSPStatus: gateway.LSPStatus,
			ReadOnly: true, ExpiresAt: now.Add(CodeWorkspaceTTL),
		},
		Snapshot: *snapshot, BaseURL: baseURL, Token: token,
	}
	// Recheck the Agent after the potentially slow Gateway start. A disable or
	// role change racing creation must not publish a session for the old identity.
	if _, err := s.authorizeAgent(ctx, workspaceID, agent.ID); err != nil {
		_ = s.deleteGatewayWorkspace(ctx, session)
		return nil, err
	}
	if _, _, err := s.resolveSnapshot(ctx, session); err != nil {
		_ = s.deleteGatewayWorkspace(ctx, session)
		return nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.deleteGatewayWorkspace(cleanupCtx, session)
		cancel()
		return nil, fmt.Errorf("%w: code workspace service closed during creation", ErrCodeWorkspaceExpired)
	}
	if len(s.sessions) >= maxCodeWorkspaceSessions {
		s.mu.Unlock()
		_ = s.deleteGatewayWorkspace(ctx, session)
		return nil, fmt.Errorf("%w: code workspace session limit reached", domain.ErrCapabilityMissing)
	}
	s.sessions[id] = session
	s.mu.Unlock()
	return cloneCodeWorkspace(session), nil
}

func (s *CodeWorkspaceService) dropStaleSessions(baseURL, token string) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, session := range s.sessions {
		if session.BaseURL != baseURL || session.Token != token {
			delete(s.sessions, id)
		}
	}
}

func (s *CodeWorkspaceService) authorizeAgent(ctx context.Context, workspaceID, agentID string) (*domain.AgentProfile, error) {
	agent, err := s.store.Agents().Get(ctx, agentID)
	if err != nil || agent == nil || agent.WorkspaceID != workspaceID || agent.Kind != domain.AgentProfileKindUser ||
		agent.Availability != domain.AgentEnabled || strings.TrimSpace(agent.Role) != "developer" {
		return nil, fmt.Errorf("%w: 代码工作台只对已启用的用户管理 developer Agent 开放", ErrCodeWorkspaceForbidden)
	}
	return agent, nil
}

func (s *CodeWorkspaceService) resolveContext(ctx context.Context, workspaceID, agentID string, p CodeWorkspaceCreateParams) (*domain.ExecutionContextSnapshot, domain.ResolvedExecutionContext, error) {
	var run *domain.ExecutionRun
	if p.RunID != "" {
		var err error
		run, err = s.store.Runs().Get(ctx, p.RunID)
		if err != nil || run == nil || run.WorkspaceID != workspaceID || run.AgentProfileID != agentID {
			return nil, domain.ResolvedExecutionContext{}, fmt.Errorf("%w: Run 不属于当前 Agent 和 Workspace", ErrCodeWorkspaceForbidden)
		}
		if p.ConversationID != "" && run.WorkItemID != p.ConversationID {
			return nil, domain.ResolvedExecutionContext{}, fmt.Errorf("%w: Conversation 与 Run 不匹配", ErrCodeWorkspaceForbidden)
		}
	}
	if p.ConversationID != "" {
		wi, err := s.store.WorkItems().Get(ctx, p.ConversationID)
		if err != nil || wi == nil || wi.WorkspaceID != workspaceID || wi.AgentProfileID != agentID || wi.RecordKind != domain.RecordKindChat {
			return nil, domain.ResolvedExecutionContext{}, fmt.Errorf("%w: Conversation 不属于当前 Agent 和 Workspace", ErrCodeWorkspaceForbidden)
		}
	}
	if run != nil {
		if snap, err := s.store.ContextSnapshots().GetByRun(ctx, run.ID); err == nil {
			return s.resolveStoredSnapshot(ctx, snap)
		} else {
			return nil, domain.ResolvedExecutionContext{}, fmt.Errorf("%w: Run %s has no immutable execution context", domain.ErrWorkspaceContextMismatch, run.ID)
		}
	}
	if p.ConversationID != "" {
		c, _, err := s.svc.EffectiveDevelopmentContext(ctx, p.ConversationID)
		if err != nil {
			return nil, domain.ResolvedExecutionContext{}, err
		}
		return s.resolveDevelopmentContext(ctx, workspaceID, c)
	}
	loc, err := s.store.WorkspaceLocations().DefaultFor(ctx, workspaceID)
	if err != nil {
		return nil, domain.ResolvedExecutionContext{}, err
	}
	c := &domain.DevelopmentContext{WorkItemID: "code-workspace-default", WorkspaceLocationID: loc.ID, RefKind: domain.RefRoot, Version: loc.Version}
	return s.resolveDevelopmentContext(ctx, workspaceID, c)
}

func (s *CodeWorkspaceService) resolveStoredSnapshot(ctx context.Context, snap *domain.ExecutionContextSnapshot) (*domain.ExecutionContextSnapshot, domain.ResolvedExecutionContext, error) {
	if snap == nil || snap.WorkspaceID == "" {
		return nil, domain.ResolvedExecutionContext{}, fmt.Errorf("%w: Run execution context is unavailable", ErrCodeWorkspaceForbidden)
	}
	return s.resolveSnapshot(ctx, &codeWorkspaceSession{Snapshot: *snap})
}

func (s *CodeWorkspaceService) resolveSnapshot(ctx context.Context, session *codeWorkspaceSession) (*domain.ExecutionContextSnapshot, domain.ResolvedExecutionContext, error) {
	snapshot := &session.Snapshot
	if snapshot.ExecutionHostID != domain.LocalHostID {
		return nil, domain.ResolvedExecutionContext{}, fmt.Errorf("%w: 远程 Host 暂无代码阅读通道", ErrCodeWorkspaceForbidden)
	}
	resolved, err := s.registry.Resolve(snapshot)
	if err != nil {
		return nil, domain.ResolvedExecutionContext{}, fmt.Errorf("%w: code workspace directory is no longer available", domain.ErrWorkspaceContextMismatch)
	}
	return snapshot, resolved, nil
}

func (s *CodeWorkspaceService) resolveDevelopmentContext(ctx context.Context, workspaceID string, c *domain.DevelopmentContext) (*domain.ExecutionContextSnapshot, domain.ResolvedExecutionContext, error) {
	if c == nil || c.WorkspaceLocationID == "" {
		return nil, domain.ResolvedExecutionContext{}, fmt.Errorf("%w: workspace location is required", domain.ErrWorkspaceLocationRequired)
	}
	loc, err := s.store.WorkspaceLocations().Get(ctx, c.WorkspaceLocationID)
	if err != nil {
		return nil, domain.ResolvedExecutionContext{}, err
	}
	if loc.WorkspaceID != workspaceID {
		return nil, domain.ResolvedExecutionContext{}, fmt.Errorf("%w: location belongs to another workspace", domain.ErrWorkspaceContextMismatch)
	}
	if loc.ExecutionHostID != domain.LocalHostID {
		return nil, domain.ResolvedExecutionContext{}, fmt.Errorf("%w: 远程 Host 暂无代码阅读通道", ErrCodeWorkspaceForbidden)
	}
	host, err := s.store.ExecutionHosts().Get(ctx, loc.ExecutionHostID)
	if err != nil {
		return nil, domain.ResolvedExecutionContext{}, err
	}
	if host.Status == domain.HostStatusOffline {
		return nil, domain.ResolvedExecutionContext{}, fmt.Errorf("%w: host is offline", domain.ErrExecutionHostUnavailable)
	}
	mount, err := s.store.ExecutionHosts().GetMount(ctx, loc.ExecutionHostID, loc.MountAlias)
	if err != nil {
		return nil, domain.ResolvedExecutionContext{}, err
	}
	if mount.Status != domain.MountStatusReady || mount.RegistryGeneration != loc.MountGeneration || mount.RepositoryIdentity != loc.RepositoryIdentity {
		return nil, domain.ResolvedExecutionContext{}, fmt.Errorf("%w: location mount identity changed", domain.ErrWorkspaceContextMismatch)
	}
	if err := domain.ValidateRefCombo(c.RefKind, c.BranchName, c.CheckoutRef, c.WorktreeRef); err != nil {
		return nil, domain.ResolvedExecutionContext{}, err
	}
	snapshot := &domain.ExecutionContextSnapshot{
		ID: "code_workspace_context", SchemaVersion: domain.SnapshotSchemaV1, WorkspaceID: workspaceID,
		WorkspaceLocationID: loc.ID, LocationVersion: loc.Version, MountGeneration: loc.MountGeneration,
		ExecutionHostID: loc.ExecutionHostID, MountAlias: loc.MountAlias, RepositoryIdentity: loc.RepositoryIdentity,
		RefKind: c.RefKind, BranchName: c.BranchName, CheckoutRef: c.CheckoutRef, WorktreeRef: c.WorktreeRef,
		BaseRevision: c.BaseRevision, ContextGeneration: c.Version, Source: domain.SnapshotSourceCurrent,
		CreatedAt: time.Now().UTC(),
	}
	snapshot.SnapshotDigest = snapshot.ComputeDigest()
	return s.resolveSnapshot(ctx, &codeWorkspaceSession{Snapshot: *snapshot})
}

type gatewayWorkspaceInfo struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	RootDisplay string `json:"root_display"`
	LSPStatus   string `json:"lsp_status"`
	LSPEnabled  bool   `json:"lsp_enabled"`
}

func (s *CodeWorkspaceService) createGatewayWorkspace(ctx context.Context, baseURL, token, root string) (gatewayWorkspaceInfo, error) {
	createID, err := randomCodeWorkspaceID()
	if err != nil {
		return gatewayWorkspaceInfo{}, err
	}
	// The Gateway owner uses this stable key to make creation recoverable when
	// the HTTP response is interrupted, and to expire an unclaimed upstream
	// workspace. Current Gateways may ignore these fields; they are safe
	// additive protocol metadata and keep the orphan boundary explicit.
	payload, _ := json.Marshal(map[string]any{
		"root": root, "read_only": true, "client_key": createID,
		"ttl_seconds": int(CodeWorkspaceTTL / time.Second),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/workspaces", bytes.NewReader(payload))
	if err != nil {
		return gatewayWorkspaceInfo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-ATW-Code-Workspace-Create-ID", createID)
	resp, err := s.client.Do(req)
	if err != nil {
		return gatewayWorkspaceInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return gatewayWorkspaceInfo{}, fmt.Errorf("gateway create workspace status %d", resp.StatusCode)
	}
	var body gatewayWorkspaceInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil || body.ID == "" {
		return gatewayWorkspaceInfo{}, fmt.Errorf("gateway create workspace response invalid")
	}
	return body, nil
}

func (s *CodeWorkspaceService) deleteGatewayWorkspace(ctx context.Context, session *codeWorkspaceSession) error {
	if session == nil || session.BaseURL == "" || session.Token == "" || session.GatewayWorkspaceID == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, session.BaseURL+"/api/v1/workspaces/"+url.PathEscape(session.GatewayWorkspaceID), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+session.Token)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("gateway delete workspace status %d", resp.StatusCode)
	}
	return nil
}

func (s *CodeWorkspaceService) Get(ctx context.Context, id string) (*CodeWorkspace, error) {
	session, err := s.sessionForRequest(ctx, id)
	if err != nil {
		return nil, err
	}
	return cloneCodeWorkspace(session), nil
}

func (s *CodeWorkspaceService) Proxy(ctx context.Context, id string) (*CodeWorkspaceProxy, error) {
	session, err := s.sessionForRequest(ctx, id)
	if err != nil {
		return nil, err
	}
	return &CodeWorkspaceProxy{BaseURL: session.BaseURL, Token: session.Token, GatewayWorkspaceID: session.GatewayWorkspaceID}, nil
}

// Recheck is used by the long-lived LSP proxy on a bounded timer. It reuses
// the same Agent, ownership, Gateway generation, and HostRegistry checks as
// short HTTP requests without inspecting every WebSocket frame.
func (s *CodeWorkspaceService) Recheck(ctx context.Context, id string) error {
	_, err := s.sessionForRequest(ctx, id)
	return err
}

func (s *CodeWorkspaceService) CloseSession(ctx context.Context, id string) error {
	s.mu.Lock()
	session := s.sessions[id]
	if session != nil {
		session.closing = true
	}
	s.mu.Unlock()
	if session == nil {
		return nil
	}
	if err := s.deleteGatewayWorkspace(ctx, session); err != nil {
		// Keep the local record so an explicit retry or the TTL sweeper can
		// attempt the upstream release again after a transient Gateway error.
		return err
	}
	s.mu.Lock()
	if current := s.sessions[id]; current == session {
		delete(s.sessions, id)
	}
	s.mu.Unlock()
	return nil
}

func (s *CodeWorkspaceService) sessionForRequest(ctx context.Context, id string) (*codeWorkspaceSession, error) {
	s.mu.Lock()
	session := s.sessions[id]
	if session != nil {
		copy := *session
		session = &copy
	}
	s.mu.Unlock()
	if session == nil {
		return nil, fmt.Errorf("%w: session not found", ErrCodeWorkspaceExpired)
	}
	if !time.Now().UTC().Before(session.ExpiresAt) {
		_ = s.CloseSession(context.WithoutCancel(ctx), id)
		return nil, fmt.Errorf("%w: session expired", ErrCodeWorkspaceExpired)
	}
	if session.closing {
		return nil, fmt.Errorf("%w: session is closing", ErrCodeWorkspaceExpired)
	}
	// Endpoint is checked on every request so a Gateway crash/restart cannot
	// silently move an existing session to a new process and new credential.
	// The caller must reopen, which also creates a fresh upstream workspace.
	if s.endpoint != nil {
		baseURL, token, endpointErr := s.endpoint(ctx)
		if endpointErr != nil {
			return nil, fmt.Errorf("%w: Gateway generation unavailable", ErrCodeWorkspaceUpstream)
		}
		if strings.TrimRight(strings.TrimSpace(baseURL), "/") != session.BaseURL || token != session.Token {
			s.discardSession(id)
			return nil, fmt.Errorf("%w: Gateway 已重启，请重新打开代码工作台", ErrCodeWorkspaceExpired)
		}
	}
	if _, err := s.authorizeAgent(ctx, session.WorkspaceID, session.AgentID); err != nil {
		_ = s.CloseSession(context.WithoutCancel(ctx), id)
		return nil, err
	}
	if session.ConversationID != "" {
		wi, err := s.store.WorkItems().Get(ctx, session.ConversationID)
		if err != nil || wi.WorkspaceID != session.WorkspaceID || wi.AgentProfileID != session.AgentID {
			_ = s.CloseSession(context.WithoutCancel(ctx), id)
			return nil, fmt.Errorf("%w: conversation ownership changed", ErrCodeWorkspaceForbidden)
		}
	}
	if session.RunID != "" {
		run, err := s.store.Runs().Get(ctx, session.RunID)
		if err != nil || run.WorkspaceID != session.WorkspaceID || run.AgentProfileID != session.AgentID {
			_ = s.CloseSession(context.WithoutCancel(ctx), id)
			return nil, fmt.Errorf("%w: Run ownership changed", ErrCodeWorkspaceForbidden)
		}
	}
	if _, _, err := s.resolveSnapshot(ctx, session); err != nil {
		_ = s.CloseSession(context.WithoutCancel(ctx), id)
		return nil, err
	}
	return session, nil
}

func (s *CodeWorkspaceService) discardSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

func randomCodeWorkspaceID() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "cw_" + hex.EncodeToString(b), nil
}

func cloneCodeWorkspace(s *codeWorkspaceSession) *CodeWorkspace {
	if s == nil {
		return nil
	}
	v := s.CodeWorkspace
	return &v
}

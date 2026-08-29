package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/ybs/agent-team-workbench/internal/agentconfig"
	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/modelconfig"
	"github.com/ybs/agent-team-workbench/internal/security"
	"github.com/ybs/agent-team-workbench/internal/sse"
)

type ctxKey int

const ctxKeyRequestID ctxKey = iota

// AgentConfigSync 由 agentconfig.Importer 实现：agents/ 目录 ⇄ DB 投影的同步。
type AgentConfigSync interface {
	Import(ctx context.Context, workspaceID string) (agentconfig.ImportResult, error)
	WriteBackOne(ctx context.Context, a *domain.AgentProfile) error
}

// Server 承载 REST + SSE；M1 认证为演示用户（owner），RBAC 守卫已接入（M4 会话化）。
type Server struct {
	svc           *application.Service
	store         application.Store
	hub           *sse.Hub
	demoRole      domain.MemberRole
	agentCfg      AgentConfigSync
	models        *modelconfig.Registry
	credentials   *modelconfig.CredentialsStore
	workbenchRoot string
}

func NewServer(svc *application.Service, store application.Store, hub *sse.Hub) *Server {
	return &Server{svc: svc, store: store, hub: hub, demoRole: domain.RoleOwner}
}

// SetDemoRole 仅用于测试 RBAC 守卫；生产由会话中间件注入角色。
func (s *Server) SetDemoRole(role domain.MemberRole) { s.demoRole = role }

// SetAgentConfigSync 挂载 agents/ 目录同步器（可选；未挂载时配置只落 DB）。
func (s *Server) SetAgentConfigSync(sync AgentConfigSync) { s.agentCfg = sync }

// SetModelRegistry 挂载 models/ 模型注册表（可选；未挂载时 /models 端点返回 404）。
func (s *Server) SetModelRegistry(reg *modelconfig.Registry) { s.models = reg }

// SetCredentialsStore 挂载 models/credentials.local.yaml 本地凭据存储。
func (s *Server) SetCredentialsStore(store *modelconfig.CredentialsStore) { s.credentials = store }

// SetWorkbenchRoot 用于解析 DSH agent preset 目录等相对路径。
func (s *Server) SetWorkbenchRoot(root string) { s.workbenchRoot = root }

// guard 按权限点拦截命令；拒绝时不泄漏资源是否存在（统一 403）。
func (s *Server) guard(perm string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !security.Allow(s.demoRole, perm) {
			writeProblem(w, r, Problem{
				Type:  "https://workbench.example/problems/forbidden",
				Title: "Forbidden", Status: http.StatusForbidden,
				Code: "forbidden", Detail: "当前角色无权执行该操作",
			})
			return
		}
		next(w, r)
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/me", s.guard(security.PermRead, s.handleMe))
	// /health 与 /runtimes/dsh/catalog 在 openapi 中声明为公开端点（security: []），不挂守卫。
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/workspaces", s.guard(security.PermRead, s.handleListWorkspaces))
	mux.HandleFunc("PATCH /api/v1/workspaces/{workspace_id}", s.guard(security.PermWorkspaceAdmin, s.handlePatchWorkspace))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/bootstrap", s.guard(security.PermRead, s.handleBootstrap))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/dashboard", s.guard(security.PermRead, s.handleDashboard))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/activities", s.guard(security.PermRead, s.handleActivities))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/events", s.guard(security.PermRead, s.handleSSE))

	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/agent-profiles", s.guard(security.PermRead, s.handleListAgents))
	mux.HandleFunc("POST /api/v1/workspaces/{workspace_id}/agent-profiles", s.guard(security.PermAgentWrite, s.handleCreateAgent))
	mux.HandleFunc("POST /api/v1/workspaces/{workspace_id}/agent-config/reload", s.guard(security.PermAgentWrite, s.handleReloadAgentConfig))
	mux.HandleFunc("GET /api/v1/agent-profiles/{agent_profile_id}", s.guard(security.PermRead, s.handleGetAgent))
	mux.HandleFunc("PATCH /api/v1/agent-profiles/{agent_profile_id}", s.guard(security.PermAgentWrite, s.handlePatchAgent))
	mux.HandleFunc("POST /api/v1/agent-profiles/{agent_profile_id}/commands/enable", s.guard(security.PermAgentWrite, s.handleEnableAgent))
	mux.HandleFunc("POST /api/v1/agent-profiles/{agent_profile_id}/commands/disable", s.guard(security.PermAgentWrite, s.handleDisableAgent))
	mux.HandleFunc("POST /api/v1/agent-profiles/{agent_profile_id}/commands/wake", s.guard(security.PermAgentWrite, s.handleWakeAgent))
	mux.HandleFunc("GET /api/v1/agent-profiles/{agent_profile_id}/task-sessions", s.guard(security.PermRead, s.handleListTaskSessions))
	mux.HandleFunc("POST /api/v1/agent-profiles/{agent_profile_id}/task-sessions/reset", s.guard(security.PermAgentWrite, s.handleResetTaskSession))

	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/work-items", s.guard(security.PermRead, s.handleListWorkItems))
	mux.HandleFunc("POST /api/v1/workspaces/{workspace_id}/work-items", s.guard(security.PermWorkItemWrite, s.handleCreateWorkItem))
	mux.HandleFunc("GET /api/v1/work-items/{work_item_id}", s.guard(security.PermRead, s.handleGetWorkItem))
	mux.HandleFunc("PATCH /api/v1/work-items/{work_item_id}", s.guard(security.PermWorkItemWrite, s.handlePatchWorkItem))
	mux.HandleFunc("POST /api/v1/work-items/{work_item_id}/commands/move", s.guard(security.PermWorkItemWrite, s.handleMoveWorkItem))
	mux.HandleFunc("POST /api/v1/work-items/{work_item_id}/commands/assign", s.guard(security.PermWorkItemWrite, s.handleAssignWorkItem))
	mux.HandleFunc("POST /api/v1/work-items/{work_item_id}/commands/claim", s.guard(security.PermWorkItemWrite, s.handleClaimWorkItem))
	mux.HandleFunc("POST /api/v1/work-items/{work_item_id}/commands/return", s.guard(security.PermWorkItemWrite, s.handleReturnWorkItem))
	mux.HandleFunc("POST /api/v1/work-items/{work_item_id}/commands/block", s.guard(security.PermWorkItemWrite, s.handleBlockWorkItem))
	mux.HandleFunc("POST /api/v1/work-items/{work_item_id}/commands/unblock", s.guard(security.PermWorkItemWrite, s.handleUnblockWorkItem))
	mux.HandleFunc("POST /api/v1/work-items/{work_item_id}/commands/accept", s.guard(security.PermApproval, s.handleAcceptWorkItem))
	mux.HandleFunc("GET /api/v1/work-items/{work_item_id}/tree", s.guard(security.PermRead, s.handleWorkItemTree))
	mux.HandleFunc("GET /api/v1/work-items/{work_item_id}/plan", s.guard(security.PermRead, s.handleWorkItemPlan))

	mux.HandleFunc("POST /api/v1/workspaces/{workspace_id}/plans", s.guard(security.PermWorkItemWrite, s.handleCreatePlan))
	mux.HandleFunc("GET /api/v1/plans/{plan_id}", s.guard(security.PermRead, s.handleGetPlan))

	mux.HandleFunc("POST /api/v1/work-items/{work_item_id}/runs", s.guard(security.PermRunControl, s.handleCreateRun))
	mux.HandleFunc("GET /api/v1/work-items/{work_item_id}/runs", s.guard(security.PermRead, s.handleListWorkItemRuns))
	mux.HandleFunc("GET /api/v1/work-items/{work_item_id}/dispatches", s.guard(security.PermRead, s.handleListWorkItemDispatches))
	mux.HandleFunc("GET /api/v1/runs/{run_id}", s.guard(security.PermRead, s.handleGetRun))
	mux.HandleFunc("GET /api/v1/runs/{run_id}/events", s.guard(security.PermRead, s.handleListRunEvents))
	mux.HandleFunc("POST /api/v1/runs/{run_id}/commands/input", s.guard(security.PermRunControl, s.handleRunInput))
	mux.HandleFunc("POST /api/v1/runs/{run_id}/commands/interrupt", s.guard(security.PermRunControl, s.handleInterruptRun))
	mux.HandleFunc("POST /api/v1/runs/{run_id}/commands/cancel", s.guard(security.PermRunControl, s.handleCancelRun))
	mux.HandleFunc("POST /api/v1/runs/{run_id}/commands/retry", s.guard(security.PermRunControl, s.handleRetryRun))
	mux.HandleFunc("POST /api/v1/runs/{run_id}/commands/resume", s.guard(security.PermRunControl, s.handleResumeRun))
	mux.HandleFunc("GET /api/v1/runs/{run_id}/approvals", s.guard(security.PermRead, s.handleListApprovals))
	mux.HandleFunc("POST /api/v1/runs/{run_id}/approvals/{approval_id}/commands/resolve", s.guard(security.PermApproval, s.handleResolveApproval))
	mux.HandleFunc("GET /api/v1/runs/{run_id}/artifacts", s.guard(security.PermRead, s.handleListArtifacts))
	mux.HandleFunc("GET /api/v1/runs/{run_id}/changes", s.guard(security.PermRead, s.handleRunChanges))
	mux.HandleFunc("GET /api/v1/runs/{run_id}/changes/diff", s.guard(security.PermRead, s.handleRunChangeDiff))
	mux.HandleFunc("POST /api/v1/runs/{run_id}/commands/revert-changes", s.guard(security.PermRunControl, s.handleRevertRunChanges))

	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/runtime-bindings", s.guard(security.PermRead, s.handleListBindings))
	mux.HandleFunc("POST /api/v1/workspaces/{workspace_id}/runtime-bindings", s.guard(security.PermRuntimeManage, s.handleCreateBinding))
	mux.HandleFunc("PATCH /api/v1/runtime-bindings/{runtime_binding_id}", s.guard(security.PermRuntimeManage, s.handlePatchBinding))
	mux.HandleFunc("POST /api/v1/runtime-bindings/{runtime_binding_id}/commands/probe", s.guard(security.PermRuntimeManage, s.handleProbeBinding))

	// 模型注册表（models/ 目录为真相源，全局共享不按 workspace 隔离）。
	mux.HandleFunc("GET /api/v1/models", s.guard(security.PermRead, s.handleListModels))
	mux.HandleFunc("POST /api/v1/models", s.guard(security.PermRuntimeManage, s.handleCreateModel))
	mux.HandleFunc("PUT /api/v1/models/{model_id}", s.guard(security.PermRuntimeManage, s.handleUpsertModel))
	mux.HandleFunc("DELETE /api/v1/models/{model_id}", s.guard(security.PermRuntimeManage, s.handleDeleteModel))
	// 凭据明文回显与写入同级管控（PermRuntimeManage；仅本机工作台定位，回显行为不变）。
	mux.HandleFunc("GET /api/v1/models/provider-credentials", s.guard(security.PermRuntimeManage, s.handleGetProviderCredential))
	mux.HandleFunc("PUT /api/v1/models/provider-credentials", s.guard(security.PermRuntimeManage, s.handlePutProviderCredential))

	mux.HandleFunc("GET /api/v1/runtimes/dsh/catalog", s.handleDSHCatalog)

	return s.withRequestID(mux)
}

func (s *Server) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get("X-Request-Id")
		if rid == "" {
			rid = domain.NewID("req_")
		}
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, rid)
		w.Header().Set("X-Request-Id", rid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// idempotencyClaimer 是 IdempotencyRepo 的可选扩展（sqlstore 已实现）：
// claim-first 原子幂等——请求先 INSERT 占位行（status_code=NULL 表示执行中）再执行，
// 消除旧 Check→exec→Record 间隙中并发同 key 双执行的竞态。
type idempotencyClaimer interface {
	// Claim 返回 true 表示占位成功（调用方独占执行权）；
	// 返回 false 时 rec 非 nil：rec.StatusCode > 0 为已完成响应（可重放），
	// rec.StatusCode == 0 表示同 hash 请求仍在执行中。
	Claim(ctx context.Context, workspaceID, key, requestHash string) (bool, *application.IdempotencyRecord, error)
	// Complete 把执行结果写回占位行（exec 产出确定性状态码后调用）。
	Complete(ctx context.Context, workspaceID, key string, statusCode int, resultBody string) error
	// Release 删除未完成占位行（exec 返回 5xx 时调用，允许客户端同 key 重试）。
	Release(ctx context.Context, workspaceID, key string) error
}

// idempotent 包装写命令：校验 Idempotency-Key，重放或冲突（协议文档 §5.1）。
// 存储实现 idempotencyClaimer 时走 claim-first 原子路径；否则退回 Check→exec→Record
// （非原子，仅用于未实现 claimer 的测试替身）。
func (s *Server) idempotent(w http.ResponseWriter, r *http.Request, scope string, exec func() (int, []byte)) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeProblem(w, r, Problem{
			Type:  "https://workbench.example/problems/missing-idempotency-key",
			Title: "Idempotency-Key required", Status: http.StatusBadRequest,
			Code: "missing_idempotency_key", Detail: "所有写命令必须携带 Idempotency-Key",
		})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		fail(w, r, err)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	h := sha256.Sum256(append([]byte(r.Method+" "+r.URL.Path+" "), body...))
	reqHash := hex.EncodeToString(h[:])

	if c, ok := s.store.Idempotency().(idempotencyClaimer); ok {
		s.idempotentClaimFirst(c, w, r, scope, key, reqHash, exec)
		return
	}

	// 兼容路径：Check→exec→Record（行为与旧版一致）。
	rec, err := s.store.Idempotency().Check(r.Context(), scope, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	if rec != nil {
		if rec.RequestHash != reqHash {
			fail(w, r, domain.ErrIdempotencyConflict)
			return
		}
		// 重放原结果，不重复副作用。
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Idempotent-Replayed", "true")
		w.WriteHeader(rec.StatusCode)
		_, _ = w.Write([]byte(rec.ResultBody))
		return
	}

	status, result := exec()
	if status < 500 {
		_ = s.store.Idempotency().Record(r.Context(), scope, key, application.IdempotencyRecord{
			RequestHash: reqHash, StatusCode: status, ResultBody: string(result),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(result)
}

// idempotentClaimFirst：占位→执行→落盘/释放。
//   - 占位冲突：hash 不同 → 409 冲突；hash 相同且已完成 → 重放原响应；
//     hash 相同但仍在执行 → 409 idempotency_in_progress（Retryable，客户端稍后重试）。
//   - exec 结果 < 500（含 4xx problem）视为确定性结果，Complete 落盘可重放；
//     ≥ 500 视为基础设施故障，Release 释放占位行，允许客户端以同 key 重试。
func (s *Server) idempotentClaimFirst(c idempotencyClaimer, w http.ResponseWriter, r *http.Request,
	scope, key, reqHash string, exec func() (int, []byte)) {
	claimed, rec, err := c.Claim(r.Context(), scope, key, reqHash)
	if err != nil {
		fail(w, r, err)
		return
	}
	if !claimed {
		if rec.RequestHash != reqHash {
			fail(w, r, domain.ErrIdempotencyConflict)
			return
		}
		if rec.StatusCode > 0 {
			// 重放原结果，不重复副作用。
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Idempotent-Replayed", "true")
			w.WriteHeader(rec.StatusCode)
			_, _ = w.Write([]byte(rec.ResultBody))
			return
		}
		writeProblem(w, r, Problem{
			Type:  "https://workbench.example/problems/idempotency-in-progress",
			Title: "Request already in progress", Status: http.StatusConflict,
			Code: "idempotency_in_progress", Retryable: true,
			Detail: "相同 Idempotency-Key 的请求正在执行中，请稍后重试",
		})
		return
	}

	status, result := exec()
	if status < 500 {
		_ = c.Complete(r.Context(), scope, key, status, string(result))
	} else {
		_ = c.Release(r.Context(), scope, key)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(result)
}

// renderJSON 在 exec 闭包内序列化响应与错误。
func renderJSON(w http.ResponseWriter, r *http.Request, status int, v any) (int, []byte) {
	b, err := json.Marshal(v)
	if err != nil {
		return http.StatusInternalServerError, []byte(`{"error":"internal"}`)
	}
	return status, b
}

func renderProblem(status int, code, title, detail string) (int, []byte) {
	b, _ := json.Marshal(Problem{
		Type:  "https://workbench.example/problems/" + code,
		Title: title, Status: status, Code: code, Detail: detail,
	})
	return status, b
}

// ── Workspace / Dashboard / Activities / SSE ─────────────────────────

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id": "user_demo", "name": "Demo User", "role": "owner",
		"feature_flags": map[string]bool{"runtime_bridge": true},
	})
}

// handleHealth 分层健康投影（协议文档 §2.1：System Online 不是固定文案）。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ids, err := s.store.Workspaces().ListIDs(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	runners := []map[string]any{}
	for _, wsID := range ids {
		list, err := s.store.Runners().List(r.Context(), wsID)
		if err != nil {
			fail(w, r, err)
			return
		}
		for _, rn := range list {
			runners = append(runners, map[string]any{"runner_id": rn.ID, "status": rn.Status})
		}
	}
	// 对齐 openapi HealthStatus：{health:{control_plane, runners}}（bootstrap 内嵌 health 仍为扁平投影）。
	writeJSON(w, http.StatusOK, map[string]any{
		"health": map[string]any{
			"control_plane": "healthy",
			"runners":       runners,
		},
	})
}

func (s *Server) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	ids, err := s.store.Workspaces().ListIDs(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	items := []workspaceDTO{}
	for _, id := range ids {
		ws, err := s.store.Workspaces().Get(r.Context(), id)
		if err != nil {
			fail(w, r, err)
			return
		}
		items = append(items, toWorkspaceDTO(ws))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) dashboardData(ctx context.Context, workspaceID string) (map[string]any, error) {
	counts, err := s.store.WorkItems().BoardCounts(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	agents, err := s.store.Agents().List(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	active := 0
	for _, a := range agents {
		if a.Availability == domain.AgentEnabled {
			active++
		}
	}
	running, err := s.store.Runs().ActiveCount(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	dayStart := time.Now().UTC().Truncate(24 * time.Hour)
	completed, err := s.store.WorkItems().CompletedToday(ctx, workspaceID, dayStart)
	if err != nil {
		return nil, err
	}
	acts, err := s.store.Events().ListActivities(ctx, workspaceID, 10)
	if err != nil {
		return nil, err
	}
	recent := make([]activityDTO, 0, len(acts))
	for _, a := range acts {
		recent = append(recent, toActivityDTO(a))
	}
	return map[string]any{
		"active_agents":   active,
		"running_tasks":   running,
		"completed_today": completed,
		"board_counts": map[string]int{
			"todo": counts[domain.WorkItemTodo], "in_progress": counts[domain.WorkItemInProgress],
			"blocked": counts[domain.WorkItemBlocked], "completed": counts[domain.WorkItemCompleted],
		},
		"recent_activities": recent,
	}, nil
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	wsID := r.PathValue("workspace_id")
	data, err := s.dashboardData(r.Context(), wsID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, data)
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	wsID := r.PathValue("workspace_id")
	ws, err := s.store.Workspaces().Get(r.Context(), wsID)
	if err != nil {
		fail(w, r, err)
		return
	}
	dash, err := s.dashboardData(r.Context(), wsID)
	if err != nil {
		fail(w, r, err)
		return
	}
	agents, err := s.svc.Agents(r.Context(), wsID)
	if err != nil {
		fail(w, r, err)
		return
	}
	agentDTOs := make([]agentDTO, 0, len(agents))
	for _, a := range agents {
		agentDTOs = append(agentDTOs, toAgentDTO(a))
	}
	items, _, err := s.svc.WorkItems(r.Context(), wsID, application.WorkItemFilter{Limit: 100})
	if err != nil {
		fail(w, r, err)
		return
	}
	wiDTOs := make([]workItemDTO, 0, len(items))
	for _, wi := range items {
		wiDTOs = append(wiDTOs, toWorkItemDTO(wi))
	}
	cursor, err := s.store.Events().LatestSeq(r.Context(), wsID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workspace":    toWorkspaceDTO(ws),
		"dashboard":    dash,
		"agents":       map[string]any{"items": agentDTOs},
		"work_items":   map[string]any{"items": wiDTOs},
		"health":       map[string]any{"control_plane": "healthy", "runners": []any{}},
		"event_cursor": cursor,
	})
}

func (s *Server) handleActivities(w http.ResponseWriter, r *http.Request) {
	wsID := r.PathValue("workspace_id")
	acts, err := s.store.Events().ListActivities(r.Context(), wsID, 50)
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]activityDTO, 0, len(acts))
	for _, a := range acts {
		items = append(items, toActivityDTO(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nil})
}

// handleSSE 事件流：Last-Event-ID / ?cursor= 补发，heartbeat 15s（协议文档 §6）。
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	wsID := r.PathValue("workspace_id")
	if _, err := s.store.Workspaces().Get(r.Context(), wsID); err != nil {
		fail(w, r, err)
		return
	}
	after := int64(0)
	if lid := r.Header.Get("Last-Event-ID"); lid != "" {
		after, _ = strconv.ParseInt(lid, 10, 64)
	} else if c := r.URL.Query().Get("cursor"); c != "" {
		after, _ = strconv.ParseInt(c, 10, 64)
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, r, fmt.Errorf("%w: streaming unsupported", domain.ErrValidation))
		return
	}

	// 预检游标保留窗口：过期直接 410。
	if _, err := s.store.Events().Since(r.Context(), wsID, after, 1); err != nil {
		fail(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()

	last := after
	sendBacklog := func() error {
		events, err := s.store.Events().Since(r.Context(), wsID, last, 500)
		if err != nil {
			return err
		}
		for _, ev := range events {
			data, err := json.Marshal(ev)
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.StreamSeq, ev.Type, data)
			last = ev.StreamSeq
		}
		flusher.Flush()
		return nil
	}
	if err := sendBacklog(); err != nil {
		return
	}

	wake, unsub := s.hub.Subscribe(wsID)
	defer unsub()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-wake:
			if err := sendBacklog(); err != nil {
				return
			}
		case <-heartbeat.C:
			fmt.Fprintf(w, ": heartbeat %s\n\n", time.Now().UTC().Format(time.RFC3339))
			flusher.Flush()
			if err := sendBacklog(); err != nil {
				return
			}
		}
	}
}

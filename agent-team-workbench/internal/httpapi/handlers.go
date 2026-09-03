package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// ── AgentProfile ─────────────────────────────────────────────────────

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.svc.Agents(r.Context(), r.PathValue("workspace_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]agentDTO, 0, len(agents))
	for _, a := range agents {
		items = append(items, toAgentDTO(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	wsID := r.PathValue("workspace_id")
	s.idempotent(w, r, wsID, func() (int, []byte) {
		var durable AgentConfigIntentSync
		if s.agentCfg != nil {
			var ok bool
			durable, ok = s.agentCfg.(AgentConfigIntentSync)
			if !ok {
				return renderProblem(http.StatusInternalServerError, "agent_config_sync_not_durable",
					"Agent configuration sync unavailable",
					"configured Agent file synchronizer does not support durable recovery")
			}
		}
		var req createAgentRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		params := application.CreateAgentParams{
			Name: req.Name, Role: req.Role, Skills: req.Skills, Avatar: req.Avatar,
		}
		if req.RuntimePreference != nil {
			params.RuntimePreference = domain.RuntimePreference{
				Preferred:   req.RuntimePreference.Preferred,
				Fallbacks:   req.RuntimePreference.Fallbacks,
				Mode:        req.RuntimePreference.Mode,
				AgentPreset: req.RuntimePreference.AgentPreset,
			}
		}
		a, err := s.svc.CreateAgent(r.Context(), wsID, params)
		if err != nil {
			return problemBytes(err)
		}
		if durable != nil {
			if syncErr := durable.ReconcileAgent(r.Context(), a.ID); syncErr != nil {
				if errors.Is(syncErr, domain.ErrAgentConfigSyncConflict) {
					return s.agentConfigIntentFailure(syncErr)
				}
				// The DB Agent and its intent are already durable. Keep this
				// response below 500 so idempotency stores it and a retry with
				// the same key cannot create a second Agent; startup/reload can
				// replay the pending external bundle later.
				return renderJSON(w, r, http.StatusAccepted, struct {
					agentDTO
					ConfigSyncPending bool `json:"config_sync_pending"`
				}{agentDTO: toAgentDTO(a), ConfigSyncPending: true})
			}
		}
		return renderJSON(w, r, http.StatusCreated, toAgentDTO(a))
	})
}

func (s *Server) handleEnableAgent(w http.ResponseWriter, r *http.Request) {
	s.setAvailability(w, r, true)
}

func (s *Server) handleDisableAgent(w http.ResponseWriter, r *http.Request) {
	s.setAvailability(w, r, false)
}

// ── TaskSession（会话锚点）───────────────────────────────────────────

func (s *Server) handleListTaskSessions(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_profile_id")
	agent, err := s.svc.Agent(r.Context(), agentID)
	if err != nil {
		fail(w, r, err)
		return
	}
	sessions, err := s.svc.TaskSessionsByAgent(r.Context(), agent.WorkspaceID, agentID)
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]taskSessionDTO, 0, len(sessions))
	for _, t := range sessions {
		if t.SessionRef() == "" {
			continue // 墓碑行（reset/清除后）不展示
		}
		items = append(items, toTaskSessionDTO(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleResetTaskSession(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_profile_id")
	s.idempotent(w, r, agentID, func() (int, []byte) {
		var req resetTaskSessionRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		if req.TaskKey == "" || req.AdapterID == "" {
			return renderProblem(http.StatusBadRequest, "bad_request", "task_key and adapter_id are required", "")
		}
		agent, err := s.svc.Agent(r.Context(), agentID)
		if err != nil {
			return problemBytes(err)
		}
		if err := s.svc.ResetTaskSession(r.Context(), agent.WorkspaceID, agentID, req.AdapterID, req.TaskKey); err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, map[string]any{"status": "reset"})
	})
}

func (s *Server) setAvailability(w http.ResponseWriter, r *http.Request, enabled bool) {
	agentID := r.PathValue("agent_profile_id")
	s.idempotent(w, r, agentID, func() (int, []byte) {
		a, err := s.svc.SetAgentAvailability(r.Context(), agentID, enabled)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, toAgentDTO(a))
	})
}

// ── WorkItem ─────────────────────────────────────────────────────────

func parseWorkItemRecordKind(raw string) (domain.WorkItemRecordKind, error) {
	if raw == "" {
		return "", nil
	}
	kind := domain.WorkItemRecordKind(raw)
	if !kind.Valid() {
		return "", fmt.Errorf("%w: record_kind 必须为 chat 或 task", domain.ErrValidation)
	}
	return kind, nil
}

func normalizedWorkItemRecordKind(w *domain.WorkItem) domain.WorkItemRecordKind {
	if w == nil || w.RecordKind == "" {
		return domain.RecordKindTask
	}
	return w.RecordKind
}

func validatePublicRootAcceptanceCriteria(criteria []string) error {
	if len(criteria) == 0 {
		return fmt.Errorf("%w: 根 Task 至少需要一条验收标准", domain.ErrValidation)
	}
	if len(criteria) > 64 {
		return fmt.Errorf("%w: 根 Task 验收标准最多 64 条", domain.ErrValidation)
	}
	for i, criterion := range criteria {
		if strings.TrimSpace(criterion) == "" {
			return fmt.Errorf("%w: 根 Task 验收标准第 %d 条不能为空", domain.ErrValidation, i+1)
		}
		if !utf8.ValidString(criterion) || utf8.RuneCountInString(strings.TrimSpace(criterion)) > 2000 {
			return fmt.Errorf("%w: 根 Task 验收标准第 %d 条超过 2000 个字符或不是有效 UTF-8", domain.ErrValidation, i+1)
		}
	}
	return nil
}

func requireTaskWorkItemHTTP(w *domain.WorkItem) error {
	if w == nil {
		return fmt.Errorf("%w: work item required", domain.ErrValidation)
	}
	kind := normalizedWorkItemRecordKind(w)
	if !kind.Valid() {
		return fmt.Errorf("%w: work item %s 的 record_kind 无效", domain.ErrValidation, w.ID)
	}
	if kind != domain.RecordKindTask {
		return fmt.Errorf("%w: record_kind=chat 不支持任务操作", domain.ErrValidation)
	}
	return nil
}

func (s *Server) handleListWorkItems(w http.ResponseWriter, r *http.Request) {
	wsID := r.PathValue("workspace_id")
	recordKind, err := parseWorkItemRecordKind(r.URL.Query().Get("record_kind"))
	if err != nil {
		fail(w, r, err)
		return
	}
	if recordKind == "" {
		// The public task-list endpoint is task-only by default. Chat callers
		// must opt into the independent conversation surface explicitly.
		recordKind = domain.RecordKindTask
	}
	f := application.WorkItemFilter{
		Status:     domain.WorkItemStatus(r.URL.Query().Get("status")),
		Priority:   domain.Priority(r.URL.Query().Get("priority")),
		Assignee:   r.URL.Query().Get("assignee"),
		RecordKind: recordKind,
		// parent_id：缺省不过滤；none = 只看根任务（无父链接）。
		ParentID: r.URL.Query().Get("parent_id"),
		Cursor:   r.URL.Query().Get("cursor"),
	}
	items, next, err := s.svc.WorkItems(r.Context(), wsID, f)
	if err != nil {
		fail(w, r, err)
		return
	}
	dtos := make([]workItemDTO, 0, len(items))
	for _, wi := range items {
		dtos = append(dtos, s.enrichWorkItem(r, wi))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": dtos, "next_cursor": nullableString(next)})
}

func (s *Server) handleGetWorkItem(w http.ResponseWriter, r *http.Request) {
	wi, err := s.svc.WorkItem(r.Context(), r.PathValue("work_item_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	dto := s.enrichWorkItem(r, wi)
	// 台账摘要只在详情响应携带（S2）：enrichWorkItem 被列表路径共用，
	// 4KB 级摘要不得进列表/bootstrap 载荷。
	if normalizedWorkItemRecordKind(wi) == domain.RecordKindTask {
		dto.RollingDigest = wi.RollingDigest
	}
	writeJSON(w, http.StatusOK, dto)
}

// enrichWorkItem 附带 blocker 与 run 计数（只读投影）。
func (s *Server) enrichWorkItem(r *http.Request, wi *domain.WorkItem) workItemDTO {
	dto := toWorkItemDTO(wi)
	if b, err := s.store.WorkItems().ActiveBlocker(r.Context(), wi.ID); err == nil && b != nil {
		dto.Blocker = &blockerDTO{Code: b.Code, Message: b.Message, Source: b.Source, CreatedAt: b.CreatedAt}
	}
	if latest, count, err := s.store.WorkItems().LatestRunID(r.Context(), wi.ID); err == nil {
		dto.LatestRunID = latest
		dto.RunsCount = count
	}
	return dto
}

func (s *Server) handleCreateWorkItem(w http.ResponseWriter, r *http.Request) {
	wsID := r.PathValue("workspace_id")
	s.idempotent(w, r, wsID, func() (int, []byte) {
		var req createWorkItemRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		recordKind, err := parseWorkItemRecordKind(req.RecordKind)
		if err != nil {
			return problemBytes(err)
		}
		if recordKind == "" {
			// The public create contract is task-first. Chat callers must opt into
			// the independent conversation surface with record_kind=chat.
			recordKind = domain.RecordKindTask
		}
		if recordKind == domain.RecordKindTask && strings.TrimSpace(req.AgentProfileID) != "" {
			return renderProblem(http.StatusUnprocessableEntity, "validation_failed", "Task assignment is coordinator-owned", "Task 不接受手工 agent_profile_id，由系统 Coordinator 自动选择 Agent")
		}
		if recordKind == domain.RecordKindTask && req.ParentID == "" && req.Status != "" &&
			domain.WorkItemStatus(req.Status) != domain.WorkItemTodo {
			return renderProblem(http.StatusUnprocessableEntity, "validation_failed", "Invalid initial Task status", "根 Task 创建时 status 必须为 todo，由系统 Coordinator 推进任务状态")
		}
		if recordKind == domain.RecordKindTask && req.ParentID == "" {
			if err := validatePublicRootAcceptanceCriteria(req.AcceptanceCriteria); err != nil {
				return problemBytes(err)
			}
		}
		p := application.CreateWorkItemParams{
			Title: req.Title, Description: req.Description,
			Status: domain.WorkItemStatus(req.Status), Priority: domain.Priority(req.Priority),
			AgentProfileID: req.AgentProfileID, ParentID: req.ParentID, ClientKey: req.ClientKey,
			RecordKind: recordKind,
			// Public root Task creation always enters the coordinator queue;
			// callers cannot disable or bypass this hand-off.
			AutoCoordinate:     recordKind == domain.RecordKindTask && req.ParentID == "",
			AcceptanceCriteria: req.AcceptanceCriteria,
		}
		if req.DueDate != nil {
			if d, err := time.Parse("2006-01-02", *req.DueDate); err == nil {
				p.DueDate = &d
			} else {
				return renderProblem(http.StatusBadRequest, "bad_request", "Invalid due_date", "due_date 必须为 YYYY-MM-DD")
			}
		}
		wi, replayed, err := s.svc.CreateWorkItemIdempotent(r.Context(), wsID, p)
		if err != nil {
			return problemBytes(err)
		}
		// 实体级幂等重放：同 client_key 返回既有实体，200 + 重放标记（区别于新建 201）。
		if replayed {
			w.Header().Set("Idempotent-Replayed", "true")
			return renderJSON(w, r, http.StatusOK, s.enrichWorkItem(r, wi))
		}
		return renderJSON(w, r, http.StatusCreated, s.enrichWorkItem(r, wi))
	})
}

func (s *Server) handleMoveWorkItem(w http.ResponseWriter, r *http.Request) {
	wiID := r.PathValue("work_item_id")
	s.idempotent(w, r, wiID, func() (int, []byte) {
		var req moveWorkItemRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		wi, err := s.svc.MoveWorkItem(r.Context(), wiID, domain.WorkItemStatus(req.Status), req.ExpectedVersion)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, s.enrichWorkItem(r, wi))
	})
}

func (s *Server) handleAssignWorkItem(w http.ResponseWriter, r *http.Request) {
	wiID := r.PathValue("work_item_id")
	s.idempotent(w, r, wiID, func() (int, []byte) {
		var req assignWorkItemRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		wi, err := s.svc.AssignWorkItem(r.Context(), wiID, req.AgentProfileID, req.ExpectedVersion)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, s.enrichWorkItem(r, wi))
	})
}

// handleClaimWorkItem M4 认领命令：todo 且未指派才可认领（已指派 409、
// 同 agent 重复认领幂等）；认领 = 指派 + 按 WakeOnAssignment 入队唤醒。
func (s *Server) handleClaimWorkItem(w http.ResponseWriter, r *http.Request) {
	wiID := r.PathValue("work_item_id")
	s.idempotent(w, r, wiID, func() (int, []byte) {
		var req claimWorkItemRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		wi, err := s.svc.ClaimWorkItem(r.Context(), wiID, req.AgentProfileID, req.ExpectedVersion)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, s.enrichWorkItem(r, wi))
	})
}

// handleReturnWorkItem M4 手动打回：acceptance/review → execution（activity
// 记录 reason）；其余 phase 409。
func (s *Server) handleReturnWorkItem(w http.ResponseWriter, r *http.Request) {
	wiID := r.PathValue("work_item_id")
	s.idempotent(w, r, wiID, func() (int, []byte) {
		var req returnWorkItemRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		wi, err := s.svc.ReturnWorkItem(r.Context(), wiID, req.Reason, req.ExpectedVersion)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, s.enrichWorkItem(r, wi))
	})
}

func (s *Server) handleBlockWorkItem(w http.ResponseWriter, r *http.Request) {
	wiID := r.PathValue("work_item_id")
	s.idempotent(w, r, wiID, func() (int, []byte) {
		var req blockWorkItemRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		wi, err := s.svc.BlockWorkItem(r.Context(), wiID, application.BlockParams{
			Code: req.Code, Message: req.Message, Source: req.Source,
		}, req.ExpectedVersion)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, s.enrichWorkItem(r, wi))
	})
}

func (s *Server) handleUnblockWorkItem(w http.ResponseWriter, r *http.Request) {
	wiID := r.PathValue("work_item_id")
	s.idempotent(w, r, wiID, func() (int, []byte) {
		var req unblockWorkItemRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		wi, err := s.svc.UnblockWorkItem(r.Context(), wiID, req.ExpectedVersion)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, s.enrichWorkItem(r, wi))
	})
}

func (s *Server) handleAcceptWorkItem(w http.ResponseWriter, r *http.Request) {
	wiID := r.PathValue("work_item_id")
	s.idempotent(w, r, wiID, func() (int, []byte) {
		var req unblockWorkItemRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		wi, err := s.svc.AcceptWorkItem(r.Context(), wiID, req.ExpectedVersion)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, s.enrichWorkItem(r, wi))
	})
}

// ── Run / Approval / Artifact ────────────────────────────────────────

func (s *Server) handleCreateRun(w http.ResponseWriter, r *http.Request) {
	wiID := r.PathValue("work_item_id")
	s.idempotent(w, r, wiID, func() (int, []byte) {
		var req createRunRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		p := application.CreateRunParams{
			AgentProfileID:     req.AgentProfileID,
			OutputContract:     req.OutputContract,
			Requirements:       req.Requirements,
			Instruction:        req.Input.Instruction,
			AcceptanceCriteria: req.Input.AcceptanceCriteria,
			ClientKey:          req.ClientKey,
			// HTTP 建 run 即用户消息入口：同事务落派发批次并做 @直达/接诊路由
			//（会话元模型 S1）。
			DispatchTrigger: domain.DispatchTriggerUserMessage,
		}
		if req.RuntimePreference != nil {
			p.RuntimePreference = &domain.RuntimePreference{
				Preferred:   req.RuntimePreference.Preferred,
				Fallbacks:   req.RuntimePreference.Fallbacks,
				Mode:        req.RuntimePreference.Mode,
				AgentPreset: req.RuntimePreference.AgentPreset,
			}
		}
		run, replayed, err := s.svc.CreateRunIdempotent(r.Context(), wiID, p)
		if err != nil {
			return problemBytes(err)
		}
		// 实体级幂等重放：同 client_key 返回既有 run，200 + 重放标记（区别于新建 202）。
		if replayed {
			w.Header().Set("Idempotent-Replayed", "true")
			return renderJSON(w, r, http.StatusOK, map[string]any{
				"run_id": run.ID, "work_item_id": run.WorkItemID,
				"status": run.Status, "version": run.Version,
				"capability_snapshot_id": nullableString(run.CapabilitySnapshotID),
			})
		}
		// 202 Accepted：真正开始执行由 SSE 事件确认。
		return renderJSON(w, r, http.StatusAccepted, map[string]any{
			"run_id": run.ID, "work_item_id": run.WorkItemID,
			"status": run.Status, "version": run.Version,
			"capability_snapshot_id": nullableString(run.CapabilitySnapshotID),
		})
	})
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.svc.Run(r.Context(), r.PathValue("run_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toRunDTO(run))
}

// handleListRunEvents 按 run_seq 回放 Run 事件历史（对话页加载；只读投影）。
func (s *Server) handleListRunEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.svc.RunEvents(r.Context(), r.PathValue("run_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": events})
}

// handleListWorkItemRuns 列出一个任务的全部 Run（对话轮次历史）。
func (s *Server) handleListWorkItemRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.svc.RunsByWorkItem(r.Context(), r.PathValue("work_item_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]runDTO, 0, len(runs))
	for _, run := range runs {
		items = append(items, toRunDTO(run))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleListWorkItemDispatches 派发卡片时间线（会话元模型 S1）：任务全部批次
// 新→旧；卡片 = 批次 + 成员 runs（会话组）摘要 + 触发消息摘录。
func (s *Server) handleListWorkItemDispatches(w http.ResponseWriter, r *http.Request) {
	wiID := r.PathValue("work_item_id")
	ctx := r.Context()
	wi, err := s.store.WorkItems().Get(ctx, wiID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := requireTaskWorkItemHTTP(wi); err != nil {
		fail(w, r, err)
		return
	}
	dispatches, err := s.store.Dispatches().ListByWorkItem(ctx, wiID)
	if err != nil {
		fail(w, r, err)
		return
	}
	agentList, err := s.store.Agents().List(ctx, wi.WorkspaceID)
	if err != nil {
		fail(w, r, err)
		return
	}
	agentNames := make(map[string]string, len(agentList))
	for _, a := range agentList {
		agentNames[a.ID] = a.Name
	}
	// AgentRepo.List intentionally hides system identities from Chat and normal
	// Agent management. Task dispatch cards still need the protected
	// Coordinator's display name for an intelligible execution timeline.
	if config, configErr := s.store.TaskCoordinators().GetConfig(ctx, wi.WorkspaceID); configErr == nil {
		if coordinator, coordinatorErr := s.store.Agents().Get(ctx, config.AgentProfileID); coordinatorErr == nil {
			agentNames[coordinator.ID] = coordinator.Name
		}
	}
	items := make([]dispatchCardDTO, 0, len(dispatches))
	// ListByWorkItem 升序，时间线倒序输出（新→旧）。
	for i := len(dispatches) - 1; i >= 0; i-- {
		d := dispatches[i]
		runs, err := s.store.Runs().ListByDispatch(ctx, d.ID)
		if err != nil {
			fail(w, r, err)
			return
		}
		// 触发消息：接诊/lead_plan 批次取 lead run（不在成员列时单独读取，
		// 如 lead_plan 兜底批的 source run）；@直达批次取最早成员 run。
		var trigger *domain.ExecutionRun
		if d.LeadRunID != "" {
			for _, run := range runs {
				if run.ID == d.LeadRunID {
					trigger = run
					break
				}
			}
			if trigger == nil {
				if lead, err := s.store.Runs().Get(ctx, d.LeadRunID); err == nil {
					trigger = lead
				}
			}
		}
		if trigger == nil && len(runs) > 0 {
			trigger = runs[0]
		}
		items = append(items, toDispatchCardDTO(d, runs, trigger, agentNames))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleInterruptRun(w http.ResponseWriter, r *http.Request) {
	s.controlRun(w, r, "interrupt")
}

func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	s.controlRun(w, r, "cancel")
}

func (s *Server) controlRun(w http.ResponseWriter, r *http.Request, action string) {
	runID := r.PathValue("run_id")
	s.idempotent(w, r, runID, func() (int, []byte) {
		run, err := s.svc.ControlRun(r.Context(), runID, action)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusAccepted, toRunDTO(run))
	})
}

func (s *Server) handleRetryRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run_id")
	s.idempotent(w, r, runID, func() (int, []byte) {
		run, err := s.svc.RetryRun(r.Context(), runID)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusAccepted, toRunDTO(run))
	})
}

func (s *Server) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	approvals, err := s.svc.Approvals(r.Context(), r.PathValue("run_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]approvalDTO, 0, len(approvals))
	for _, a := range approvals {
		items = append(items, toApprovalDTO(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleResolveApproval(w http.ResponseWriter, r *http.Request) {
	approvalID := r.PathValue("approval_id")
	s.idempotent(w, r, approvalID, func() (int, []byte) {
		var req resolveApprovalRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		if req.Decision != "approved" && req.Decision != "rejected" {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid decision", "decision 必须是 approved 或 rejected")
		}
		scope := domain.ApprovalScope(req.Scope)
		if scope == "" {
			scope = domain.ApprovalScopeOnce
		}
		if !scope.Valid() {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid scope", "scope 必须是 once、thread 或 workspace")
		}
		a, err := s.svc.ResolveApproval(r.Context(), approvalID, req.Decision == "approved", "user_demo", req.Reason, scope)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, toApprovalDTO(a))
	})
}

func (s *Server) handleListArtifacts(w http.ResponseWriter, r *http.Request) {
	arts, err := s.svc.Artifacts(r.Context(), r.PathValue("run_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]artifactDTO, 0, len(arts))
	for _, a := range arts {
		items = append(items, toArtifactDTO(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleAcceptArtifact(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run_id")
	artifactID := r.PathValue("artifact_id")
	s.idempotent(w, r, runID+":"+artifactID, func() (int, []byte) {
		artifact, err := s.svc.Artifact(r.Context(), artifactID)
		if err != nil {
			return problemBytes(err)
		}
		if artifact.RunID != runID {
			return problemBytes(domain.ErrNotFound)
		}
		accepted, err := s.svc.AcceptArtifact(r.Context(), artifactID)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, toArtifactDTO(accepted))
	})
}

// ── 错误序列化辅助 ───────────────────────────────────────────────────

// problemBytes 在幂等闭包内把领域错误序列化为 problem+json。
func problemBytes(err error) (int, []byte) {
	if p, ok := contextProblem(err); ok {
		b, _ := json.Marshal(p)
		return p.Status, b
	}
	if p, ok := commentProblem(err); ok {
		b, _ := json.Marshal(p)
		return p.Status, b
	}
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return renderProblem(http.StatusNotFound, "not_found", "Resource not found", "资源在当前 Workspace 视角不可见")
	case errors.Is(err, domain.ErrVersionConflict):
		return renderProblem(http.StatusConflict, "version_conflict", "Resource version conflict", "资源版本已变化")
	case errors.Is(err, application.ErrReviewStateConflict):
		// Accept/Return/feedback 竞态（RFC §9.7）；先于 ErrStateConflict 判定。
		return renderProblem(http.StatusConflict, "review_state_conflict", "Review state conflict", err.Error())
	case errors.Is(err, domain.ErrStateConflict):
		return renderProblem(http.StatusConflict, "state_conflict", "Command conflicts with current state", err.Error())
	case errors.Is(err, domain.ErrIdempotencyConflict):
		return renderProblem(http.StatusConflict, "idempotency_conflict", "Idempotency conflict", "相同 Idempotency-Key 提交了不同请求体")
	case errors.Is(err, domain.ErrIllegalTransition):
		return renderProblem(http.StatusUnprocessableEntity, "illegal_transition", "Illegal state transition", err.Error())
	case errors.Is(err, domain.ErrValidation):
		return renderProblem(http.StatusUnprocessableEntity, "validation_failed", "Validation failed", err.Error())
	case errors.Is(err, domain.ErrCapabilityMissing):
		return renderProblem(http.StatusUnprocessableEntity, "capability_missing", "Required capability unavailable", err.Error())
	default:
		return renderProblem(http.StatusUnprocessableEntity, "domain_error", "Command rejected", err.Error())
	}
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

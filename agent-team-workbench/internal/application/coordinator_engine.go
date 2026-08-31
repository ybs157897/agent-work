package application

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

const (
	coordinatorRole              = "coordinator"
	coordinatorWorkerRole        = "worker"
	coordinatorMaxWorkerAttempts = 3 // initial attempt + two automatic retries
	coordinatorMaxOwnFailures    = 2
	coordinatorSettlementAction  = "settle_dispatch"
)

func mapsCloneAny(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func coordinatorAttemptValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func coordinatorContextOf(run *domain.ExecutionRun) map[string]any {
	if run == nil || run.Input == nil {
		return nil
	}
	value, _ := run.Input["task_coordinator"].(map[string]any)
	return value
}

func coordinatorControlAction(state *domain.TaskCoordinatorState) string {
	if state == nil || state.Data == nil {
		return ""
	}
	action, _ := state.Data["control_action"].(string)
	return action
}

func coordinatorRetryPending(state *domain.TaskCoordinatorState) bool {
	if state == nil {
		return false
	}
	if coordinatorControlAction(state) == coordinatorSettlementAction {
		return false
	}
	return state.Status == domain.CoordinatorWaitingRetry || coordinatorControlAction(state) == "retry_worker"
}

func coordinatorSettlementPending(state *domain.TaskCoordinatorState) bool {
	return state != nil && coordinatorControlAction(state) == coordinatorSettlementAction
}

func coordinatorStateExecutionStopped(state *domain.TaskCoordinatorState) bool {
	if state == nil {
		return false
	}
	switch state.Status {
	case domain.CoordinatorBlocked, domain.CoordinatorWaitingUser, domain.CoordinatorCompleted, domain.CoordinatorCancelled:
		return true
	default:
		return false
	}
}

func isSystemCoordinatorRun(run *domain.ExecutionRun) bool {
	context := coordinatorContextOf(run)
	role, _ := context["role"].(string)
	return role == coordinatorRole
}

func coordinatorRunDefersReview(run *domain.ExecutionRun) bool {
	if !isSystemCoordinatorRun(run) {
		return false
	}
	action, _ := coordinatorContextOf(run)["action"].(string)
	return action != "evaluation"
}

func coordinatorRuntimePreference(config *domain.TaskCoordinatorConfig, useFallback bool) *domain.RuntimePreference {
	if config == nil {
		return nil
	}
	preferred := config.RuntimeLabel
	fallbacks := []string{}
	if useFallback && config.FallbackRuntimeLabel != "" {
		preferred = config.FallbackRuntimeLabel
		if config.RuntimeLabel != preferred {
			fallbacks = append(fallbacks, config.RuntimeLabel)
		}
	} else if config.FallbackRuntimeLabel != "" && config.FallbackRuntimeLabel != preferred {
		fallbacks = append(fallbacks, config.FallbackRuntimeLabel)
	}
	return &domain.RuntimePreference{Preferred: preferred, Fallbacks: fallbacks, Mode: "plan"}
}

// acceptanceCriteriaFromWorkItem returns the canonical acceptance contract.
// Coordinator state and Run input are historical snapshots only; allowing the
// pre-first-Run edit window means neither may become the source of truth.
func acceptanceCriteriaFromWorkItem(wi *domain.WorkItem) []string {
	if wi == nil {
		return nil
	}
	return append([]string(nil), wi.AcceptanceCriteria...)
}

func (s *Service) appendCoordinatorEvent(ctx context.Context, state *domain.TaskCoordinatorState,
	workItemID, kind, summary, runID, agentID string, attempt int, reason string,
	nextActionAt *time.Time, data map[string]any) error {
	if state == nil {
		return fmt.Errorf("%w: coordinator state required", domain.ErrValidation)
	}
	payload := mapsCloneAny(data)
	if payload == nil {
		payload = map[string]any{}
	}
	payload["root_work_item_id"] = state.RootWorkItemID
	payload["work_item_id"] = workItemID
	payload["record_kind"] = string(domain.RecordKindTask)
	payload["status"] = string(state.Status)
	payload["attempt"] = attempt
	if reason != "" {
		payload["reason"] = reason
	}
	if nextActionAt != nil {
		payload["next_action_at"] = nextActionAt.UTC().Format(time.RFC3339Nano)
	}
	event := &domain.TaskCoordinatorEvent{
		ID: domain.NewID(domain.PrefixCoordinatorEvent), WorkspaceID: state.WorkspaceID,
		RootWorkItemID: state.RootWorkItemID, WorkItemID: workItemID,
		Kind: kind, Summary: summary, RunID: runID, AgentID: agentID,
		Attempt: attempt, Reason: reason, NextActionAt: nextActionAt,
		Data: payload, OccurredAt: time.Now().UTC(),
	}
	if err := s.store.TaskCoordinators().AppendEvent(ctx, event); err != nil {
		return err
	}
	return s.emit(ctx, state.WorkspaceID, kind, domain.AggregateTaskCoordinator,
		state.ID, state.Version, nil, payload)
}

// StartCoordinator starts (or idempotently observes) the next system control
// turn for a root Task. State, Run and causal event are committed before the
// Runtime side effect; a crash after commit is recovered by the due-state loop.
func (s *Service) StartCoordinator(ctx context.Context, workItemID string) error {
	if state, stateErr := s.store.TaskCoordinators().GetStateForWorkItem(ctx, workItemID); stateErr == nil {
		if (state.Status == domain.CoordinatorWaitingRetry ||
			(state.Status == domain.CoordinatorRunning && state.CurrentRunID == "")) &&
			coordinatorControlAction(state) == "retry_worker" {
			return s.startScheduledWorkerRetry(ctx, state)
		}
		if coordinatorSettlementPending(state) {
			return s.retryPendingSettlement(ctx, state)
		}
		if state.Status == domain.CoordinatorCompleted || state.Status == domain.CoordinatorCancelled ||
			state.Status == domain.CoordinatorBlocked || state.Status == domain.CoordinatorWaitingUser {
			return nil
		}
		if state.Status == domain.CoordinatorRunning && state.CurrentRunID == "" &&
			state.NextActionAt == nil && coordinatorControlAction(state) == "" {
			// Observation-only checkpoint while Workers/settlement are active.
			// This guard is required in addition to due-scan filtering because an
			// HTTP idempotency replay can call StartCoordinator directly.
			// RFC §7.7：checkpoint 上存在未消费 actionable 评论且树内已无活动
			// Worker/settlement 时，放行到 startCoordinatorTurn 在事务内改排
			// queued/message 并消费评论（durable due 的兜底路径）。
			unconsumed, hcErr := s.store.TaskComments().HasUnconsumedActionable(
				context.WithoutCancel(ctx), state.RootWorkItemID, state.ConsumedCommentRevision)
			if hcErr != nil || !unconsumed {
				return nil
			}
			active, activeErr := s.taskTreeHasActiveRuns(context.WithoutCancel(ctx), state.RootWorkItemID)
			if activeErr != nil || active || coordinatorSettlementPending(state) {
				return nil
			}
		}
		if state.CurrentRunID != "" {
			if active, runErr := s.store.Runs().Get(ctx, state.CurrentRunID); runErr == nil {
				if !active.Status.IsTerminal() {
					return nil
				}
				// Run status and the Coordinator projection are committed in
				// separate phases. A recovery scan can observe a terminal Run in
				// that gap; replay the terminal hook pipeline and let its CAS
				// projection decide the next action before opening another turn.
				s.replayCoordinatorTerminalHooks(context.WithoutCancel(ctx), active)
				return nil
			}
		}
		if err := s.ensureCoordinatorRuntimeReady(context.WithoutCancel(ctx), state); err != nil {
			_ = s.blockCoordinatorForStartFailure(context.WithoutCancel(ctx), workItemID, err)
			return err
		}
	}
	run, err := s.startCoordinatorTurn(ctx, workItemID)
	if err != nil {
		// Concurrent due-scan / HTTP replay may lose the CAS or hit the
		// deterministic Run client key. The winning caller already owns progress;
		// never turn that benign race into a user-visible blocker.
		if errors.Is(err, domain.ErrVersionConflict) || errors.Is(err, domain.ErrIdempotencyConflict) {
			return nil
		}
		// 目标 Host 明确 offline：不创建 Run，转 durable waiting_retry
		//（execution_host_unavailable），由恢复循环在 Host 回落后重驱（RFC §7.4）。
		if errors.Is(err, domain.ErrExecutionHostUnavailable) {
			if deferErr := s.deferCoordinatorForHostUnavailable(ctx, workItemID, err); deferErr != nil {
				log.Printf("coordinator: task %s host 不可用 checkpoint 写入失败: %v", workItemID, deferErr)
				return deferErr
			}
			return err
		}
		_ = s.blockCoordinatorForStartFailure(context.WithoutCancel(ctx), workItemID, err)
		return err
	}
	if run == nil {
		return nil
	}
	s.notifier.Notify(run.WorkspaceID)
	if s.dispatcher != nil {
		if err := s.dispatcher.Dispatch(context.WithoutCancel(ctx), run); err != nil {
			_ = s.RecordRunStatus(context.WithoutCancel(ctx), run.ID, domain.RunFailed,
				map[string]any{"code": "dispatch_failed", "message": err.Error(), "retryable": true,
					"family": string(runtime.FamilyTransientUpstream)})
			return err
		}
	}
	return nil
}

func (s *Service) replayCoordinatorTerminalHooks(ctx context.Context, run *domain.ExecutionRun) {
	if run == nil || !run.Status.IsTerminal() {
		return
	}
	wi, err := s.store.WorkItems().Get(ctx, run.WorkItemID)
	if err != nil || !isTaskWorkItem(wi) {
		return
	}
	// Keep this order aligned with RecordRunStatus: plan/verdict projections
	// must run before the Coordinator terminal decision, and dispatch settlement
	// must remain last so a retry/replan can keep ownership of the batch.
	s.maybeAdvancePlans(ctx, run)
	s.maybeProcessVerdict(ctx, run)
	s.maybeExtractPlan(ctx, run)
	s.maybeSummarizeSegment(ctx, run)
	s.maybeAdvanceTaskCoordinator(ctx, run)
	s.maybeSettleDispatch(ctx, run)
}

func (s *Service) retryPendingSettlement(ctx context.Context, state *domain.TaskCoordinatorState) error {
	if state == nil {
		return nil
	}
	fresh, err := s.store.TaskCoordinators().GetState(ctx, state.RootWorkItemID)
	if err != nil {
		return err
	}
	state = fresh
	if !coordinatorSettlementPending(state) || (state.NextActionAt != nil && state.NextActionAt.After(time.Now().UTC())) {
		return nil
	}
	dispatchID, _ := state.Data["settle_dispatch_id"].(string)
	runID, _ := state.Data["settle_run_id"].(string)
	if dispatchID == "" || runID == "" {
		return s.blockCoordinator(ctx, state, nil, "settlement_retry_state_invalid", "缺少派发收口重试上下文", "检查任务时间线后解除阻塞")
	}
	run, err := s.store.Runs().Get(ctx, runID)
	if err != nil {
		return s.blockCoordinator(ctx, state, nil, "settlement_retry_run_missing", err.Error(), "检查任务时间线后解除阻塞")
	}
	if run.DispatchID != dispatchID || !run.Status.IsTerminal() {
		return s.blockCoordinator(ctx, state, nil, "settlement_retry_run_invalid", "派发收口重试 Run 不可用", "检查任务时间线后解除阻塞")
	}
	// maybeSettleDispatch records a fresh retry checkpoint on failure and clears
	// the checkpoint after a successful settlement transaction.
	s.maybeSettleDispatch(context.WithoutCancel(ctx), run)
	return nil
}

func coordinatorRetryDelay(attempt int) time.Duration {
	if attempt <= 2 {
		return 5 * time.Second
	}
	return 30 * time.Second
}

func (s *Service) startScheduledWorkerRetry(ctx context.Context, state *domain.TaskCoordinatorState) error {
	now := time.Now().UTC()
	if state.NextActionAt != nil && state.NextActionAt.After(now) {
		return nil
	}
	sourceRunID, _ := state.Data["retry_worker_run_id"].(string)
	if sourceRunID == "" {
		return s.blockCoordinator(ctx, state, nil, "retry_state_invalid", "缺少待重试 Worker Run", "检查任务时间线后解除阻塞")
	}
	parent, err := s.store.Runs().Get(ctx, sourceRunID)
	if err != nil {
		return s.blockCoordinator(ctx, state, nil, "worker_retry_run_missing", err.Error(), "检查 Worker Runtime 后解除阻塞")
	}
	if !parent.Status.IsTerminal() {
		return s.blockCoordinator(ctx, state, nil, "worker_retry_run_invalid", "待重试 Worker Run 尚未终态", "检查任务时间线后解除阻塞")
	}
	wi, err := s.store.WorkItems().Get(ctx, parent.WorkItemID)
	if err != nil {
		return err
	}
	var retry *domain.ExecutionRun
	err = s.store.InTx(ctx, func(ctx context.Context) error {
		fresh, err := s.store.TaskCoordinators().GetState(ctx, state.RootWorkItemID)
		if err != nil {
			return err
		}
		if (fresh.Status != domain.CoordinatorWaitingRetry && fresh.Status != domain.CoordinatorRunning) ||
			coordinatorControlAction(fresh) != "retry_worker" || fresh.CurrentRunID != "" {
			return nil
		}
		if fresh.NextActionAt != nil && fresh.NextActionAt.After(time.Now().UTC()) {
			return nil
		}
		run, err := s.createRetryRunLocked(ctx, parent, wi, "coordinator-retry:"+parent.ID)
		if err != nil {
			return err
		}
		retry = run
		attempt := coordinatorRunAttempt(ctx, s.store.Runs(), run)
		expected := fresh.Version
		fresh.Status = domain.CoordinatorRunning
		fresh.Phase = "recovering"
		fresh.CurrentAgentID = run.AgentProfileID
		fresh.CurrentStep = run.WorkItemID
		fresh.CurrentRunID = run.ID
		fresh.CurrentAction = "观察 Worker 重试结果"
		fresh.LastError = ""
		fresh.NextActionAt = nil
		if fresh.Data == nil {
			fresh.Data = map[string]any{}
		}
		if err := s.store.TaskCoordinators().UpdateState(ctx, fresh, expected); err != nil {
			return err
		}
		fresh.Version = expected + 1
		return s.appendCoordinatorEvent(ctx, fresh, run.WorkItemID, domain.EventCoordinatorAttemptUpdated,
			fmt.Sprintf("Worker 自动重试已开始（%d/%d）", attempt, coordinatorMaxWorkerAttempts),
			run.ID, run.AgentProfileID, attempt, "", nil,
			map[string]any{"stage": "attempt", "status": "running", "retry_of": sourceRunID,
				"max_attempts": coordinatorMaxWorkerAttempts, "next_action": fresh.CurrentAction})
	})
	if err != nil {
		if errors.Is(err, domain.ErrVersionConflict) || errors.Is(err, domain.ErrIdempotencyConflict) {
			return nil
		}
		return err
	}
	if retry == nil {
		return nil
	}
	s.notifier.Notify(retry.WorkspaceID)
	latestState, stateErr := s.store.TaskCoordinators().GetState(ctx, state.RootWorkItemID)
	latestRun, runErr := s.store.Runs().Get(ctx, retry.ID)
	if stateErr != nil || runErr != nil || coordinatorStateExecutionStopped(latestState) ||
		latestState.CurrentRunID != retry.ID || latestRun.Status != domain.RunQueued {
		if runErr == nil && !latestRun.Status.IsTerminal() {
			_, _ = s.ControlRun(context.WithoutCancel(ctx), retry.ID, "cancel")
		}
		return nil
	}
	if s.dispatcher != nil {
		if err := s.dispatcher.Dispatch(context.WithoutCancel(ctx), retry); err != nil {
			_ = s.RecordRunStatus(context.WithoutCancel(ctx), retry.ID, domain.RunFailed,
				map[string]any{"code": "dispatch_failed", "message": err.Error(), "retryable": true})
		}
	}
	return nil
}

func (s *Service) startCoordinatorTurn(ctx context.Context, workItemID string) (*domain.ExecutionRun, error) {
	var created *domain.ExecutionRun
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		state, err := s.store.TaskCoordinators().GetStateForWorkItem(ctx, workItemID)
		if err != nil {
			return err
		}
		if state.Status == domain.CoordinatorCompleted || state.Status == domain.CoordinatorCancelled ||
			state.Status == domain.CoordinatorBlocked || state.Status == domain.CoordinatorWaitingUser {
			return nil
		}
		now := time.Now().UTC()
		if state.NextActionAt != nil && state.NextActionAt.After(now) {
			return nil
		}
		if state.CurrentRunID != "" {
			existing, runErr := s.store.Runs().Get(ctx, state.CurrentRunID)
			if runErr == nil && !existing.Status.IsTerminal() {
				return nil
			}
		}
		root, err := s.store.WorkItems().Get(ctx, state.RootWorkItemID)
		if err != nil {
			return err
		}
		if root.Status.IsTerminal() {
			return nil
		}
		config, err := s.store.TaskCoordinators().GetConfig(ctx, root.WorkspaceID)
		if err != nil {
			return err
		}
		workers, err := s.coordinatorWorkerRoster(ctx, root.WorkspaceID, config.AgentProfileID)
		if err != nil {
			return err
		}
		if len(workers) == 0 {
			return fmt.Errorf("%w: no_available_worker: 当前 Workspace 没有已启用的普通 Agent", domain.ErrValidation)
		}
		action := strings.TrimSpace(state.CurrentAction)
		if controlAction := coordinatorControlAction(state); controlAction != "" {
			action = controlAction
		}
		if action == "" || action == "queued" {
			action = "intake"
		}
		if state.Status == domain.CoordinatorRunning {
			// Observation-only checkpoint（running 且无 current Run、无控制动作）：
			// 有活动 Worker/settlement 保持观察；存在未消费 actionable 评论且现场
			// 已安静时，本事务内 CAS queued/message 并继续本轮消费（RFC §7.7）。
			// 纯观察 checkpoint 不重启（避免活动批次被必达 steering 打断）。
			if state.CurrentRunID == "" && coordinatorControlAction(state) == "" {
				unconsumed, hcErr := s.store.TaskComments().HasUnconsumedActionable(ctx, state.RootWorkItemID, state.ConsumedCommentRevision)
				if hcErr != nil {
					return hcErr
				}
				if !unconsumed {
					return nil
				}
				active, activeErr := s.taskTreeHasActiveRuns(ctx, state.RootWorkItemID)
				if activeErr != nil {
					return activeErr
				}
				if active || coordinatorSettlementPending(state) {
					return nil
				}
				expected := state.Version
				state.Status = domain.CoordinatorQueued
				state.Phase = "message"
				state.CurrentAction = "message"
				state.NextActionAt = nil
				if err := s.store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
					return err
				}
				state.Version = expected + 1
				if err := s.appendCoordinatorEvent(ctx, state, state.RootWorkItemID,
					domain.EventCoordinatorMessageReceived, "未消费任务反馈改排 Coordinator 队列",
					"", state.CoordinatorAgentID, state.Attempt, "", nil,
					map[string]any{"stage": "message", "next_action": "消费未处理的任务反馈"}); err != nil {
					return err
				}
				action = "message"
			} else {
				action = "recover"
			}
		}
		// RFC §7.8：Run 创建事务内读取未消费评论、快照进 TASK_DATA_JSON_V1 与
		// Run input，并同事务推进 consumed_comment_revision——失败整体回滚。
		commentSnapshot, revFrom, revTo, err := s.coordinatorCommentSnapshot(ctx, state.RootWorkItemID, state.ConsumedCommentRevision)
		if err != nil {
			return err
		}
		useFallback, _ := state.Data["use_fallback"].(bool)
		instruction := BuildCoordinatorInstruction(CoordinatorPromptInput{
			RootWorkItemID: root.ID, Title: root.Title, Description: root.Description,
			Acceptance: acceptanceCriteriaFromWorkItem(root), Workers: workers,
			Phase: state.Phase, CurrentStep: state.CurrentStep, CurrentAction: action,
			Attempt: state.Attempt + 1, Failure: state.LastError, Comments: commentSnapshot,
		})
		contextData := map[string]any{
			"role": coordinatorRole, "root_work_item_id": root.ID,
			"state_id": state.ID, "action": action, "attempt": state.Attempt + 1,
			"use_fallback":          useFallback,
			"comment_revision_from": revFrom,
			"comment_revision_to":   revTo,
		}
		if len(commentSnapshot) > 0 {
			// Run input 保存评论快照（§4.9：重启后可审计重建）。
			contextData["comments"] = commentSnapshot
		}
		run, err := s.createRunLocked(ctx, root.ID, CreateRunParams{
			AgentProfileID: config.AgentProfileID, Instruction: instruction,
			AcceptanceCriteria: acceptanceCriteriaFromWorkItem(root),
			RuntimePreference:  coordinatorRuntimePreference(config, useFallback),
			DispatchTrigger:    domain.DispatchTriggerUserMessage,
			ClientKey:          fmt.Sprintf("coordinator:%s:%d:%s", state.ID, state.Attempt+1, action),
			CoordinatorContext: contextData,
		})
		if err != nil {
			return err
		}
		expected := state.Version
		state.Status = domain.CoordinatorRunning
		state.Phase = action
		state.CurrentAction = "观察 Coordinator 输出并执行下一步"
		state.CurrentAgentID = config.AgentProfileID
		state.CurrentRunID = run.ID
		state.Attempt++
		state.NextActionAt = nil
		state.BlockerCode, state.BlockerMessage = "", ""
		// 消费水位与 Run 创建同事务（RFC §7.8）：Run 创建失败则整体回滚。
		state.ConsumedCommentRevision = revTo
		if state.Data == nil {
			state.Data = map[string]any{}
		}
		delete(state.Data, "control_action")
		state.Data["runtime"] = run.RuntimeLabel
		if err := s.store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
			return err
		}
		state.Version = expected + 1
		if err := s.appendCoordinatorEvent(ctx, state, root.ID, domain.EventCoordinatorStarted,
			"Coordinator 已接取并开始", run.ID, config.AgentProfileID, state.Attempt, action, nil,
			map[string]any{"stage": "plan", "runtime": run.RuntimeLabel, "next_action": state.CurrentAction}); err != nil {
			return err
		}
		created = run
		return nil
	})
	return created, err
}

func (s *Service) blockCoordinatorForStartFailure(ctx context.Context, workItemID string, cause error) error {
	state, err := s.store.TaskCoordinators().GetStateForWorkItem(ctx, workItemID)
	if err != nil {
		return err
	}
	return s.blockCoordinator(ctx, state, nil, "coordinator_start_failed", cause.Error(), "检查 Coordinator Runtime、模型和可用 Agent 后解除阻塞")
}

func (s *Service) blockCoordinator(ctx context.Context, state *domain.TaskCoordinatorState,
	failedRun *domain.ExecutionRun, code, message, nextAction string) error {
	return s.store.InTx(ctx, func(ctx context.Context) error {
		fresh, err := s.store.TaskCoordinators().GetState(ctx, state.RootWorkItemID)
		if err != nil {
			return err
		}
		if coordinatorWorkerRunSuperseded(fresh, failedRun) {
			return nil
		}
		if failedRun != nil && isSystemCoordinatorRun(failedRun) && fresh.CurrentRunID != failedRun.ID {
			return nil
		}
		if fresh.Status == domain.CoordinatorCompleted || fresh.Status == domain.CoordinatorCancelled {
			return nil
		}
		expected := fresh.Version
		fresh.Status = domain.CoordinatorBlocked
		fresh.Phase = "blocked"
		fresh.BlockerCode, fresh.BlockerMessage = code, message
		fresh.LastError = message
		fresh.CurrentAction = nextAction
		fresh.CurrentRunID = ""
		fresh.NextActionAt = nil
		if err := s.store.TaskCoordinators().UpdateState(ctx, fresh, expected); err != nil {
			return err
		}
		fresh.Version = expected + 1
		root, err := s.store.WorkItems().Get(ctx, fresh.RootWorkItemID)
		if err != nil {
			return err
		}
		if root.Status != domain.WorkItemBlocked && !root.Status.IsTerminal() {
			if err := s.blockLocked(ctx, root, BlockParams{Code: code, Message: message, Source: "coordinator"}); err != nil {
				return err
			}
		}
		if failedRun != nil && failedRun.DispatchID != "" {
			dispatch, dispatchErr := s.store.Dispatches().Get(ctx, failedRun.DispatchID)
			if dispatchErr != nil && !errors.Is(dispatchErr, domain.ErrNotFound) {
				return dispatchErr
			}
			if dispatch != nil && !dispatch.Status.IsTerminal() {
				closedAt := time.Now().UTC()
				closed, closeErr := s.store.Dispatches().CloseStatus(ctx, dispatch.ID, domain.DispatchDegraded, closedAt)
				if closeErr != nil {
					return closeErr
				}
				if closed {
					dispatch.Status, dispatch.ClosedAt = domain.DispatchDegraded, &closedAt
					if err := s.emitDispatchUpdated(ctx, fresh.WorkspaceID, dispatch); err != nil {
						return err
					}
				}
			}
		}
		runID, agentID, attempt := "", fresh.CoordinatorAgentID, fresh.Attempt
		if failedRun != nil {
			runID, agentID = failedRun.ID, failedRun.AgentProfileID
			attempt = coordinatorRunAttempt(ctx, s.store.Runs(), failedRun)
		}
		return s.appendCoordinatorEvent(ctx, fresh, fresh.RootWorkItemID, domain.EventCoordinatorBlocked,
			"Coordinator 需要用户介入", runID, agentID, attempt, message, nil,
			map[string]any{"stage": "failure", "failure_code": code, "failure_message": message,
				"retryable": false, "next_action": nextAction})
	})
}

// SendCoordinatorInstruction 已随 RFC §7.7 删除清单移除：用户追加指令统一改走
// AppendTaskComment（requirement 评论，见 task_comments.go），不再保留
// pending_instruction 单槽双轨。

func (s *Service) maybeAdvanceTaskCoordinator(ctx context.Context, run *domain.ExecutionRun) {
	if run == nil || !run.Status.IsTerminal() {
		return
	}
	state, err := s.store.TaskCoordinators().GetStateForWorkItem(ctx, run.WorkItemID)
	if err != nil {
		return // legacy Task without a Coordinator
	}
	if state.Status == domain.CoordinatorBlocked || state.Status == domain.CoordinatorWaitingUser ||
		state.Status == domain.CoordinatorCompleted || state.Status == domain.CoordinatorCancelled {
		return
	}
	// A system Run is the exact terminal checkpoint that owns the projection.
	// Once its hook has advanced the state (or another turn owns it), a due-scan
	// replay of the same terminal Run must be a no-op rather than append another
	// plan/completed event or reset progress.
	if isSystemCoordinatorRun(run) && state.CurrentRunID != run.ID {
		return
	}
	// A settlement wake can race a scheduled Worker retry. It belongs to the
	// same dispatch, but it must not advance the Coordinator control line while
	// the retry action owns recovery. The queued wake remains retryable and is
	// consumed after the Worker retry settles.
	if settlementRunID(run) != "" && coordinatorRetryPending(state) {
		return
	}
	if isSystemCoordinatorRun(run) {
		s.handleCoordinatorTerminal(context.WithoutCancel(ctx), state, run)
		return
	}
	s.handleCoordinatorWorkerTerminal(context.WithoutCancel(ctx), state, run)
}

func (s *Service) recordCoordinatorSessionHeal(ctx context.Context, parent, retry *domain.ExecutionRun) {
	if parent == nil || retry == nil {
		return
	}
	state, err := s.store.TaskCoordinators().GetStateForWorkItem(ctx, parent.WorkItemID)
	if err != nil {
		return
	}
	attempt := coordinatorRunAttempt(ctx, s.store.Runs(), retry)
	_ = s.store.InTx(ctx, func(ctx context.Context) error {
		fresh, err := s.store.TaskCoordinators().GetState(ctx, state.RootWorkItemID)
		if err != nil {
			return err
		}
		return s.appendCoordinatorEvent(ctx, fresh, retry.WorkItemID, domain.EventCoordinatorRetryScheduled,
			"会话丢失，已重建 Worker 会话", retry.ID, retry.AgentProfileID, attempt,
			"session_unknown", nil, map[string]any{"stage": "retry", "status": "waiting_retry",
				"retry_of": parent.ID, "failure_code": "session_unknown",
				"failure_message": "provider 会话已丢失", "retryable": true,
				"max_attempts": coordinatorMaxWorkerAttempts, "next_action": "使用 fresh 会话继续执行"})
	})
}

func (s *Service) handleCoordinatorTerminal(ctx context.Context, state *domain.TaskCoordinatorState, run *domain.ExecutionRun) {
	if run.Status == domain.RunSucceeded {
		if err := s.handleCoordinatorSuccess(ctx, state, run); err != nil {
			log.Printf("coordinator: run %s success projection failed: %v", run.ID, err)
		}
		return
	}
	if run.Status == domain.RunCancelled || run.Status == domain.RunInterrupted {
		_ = s.setCoordinatorWaitingUser(ctx, state, run, "Coordinator 已停止", "补充指令后继续")
		return
	}
	if coordinatorRetryPending(state) || coordinatorSettlementPending(state) {
		return
	}
	retryable := coordinatorRunRetryable(run)
	failures := coordinatorAttemptValue(state.Data["coordinator_failures"]) + 1
	if retryable && failures < coordinatorMaxOwnFailures {
		nextActionAt := time.Now().UTC().Add(coordinatorRetryDelay(failures + 1))
		err := s.store.InTx(ctx, func(ctx context.Context) error {
			fresh, err := s.store.TaskCoordinators().GetState(ctx, state.RootWorkItemID)
			if err != nil {
				return err
			}
			if fresh.CurrentRunID != run.ID {
				return nil
			}
			expected := fresh.Version
			if fresh.Data == nil {
				fresh.Data = map[string]any{}
			}
			fresh.Data["coordinator_failures"] = failures
			fresh.Data["use_fallback"] = failures > 0
			fresh.Data["control_action"] = "recover"
			fresh.Status = domain.CoordinatorWaitingRetry
			fresh.Phase = "recovering"
			fresh.CurrentAction = "等待退避后恢复 Coordinator"
			fresh.CurrentRunID = ""
			fresh.LastError = coordinatorFailureText(run)
			fresh.NextActionAt = &nextActionAt
			if err := s.store.TaskCoordinators().UpdateState(ctx, fresh, expected); err != nil {
				return err
			}
			fresh.Version = expected + 1
			return s.appendCoordinatorEvent(ctx, fresh, fresh.RootWorkItemID, domain.EventCoordinatorRetryScheduled,
				"Coordinator 执行失败，准备有界重试", run.ID, run.AgentProfileID, failures,
				fresh.LastError, &nextActionAt, map[string]any{"stage": "retry", "retry_of": run.ID,
					"failure_code": coordinatorFailureCode(run), "failure_message": fresh.LastError,
					"retryable": true, "max_attempts": coordinatorMaxOwnFailures,
					"next_action": "切换备用 Runtime 或重建 Coordinator 会话"})
		})
		if err != nil {
			log.Printf("coordinator: schedule retry for run %s: %v", run.ID, err)
		}
		return
	}
	_ = s.blockCoordinator(ctx, state, run, "coordinator_failed", coordinatorFailureText(run),
		"检查 Runtime/模型配置或补充任务信息后解除阻塞")
}

func (s *Service) handleCoordinatorSuccess(ctx context.Context, state *domain.TaskCoordinatorState, run *domain.ExecutionRun) error {
	startNext := false
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		fresh, err := s.store.TaskCoordinators().GetState(ctx, state.RootWorkItemID)
		if err != nil {
			return err
		}
		if fresh.CurrentRunID != run.ID {
			return nil
		}
		root, err := s.store.WorkItems().Get(ctx, fresh.RootWorkItemID)
		if err != nil {
			return err
		}
		if root.Status == domain.WorkItemBlocked {
			expected := fresh.Version
			fresh.Status = domain.CoordinatorBlocked
			fresh.BlockerCode = "coordinator_plan_failed"
			fresh.BlockerMessage = "Coordinator 输出无法形成有效计划"
			fresh.CurrentAction = "修正任务信息或 Coordinator 输出后解除阻塞"
			if err := s.store.TaskCoordinators().UpdateState(ctx, fresh, expected); err != nil {
				return err
			}
			fresh.Version = expected + 1
			return s.appendCoordinatorEvent(ctx, fresh, fresh.RootWorkItemID, domain.EventCoordinatorBlocked,
				"Coordinator 计划无法执行", run.ID, run.AgentProfileID, fresh.Attempt,
				fresh.BlockerMessage, nil, map[string]any{"stage": "failure",
					"failure_code": fresh.BlockerCode, "failure_message": fresh.BlockerMessage,
					"retryable": false, "next_action": fresh.CurrentAction})
		}
		plan, planErr := s.store.Plans().LatestByWorkItem(ctx, fresh.RootWorkItemID)
		var evaluationRun *domain.ExecutionRun
		if planErr == nil && plan != nil {
			evaluationRun, err = s.pendingPlanEvaluation(ctx, plan)
			if err != nil {
				return err
			}
		}
		expected := fresh.Version
		fresh.CurrentRunID = ""
		fresh.LastError = ""
		if fresh.Data == nil {
			fresh.Data = map[string]any{}
		}
		delete(fresh.Data, "coordinator_failures")
		delete(fresh.Data, "use_fallback")
		if evaluationRun != nil {
			fresh.Status = domain.CoordinatorRunning
			fresh.Phase = "evaluation"
			fresh.CurrentRunID = evaluationRun.ID
			fresh.CurrentAgentID = evaluationRun.AgentProfileID
			fresh.CurrentAction = "等待评估 Run 完成"
			fresh.Summary = "Coordinator 正在评估任务结果"
		} else if action, _ := coordinatorContextOf(run)["action"].(string); action != "" &&
			action != "evaluation" && !(action == "intake" && run.RuntimeLabel == "mock") &&
			(planErr != nil || plan == nil || plan.SourceRunID != run.ID ||
				(plan.Status != domain.PlanActive && plan.Status != domain.PlanWaiting)) {
			// Recovery/message/wakeup turns must produce a new actionable plan.
			// Otherwise a stale waiting Plan could be mistaken for a delivered
			// result and move the Task to user acceptance without new work.
			fresh.Status = domain.CoordinatorBlocked
			fresh.Phase = "blocked"
			fresh.BlockerCode = "coordinator_plan_missing"
			fresh.BlockerMessage = "Coordinator 本轮未输出可执行的新计划"
			fresh.CurrentAction = "补充任务信息或重新提交 Coordinator 指令"
			fresh.CurrentRunID = ""
			fresh.NextActionAt = nil
			fresh.LastError = fresh.BlockerMessage
			if root.Status == domain.WorkItemInProgress {
				if err := s.blockLocked(ctx, root, BlockParams{Code: fresh.BlockerCode,
					Message: fresh.BlockerMessage, Source: "coordinator"}); err != nil {
					return err
				}
			}
		} else if action, _ := coordinatorContextOf(run)["action"].(string); action == "evaluation" &&
			root.Phase == domain.PhaseExecution {
			// A completed evaluation can reject the current result. The verdict
			// hook moves the root back to execution before this projection runs;
			// do not misread the finished Plan as final delivery and wait for user
			// acceptance. Queue a bounded Coordinator replan instead.
			fresh.Status = domain.CoordinatorQueued
			fresh.Phase = "recovering"
			fresh.CurrentAction = "recover"
			fresh.LastError = "评估未通过，Coordinator 重新规划"
			fresh.Summary = "评估未通过，Coordinator 将重新规划"
			fresh.Data["evaluation_rejected_run_id"] = run.ID
		} else if planErr == nil && plan != nil && plan.SourceRunID == run.ID &&
			(plan.Status == domain.PlanActive || plan.Status == domain.PlanWaiting) {
			fresh.Status = domain.CoordinatorRunning
			fresh.Phase = "executing"
			fresh.CurrentAction = "等待 Worker 结果并继续规划"
			fresh.Summary = "Coordinator 已生成计划并派发任务"
			fresh.Data["total_steps"] = len(plan.Steps)
			fresh.Data["completed_steps"] = 0
		} else {
			// §7.11 终态钩子：进入 waiting_user 前必须检查未消费 actionable 评论
			//（revision > consumed_watermark 且 kind IN (requirement, review_feedback)）。
			// 存在则不进入 waiting_user、改排 queued/message，由下一轮消费评论；
			// 该检查与 Coordinator state CAS 配合，不依赖进程内 Notify 消除竞态。
			hasUnconsumed, hcErr := s.store.TaskComments().HasUnconsumedActionable(ctx, fresh.RootWorkItemID, fresh.ConsumedCommentRevision)
			if hcErr != nil {
				return hcErr
			}
			if hasUnconsumed {
				fresh.Status = domain.CoordinatorQueued
				fresh.Phase = "message"
				fresh.CurrentAction = "message"
				fresh.Summary = "存在未消费任务反馈，Coordinator 继续处理"
			} else {
				fresh.Status = domain.CoordinatorWaitingUser
				fresh.Phase = "acceptance"
				fresh.CurrentAction = "等待用户验收"
				fresh.Summary = "Coordinator 已完成本轮任务"
				if total := coordinatorAttemptValue(fresh.Data["total_steps"]); total > 0 {
					fresh.Data["completed_steps"] = total
					fresh.Data["progress"] = float64(1)
				}
				if root.Status == domain.WorkItemInProgress && root.Phase != domain.PhaseReview {
					rootExpected := root.Version
					if err := root.EnterReview(time.Now().UTC()); err == nil {
						if err := s.store.WorkItems().Update(ctx, root, rootExpected); err != nil {
							return err
						}
						if err := s.emit(ctx, root.WorkspaceID, domain.EventWorkItemUpdated,
							domain.AggregateWorkItem, root.ID, root.Version, nil,
							map[string]any{"phase": string(root.Phase), "record_kind": string(domain.RecordKindTask)}); err != nil {
							return err
						}
					}
				}
			}
		}
		if err := s.store.TaskCoordinators().UpdateState(ctx, fresh, expected); err != nil {
			return err
		}
		fresh.Version = expected + 1
		kind, summary, stage := domain.EventCoordinatorPlanUpdated, "Coordinator 已更新任务计划", "plan"
		if fresh.Status == domain.CoordinatorQueued && fresh.Phase == "recovering" {
			kind, summary, stage = domain.EventCoordinatorRecoveryStarted, "评估未通过，Coordinator 重新规划", "recovery"
		}
		if fresh.Status == domain.CoordinatorQueued && fresh.Phase == "message" {
			kind, summary, stage = domain.EventCoordinatorMessageReceived, "存在未消费任务反馈，Coordinator 继续处理", "message"
		}
		if fresh.Status == domain.CoordinatorBlocked {
			kind, summary, stage = domain.EventCoordinatorBlocked, "Coordinator 本轮未输出可执行的新计划", "failure"
		}
		if fresh.Status == domain.CoordinatorWaitingUser {
			kind, summary, stage = domain.EventCoordinatorCompleted, "Coordinator 已交付，等待用户验收", "acceptance"
		}
		if err := s.appendCoordinatorEvent(ctx, fresh, fresh.RootWorkItemID, kind, summary,
			run.ID, run.AgentProfileID, fresh.Attempt, "", nil,
			map[string]any{"stage": stage, "next_action": fresh.CurrentAction}); err != nil {
			return err
		}
		if fresh.Status == domain.CoordinatorQueued {
			startNext = true
		}
		return nil
	})
	if err == nil && startNext {
		_ = s.StartCoordinator(context.WithoutCancel(ctx), state.RootWorkItemID)
	}
	return err
}

// pendingPlanEvaluation returns the evaluation Run created by a
// finish{evaluation:true} step while it is still non-terminal. The plan is
// already finished at that point, so looking only at Plan.Status would make a
// Coordinator incorrectly enter user acceptance before the evaluation result
// exists.
func (s *Service) pendingPlanEvaluation(ctx context.Context, plan *domain.Plan) (*domain.ExecutionRun, error) {
	if plan == nil {
		return nil, nil
	}
	runs, err := s.store.Runs().ListByWorkItem(ctx, plan.WorkItemID)
	if err != nil {
		return nil, err
	}
	for _, candidate := range runs {
		if candidate == nil || candidate.Status.IsTerminal() {
			continue
		}
		evaluation, _ := candidate.Input["evaluation"].(bool)
		if !evaluation {
			continue
		}
		control := coordinatorContextOf(candidate)
		if candidatePlanID, _ := control["plan_id"].(string); candidatePlanID == plan.ID {
			return candidate, nil
		}
	}
	return nil, nil
}

func (s *Service) retryCheckpointBelongsToRun(ctx context.Context, checkpointID string, run *domain.ExecutionRun) bool {
	if strings.TrimSpace(checkpointID) == "" || run == nil {
		return false
	}
	current := run
	for hops := 0; current != nil && hops < 100; hops++ {
		if current.ID == checkpointID {
			return true
		}
		if current.RetryOf == "" {
			return false
		}
		parent, err := s.store.Runs().Get(ctx, current.RetryOf)
		if err != nil {
			return false
		}
		current = parent
	}
	return false
}

func (s *Service) handleCoordinatorWorkerTerminal(ctx context.Context, state *domain.TaskCoordinatorState, run *domain.ExecutionRun) {
	attempt := coordinatorRunAttempt(ctx, s.store.Runs(), run)
	if latest, err := s.store.TaskCoordinators().GetState(ctx, state.RootWorkItemID); err == nil {
		if coordinatorWorkerRunSuperseded(latest, run) {
			return
		}
		state = latest
	}
	if run.Status == domain.RunSucceeded {
		_ = s.store.InTx(ctx, func(ctx context.Context) error {
			fresh, err := s.store.TaskCoordinators().GetState(ctx, state.RootWorkItemID)
			if err != nil {
				return err
			}
			if coordinatorWorkerRunSuperseded(fresh, run) {
				return nil
			}
			checkpointID, _ := fresh.Data["retry_worker_run_id"].(string)
			if coordinatorControlAction(fresh) == "retry_worker" && checkpointID == "" {
				return nil
			}
			expected := fresh.Version
			fresh.CurrentAgentID = run.AgentProfileID
			fresh.CurrentStep = run.WorkItemID
			fresh.CurrentRunID = ""
			fresh.Summary = "Worker 已返回结果，等待 Coordinator 汇总"
			if fresh.Data == nil {
				fresh.Data = map[string]any{}
			} else if pendingRetryID, _ := fresh.Data["retry_worker_run_id"].(string); pendingRetryID == "" ||
				s.retryCheckpointBelongsToRun(ctx, pendingRetryID, run) {
				delete(fresh.Data, "recovery_rounds")
				delete(fresh.Data, "retry_worker_run_id")
				delete(fresh.Data, "control_action")
			}
			completed := coordinatorAttemptValue(fresh.Data["completed_steps"]) + 1
			total := coordinatorAttemptValue(fresh.Data["total_steps"])
			if total > 0 && completed > total {
				completed = total
			}
			fresh.Data["completed_steps"] = completed
			if total > 0 {
				fresh.Data["progress"] = float64(completed) / float64(total)
			}
			if err := s.store.TaskCoordinators().UpdateState(ctx, fresh, expected); err != nil {
				return err
			}
			fresh.Version = expected + 1
			return s.appendCoordinatorEvent(ctx, fresh, run.WorkItemID, domain.EventCoordinatorAttemptUpdated,
				"Worker 尝试已完成", run.ID, run.AgentProfileID, attempt, "", nil,
				map[string]any{"stage": "result", "status": "succeeded", "max_attempts": coordinatorMaxWorkerAttempts})
		})
		return
	}
	if run.Status == domain.RunCancelled || run.Status == domain.RunInterrupted {
		_ = s.setCoordinatorWaitingUser(ctx, state, run, "Worker 已停止", "补充指令或解除阻塞后继续")
		return
	}
	if run.ErrorFamily == string(runtime.FamilySessionUnknown) && coordinatorSessionHealOwns(run) {
		return // the first resume failure is owned by the one-shot session heal
	}
	failure := coordinatorFailureText(run)
	if coordinatorRunRetryable(run) && attempt < coordinatorMaxWorkerAttempts {
		nextActionAt := time.Now().UTC().Add(coordinatorRetryDelay(attempt + 1))
		_ = s.store.InTx(ctx, func(ctx context.Context) error {
			fresh, err := s.store.TaskCoordinators().GetState(ctx, state.RootWorkItemID)
			if err != nil {
				return err
			}
			if coordinatorWorkerRunSuperseded(fresh, run) {
				return nil
			}
			expected := fresh.Version
			fresh.Status = domain.CoordinatorWaitingRetry
			fresh.Phase = "recovering"
			fresh.CurrentAgentID = run.AgentProfileID
			fresh.CurrentStep = run.WorkItemID
			fresh.CurrentAction = "等待退避后自动重试 Worker"
			fresh.CurrentRunID = ""
			fresh.LastError = failure
			fresh.NextActionAt = &nextActionAt
			if fresh.Data == nil {
				fresh.Data = map[string]any{}
			}
			fresh.Data["retry_worker_run_id"] = run.ID
			fresh.Data["control_action"] = "retry_worker"
			if err := s.store.TaskCoordinators().UpdateState(ctx, fresh, expected); err != nil {
				return err
			}
			fresh.Version = expected + 1
			return s.appendCoordinatorEvent(ctx, fresh, run.WorkItemID, domain.EventCoordinatorRetryScheduled,
				fmt.Sprintf("Worker 失败，自动重试（%d/%d）", attempt+1, coordinatorMaxWorkerAttempts),
				run.ID, run.AgentProfileID, attempt+1, failure, &nextActionAt,
				map[string]any{"stage": "retry", "status": "waiting_retry", "retry_of": run.ID,
					"failure_code": coordinatorFailureCode(run), "failure_message": failure,
					"retryable": true, "max_attempts": coordinatorMaxWorkerAttempts,
					"next_action": "退避后由同一 Worker 使用新 Run 继续执行"})
		})
		return
	}
	if coordinatorRunRetryable(run) {
		if coordinatorAttemptValue(state.Data["recovery_rounds"]) >= 1 {
			_ = s.blockCoordinator(ctx, state, run, "worker_retry_budget_exhausted", failure,
				"自动重试和一次重新规划均失败，请检查任务要求或可用 Agent")
			return
		}
		err := s.store.InTx(ctx, func(ctx context.Context) error {
			fresh, err := s.store.TaskCoordinators().GetState(ctx, state.RootWorkItemID)
			if err != nil {
				return err
			}
			if coordinatorWorkerRunSuperseded(fresh, run) {
				return nil
			}
			expected := fresh.Version
			fresh.Status = domain.CoordinatorQueued
			fresh.Phase = "recovering"
			fresh.CurrentAction = "recover"
			fresh.CurrentRunID = ""
			fresh.LastError = failure
			if fresh.Data == nil {
				fresh.Data = map[string]any{}
			}
			delete(fresh.Data, "retry_worker_run_id")
			delete(fresh.Data, "control_action")
			fresh.Data["failed_worker_run_id"] = run.ID
			fresh.Data["recovery_rounds"] = coordinatorAttemptValue(fresh.Data["recovery_rounds"]) + 1
			if err := s.store.TaskCoordinators().UpdateState(ctx, fresh, expected); err != nil {
				return err
			}
			fresh.Version = expected + 1
			return s.appendCoordinatorEvent(ctx, fresh, run.WorkItemID, domain.EventCoordinatorRecoveryStarted,
				"Worker 重试预算用尽，Coordinator 重新规划", run.ID, run.AgentProfileID,
				attempt, failure, nil, map[string]any{"stage": "reassign", "status": "retrying",
					"failure_code": coordinatorFailureCode(run), "failure_message": failure,
					"retryable": true, "next_action": "调整指令或选择其他 Agent"})
		})
		if err == nil {
			_ = s.StartCoordinator(ctx, state.RootWorkItemID)
		}
		return
	}
	_ = s.blockCoordinator(ctx, state, run, "worker_failed", failure, "修复权限、认证或输入后解除阻塞")
}

func coordinatorWorkerRunSuperseded(state *domain.TaskCoordinatorState, run *domain.ExecutionRun) bool {
	return state != nil && run != nil && !isSystemCoordinatorRun(run) &&
		state.CurrentRunID != "" && state.CurrentRunID != run.ID
}

func coordinatorSessionHealOwns(run *domain.ExecutionRun) bool {
	if run == nil || run.AdapterID == "" {
		return false
	}
	if healed, _ := run.Input["auto_heal_of"].(string); strings.TrimSpace(healed) != "" {
		return false
	}
	conversation, _ := run.Input["conversation"].(map[string]any)
	resume, _ := conversation["resume_session_ref"].(string)
	return strings.TrimSpace(resume) != ""
}

func (s *Service) setCoordinatorWaitingUser(ctx context.Context, state *domain.TaskCoordinatorState,
	run *domain.ExecutionRun, summary, nextAction string) error {
	return s.store.InTx(ctx, func(ctx context.Context) error {
		fresh, err := s.store.TaskCoordinators().GetState(ctx, state.RootWorkItemID)
		if err != nil {
			return err
		}
		if fresh.CurrentRunID != run.ID {
			return nil
		}
		// §7.11 终态钩子：存在未消费 actionable 评论时不进入 waiting_user，
		// 改排 queued/message，由下一 durable turn 消费评论。
		hasUnconsumed, hcErr := s.store.TaskComments().HasUnconsumedActionable(ctx, fresh.RootWorkItemID, fresh.ConsumedCommentRevision)
		if hcErr != nil {
			return hcErr
		}
		expected := fresh.Version
		kind := domain.EventCoordinatorStateChanged
		if hasUnconsumed {
			fresh.Status = domain.CoordinatorQueued
			fresh.Phase = "message"
			fresh.CurrentAction = "message"
			fresh.Summary = "存在未消费任务反馈，Coordinator 继续处理"
			kind = domain.EventCoordinatorMessageReceived
			summary = "存在未消费任务反馈，已改排 Coordinator 队列"
			nextAction = "消费未处理的任务反馈"
		} else {
			fresh.Status = domain.CoordinatorWaitingUser
			fresh.Phase = "waiting_user"
			fresh.CurrentAction = nextAction
		}
		fresh.CurrentRunID = ""
		if err := s.store.TaskCoordinators().UpdateState(ctx, fresh, expected); err != nil {
			return err
		}
		fresh.Version = expected + 1
		return s.appendCoordinatorEvent(ctx, fresh, run.WorkItemID, kind,
			summary, run.ID, run.AgentProfileID, coordinatorRunAttempt(ctx, s.store.Runs(), run),
			"", nil, map[string]any{"stage": "failure", "next_action": nextAction})
	})
}

func coordinatorRunAttempt(ctx context.Context, runs RunRepo, run *domain.ExecutionRun) int {
	attempt := 1
	current := run
	for current != nil && current.RetryOf != "" && attempt < 100 {
		parent, err := runs.Get(ctx, current.RetryOf)
		if err != nil {
			break
		}
		attempt++
		current = parent
	}
	return attempt
}

func coordinatorRunRetryable(run *domain.ExecutionRun) bool {
	if run == nil {
		return false
	}
	if run.Status == domain.RunLost {
		return true
	}
	if run.Failure != nil && run.Failure.Retryable {
		return true
	}
	switch runtime.ErrorFamily(run.ErrorFamily) {
	case runtime.FamilyTransientUpstream, runtime.FamilyIO, runtime.FamilyTimeout:
		return true
	case runtime.FamilySessionUnknown:
		return isSystemCoordinatorRun(run)
	}
	text := strings.ToLower(coordinatorFailureText(run))
	// Some transports fail before an adapter can classify the error. Treat the
	// observed stream/body decoding failures as retryable rather than leaving a
	// Task silently stopped.
	return strings.Contains(text, "stream disconnected") || strings.Contains(text, "transport error") ||
		strings.Contains(text, "network error") || strings.Contains(text, "error decoding response body")
}

func coordinatorFailureCode(run *domain.ExecutionRun) string {
	if run != nil && run.Failure != nil && run.Failure.Code != "" {
		return run.Failure.Code
	}
	if run != nil && run.ErrorFamily != "" {
		return run.ErrorFamily
	}
	return "run_failed"
}

func coordinatorFailureText(run *domain.ExecutionRun) string {
	if run == nil {
		return "unknown run failure"
	}
	if run.Failure != nil {
		if run.Failure.Message != "" {
			return run.Failure.Message
		}
		if run.Failure.Code != "" {
			return run.Failure.Code
		}
	}
	if run.ErrorFamily != "" {
		return run.ErrorFamily
	}
	return string(run.Status)
}

func (s *Service) planWorkerCoordinatorContext(ctx context.Context, rootID string,
	plan *domain.Plan, step *domain.PlanStep, agentID string) map[string]any {
	state, err := s.store.TaskCoordinators().GetStateForWorkItem(ctx, rootID)
	if err != nil {
		return nil
	}
	return map[string]any{
		"role": coordinatorWorkerRole, "root_work_item_id": state.RootWorkItemID,
		"state_id": state.ID, "action": "execute", "plan_id": plan.ID,
		"step_seq": step.Seq, "agent_id": agentID, "attempt": 1,
	}
}

func (s *Service) planEvaluationCoordinatorContext(ctx context.Context, plan *domain.Plan) map[string]any {
	state, err := s.store.TaskCoordinators().GetStateForWorkItem(ctx, plan.WorkItemID)
	if err != nil {
		return nil
	}
	return map[string]any{
		"role": coordinatorRole, "root_work_item_id": state.RootWorkItemID,
		"state_id": state.ID, "action": "evaluation", "plan_id": plan.ID,
		"attempt": state.Attempt + 1,
	}
}

func (s *Service) recordCoordinatorWorkerDispatch(ctx context.Context, root, child *domain.WorkItem,
	run *domain.ExecutionRun, plan *domain.Plan, step *domain.PlanStep) error {
	state, err := s.store.TaskCoordinators().GetStateForWorkItem(ctx, root.ID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		return err
	}
	fresh, err := s.store.TaskCoordinators().GetState(ctx, state.RootWorkItemID)
	if err != nil {
		return err
	}
	expected := fresh.Version
	fresh.Status = domain.CoordinatorRunning
	fresh.Phase = "executing"
	fresh.CurrentAgentID = run.AgentProfileID
	fresh.CurrentStep = child.ID
	fresh.CurrentAction = "等待 Worker 执行结果"
	if fresh.Data == nil {
		fresh.Data = map[string]any{}
	}
	fresh.Data["current_agent_id"] = run.AgentProfileID
	if err := s.store.TaskCoordinators().UpdateState(ctx, fresh, expected); err != nil {
		return err
	}
	fresh.Version = expected + 1
	return s.appendCoordinatorEvent(ctx, fresh, child.ID, domain.EventCoordinatorAttemptUpdated,
		"Coordinator 已派发 Worker", run.ID, run.AgentProfileID, 1, "", nil,
		map[string]any{"stage": "dispatch", "status": "queued", "plan_id": plan.ID,
			"step_seq": step.Seq, "max_attempts": coordinatorMaxWorkerAttempts,
			"next_action": fresh.CurrentAction})
}

// ResumeDueTaskCoordinators is the durable recovery entrypoint used both at
// startup and by the periodic coordinator loop.
func (s *Service) ResumeDueTaskCoordinators(ctx context.Context, workspaceID string, limit int) (int, error) {
	states, err := s.store.TaskCoordinators().ListDueStates(ctx, workspaceID, time.Now().UTC(), limit)
	if err != nil {
		return 0, err
	}
	started := 0
	var firstErr error
	for _, state := range states {
		if err := s.StartCoordinator(context.WithoutCancel(ctx), state.RootWorkItemID); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		started++
	}
	return started, firstErr
}

func (s *Service) RunCoordinatorRecoveryLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	_, _ = s.ResumeDueTaskCoordinators(ctx, "", 100)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.ResumeDueTaskCoordinators(ctx, "", 100); err != nil {
				log.Printf("coordinator recovery: %v", err)
			}
		}
	}
}

func (s *Service) resumeCoordinatorAfterUserAction(ctx context.Context, workItemID, action string) {
	state, err := s.store.TaskCoordinators().GetStateForWorkItem(ctx, workItemID)
	if err != nil {
		return
	}
	err = s.store.InTx(ctx, func(ctx context.Context) error {
		fresh, err := s.store.TaskCoordinators().GetState(ctx, state.RootWorkItemID)
		if err != nil {
			return err
		}
		if fresh.Status == domain.CoordinatorCompleted || fresh.Status == domain.CoordinatorCancelled {
			return nil
		}
		expected := fresh.Version
		fresh.Status = domain.CoordinatorQueued
		fresh.Phase = "recovering"
		fresh.CurrentAction = action
		fresh.CurrentRunID = ""
		fresh.BlockerCode, fresh.BlockerMessage = "", ""
		fresh.NextActionAt = nil
		if err := s.store.TaskCoordinators().UpdateState(ctx, fresh, expected); err != nil {
			return err
		}
		fresh.Version = expected + 1
		return s.appendCoordinatorEvent(ctx, fresh, workItemID, domain.EventCoordinatorRecoveryStarted,
			"用户操作后恢复 Coordinator", "", fresh.CoordinatorAgentID, fresh.Attempt,
			action, nil, map[string]any{"stage": "retry", "next_action": "重新规划并继续执行"})
	})
	if err == nil {
		_ = s.StartCoordinator(context.WithoutCancel(ctx), state.RootWorkItemID)
	}
}

func (s *Service) markCoordinatorUserBlocked(ctx context.Context, workItemID string, params BlockParams) {
	state, err := s.store.TaskCoordinators().GetStateForWorkItem(ctx, workItemID)
	if err != nil {
		return
	}
	activeRunID := state.CurrentRunID
	_ = s.store.InTx(ctx, func(ctx context.Context) error {
		fresh, err := s.store.TaskCoordinators().GetState(ctx, state.RootWorkItemID)
		if err != nil {
			return err
		}
		expected := fresh.Version
		fresh.Status = domain.CoordinatorBlocked
		fresh.Phase = "blocked"
		fresh.BlockerCode = params.Code
		fresh.BlockerMessage = params.Message
		fresh.CurrentAction = "等待用户解除阻塞"
		fresh.NextActionAt = nil
		if err := s.store.TaskCoordinators().UpdateState(ctx, fresh, expected); err != nil {
			return err
		}
		fresh.Version = expected + 1
		return s.appendCoordinatorEvent(ctx, fresh, workItemID, domain.EventCoordinatorBlocked,
			"用户暂停并阻塞任务", activeRunID, fresh.CoordinatorAgentID, fresh.Attempt,
			params.Message, nil, map[string]any{"stage": "failure", "failure_code": params.Code,
				"failure_message": params.Message, "retryable": false,
				"next_action": "解除阻塞后 Coordinator 自动继续"})
	})
	if activeRunID != "" {
		_, _ = s.ControlRun(context.WithoutCancel(ctx), activeRunID, "cancel")
	}
}

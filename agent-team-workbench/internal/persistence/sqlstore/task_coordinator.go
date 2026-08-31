package sqlstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// TaskCoordinatorRepo persists the workspace coordinator profile/config and
// the durable root-task control line. It deliberately does not participate in
// Run/Plan state transitions; the engine owns those use cases and consumes this
// port as its recovery/read-model boundary.
type TaskCoordinatorRepo struct{ store *Store }

const coordinatorConfigCols = `id, workspace_id, agent_profile_id, prompt_version,
	runtime_label, fallback_runtime_label, model_ref, fallback_model_ref, reasoning_effort,
	version, created_at, updated_at`

const coordinatorStateCols = `id, workspace_id, root_work_item_id, coordinator_agent_id,
	status, phase, summary, current_action, current_step, current_agent_id,
	current_run_id, attempt, next_action_at, blocker_code, blocker_message,
	last_error, data, consumed_comment_revision, version, created_at, updated_at`

const coordinatorEventCols = `id, workspace_id, root_work_item_id, work_item_id,
	kind, summary, run_id, agent_id, attempt, reason, next_action_at, data, occurred_at`

const coordinatorStateColsQualified = `s.id, s.workspace_id, s.root_work_item_id, s.coordinator_agent_id,
	s.status, s.phase, s.summary, s.current_action, s.current_step, s.current_agent_id,
	s.current_run_id, s.attempt, s.next_action_at, s.blocker_code, s.blocker_message,
	s.last_error, s.data, s.consumed_comment_revision, s.version, s.created_at, s.updated_at`

const coordinatorEventColsQualified = `e.id, e.workspace_id, e.root_work_item_id, e.work_item_id,
	e.kind, e.summary, e.run_id, e.agent_id, e.attempt, e.reason, e.next_action_at, e.data, e.occurred_at`

func coordinatorAgentID(workspaceID string) string  { return "agent_coordinator_" + workspaceID }
func coordinatorConfigID(workspaceID string) string { return "coordcfg_" + workspaceID }

// defaultCoordinatorRuntime follows the machine's configured production
// bindings. A ready Codex binding wins, then ready Kimi; an unavailable
// configured Codex/Kimi is still preferred over silently switching to mock so
// the engine can expose the real configuration error. Mock is only the final
// no-binding default used by tests and installations without a CLI runtime.
func (r *TaskCoordinatorRepo) defaultCoordinatorRuntime(ctx context.Context, workspaceID string) (string, domain.ModelRef) {
	var fallback *domain.RuntimeBinding
	for _, label := range []string{"codex_local", "kimi_local"} {
		binding, err := r.store.Bindings().GetByLabel(ctx, workspaceID, label)
		if err != nil {
			continue
		}
		if !domain.TaskCoordinatorRuntimeMatchesAdapter(label, binding.AdapterID) {
			continue
		}
		if binding.Status == domain.BindingReady {
			return label, domain.ModelRef{Provider: binding.Provider, Model: binding.Model, ReasoningEffort: "medium"}
		}
		if fallback == nil {
			fallback = binding
		}
	}
	if fallback != nil {
		return fallback.RuntimeLabel, domain.ModelRef{Provider: fallback.Provider, Model: fallback.Model, ReasoningEffort: "medium"}
	}
	return "mock", domain.ModelRef{Provider: "mock", Model: "mock", ReasoningEffort: "medium"}
}

func (r *TaskCoordinatorRepo) scanConfig(row interface{ Scan(...any) error }, c *domain.TaskCoordinatorConfig) error {
	var modelRef, fallbackModelRef string
	var fallback *string
	var created, updated scanTime
	if err := row.Scan(&c.ID, &c.WorkspaceID, &c.AgentProfileID, &c.PromptVersion,
		&c.RuntimeLabel, &fallback, &modelRef, &fallbackModelRef, &c.ReasoningEffort,
		&c.Version, &created, &updated); err != nil {
		return err
	}
	if fallback != nil {
		c.FallbackRuntimeLabel = *fallback
	}
	if err := jsonInto(modelRef, &c.ModelRef); err != nil {
		return err
	}
	if err := jsonInto(fallbackModelRef, &c.FallbackModelRef); err != nil {
		return err
	}
	if c.ReasoningEffort == "" {
		c.ReasoningEffort = c.ModelRef.ReasoningEffort
	}
	c.CreatedAt, c.UpdatedAt = mustTime(created), mustTime(updated)
	return nil
}

func (r *TaskCoordinatorRepo) scanState(row interface{ Scan(...any) error }, state *domain.TaskCoordinatorState, extra ...any) error {
	var currentAgent, currentRun, blockerCode, blockerMessage, lastError *string
	var nextAction scanTime
	var data string
	var created, updated scanTime
	dest := []any{&state.ID, &state.WorkspaceID, &state.RootWorkItemID,
		&state.CoordinatorAgentID, &state.Status, &state.Phase, &state.Summary,
		&state.CurrentAction, &state.CurrentStep, &currentAgent, &currentRun,
		&state.Attempt, &nextAction, &blockerCode, &blockerMessage, &lastError,
		&data, &state.ConsumedCommentRevision, &state.Version, &created, &updated}
	dest = append(dest, extra...)
	if err := row.Scan(dest...); err != nil {
		return err
	}
	if currentAgent != nil {
		state.CurrentAgentID = *currentAgent
	}
	if currentRun != nil {
		state.CurrentRunID = *currentRun
	}
	if blockerCode != nil {
		state.BlockerCode = *blockerCode
	}
	if blockerMessage != nil {
		state.BlockerMessage = *blockerMessage
	}
	if lastError != nil {
		state.LastError = *lastError
	}
	if err := jsonInto(data, &state.Data); err != nil {
		return err
	}
	state.NextActionAt = optTime(nextAction)
	state.CreatedAt, state.UpdatedAt = mustTime(created), mustTime(updated)
	return nil
}

func (r *TaskCoordinatorRepo) scanEvent(row interface{ Scan(...any) error }, event *domain.TaskCoordinatorEvent) error {
	var runID, agentID, reason *string
	var nextAction scanTime
	var occurred scanTime
	var data string
	if err := row.Scan(&event.ID, &event.WorkspaceID, &event.RootWorkItemID,
		&event.WorkItemID, &event.Kind, &event.Summary, &runID, &agentID,
		&event.Attempt, &reason, &nextAction, &data, &occurred); err != nil {
		return err
	}
	if runID != nil {
		event.RunID = *runID
	}
	if agentID != nil {
		event.AgentID = *agentID
	}
	if reason != nil {
		event.Reason = *reason
	}
	event.NextActionAt = optTime(nextAction)
	event.OccurredAt = mustTime(occurred)
	if err := jsonInto(data, &event.Data); err != nil {
		return err
	}
	return nil
}

// EnsureConfig creates the hidden system profile and its workspace config in
// one transaction. The deterministic profile/config IDs make retries safe
// even when the caller crashes after either insert.
func (r *TaskCoordinatorRepo) EnsureConfig(ctx context.Context, workspaceID string) (*domain.TaskCoordinatorConfig, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return nil, fmt.Errorf("%w: workspace id required", domain.ErrValidation)
	}
	var out *domain.TaskCoordinatorConfig
	err := r.store.InTx(ctx, func(ctx context.Context) error {
		if _, err := r.store.Workspaces().Get(ctx, workspaceID); err != nil {
			return err
		}
		now := timeNow()
		profileID := coordinatorAgentID(workspaceID)
		runtimeLabel, modelRef := r.defaultCoordinatorRuntime(ctx, workspaceID)
		_, err := r.store.execStmt(ctx, r.store.exec(ctx),
			`INSERT INTO agent_profiles(
				id, workspace_id, kind, slug, name, role, skills, instructions, avatar,
				availability, presence, runtime_preference, model_override, policy,
				heartbeat_enabled, heartbeat_interval_sec, wake_on_assignment, wake_on_demand,
				wake_on_automation, prompt_template, last_heartbeat_at, version, created_at,
				updated_at, prompt_version, instructions_editable
			) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			 ON CONFLICT (id) DO NOTHING`,
			profileID, workspaceID, domain.AgentProfileKindTaskCoordinator, "",
			"Task Coordinator", "task_coordinator", "[]", "", nil,
			domain.AgentEnabled, domain.PresenceIdle, "{}", "{}",
			`{"tools":[],"approval_policy":"manual","sandbox":"read-only"}`,
			false, 0, false, false, false, "", nil, 1,
			r.store.dialect.TimeParam(now), r.store.dialect.TimeParam(now),
			domain.TaskCoordinatorPromptVersion, false)
		if err != nil {
			return r.store.mapErr(err)
		}
		var kind, profileWorkspace string
		if err := r.store.queryRow(ctx, r.store.exec(ctx),
			`SELECT workspace_id, kind FROM agent_profiles WHERE id=?`, profileID).
			Scan(&profileWorkspace, &kind); err != nil {
			return r.store.mapErr(err)
		}
		if profileWorkspace != workspaceID || domain.AgentProfileKind(kind) != domain.AgentProfileKindTaskCoordinator {
			return fmt.Errorf("%w: coordinator profile identity conflict", domain.ErrStateConflict)
		}
		_, err = r.store.execStmt(ctx, r.store.exec(ctx),
			`INSERT INTO task_coordinator_configs(
				id, workspace_id, agent_profile_id, prompt_version, runtime_label,
				fallback_runtime_label, model_ref, fallback_model_ref, reasoning_effort, version, created_at, updated_at
			) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
			 ON CONFLICT (workspace_id) DO NOTHING`,
			coordinatorConfigID(workspaceID), workspaceID, profileID,
			domain.TaskCoordinatorPromptVersion, runtimeLabel, nil, jsonText(modelRef), "{}", "medium", 1,
			r.store.dialect.TimeParam(now), r.store.dialect.TimeParam(now))
		if err != nil {
			return r.store.mapErr(err)
		}
		c := &domain.TaskCoordinatorConfig{}
		if err := r.scanConfig(r.store.queryRow(ctx, r.store.exec(ctx),
			`SELECT `+coordinatorConfigCols+` FROM task_coordinator_configs WHERE workspace_id=?`, workspaceID), c); err != nil {
			return r.store.mapErr(err)
		}
		out = c
		return nil
	})
	return out, err
}

func (r *TaskCoordinatorRepo) GetConfig(ctx context.Context, workspaceID string) (*domain.TaskCoordinatorConfig, error) {
	c := &domain.TaskCoordinatorConfig{}
	if err := r.scanConfig(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+coordinatorConfigCols+` FROM task_coordinator_configs WHERE workspace_id=?`, workspaceID), c); err != nil {
		return nil, r.store.mapErr(err)
	}
	return c, nil
}

func (r *TaskCoordinatorRepo) UpdateConfig(ctx context.Context, c *domain.TaskCoordinatorConfig, expectedVersion int) error {
	if c == nil || strings.TrimSpace(c.WorkspaceID) == "" {
		return fmt.Errorf("%w: coordinator config required", domain.ErrValidation)
	}
	if c.PromptVersion != "" && c.PromptVersion != domain.TaskCoordinatorPromptVersion {
		return fmt.Errorf("%w: coordinator prompt is system-owned", domain.ErrValidation)
	}
	reasoning := c.EffectiveReasoningEffort()
	if reasoning == "" {
		reasoning = "medium"
	}
	if !domain.ValidTaskCoordinatorRuntimeLabel(c.RuntimeLabel) {
		return fmt.Errorf("%w: coordinator runtime must be mock, codex_local, or kimi_local", domain.ErrValidation)
	}
	if c.FallbackRuntimeLabel != "" && !domain.ValidTaskCoordinatorRuntimeLabel(c.FallbackRuntimeLabel) {
		return fmt.Errorf("%w: coordinator fallback runtime must be mock, codex_local, or kimi_local", domain.ErrValidation)
	}
	if c.FallbackRuntimeLabel != "" && c.FallbackRuntimeLabel == c.RuntimeLabel {
		return fmt.Errorf("%w: coordinator fallback runtime 必须与主 runtime 不同", domain.ErrValidation)
	}
	if !domain.ValidTaskCoordinatorReasoningEffort(reasoning) {
		return fmt.Errorf("%w: coordinator reasoning_effort must be minimal, low, medium, high, or xhigh", domain.ErrValidation)
	}
	if expectedVersion == 0 {
		current, err := r.GetConfig(ctx, c.WorkspaceID)
		if err != nil {
			return err
		}
		expectedVersion = current.Version
	}
	model := c.ModelRef
	model.ReasoningEffort = reasoning
	fallbackModel := c.FallbackModelRef
	if fallbackModel.ReasoningEffort == "" {
		fallbackModel.ReasoningEffort = reasoning
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE task_coordinator_configs SET runtime_label=?, fallback_runtime_label=?,
			model_ref=?, fallback_model_ref=?, reasoning_effort=?, version=version+1, updated_at=?
		 WHERE workspace_id=? AND version=?`,
		c.RuntimeLabel, nullString(c.FallbackRuntimeLabel), jsonText(model), jsonText(fallbackModel), reasoning,
		r.store.dialect.TimeParam(timeNow()), c.WorkspaceID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	return nil
}

func (r *TaskCoordinatorRepo) validateRootTask(ctx context.Context, rootID, coordinatorAgentID string) (*domain.WorkItem, error) {
	wi, err := r.store.WorkItems().Get(ctx, rootID)
	if err != nil {
		return nil, err
	}
	if wi.ParentID != "" || wi.RecordKind != domain.RecordKindTask {
		return nil, fmt.Errorf("%w: coordinator state requires a root task", domain.ErrValidation)
	}
	profile, err := r.store.Agents().Get(ctx, coordinatorAgentID)
	if err != nil {
		return nil, err
	}
	if profile.WorkspaceID != wi.WorkspaceID || profile.Kind != domain.AgentProfileKindTaskCoordinator {
		return nil, fmt.Errorf("%w: coordinator agent must be the workspace system profile", domain.ErrValidation)
	}
	return wi, nil
}

func (r *TaskCoordinatorRepo) CreateState(ctx context.Context, state *domain.TaskCoordinatorState) error {
	if state == nil || state.ID == "" {
		return fmt.Errorf("%w: coordinator state required", domain.ErrValidation)
	}
	if !state.Status.Valid() {
		return fmt.Errorf("%w: invalid coordinator status", domain.ErrValidation)
	}
	wi, err := r.validateRootTask(ctx, state.RootWorkItemID, state.CoordinatorAgentID)
	if err != nil {
		return err
	}
	if state.WorkspaceID == "" {
		state.WorkspaceID = wi.WorkspaceID
	}
	if state.WorkspaceID != wi.WorkspaceID {
		return fmt.Errorf("%w: coordinator state workspace mismatch", domain.ErrValidation)
	}
	if state.Version == 0 {
		state.Version = 1
	}
	if state.CreatedAt.IsZero() {
		state.CreatedAt = timeNow()
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = state.CreatedAt
	}
	_, err = r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO task_coordinator_states(`+coordinatorStateCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		state.ID, state.WorkspaceID, state.RootWorkItemID, state.CoordinatorAgentID,
		state.Status, state.Phase, state.Summary, state.CurrentAction, state.CurrentStep,
		nullString(state.CurrentAgentID), nullString(state.CurrentRunID), state.Attempt,
		r.store.dialect.NullTimeParam(state.NextActionAt), state.BlockerCode,
		state.BlockerMessage, state.LastError, jsonText(state.Data),
		state.ConsumedCommentRevision,
		state.Version, r.store.dialect.TimeParam(state.CreatedAt), r.store.dialect.TimeParam(state.UpdatedAt))
	return r.store.mapErr(err)
}

func (r *TaskCoordinatorRepo) GetState(ctx context.Context, rootWorkItemID string) (*domain.TaskCoordinatorState, error) {
	state := &domain.TaskCoordinatorState{}
	if err := r.scanState(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+coordinatorStateCols+` FROM task_coordinator_states WHERE root_work_item_id=?`, rootWorkItemID), state); err != nil {
		return nil, r.store.mapErr(err)
	}
	return state, nil
}

func (r *TaskCoordinatorRepo) GetStateForWorkItem(ctx context.Context, workItemID string) (*domain.TaskCoordinatorState, error) {
	state := &domain.TaskCoordinatorState{}
	query := `WITH RECURSIVE ancestors(id, parent_id) AS (
			SELECT id, parent_id FROM work_items WHERE id=?
			UNION ALL
			SELECT wi.id, wi.parent_id FROM work_items wi JOIN ancestors a ON wi.id=a.parent_id
		)
		SELECT ` + coordinatorStateColsQualified + `
		FROM task_coordinator_states s
		JOIN ancestors a ON a.parent_id IS NULL AND a.id=s.root_work_item_id
		LIMIT 1`
	if err := r.scanState(r.store.queryRow(ctx, r.store.exec(ctx), query, workItemID), state); err != nil {
		return nil, r.store.mapErr(err)
	}
	return state, nil
}

func (r *TaskCoordinatorRepo) UpdateState(ctx context.Context, state *domain.TaskCoordinatorState, expectedVersion int) error {
	if state == nil || state.RootWorkItemID == "" {
		return fmt.Errorf("%w: coordinator state required", domain.ErrValidation)
	}
	if !state.Status.Valid() {
		return fmt.Errorf("%w: invalid coordinator status", domain.ErrValidation)
	}
	if state.Attempt < 0 {
		return fmt.Errorf("%w: coordinator attempt must be non-negative", domain.ErrValidation)
	}
	wi, err := r.validateRootTask(ctx, state.RootWorkItemID, state.CoordinatorAgentID)
	if err != nil {
		return err
	}
	if state.WorkspaceID == "" {
		state.WorkspaceID = wi.WorkspaceID
	}
	if state.WorkspaceID != wi.WorkspaceID {
		return fmt.Errorf("%w: coordinator state workspace mismatch", domain.ErrValidation)
	}
	if expectedVersion == 0 {
		current, err := r.GetState(ctx, state.RootWorkItemID)
		if err != nil {
			return err
		}
		expectedVersion = current.Version
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE task_coordinator_states SET status=?, phase=?, summary=?, current_action=?,
			current_step=?, current_agent_id=?, current_run_id=?, attempt=?, next_action_at=?,
			blocker_code=?, blocker_message=?, last_error=?, data=?, consumed_comment_revision=?,
			version=version+1, updated_at=?
		 WHERE root_work_item_id=? AND version=?`,
		state.Status, state.Phase, state.Summary, state.CurrentAction, state.CurrentStep,
		nullString(state.CurrentAgentID), nullString(state.CurrentRunID), state.Attempt,
		r.store.dialect.NullTimeParam(state.NextActionAt), state.BlockerCode,
		state.BlockerMessage, state.LastError, jsonText(state.Data),
		state.ConsumedCommentRevision,
		r.store.dialect.TimeParam(timeNow()), state.RootWorkItemID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	return nil
}

// coordinatorStateNeedsResume keeps the periodic recovery scan from treating
// an execution checkpoint as a new Coordinator turn. A running state without
// a current run is only recoverable when the state explicitly records a
// control action, a due time, or an unconsumed actionable comment (RFC §7.7:
// ListDueStates 把「无 current Run 且有未消费 actionable comment」的 state 作
// 为 durable due 候选，避免评论永久悬置)；otherwise it is an observation-only
// checkpoint (for example, while a worker result is being settled).
func coordinatorStateNeedsResume(state *domain.TaskCoordinatorState, hasUnconsumedActionable bool) bool {
	if state == nil {
		return false
	}
	switch state.Status {
	case domain.CoordinatorQueued:
		return true
	case domain.CoordinatorWaitingRetry:
		if state.NextActionAt != nil {
			return true
		}
		if state.Data != nil {
			action, _ := state.Data["control_action"].(string)
			return strings.TrimSpace(action) != ""
		}
	case domain.CoordinatorRunning:
		if state.CurrentRunID != "" || state.NextActionAt != nil {
			return true
		}
		if hasUnconsumedActionable {
			return true
		}
		if state.Data != nil {
			action, _ := state.Data["control_action"].(string)
			return strings.TrimSpace(action) != ""
		}
	}
	return false
}

func (r *TaskCoordinatorRepo) ListDueStates(ctx context.Context, workspaceID string, now time.Time, limit int) ([]*domain.TaskCoordinatorState, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	// Keep the control-action predicate in SQL so a large number of idle
	// running checkpoints cannot consume the LIMIT before a real queued/retry
	// state is returned. The JSON column needs a dialect-specific accessor.
	actionText := `NULLIF(TRIM(COALESCE(json_extract(data, '$.control_action'), '')), '') IS NOT NULL`
	if r.store.dialect.Name == "postgres" {
		actionText = `NULLIF(BTRIM(COALESCE(data->>'control_action', '')), '') IS NOT NULL`
	}
	// RFC §7.7：running 且无 current Run 的 observation checkpoint 若有未消费
	// actionable 评论，也是 durable due 候选（恢复循环兜底，避免永久悬置）。
	unconsumedActionable := `EXISTS (SELECT 1 FROM task_comments tc
		WHERE tc.root_work_item_id = task_coordinator_states.root_work_item_id
		  AND tc.revision > task_coordinator_states.consumed_comment_revision
		  AND tc.kind IN ('requirement','review_feedback'))`
	where := `((status=? OR (status=? AND
		(next_action_at IS NOT NULL OR ` + actionText + `)) OR (status=? AND
		(current_run_id IS NOT NULL OR next_action_at IS NOT NULL OR ` + actionText + `)) OR
		(status=? AND COALESCE(current_run_id,'')='' AND ` + unconsumedActionable + `))
		AND (next_action_at IS NULL OR next_action_at <= ?))`
	args := []any{domain.CoordinatorQueued, domain.CoordinatorWaitingRetry,
		domain.CoordinatorRunning, domain.CoordinatorRunning, r.store.dialect.TimeParam(now)}
	if workspaceID != "" {
		where += ` AND workspace_id=?`
		args = append(args, workspaceID)
	}
	args = append(args, limit)
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+coordinatorStateCols+`, (`+unconsumedActionable+`) AS has_unconsumed FROM task_coordinator_states WHERE `+where+
			` ORDER BY COALESCE(next_action_at, updated_at), updated_at, id LIMIT ?`, args...)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.TaskCoordinatorState
	for rows.Next() {
		state := &domain.TaskCoordinatorState{}
		var hasUnconsumed bool
		if err := r.scanState(rows, state, &hasUnconsumed); err != nil {
			return nil, err
		}
		if !coordinatorStateNeedsResume(state, hasUnconsumed) {
			continue
		}
		out = append(out, state)
	}
	return out, rows.Err()
}

func (r *TaskCoordinatorRepo) validateEventScope(ctx context.Context, event *domain.TaskCoordinatorEvent) error {
	if event == nil || event.ID == "" || event.Kind == "" || event.RootWorkItemID == "" || event.WorkItemID == "" {
		return fmt.Errorf("%w: coordinator event requires id/kind/root/work item", domain.ErrValidation)
	}
	if event.Attempt < 0 {
		return fmt.Errorf("%w: coordinator event attempt must be non-negative", domain.ErrValidation)
	}
	// Resolve the ancestry in SQL so an event cannot attach an unrelated Task
	// in the same workspace to a root timeline.
	var workspaceID string
	var rootID string
	query := `WITH RECURSIVE ancestors(id, parent_id) AS (
			SELECT id, parent_id FROM work_items WHERE id=?
			UNION ALL
			SELECT wi.id, wi.parent_id FROM work_items wi JOIN ancestors a ON wi.id=a.parent_id
		)
		SELECT root.workspace_id, root.id
		FROM work_items root
		JOIN ancestors a ON a.parent_id IS NULL AND a.id=root.id
		JOIN work_items item ON item.id=?
		WHERE root.record_kind=? AND item.record_kind=? AND item.workspace_id=root.workspace_id
		LIMIT 1`
	if err := r.store.queryRow(ctx, r.store.exec(ctx), query,
		event.WorkItemID, event.WorkItemID, domain.RecordKindTask, domain.RecordKindTask).
		Scan(&workspaceID, &rootID); err != nil {
		return r.store.mapErr(err)
	}
	if rootID != event.RootWorkItemID {
		return fmt.Errorf("%w: coordinator event work item is outside root task", domain.ErrValidation)
	}
	if event.WorkspaceID == "" {
		event.WorkspaceID = workspaceID
	}
	if event.WorkspaceID != workspaceID {
		return fmt.Errorf("%w: coordinator event workspace mismatch", domain.ErrValidation)
	}
	return nil
}

func (r *TaskCoordinatorRepo) AppendEvent(ctx context.Context, event *domain.TaskCoordinatorEvent) error {
	if err := r.validateEventScope(ctx, event); err != nil {
		return err
	}
	if event.Data == nil {
		event.Data = make(map[string]any)
	}
	// Persist the same scope facts that the frontend uses for fail-closed SSE
	// routing. Do not let individual engine callers forget record_kind.
	event.Data["root_work_item_id"] = event.RootWorkItemID
	event.Data["work_item_id"] = event.WorkItemID
	event.Data["record_kind"] = string(domain.RecordKindTask)
	if event.OccurredAt.IsZero() {
		event.OccurredAt = timeNow()
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO task_coordinator_events(`+coordinatorEventCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		event.ID, event.WorkspaceID, event.RootWorkItemID, event.WorkItemID,
		event.Kind, event.Summary, nullString(event.RunID), nullString(event.AgentID),
		event.Attempt, event.Reason, r.store.dialect.NullTimeParam(event.NextActionAt),
		jsonText(event.Data), r.store.dialect.TimeParam(event.OccurredAt))
	return r.store.mapErr(err)
}

func (r *TaskCoordinatorRepo) ListEvents(ctx context.Context, workItemID string, limit int) ([]*domain.TaskCoordinatorEvent, error) {
	if strings.TrimSpace(workItemID) == "" {
		return nil, fmt.Errorf("%w: work item id required", domain.ErrValidation)
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	query := `WITH RECURSIVE ancestors(id, parent_id) AS (
			SELECT id, parent_id FROM work_items WHERE id=?
			UNION ALL
			SELECT wi.id, wi.parent_id FROM work_items wi JOIN ancestors a ON wi.id=a.parent_id
		)
		SELECT ` + coordinatorEventColsQualified + `
		FROM task_coordinator_events e
		JOIN ancestors a ON a.parent_id IS NULL AND a.id=e.root_work_item_id
		ORDER BY e.occurred_at ASC, e.id ASC LIMIT ?`
	rows, err := r.store.query(ctx, r.store.exec(ctx), query, workItemID, limit)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.TaskCoordinatorEvent
	for rows.Next() {
		event := &domain.TaskCoordinatorEvent{}
		if err := r.scanEvent(rows, event); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

var _ application.TaskCoordinatorRepo = (*TaskCoordinatorRepo)(nil)

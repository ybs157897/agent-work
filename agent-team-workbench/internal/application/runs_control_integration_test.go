package application_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// ── F1：starting/reconnecting/succeeding 双向控制断裂回归 ────────────────

// TestControlRunStartingInterrupt 回归：starting 态 interrupt 曾因无
// starting→interrupted 边整体 TransitionError；现在直达终态成功。
func TestControlRunStartingInterrupt(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wi := seedRunEnv(t, ctx, svc, store)
	run, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: wi.AgentProfileID, Instruction: "排队中",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		action string
		want   domain.RunStatus
	}{
		{"interrupt", domain.RunInterrupted},
	} {
		got, err := svc.ControlRun(ctx, run.ID, tc.action)
		if err != nil {
			t.Fatalf("%s: starting 态控制不应报错: %v", tc.action, err)
		}
		if got.Status != tc.want {
			t.Fatalf("%s: starting 态应直达 %s，实际 %s", tc.action, tc.want, got.Status)
		}
	}
}

// TestControlRunReconnectingSucceedingDirect 回归：reconnecting/succeeding 态的
// interrupt/cancel 曾固定映射中间态而非法迁移；现在直达终态。
func TestControlRunReconnectingSucceedingDirect(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wi := seedRunEnv(t, ctx, svc, store)
	cases := []struct {
		name   string
		via    []domain.RunStatus
		action string
		want   domain.RunStatus
	}{
		{"reconnecting cancel", []domain.RunStatus{domain.RunStarting, domain.RunRunning, domain.RunReconnecting}, "cancel", domain.RunCancelled},
		{"reconnecting interrupt", []domain.RunStatus{domain.RunStarting, domain.RunRunning, domain.RunReconnecting}, "interrupt", domain.RunInterrupted},
		{"succeeding cancel", []domain.RunStatus{domain.RunStarting, domain.RunRunning, domain.RunSucceeding}, "cancel", domain.RunCancelled},
		{"succeeding interrupt", []domain.RunStatus{domain.RunStarting, domain.RunRunning, domain.RunSucceeding}, "interrupt", domain.RunInterrupted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
				AgentProfileID: wi.AgentProfileID, Instruction: "控制路径",
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, to := range tc.via {
				if err := svc.RecordRunStatus(ctx, run.ID, to, nil); err != nil {
					t.Fatalf("前置迁移 %s 失败: %v", to, err)
				}
			}
			got, err := svc.ControlRun(ctx, run.ID, tc.action)
			if err != nil {
				t.Fatalf("%s 不应报错: %v", tc.name, err)
			}
			if got.Status != tc.want {
				t.Fatalf("%s 应直达 %s，实际 %s", tc.name, tc.want, got.Status)
			}
		})
	}
}

// ── F3：input_tokens_cum 幂等累计回归 ─────────────────────────────────

// TestUsageAnchorIdempotentAccumulation 回归：同一 run 的重复用量上报
// （OnUsage 与 ExecResult.Usage 双路径）曾重复累加锚点 token；
// 现在按 run 维度差值累计，不同 run 正常累加，session_cumulative 不触碰锚点。
func TestUsageAnchorIdempotentAccumulation(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wi := seedRunEnv(t, ctx, svc, store)
	run, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: wi.AgentProfileID, Instruction: "用量",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunSessionRef(ctx, run.ID, "codex://thread_usage"); err != nil {
		t.Fatal(err)
	}
	cum := func() int64 {
		ts, err := store.TaskSessions().Get(ctx, wi.WorkspaceID, wi.AgentProfileID, "codex-appserver", wi.ID)
		if err != nil || ts == nil {
			t.Fatalf("锚点缺失: %v %#v", err, ts)
		}
		return ts.InputTokensCum
	}
	perRun := func(in int64) atwruntime.Usage {
		return atwruntime.Usage{InputTokens: in, OutputTokens: 10, Basis: atwruntime.UsagePerRun}
	}

	// 首报计入；同值重报不双计（曾重复累加为 2400）。
	if err := svc.RecordRunUsage(ctx, run.ID, perRun(1200)); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunUsage(ctx, run.ID, perRun(1200)); err != nil {
		t.Fatal(err)
	}
	if got := cum(); got != 1200 {
		t.Fatalf("同值重报不应双计: %d", got)
	}
	// 增量成长只补差。
	if err := svc.RecordRunUsage(ctx, run.ID, perRun(1500)); err != nil {
		t.Fatal(err)
	}
	if got := cum(); got != 1500 {
		t.Fatalf("增长上报只补差: %d", got)
	}
	// session_cumulative 口径不触碰锚点。
	if err := svc.RecordRunUsage(ctx, run.ID, atwruntime.Usage{
		InputTokens: 99, OutputTokens: 1, Basis: atwruntime.UsageSessionCumulative}); err != nil {
		t.Fatal(err)
	}
	if got := cum(); got != 1500 {
		t.Fatalf("session_cumulative 不应累计锚点: %d", got)
	}
	after, err := store.Runs().Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.UsageIn != 99 || after.UsageBasis != "session_cumulative" {
		t.Fatalf("run 用量覆盖异常: in=%d basis=%s", after.UsageIn, after.UsageBasis)
	}

	// 不同 run 各自从 0 水位起算，正常累加。
	second, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: wi.AgentProfileID, Instruction: "第二轮",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, second.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunSessionRef(ctx, second.ID, "codex://thread_usage"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunUsage(ctx, second.ID, perRun(800)); err != nil {
		t.Fatal(err)
	}
	if got := cum(); got != 2300 {
		t.Fatalf("不同 run 应正常累加: %d", got)
	}
}

// ── F4：agent_presence.updated 事件生产者回归 ─────────────────────────

// TestAgentPresenceEventsOnRunTransitions 回归：presence 变化只写库不发事件，
// 前端 agents 列表无法刷新；现在变化时同事务发 agent_presence.updated。
func TestAgentPresenceEventsOnRunTransitions(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wi := seedRunEnv(t, ctx, svc, store)
	run, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: wi.AgentProfileID, Instruction: "presence",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunRunning, nil); err != nil {
		t.Fatal(err)
	}
	// run 已在 running：直接推进终态（finishRun 会重复迁移 running）。
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunSucceeding, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunSucceeded, nil); err != nil {
		t.Fatal(err)
	}

	agent, err := store.Agents().Get(ctx, wi.AgentProfileID)
	if err != nil {
		t.Fatal(err)
	}
	if agent.Presence != domain.PresenceIdle {
		t.Fatalf("终态后 presence 应回 idle，实际 %s", agent.Presence)
	}
	events, err := store.Events().Since(ctx, wi.WorkspaceID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	var seen []map[string]any
	for _, ev := range events {
		if ev.Type == domain.EventAgentPresenceUpdated {
			seen = append(seen, ev.Data)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("应产生 2 条 presence 事件（busy→idle），实际 %d: %#v", len(seen), seen)
	}
	if seen[0]["presence"] != string(domain.PresenceBusy) || seen[1]["presence"] != string(domain.PresenceIdle) {
		t.Fatalf("presence 事件顺序/取值异常: %#v", seen)
	}
	for _, d := range seen {
		if d["agent_id"] != wi.AgentProfileID || d["run_id"] != run.ID {
			t.Fatalf("presence 事件载荷应带 agent_id/run_id: %#v", d)
		}
	}
}

// ── F5：activity 事务原子性回归 ───────────────────────────────────────

// TestActivityOutsideTxWritesStreamAndOutbox 回归：事务外调用的 activity
// （如 RequestAgentWake）曾以两条独立 autocommit 写 activities 与
// stream_events/outbox，中途崩溃会分裂；现在自包事务，两边同事务成对出现。
func TestActivityOutsideTxWritesStreamAndOutbox(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_act", Name: "act", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	binding := &domain.RuntimeBinding{
		ID: "rb_act", WorkspaceID: ws.ID, RuntimeLabel: "codex_local", AdapterID: "codex-appserver",
		Capabilities: map[string]string{"resume": "supported"}, Status: domain.BindingReady,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Bindings().Create(ctx, binding); err != nil {
		t.Fatal(err)
	}
	agent, err := svc.CreateAgent(ctx, ws.ID, application.CreateAgentParams{
		Name: "Waker", Role: "developer",
		RuntimePreference: domain.RuntimePreference{Preferred: "codex_local"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wi, err := svc.CreateWorkItem(ctx, ws.ID, application.CreateWorkItemParams{Title: "唤醒", AgentProfileID: agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	// RequestAgentWake 在事务外调用 activity（wakeup.go）。
	if _, err := svc.RequestAgentWake(ctx, agent.ID, wi.ID, "推进", nil); err != nil {
		t.Fatal(err)
	}

	// 找到 wake_requested 活动事件，校验 stream_events 与 outbox 成对存在。
	rows, err := db.Query(`SELECT s.event_id FROM stream_events s
		WHERE s.workspace_id=? AND s.event_type='activity.appended'
		  AND s.payload LIKE '%agent.wake_requested%'`, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	var eventIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		eventIDs = append(eventIDs, id)
	}
	rows.Close()
	if len(eventIDs) == 0 {
		t.Fatal("事务外 activity 应产生 activity.appended 事件")
	}
	for _, id := range eventIDs {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM outbox_messages WHERE event_id=?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("activity 事件 %s 应同事务写入 outbox（found %d）", id, n)
		}
	}
	// activities 流本身也要有对应记录。
	var activities int
	if err := db.QueryRow(`SELECT count(*) FROM activities WHERE workspace_id=? AND kind='agent.wake_requested'`, ws.ID).Scan(&activities); err != nil {
		t.Fatal(err)
	}
	if activities != 1 {
		t.Fatalf("activities 行缺失: %d", activities)
	}
}

// ── F6：自愈重试 RetryOf 回归 ─────────────────────────────────────────

// TestSelfHealRunFillsRetryOf 回归：自愈创建的新 run 曾只写 input.auto_heal_of，
// Run.RetryOf 留空导致重试链不可追溯；现在两处都填源 run id。
func TestSelfHealRunFillsRetryOf(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wi := seedRunEnv(t, ctx, svc, store)
	first, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: wi.AgentProfileID, Instruction: "教暗号",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, first.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunSessionRef(ctx, first.ID, "codex://thread_retry"); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, first.ID, "暗号是 X-1"); err != nil {
		t.Fatal(err)
	}
	second, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: wi.AgentProfileID, Instruction: "报暗号",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, second.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, second.ID, domain.RunRunning, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, second.ID, domain.RunFailed, map[string]any{
		"family": "session_unknown", "code": "session_not_found"}); err != nil {
		t.Fatal(err)
	}

	runs, err := store.Runs().ListByWorkItem(ctx, wi.ID)
	if err != nil {
		t.Fatal(err)
	}
	var healed *domain.ExecutionRun
	for _, r := range runs {
		if r.ID != first.ID && r.ID != second.ID {
			healed = r
		}
	}
	if healed == nil {
		t.Fatal("自愈应创建重试 run")
	}
	if healed.RetryOf != second.ID {
		t.Fatalf("自愈 run RetryOf 应为源 run %s，实际 %q", second.ID, healed.RetryOf)
	}
	if healed.Input["auto_heal_of"] != second.ID {
		t.Fatalf("input.auto_heal_of 应保留: %#v", healed.Input["auto_heal_of"])
	}
}

// ── F7b：resume 能力门控回归 ─────────────────────────────────────────

// TestCreateRunResumeRequiresCapability 回归：binding 未声明 resume=supported 时
// resolveResume 曾仍注入 resume_session_ref；现在不注入（tier-3 全量内联）。
func TestCreateRunResumeRequiresCapability(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_nores", Name: "nores", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	// binding 不声明 resume 能力。
	binding := &domain.RuntimeBinding{
		ID: "rb_nores", WorkspaceID: ws.ID, RuntimeLabel: "codex_local", AdapterID: "codex-appserver",
		Capabilities: map[string]string{}, Status: domain.BindingReady,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Bindings().Create(ctx, binding); err != nil {
		t.Fatal(err)
	}
	agent := &domain.AgentProfile{
		ID: "agent_nores", WorkspaceID: ws.ID, Name: "NR", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "codex_local"},
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	wi, err := svc.CreateWorkItem(ctx, ws.ID, application.CreateWorkItemParams{Title: "nores", AgentProfileID: agent.ID})
	if err != nil {
		t.Fatal(err)
	}

	first, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agent.ID, Instruction: "第一轮"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, first.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	// 锚点存在且指纹匹配（resolveResume 主路径命中），但 binding 无 resume 能力。
	if err := svc.RecordRunSessionRef(ctx, first.ID, "codex://thread_nores"); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, first.ID, "第一轮回复内容"); err != nil {
		t.Fatal(err)
	}

	second, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agent.ID, Instruction: "第二轮"})
	if err != nil {
		t.Fatal(err)
	}
	conv, _ := second.Input["conversation"].(map[string]any)
	if _, has := conv["resume_session_ref"]; has {
		t.Fatalf("binding 未声明 resume 不应注入 resume_session_ref: %#v", conv)
	}
	// 落 tier-3：EffectiveInstruction 内联全量历史。
	effective := atwruntime.EffectiveInstruction(second)
	if !strings.Contains(effective, "第一轮回复内容") || !strings.Contains(effective, "第二轮") {
		t.Fatalf("无 resume 能力应内联历史（tier-3）: %q", effective)
	}
}

// ── F7c：审批幂等重放不重复转发回归 ─────────────────────────────────

// TestResolveApprovalForwardsOnlyOnChange 回归：幂等重放（重复相同决定）曾再次
// 调用 ApprovalForwarder，adapter 收到重复审批决定；现在仅真实变更时转发。
func TestResolveApprovalForwardsOnlyOnChange(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	forwards := 0
	svc.ApprovalForwarder = func(ctx context.Context, runID, approvalID string, approved bool) {
		forwards++
	}

	wi := seedRunEnv(t, ctx, svc, store)
	run, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: wi.AgentProfileID, Instruction: "审批",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunRunning, nil); err != nil {
		t.Fatal(err)
	}
	approval, err := svc.RequestApproval(ctx, run.ID, "command", "high", "需要审批")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveApproval(ctx, approval.ID, true, "user_demo", "ok", domain.ApprovalScopeOnce); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveApproval(ctx, approval.ID, true, "user_demo", "ok", domain.ApprovalScopeOnce); err != nil {
		t.Fatalf("幂等重放应成功: %v", err)
	}
	if forwards != 1 {
		t.Fatalf("幂等重放不应重复转发，实际转发 %d 次", forwards)
	}
	after, err := store.Runs().Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != domain.RunRunning {
		t.Fatalf("approved 后应回 running，实际 %s", after.Status)
	}
}

// ── F7d：AcceptWorkItem expectedVersion=0 回归 ─────────────────────────

// TestAcceptWorkItemZeroExpectedVersion 回归：CheckVersion(0) 放行但 Update
// WHERE version=0 恒 0 行导致验收必败；现在与 MoveWorkItem 同约定用当前版本守卫。
func TestAcceptWorkItemZeroExpectedVersion(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wi := seedRunEnv(t, ctx, svc, store)
	if _, err := svc.MoveWorkItem(ctx, wi.ID, domain.WorkItemInProgress, wi.Version); err != nil {
		t.Fatal(err)
	}
	run, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: wi.AgentProfileID, Instruction: "交付",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, run.ID, "完成"); err != nil {
		t.Fatal(err)
	}
	accepted, err := svc.AcceptWorkItem(ctx, wi.ID, 0)
	if err != nil {
		t.Fatalf("expectedVersion=0 的验收不应失败: %v", err)
	}
	if accepted.Status != domain.WorkItemCompleted {
		t.Fatalf("验收后应为 completed，实际 %s", accepted.Status)
	}
}

// ── F7f：artifact.created 事件投影回归 ─────────────────────────────────

// TestRecordRunEventArtifactProjection 回归：mock 风格 artifact.created 事件
// （载荷带 sha256/logical_path）曾只留事件不落 artifacts 表；
// 现在同时投影 artifacts 行；载荷不全时只保留事件。
func TestRecordRunEventArtifactProjection(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wi := seedRunEnv(t, ctx, svc, store)
	run, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: wi.AgentProfileID, Instruction: "产物",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunRunning, nil); err != nil {
		t.Fatal(err)
	}

	if err := svc.RecordRunEvent(ctx, run.ID, domain.EventArtifactCreated, map[string]any{
		"sha256": "a1b2c3", "logical_path": "reports/summary.md", "size": 42, "mime": "text/markdown",
	}); err != nil {
		t.Fatal(err)
	}
	arts, err := svc.Artifacts(ctx, run.ID)
	if err != nil || len(arts) != 1 {
		t.Fatalf("artifact.created 应投影 artifacts 行: %v %#v", err, arts)
	}
	a := arts[0]
	if a.LogicalPath != "reports/summary.md" || a.Sha256 != "a1b2c3" || a.Size != 42 ||
		a.Status != domain.ArtifactDraft || a.Mime != "text/markdown" {
		t.Fatalf("投影字段异常: %#v", a)
	}

	// 载荷不全：只留事件，不建 artifact。
	if err := svc.RecordRunEvent(ctx, run.ID, domain.EventArtifactCreated, map[string]any{
		"logical_path": "no-hash.md",
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunEvent(ctx, run.ID, domain.EventArtifactCreated, map[string]any{
		"sha256": "d4e5f6",
	}); err != nil {
		t.Fatal(err)
	}
	arts, err = svc.Artifacts(ctx, run.ID)
	if err != nil || len(arts) != 1 {
		t.Fatalf("载荷不全不应建 artifact: %v %#v", err, arts)
	}
	events, err := svc.RunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	artifactEvents := 0
	for _, e := range events {
		if e.EventType == domain.EventArtifactCreated {
			artifactEvents++
		}
	}
	if artifactEvents != 3 {
		t.Fatalf("事件本身应全部保留（3 条），实际 %d", artifactEvents)
	}
}

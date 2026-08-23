package application_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
	"github.com/ybs/agent-team-workbench/internal/scheduling"
)

type captureDispatcher struct{ runs []*domain.ExecutionRun }

func (d *captureDispatcher) Dispatch(ctx context.Context, run *domain.ExecutionRun) error {
	d.runs = append(d.runs, run)
	return nil
}

type noopNotifier struct{}

func (noopNotifier) Notify(string) {}

func TestCreateRunBuildsMultiTurnResumeSnapshot(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())

	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_test", Name: "test", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	agent := &domain.AgentProfile{
		ID: "agent_test", WorkspaceID: ws.ID, Name: "Agent", Role: "developer",
		Instructions: "保持上下文", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "codex_local", Mode: "default"},
		Policy:            domain.AgentPolicy{Sandbox: "workspace-write", ApprovalPolicy: "approve_high_risk"},
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	binding := &domain.RuntimeBinding{
		ID: "rb_test", WorkspaceID: ws.ID, RuntimeLabel: "codex_local", AdapterID: "codex-appserver",
		Capabilities: map[string]string{"multi_turn": "supported", "resume": "supported"},
		Status:       domain.BindingReady, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Bindings().Create(ctx, binding); err != nil {
		t.Fatal(err)
	}
	wi, err := svc.CreateWorkItem(ctx, ws.ID, application.CreateWorkItemParams{
		Title: "conversation", AgentProfileID: agent.ID,
	})
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
	if err := svc.RecordRunSessionRef(ctx, first.ID, "codex://thread_1"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, first.ID, domain.RunRunning, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunEvent(ctx, first.ID, domain.EventMessageCompleted, map[string]any{"role": "assistant", "text": "第一轮回复"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, first.ID, domain.RunSucceeding, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, first.ID, domain.RunSucceeded, nil); err != nil {
		t.Fatal(err)
	}

	second, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agent.ID, Instruction: "第二轮"})
	if err != nil {
		t.Fatal(err)
	}
	conversation, ok := second.Input["conversation"].(map[string]any)
	if !ok {
		t.Fatalf("conversation snapshot 缺失: %#v", second.Input)
	}
	if conversation["resume_session_ref"] != "codex://thread_1" || conversation["resume_from_run_id"] != first.ID {
		t.Fatalf("未续接上一轮 provider session: %#v", conversation)
	}
	if conversation["turn_index"] != 2 {
		t.Fatalf("turn_index = %#v", conversation["turn_index"])
	}
	history, ok := conversation["history"].([]map[string]any)
	if !ok || len(history) != 2 || history[0]["text"] != "第一轮" || history[1]["text"] != "第一轮回复" {
		t.Fatalf("历史快照不完整: %#v", conversation["history"])
	}
	updated, err := store.WorkItems().Get(ctx, wi.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Phase != domain.PhaseExecution {
		t.Fatalf("下一轮开始后 phase 应回到 execution，实际 %s", updated.Phase)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("dispatch count = %d", len(dispatcher.runs))
	}
}

// TestTaskSessionAnchorLifecycle 覆盖 task_sessions 锚点的核心生命周期：
// 双写 → 指纹漂移丢弃 → reset 后 fresh → 用量累计。
func TestTaskSessionAnchorLifecycle(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_ts", Name: "ts", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	agent := &domain.AgentProfile{
		ID: "agent_ts", WorkspaceID: ws.ID, Name: "TS", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "codex_local"},
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	binding := &domain.RuntimeBinding{
		ID: "rb_ts", WorkspaceID: ws.ID, RuntimeLabel: "codex_local", AdapterID: "codex-appserver",
		Capabilities: map[string]string{"resume": "supported"}, Status: domain.BindingReady,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Bindings().Create(ctx, binding); err != nil {
		t.Fatal(err)
	}
	wi, err := svc.CreateWorkItem(ctx, ws.ID, application.CreateWorkItemParams{Title: "anchor", AgentProfileID: agent.ID})
	if err != nil {
		t.Fatal(err)
	}

	first, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agent.ID, Instruction: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, first.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	// 双写：session ref 上报后锚点即存在，且 runs_count=1。
	if err := svc.RecordRunSessionRef(ctx, first.ID, "codex://thread_ts"); err != nil {
		t.Fatal(err)
	}
	ts, err := store.TaskSessions().Get(ctx, ws.ID, agent.ID, "codex-appserver", wi.ID)
	if err != nil || ts == nil {
		t.Fatalf("锚点未落库: %v %#v", err, ts)
	}
	if ts.SessionRef() != "codex://thread_ts" || ts.RunsCount != 1 || ts.Fingerprint() == "" {
		t.Fatalf("锚点内容异常: %#v", ts.SessionParams)
	}

	// 第二轮经 task_sessions 主路径续接（from_run_id 回溯到创建 run）。
	if err := finishRun(ctx, svc, first.ID, "r1"); err != nil {
		t.Fatal(err)
	}
	second, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agent.ID, Instruction: "t2"})
	if err != nil {
		t.Fatal(err)
	}
	conv2, _ := second.Input["conversation"].(map[string]any)
	if conv2["resume_session_ref"] != "codex://thread_ts" || conv2["resume_from_run_id"] != first.ID {
		t.Fatalf("主路径未续接: %#v", conv2)
	}
	if second.SessionBefore != "codex://thread_ts" {
		t.Fatalf("SessionBefore 未记录: %q", second.SessionBefore)
	}

	// 用量累计：per_run 输入 token 累加到锚点。
	if err := svc.RecordRunUsage(ctx, second.ID, atwruntime.Usage{
		InputTokens: 1200, OutputTokens: 300, Basis: atwruntime.UsagePerRun}); err != nil {
		t.Fatal(err)
	}
	ts2, err := store.TaskSessions().Get(ctx, ws.ID, agent.ID, "codex-appserver", wi.ID)
	if err != nil || ts2.InputTokensCum != 1200 {
		t.Fatalf("token 未累计: %v %#v", err, ts2)
	}
	run2, err := store.Runs().Get(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run2.UsageIn != 1200 || run2.UsageBasis != "per_run" {
		t.Fatalf("run 用量未落库: in=%d basis=%s", run2.UsageIn, run2.UsageBasis)
	}

	// reset：锚点删除后下一轮 fresh（无 resume ref）。
	if err := svc.ResetTaskSession(ctx, ws.ID, agent.ID, "codex-appserver", wi.ID); err != nil {
		t.Fatal(err)
	}
	third, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agent.ID, Instruction: "t3"})
	if err != nil {
		t.Fatal(err)
	}
	conv3, _ := third.Input["conversation"].(map[string]any)
	if _, has := conv3["resume_session_ref"]; has {
		t.Fatalf("reset 后不应续接: %#v", conv3)
	}
}

// TestSessionRotationHandoff 覆盖轮换全链路：阈值超限 → fresh + handoff 摘要 →
// 新会话上报后锚点换代（计数清零重起）→ 下一代正常续接。
func TestSessionRotationHandoff(t *testing.T) {
	application.RotationMaxRuns = 2
	defer func() { application.RotationMaxRuns = 40 }()

	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())

	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_rot", Name: "rot", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	agent := &domain.AgentProfile{
		ID: "agent_rot", WorkspaceID: ws.ID, Name: "Rot", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "codex_local"},
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	binding := &domain.RuntimeBinding{
		ID: "rb_rot", WorkspaceID: ws.ID, RuntimeLabel: "codex_local", AdapterID: "codex-appserver",
		Capabilities: map[string]string{"resume": "supported"}, Status: domain.BindingReady,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Bindings().Create(ctx, binding); err != nil {
		t.Fatal(err)
	}
	wi, err := svc.CreateWorkItem(ctx, ws.ID, application.CreateWorkItemParams{Title: "rotate", AgentProfileID: agent.ID})
	if err != nil {
		t.Fatal(err)
	}

	// 代会话：run1 创建会话（runs_count=1），run2 续接（runs_count=2 → 触达阈值）。
	first, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agent.ID, Instruction: "第一轮"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, first.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunSessionRef(ctx, first.ID, "codex://thread_A"); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, first.ID, "暗号是 ECHO-7"); err != nil {
		t.Fatal(err)
	}
	second, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agent.ID, Instruction: "第二轮"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, second.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunSessionRef(ctx, second.ID, "codex://thread_A"); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, second.ID, "收到"); err != nil {
		t.Fatal(err)
	}

	// run3：runs_count=2 达阈值 → 轮换（fresh + handoff，不携带 resume ref）。
	third, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agent.ID, Instruction: "第三轮：继续"})
	if err != nil {
		t.Fatal(err)
	}
	conv3, _ := third.Input["conversation"].(map[string]any)
	if _, has := conv3["resume_session_ref"]; has {
		t.Fatalf("轮换后不应携带 resume ref: %#v", conv3)
	}
	if rot, _ := conv3["session_rotation"].(bool); !rot {
		t.Fatalf("session_rotation 未标记: %#v", conv3)
	}
	summary, _ := conv3["handoff_summary"].(string)
	if !strings.Contains(summary, "ECHO-7") || !strings.Contains(summary, "rotate") {
		t.Fatalf("handoff 摘要缺关键内容: %q", summary)
	}
	// EffectiveInstruction 轮换档：摘要 + 当轮输入，而非全量历史回放。
	effective := atwruntime.EffectiveInstruction(third)
	if !strings.Contains(effective, "会话已轮换") || !strings.Contains(effective, "ECHO-7") || !strings.Contains(effective, "第三轮：继续") {
		t.Fatalf("轮换档 EffectiveInstruction 异常: %q", effective)
	}

	// 新会话上报 → 锚点换代：runs_count 重置为 1。
	if err := svc.RecordRunStatus(ctx, third.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunSessionRef(ctx, third.ID, "codex://thread_B"); err != nil {
		t.Fatal(err)
	}
	ts, err := store.TaskSessions().Get(ctx, ws.ID, agent.ID, "codex-appserver", wi.ID)
	if err != nil || ts == nil {
		t.Fatalf("换代锚点未落库: %v %#v", err, ts)
	}
	if ts.SessionRef() != "codex://thread_B" || ts.RunsCount != 1 {
		t.Fatalf("换代后计数未重起: ref=%s runs=%d", ts.SessionRef(), ts.RunsCount)
	}
	if ts.InputTokensCum != 0 {
		t.Fatalf("换代后 token 计数应清零: %d", ts.InputTokensCum)
	}
	if err := finishRun(ctx, svc, third.ID, "新会话已接续"); err != nil {
		t.Fatal(err)
	}

	// run4：新代正常续接 thread_B。
	fourth, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agent.ID, Instruction: "第四轮"})
	if err != nil {
		t.Fatal(err)
	}
	conv4, _ := fourth.Input["conversation"].(map[string]any)
	if conv4["resume_session_ref"] != "codex://thread_B" {
		t.Fatalf("新代未续接: %#v", conv4)
	}
}

// TestHistoryBudgetTriggersRotation 防回归：resume 不可用的 runtime（tier-3 内联档）
// 历史超出模型窗口预算时必须升级为轮换（handoff 摘要代替全量回放），而不是
// 头部截断——截断会移动请求前缀、令 provider 前缀缓存持续清零。
func TestHistoryBudgetTriggersRotation(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())

	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_budget", Name: "budget", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	agent := &domain.AgentProfile{
		ID: "agent_budget", WorkspaceID: ws.ID, Name: "Budget", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "dsh_local"},
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	binding := &domain.RuntimeBinding{
		ID: "rb_budget", WorkspaceID: ws.ID, RuntimeLabel: "dsh_local", AdapterID: "dsh",
		Capabilities: map[string]string{"resume": "unavailable"}, Status: domain.BindingReady,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Bindings().Create(ctx, binding); err != nil {
		t.Fatal(err)
	}
	wi, err := svc.CreateWorkItem(ctx, ws.ID, application.CreateWorkItemParams{Title: "budget", AgentProfileID: agent.ID})
	if err != nil {
		t.Fatal(err)
	}

	// run1：锚点阈值均未触达（runs_count=1），但回复体量超出回退窗口预算
	//（32768×35%≈11468 token；12000 个 CJK 字符即超）。
	first, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agent.ID, Instruction: "第一轮"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, first.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunSessionRef(ctx, first.ID, "dsh://session_A"); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, first.ID, strings.Repeat("长", 12000)); err != nil {
		t.Fatal(err)
	}

	// run2：锚点阈值未触发，唯一触发源是历史预算 → 必须轮换。
	second, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agent.ID, Instruction: "第二轮：继续"})
	if err != nil {
		t.Fatal(err)
	}
	conv, _ := second.Input["conversation"].(map[string]any)
	if _, has := conv["resume_session_ref"]; has {
		t.Fatalf("预算轮换不应携带 resume ref: %#v", conv)
	}
	if rot, _ := conv["session_rotation"].(bool); !rot {
		t.Fatalf("历史超预算未升级为轮换: %#v", conv)
	}
	summary, _ := conv["handoff_summary"].(string)
	if !strings.Contains(summary, "budget") {
		t.Fatalf("handoff 摘要缺任务信息: %q", summary)
	}
	// 轮换档 EffectiveInstruction：摘要代替全量回放（12000 字历史不得整体注入）。
	effective := atwruntime.EffectiveInstruction(second)
	if !strings.Contains(effective, "会话已轮换") || !strings.Contains(effective, "第二轮：继续") {
		t.Fatalf("轮换档 EffectiveInstruction 异常: %q", effective[:min(len(effective), 200)])
	}
	if strings.Contains(effective, strings.Repeat("长", 500)) {
		t.Fatal("预算轮换后仍全量回放超限历史")
	}
}

// TestSessionUnknownSelfHeal 覆盖 session_unknown 失败自愈：清锚点 + 一次性 fresh 重试，
// 且自愈产物再次 session_unknown 失败时不再递归（防循环）。
func TestSessionUnknownSelfHeal(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())

	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_heal", Name: "heal", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	agent := &domain.AgentProfile{
		ID: "agent_heal", WorkspaceID: ws.ID, Name: "Heal", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "codex_local"},
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	binding := &domain.RuntimeBinding{
		ID: "rb_heal", WorkspaceID: ws.ID, RuntimeLabel: "codex_local", AdapterID: "codex-appserver",
		Capabilities: map[string]string{"resume": "supported"}, Status: domain.BindingReady,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Bindings().Create(ctx, binding); err != nil {
		t.Fatal(err)
	}
	wi, err := svc.CreateWorkItem(ctx, ws.ID, application.CreateWorkItemParams{Title: "heal", AgentProfileID: agent.ID})
	if err != nil {
		t.Fatal(err)
	}

	// run1 建立会话；run2 携带 resume，但 provider 侧会话已丢失 → session_unknown 失败。
	first, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agent.ID, Instruction: "教暗号"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, first.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunSessionRef(ctx, first.ID, "codex://thread_h"); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, first.ID, "暗号是 ECHO-7"); err != nil {
		t.Fatal(err)
	}
	second, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agent.ID, Instruction: "报暗号"})
	if err != nil {
		t.Fatal(err)
	}
	conv2, _ := second.Input["conversation"].(map[string]any)
	if conv2["resume_session_ref"] != "codex://thread_h" {
		t.Fatalf("run2 应携带 resume: %#v", conv2)
	}
	if err := svc.RecordRunStatus(ctx, second.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, second.ID, domain.RunRunning, nil); err != nil {
		t.Fatal(err)
	}
	// RecordRunStatus 落终态后自愈：锚点写 session_unknown_heal 墓碑 + 自动 fresh 重试。
	if err := svc.RecordRunStatus(ctx, second.ID, domain.RunFailed, map[string]any{
		"family": "session_unknown", "code": "session_not_found", "retryable": true}); err != nil {
		t.Fatal(err)
	}

	failed, err := store.Runs().Get(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.ErrorFamily != "session_unknown" {
		t.Fatalf("ErrorFamily 未落库: %q", failed.ErrorFamily)
	}
	ts, err := store.TaskSessions().Get(ctx, ws.ID, agent.ID, "codex-appserver", wi.ID)
	if err != nil || ts == nil {
		t.Fatalf("自愈后锚点应存在（墓碑）: %v %#v", err, ts)
	}
	if ts.SessionRef() != "" || ts.SessionParams["__cleared_reason"] != "session_unknown_heal" {
		t.Fatalf("自愈墓碑异常: %#v", ts.SessionParams)
	}

	runs, err := store.Runs().ListByWorkItem(ctx, wi.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("自愈应自动创建 1 个重试 run，实际 %d 个", len(runs))
	}
	var healed *domain.ExecutionRun
	for _, r := range runs {
		if r.ID != first.ID && r.ID != second.ID {
			healed = r
		}
	}
	if healed == nil || healed.Input["auto_heal_of"] != second.ID {
		t.Fatalf("自愈 run 标记异常: %#v", healed)
	}
	healConv, _ := healed.Input["conversation"].(map[string]any)
	if _, has := healConv["resume_session_ref"]; has {
		t.Fatalf("自愈重试应为 fresh: %#v", healConv)
	}
	if instr, _ := healed.Input["instruction"].(string); instr != "报暗号" {
		t.Fatalf("自愈重试应沿用原指令: %q", instr)
	}

	// 防循环：自愈 run 再次 session_unknown 失败 → 不再产生新 run。
	if err := svc.RecordRunStatus(ctx, healed.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, healed.ID, domain.RunRunning, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, healed.ID, domain.RunFailed, map[string]any{
		"family": "session_unknown", "code": "session_not_found"}); err != nil {
		t.Fatal(err)
	}
	runs, err = store.Runs().ListByWorkItem(ctx, wi.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("自愈链应止于 1 次重试，实际 %d 个 run", len(runs))
	}
}

// TestWakeupSchedulingChain 覆盖 M4 唤醒全链：手动唤醒入队 → 调度循环消费建 run
// （显式 instruction 优先、wakeup 上下文固化）→ 活跃 run 期间二次唤醒被合并 →
// 指派钩子入队 assignment 唤醒并被消费。
func TestWakeupSchedulingChain(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())

	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_wake", Name: "wake", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	// 经 svc 创建：验证 agent 创建缺省（wake_on_assignment/wake_on_demand 默认开）。
	binding := &domain.RuntimeBinding{
		ID: "rb_wake", WorkspaceID: ws.ID, RuntimeLabel: "codex_local", AdapterID: "codex-appserver",
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
	if !agent.WakeOnDemand || !agent.WakeOnAssignment {
		t.Fatalf("agent 唤醒缺省未生效: %#v", agent)
	}
	wi, err := svc.CreateWorkItem(ctx, ws.ID, application.CreateWorkItemParams{Title: "唤醒目标", AgentProfileID: agent.ID})
	if err != nil {
		t.Fatal(err)
	}

	// 手动唤醒 → 入队 queued。
	res, err := svc.RequestAgentWake(ctx, agent.ID, wi.ID, "推进任务", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "queued" {
		t.Fatalf("wake result = %#v", res)
	}
	// task_key 缺失应被拒绝。
	if _, err := svc.RequestAgentWake(ctx, agent.ID, "", "x", nil); err == nil {
		t.Fatal("空 task_key 应报错")
	}

	// 调度循环消费：on_demand 旁路心跳门控（agent 未开 heartbeat），创建 run。
	sched := &scheduling.Scheduler{Store: store.Wakeups(), RunStarter: svc}
	sched.Tick(ctx, time.Now().UTC().Add(time.Minute))

	runs, err := store.Runs().ListByWorkItem(ctx, wi.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("唤醒应产生 1 个 run: %v %#v", err, runs)
	}
	if instr, _ := runs[0].Input["instruction"].(string); instr != "推进任务" {
		t.Fatalf("显式 instruction 应优先: %q", instr)
	}
	if wakeCtx, _ := runs[0].Input["wakeup"].(map[string]any); wakeCtx["instruction"] != "推进任务" {
		t.Fatalf("wakeup 上下文未固化: %#v", runs[0].Input["wakeup"])
	}

	// run 仍在活跃（queued 非终态、进程内无 lease）→ 二次唤醒被合并，不建新 run。
	if _, err := svc.RequestAgentWake(ctx, agent.ID, wi.ID, "再推一次", nil); err != nil {
		t.Fatal(err)
	}
	sched.Tick(ctx, time.Now().UTC().Add(2*time.Minute))
	runs, err = store.Runs().ListByWorkItem(ctx, wi.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("活跃 run 期间应合并，仍 1 个 run: %v %#v", err, runs)
	}
	recent, err := store.Wakeups().RecentByAgentTask(ctx, agent.ID, wi.ID, time.Time{})
	if err != nil || len(recent) != 2 {
		t.Fatalf("应留存 2 条唤醒审计: %v %#v", err, recent)
	}
	statuses := map[string]int{}
	for _, w := range recent {
		statuses[string(w.Status)]++
	}
	if statuses["consumed"] != 1 || statuses["coalesced"] != 1 {
		t.Fatalf("唤醒审计状态异常: %#v", statuses)
	}

	// 指派钩子：新 work item 指派给 agent → assignment 唤醒入队并被消费。
	wi2, err := svc.CreateWorkItem(ctx, ws.ID, application.CreateWorkItemParams{Title: "指派目标"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AssignWorkItem(ctx, wi2.ID, agent.ID, wi2.Version); err != nil {
		t.Fatal(err)
	}
	sched.Tick(ctx, time.Now().UTC().Add(3*time.Minute))
	runs2, err := store.Runs().ListByWorkItem(ctx, wi2.ID)
	if err != nil || len(runs2) != 1 {
		t.Fatalf("指派唤醒应产生 1 个 run: %v %#v", err, runs2)
	}
	// 无显式 instruction → 走缺省模板（含 agent 名与任务标题）。
	if instr, _ := runs2[0].Input["instruction"].(string); instr == "" {
		t.Fatal("指派唤醒 run 应携带渲染后的提示词")
	}
}

// seedRunEnv 搭建最小可执行环境：workspace + resume binding + agent + work item。
func seedRunEnv(t *testing.T, ctx context.Context, svc *application.Service, store *sqlstore.Store) *domain.WorkItem {
	t.Helper()
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_" + t.Name(), Name: "test", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	binding := &domain.RuntimeBinding{
		ID: "rb_" + t.Name(), WorkspaceID: ws.ID, RuntimeLabel: "codex_local", AdapterID: "codex-appserver",
		Capabilities: map[string]string{"resume": "supported"}, Status: domain.BindingReady,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Bindings().Create(ctx, binding); err != nil {
		t.Fatal(err)
	}
	agent := &domain.AgentProfile{
		ID: "agent_" + t.Name(), WorkspaceID: ws.ID, Name: "Agent", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "codex_local"},
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	wi, err := svc.CreateWorkItem(ctx, ws.ID, application.CreateWorkItemParams{
		Title: "回归", AgentProfileID: agent.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return wi
}

// TestDisableAgentWithQueuedRun 回归：禁用带排队 run 的智能体曾因 queued→interrupting
// 非法边整体回滚；现在 queued 直达终态 interrupted，不再阻塞禁用。
func TestDisableAgentWithQueuedRun(t *testing.T) {
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
	if _, err := svc.SetAgentAvailability(ctx, wi.AgentProfileID, false); err != nil {
		t.Fatalf("禁用带排队 run 的智能体不应回滚事务: %v", err)
	}
	after, err := store.Runs().Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != domain.RunInterrupted {
		t.Fatalf("排队 run 应直达 interrupted，实际 %s", after.Status)
	}
}

// TestControlRunQueuedInterrupt 回归：queued run 的 interrupt/cancel 均直达终态。
func TestControlRunQueuedInterrupt(t *testing.T) {
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
	got, err := svc.ControlRun(ctx, run.ID, "interrupt")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.RunInterrupted {
		t.Fatalf("queued interrupt 应直达终态，实际 %s", got.Status)
	}
}

// TestResumeLostRunReexecutes 回归：lost 曾是 resume 死路（终态不可逆且无出路）；
// 现在基于同一会话锚点重新执行（新 run 续接 provider 会话，不复活旧 run）。
func TestResumeLostRunReexecutes(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wi := seedRunEnv(t, ctx, svc, store)
	run, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: wi.AgentProfileID, Instruction: "可恢复指令",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunSessionRef(ctx, run.ID, "codex://thread_lost"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunRunning, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunReconnecting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunLost, nil); err != nil {
		t.Fatal(err)
	}

	redo, err := svc.ResumeRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("lost run 应可恢复: %v", err)
	}
	if redo.ID == run.ID {
		t.Fatal("lost 不可复活旧 run，应创建新 run")
	}
	if redo.Status != domain.RunQueued {
		t.Fatalf("恢复 run 应从 queued 起步，实际 %s", redo.Status)
	}
	if instr, _ := redo.Input["instruction"].(string); instr != "可恢复指令" {
		t.Fatalf("恢复 run 应沿用原指令: %q", instr)
	}
	conv, _ := redo.Input["conversation"].(map[string]any)
	if conv["resume_session_ref"] != "codex://thread_lost" {
		t.Fatalf("恢复 run 应续接原 provider 会话: %#v", conv)
	}
}

// TestMoveWorkItemCompletedGate 回归：move 直达 completed 曾可绕过验收门；
// 现在 in_progress→completed 被 move 路径拒绝，唯一入口是 commands/accept。
func TestMoveWorkItemCompletedGate(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wi := seedRunEnv(t, ctx, svc, store)
	moved, err := svc.MoveWorkItem(ctx, wi.ID, domain.WorkItemInProgress, wi.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MoveWorkItem(ctx, moved.ID, domain.WorkItemCompleted, moved.Version); err == nil {
		t.Fatal("move 直达 completed 应被验收门拒绝")
	} else if !strings.Contains(err.Error(), "accept") {
		t.Fatalf("错误应指向 accept 验收门: %v", err)
	}
	after, err := store.WorkItems().Get(ctx, wi.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != domain.WorkItemInProgress {
		t.Fatalf("被拒后状态不应变化，实际 %s", after.Status)
	}
}

func finishRun(ctx context.Context, svc *application.Service, runID string, assistantText string) error {
	if err := svc.RecordRunStatus(ctx, runID, domain.RunRunning, nil); err != nil {
		return err
	}
	if err := svc.RecordRunEvent(ctx, runID, domain.EventMessageCompleted, map[string]any{"role": "assistant", "text": assistantText}); err != nil {
		return err
	}
	if err := svc.RecordRunStatus(ctx, runID, domain.RunSucceeding, nil); err != nil {
		return err
	}
	return svc.RecordRunStatus(ctx, runID, domain.RunSucceeded, nil)
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	_, current, _, _ := runtime.Caller(0)
	migrationDir := filepath.Join(filepath.Dir(current), "..", "..", "migrations", "sqlite")
	for _, name := range []string{"0001_init.sql", "0002_runtime_binding_model_config.sql", "0003_agent_config.sql", "0004_task_sessions.sql", "0005_wakeup.sql", "0006_plans.sql", "0007_task_sessions_parent.sql", "0008_plan_source_run_unique.sql"} {
		body, err := os.ReadFile(filepath.Join(migrationDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("migration %s: %v", name, err)
		}
	}
	return db
}

// TestSystemPromptInjection 回归：agent.Instructions（章程原文，多行 markdown）经
// CreateRun 固化进 run.Input["system_prompt"] 供 adapter 消费；空 Instructions 不落键。
func TestSystemPromptInjection(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_sp", Name: "sp", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	charter := "# 开发章程\n\n## 交付纪律\n- 分刀提交\n- 证据匹配表面\n"
	agent := &domain.AgentProfile{
		ID: "agent_sp", WorkspaceID: ws.ID, Name: "SP", Role: "developer",
		Instructions: charter, Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "codex_local"},
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	bare := &domain.AgentProfile{
		ID: "agent_bare", WorkspaceID: ws.ID, Name: "Bare", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "codex_local"},
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, bare); err != nil {
		t.Fatal(err)
	}
	binding := &domain.RuntimeBinding{
		ID: "rb_sp", WorkspaceID: ws.ID, RuntimeLabel: "codex_local", AdapterID: "codex-appserver",
		Capabilities: map[string]string{"resume": "supported"}, Status: domain.BindingReady,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Bindings().Create(ctx, binding); err != nil {
		t.Fatal(err)
	}
	wi, err := svc.CreateWorkItem(ctx, ws.ID, application.CreateWorkItemParams{Title: "章程注入", AgentProfileID: agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agent.ID, Instruction: "第一轮"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := run.Input["system_prompt"].(string); got != charter {
		t.Fatalf("system_prompt 应为章程原文（不改写、不截断）: %q", got)
	}
	runBare, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: bare.ID, Instruction: "无章程"})
	if err != nil {
		t.Fatal(err)
	}
	if _, has := runBare.Input["system_prompt"]; has {
		t.Fatalf("空 Instructions 不应落 system_prompt: %#v", runBare.Input["system_prompt"])
	}
}

// TestSystemPromptChangeTriggersSessionDrift 回归：提示词即配置——Instructions 变更使
// config digest 漂移，旧 provider 会话指纹失配被丢弃（不携带 resume_session_ref，旧提示词
// 不残留进续接会话）。漂移走普通 fresh（tier-3 全量历史内联保持连续性），非 handoff 轮换档。
func TestSystemPromptChangeTriggersSessionDrift(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())

	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_spd", Name: "spd", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	agent := &domain.AgentProfile{
		ID: "agent_spd", WorkspaceID: ws.ID, Name: "SPD", Role: "developer",
		Instructions: "# 章程 v1\n保守交付", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "codex_local"},
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	binding := &domain.RuntimeBinding{
		ID: "rb_spd", WorkspaceID: ws.ID, RuntimeLabel: "codex_local", AdapterID: "codex-appserver",
		Capabilities: map[string]string{"resume": "supported"}, Status: domain.BindingReady,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Bindings().Create(ctx, binding); err != nil {
		t.Fatal(err)
	}
	wi, err := svc.CreateWorkItem(ctx, ws.ID, application.CreateWorkItemParams{Title: "漂移", AgentProfileID: agent.ID})
	if err != nil {
		t.Fatal(err)
	}

	// run1 建会话 thread_A，锚点指纹 = run1 的 config digest（含章程 v1）。
	first, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agent.ID, Instruction: "第一轮"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := first.Input["system_prompt"].(string); got != "# 章程 v1\n保守交付" {
		t.Fatalf("run1 system_prompt 异常: %q", got)
	}
	if err := svc.RecordRunStatus(ctx, first.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunSessionRef(ctx, first.ID, "codex://thread_A"); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, first.ID, "第一轮完成"); err != nil {
		t.Fatal(err)
	}
	conv1, _ := first.Input["conversation"].(map[string]any)
	digest1, _ := conv1["config_digest"].(string)
	ts, err := store.TaskSessions().Get(ctx, ws.ID, agent.ID, "codex-appserver", wi.ID)
	if err != nil || ts == nil || ts.SessionRef() != "codex://thread_A" || ts.Fingerprint() != digest1 {
		t.Fatalf("锚点未按 run1 digest 落库: %v %#v", err, ts)
	}

	// 对照组：提示词未变 → 指纹匹配，正常续接 thread_A（排除「本就续不上」的空转通过）。
	mid, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agent.ID, Instruction: "第二轮"})
	if err != nil {
		t.Fatal(err)
	}
	convMid, _ := mid.Input["conversation"].(map[string]any)
	if convMid["resume_session_ref"] != "codex://thread_A" {
		t.Fatalf("对照组：未改提示词应续接 thread_A: %#v", convMid)
	}
	if err := svc.RecordRunStatus(ctx, mid.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunSessionRef(ctx, mid.ID, "codex://thread_A"); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, mid.ID, "第二轮完成"); err != nil {
		t.Fatal(err)
	}

	// 改章程：UpdateAgent 后新 run 固化新提示词，digest 漂移 → 旧会话丢弃。
	newInstr := "# 章程 v2\n激进交付"
	if _, err := svc.UpdateAgent(ctx, agent.ID, application.AgentPatch{Instructions: &newInstr}); err != nil {
		t.Fatal(err)
	}
	third, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agent.ID, Instruction: "第三轮：继续"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := third.Input["system_prompt"].(string); got != newInstr {
		t.Fatalf("run3 system_prompt 应为新章程: %q", got)
	}
	conv3, _ := third.Input["conversation"].(map[string]any)
	if _, has := conv3["resume_session_ref"]; has {
		t.Fatalf("提示词变更后不应续接旧会话: %#v", conv3)
	}
	if digest3, _ := conv3["config_digest"].(string); digest3 == "" || digest3 == digest1 {
		t.Fatalf("提示词变更应改变 config digest: %q vs %q", digest1, digest3)
	}
	// 漂移是普通 fresh：不带 handoff 轮换档标记，经 tier-3 全量历史内联保持任务连续性。
	if _, has := conv3["session_rotation"]; has {
		t.Fatalf("指纹漂移不应标记 session_rotation（仅阈值轮换使用）: %#v", conv3)
	}
	effective := atwruntime.EffectiveInstruction(third)
	if !strings.Contains(effective, "第一轮完成") || !strings.Contains(effective, "第三轮：继续") {
		t.Fatalf("漂移后应内联全量历史（tier-3）保持连续性: %q", effective)
	}
}

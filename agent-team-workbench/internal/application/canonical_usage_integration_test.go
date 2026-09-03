package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// ── WP4-B/C 共用 helper（canonical_usage / quota_settlement 两个文件共用）──

// usageInt64 小辅助：nullable counter 指针构造。
func usageInt64(v int64) *int64 { return &v }

func usageCounterValue(value *int64) int64 {
	if value == nil {
		return -1
	}
	return *value
}

// usageCountersEqual nullable counters 深相等（anchor/provenance 往返断言用）。
func usageCountersEqual(a, b domain.UsageCountersV1) bool {
	return reflect.DeepEqual(a, b)
}

func assertUnresolvedUsageKind(t *testing.T, kinds []domain.QuotaKind, want domain.QuotaKind) {
	t.Helper()
	for _, kind := range kinds {
		if kind == want {
			return
		}
	}
	t.Fatalf("unresolved kinds=%v, want %s", kinds, want)
}

// sealedUsageReport 构造一封口的 provider 用量报告。
// Provenance: AdapterID=adapterID, Protocol="test", ProtocolVersion="v1", Source="test",
// ReportedBasis=basis, AgentID=agentID, SessionRef=sessionRef, Mapping="test"。构造后 Seal()。
func sealedUsageReport(t *testing.T, runID, agentID, adapterID, sessionRef, basis string,
	counters domain.UsageCountersV1) *domain.ProviderUsageReportV1 {
	t.Helper()
	report := &domain.ProviderUsageReportV1{
		SchemaVersion: domain.ProviderUsageReportSchemaVersionV1,
		RunID:         runID,
		Basis:         basis,
		Counters:      counters.Clone(),
		Provenance: domain.UsageProvenanceV1{
			AdapterID: adapterID, Protocol: "test", ProtocolVersion: "v1", Source: "test",
			ReportedBasis: basis, AgentID: agentID, SessionRef: sessionRef, Mapping: "test",
		},
	}
	if err := report.Seal(); err != nil {
		t.Fatalf("seal provider usage report: %v", err)
	}
	return report
}

// fullUsageCounters 构造守恒的五桶 counters（total = uncached + read + write）。
func fullUsageCounters(total, uncached, read, write, output int64) domain.UsageCountersV1 {
	return domain.UsageCountersV1{
		InputTokensTotal:    usageInt64(total),
		InputUncachedTokens: usageInt64(uncached),
		CacheReadTokens:     usageInt64(read),
		CacheWriteTokens:    usageInt64(write),
		OutputTokens:        usageInt64(output),
	}
}

// usageAgentAdapterOf 取 Run 的报告身份（agent/adapter）。
func usageAgentAdapterOf(t *testing.T, ctx context.Context, store application.Store, runID string) (string, string) {
	t.Helper()
	run, err := store.Runs().Get(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	return run.AgentProfileID, run.AdapterID
}

// recordProviderUsage 走真实 RecordRunUsage 链路注入一封口的 provider 报告。
func recordProviderUsage(t *testing.T, ctx context.Context, svc *application.Service,
	store application.Store, runID, sessionRef, basis string, counters domain.UsageCountersV1) {
	t.Helper()
	agentID, adapterID := usageAgentAdapterOf(t, ctx, store, runID)
	report := sealedUsageReport(t, runID, agentID, adapterID, sessionRef, basis, counters)
	usage := atwruntime.Usage{Basis: atwruntime.UsageBasis(basis), ProviderReport: report}
	if value := counters.InputTokensTotal; value != nil {
		usage.InputTokens = *value
	}
	if value := counters.OutputTokens; value != nil {
		usage.OutputTokens = *value
	}
	if err := svc.RecordRunUsage(ctx, runID, usage); err != nil {
		t.Fatal(err)
	}
}

// usageDriveSourceRunning 把 Coordinator source Run 推到 running（决策注入前置态）。
func usageDriveSourceRunning(t *testing.T, ctx context.Context, svc *application.Service, runID string) {
	t.Helper()
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, runID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
}

// usageDriveSourceSucceeded 把 source Run 推到 succeeded 终态（终态钩子链全跑）。
func usageDriveSourceSucceeded(t *testing.T, ctx context.Context, svc *application.Service, runID string) {
	t.Helper()
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, runID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
}

// ── WP4-B：canonical usage 应用路径 ─────────────────────────────────

func TestRecordRunUsagePersistsLatestProviderReportWithSequence(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "latest provider report", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"latest sealed report wins"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.runs) != 1 {
		t.Fatalf("precondition: expected one Coordinator Run, got %d", len(dispatcher.runs))
	}
	source := dispatcher.runs[0]

	reportA := sealedUsageReport(t, source.ID, source.AgentProfileID, source.AdapterID, "",
		domain.UsageBasisPerRun, fullUsageCounters(100, 40, 30, 30, 25))
	if err := svc.RecordRunUsage(ctx, source.ID, atwruntime.Usage{
		Basis: atwruntime.UsagePerRun, ProviderReport: reportA,
		InputTokens: 100, OutputTokens: 25,
	}); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ProviderUsageReport == nil || stored.ProviderUsageReportDigest != reportA.Digest ||
		stored.ProviderUsageReportSeq != 1 {
		t.Fatalf("report A must persist as seq=1 latest: seq=%d digest=%s", stored.ProviderUsageReportSeq, stored.ProviderUsageReportDigest)
	}
	if err := stored.ProviderUsageReport.VerifyDigest(); err != nil {
		t.Fatalf("persisted report A must stay sealed: %v", err)
	}
	if stored.UsageIn != 100 || stored.UsageOut != 25 || stored.UsageBasis != domain.UsageBasisPerRun {
		t.Fatalf("legacy projection mismatch: in=%d out=%d basis=%s", stored.UsageIn, stored.UsageOut, stored.UsageBasis)
	}
	if root.Phase != domain.PhaseExecution {
		t.Fatalf("usage recording must not disturb WorkItem projection: %+v", root)
	}

	reportB := sealedUsageReport(t, source.ID, source.AgentProfileID, source.AdapterID, "",
		domain.UsageBasisPerRun, fullUsageCounters(200, 80, 70, 50, 60))
	if err := svc.RecordRunUsage(ctx, source.ID, atwruntime.Usage{
		Basis: atwruntime.UsagePerRun, ProviderReport: reportB,
		InputTokens: 200, OutputTokens: 60,
	}); err != nil {
		t.Fatal(err)
	}
	stored, err = store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ProviderUsageReportDigest != reportB.Digest || stored.ProviderUsageReportSeq != 2 {
		t.Fatalf("changed digest must advance sequence: seq=%d digest=%s", stored.ProviderUsageReportSeq, stored.ProviderUsageReportDigest)
	}

	// exact replay：同一封口报告重复上报幂等，seq 不再增长。
	if err := svc.RecordRunUsage(ctx, source.ID, atwruntime.Usage{
		Basis: atwruntime.UsagePerRun, ProviderReport: reportB,
		InputTokens: 200, OutputTokens: 60,
	}); err != nil {
		t.Fatal(err)
	}
	stored, err = store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ProviderUsageReportDigest != reportB.Digest || stored.ProviderUsageReportSeq != 2 {
		t.Fatalf("exact replay must be idempotent: seq=%d digest=%s", stored.ProviderUsageReportSeq, stored.ProviderUsageReportDigest)
	}
}

func TestTerminalHookCanonicalizesFromLatestReport(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
	if _, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "terminal canonical", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"canonical freezes at terminal"},
	}); err != nil {
		t.Fatal(err)
	}
	source := dispatcher.runs[0]
	usageDriveSourceRunning(t, ctx, svc, source.ID)
	recordProviderUsage(t, ctx, svc, store, source.ID, "", domain.UsageBasisPerRun,
		fullUsageCounters(120, 50, 40, 30, 45))
	usageDriveSourceSucceeded(t, ctx, svc, source.ID)

	stored, err := store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CanonicalUsage == nil {
		t.Fatal("terminal hook must freeze canonical usage from the latest report")
	}
	if err := stored.CanonicalUsage.VerifyDigest(); err != nil {
		t.Fatalf("canonical usage must satisfy its digest contract: %v", err)
	}
	if stored.CanonicalUsageDigest != stored.CanonicalUsage.Digest {
		t.Fatalf("run digest column must mirror canonical digest: %s vs %s",
			stored.CanonicalUsageDigest, stored.CanonicalUsage.Digest)
	}
	if stored.CanonicalUsage.RunID != source.ID || stored.CanonicalUsage.Basis != domain.UsageBasisPerRun {
		t.Fatalf("canonical identity mismatch: %+v", stored.CanonicalUsage)
	}
	want := fullUsageCounters(120, 50, 40, 30, 45)
	if !usageCountersEqual(stored.CanonicalUsage.Counters, want) {
		t.Fatalf("canonical counters must equal report counters: %+v", stored.CanonicalUsage.Counters)
	}
	wantResolved := []domain.QuotaKind{
		domain.QuotaCacheReadTokens, domain.QuotaCacheWriteTokens,
		domain.QuotaInputTokensTotal, domain.QuotaInputUncachedTokens, domain.QuotaOutputTokens,
	}
	if !reflect.DeepEqual(stored.CanonicalUsage.ResolvedKinds, wantResolved) {
		t.Fatalf("resolved kinds mismatch: %+v", stored.CanonicalUsage.ResolvedKinds)
	}
	if len(stored.CanonicalUsage.UnresolvedKinds) != 0 || stored.CanonicalUsage.UnresolvedReason != "" {
		t.Fatalf("fully known report must leave no unresolved kinds: %+v", stored.CanonicalUsage)
	}
	// legacy 列保持投影语义：等于 RecordRunUsage 传入的 Usage 值。
	if stored.UsageIn != 120 || stored.UsageOut != 45 {
		t.Fatalf("legacy usage columns must stay the per-call projection: in=%d out=%d",
			stored.UsageIn, stored.UsageOut)
	}
}

func TestLateUsageAfterTerminalStillCanonicalizes(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
	if _, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "late usage canonical", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"late report still freezes canonical"},
	}); err != nil {
		t.Fatal(err)
	}
	source := dispatcher.runs[0]
	usageDriveSourceRunning(t, ctx, svc, source.ID)
	usageDriveSourceSucceeded(t, ctx, svc, source.ID)

	stored, err := store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CanonicalUsage != nil {
		t.Fatalf("terminal without report and governance must not synthesize canonical: %+v", stored.CanonicalUsage)
	}

	recordProviderUsage(t, ctx, svc, store, source.ID, "", domain.UsageBasisPerRun,
		fullUsageCounters(90, 30, 30, 30, 20))
	stored, err = store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CanonicalUsage == nil {
		t.Fatal("late provider report must backfill canonical usage on a terminal run")
	}
	if err := stored.CanonicalUsage.VerifyDigest(); err != nil {
		t.Fatalf("late canonical digest mismatch: %v", err)
	}
	if !usageCountersEqual(stored.CanonicalUsage.Counters, fullUsageCounters(90, 30, 30, 30, 20)) {
		t.Fatalf("late canonical counters mismatch: %+v", stored.CanonicalUsage.Counters)
	}
	if stored.CanonicalUsageDigest != stored.CanonicalUsage.Digest {
		t.Fatalf("late canonical digest column mismatch: %s vs %s",
			stored.CanonicalUsageDigest, stored.CanonicalUsage.Digest)
	}
}

func TestChangedReportAfterCanonicalCannotRewrite(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
	if _, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "canonical immutability", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"frozen canonical never rewrites"},
	}); err != nil {
		t.Fatal(err)
	}
	source := dispatcher.runs[0]
	usageDriveSourceRunning(t, ctx, svc, source.ID)
	recordProviderUsage(t, ctx, svc, store, source.ID, "", domain.UsageBasisPerRun,
		fullUsageCounters(100, 40, 30, 30, 25))
	usageDriveSourceSucceeded(t, ctx, svc, source.ID)

	frozen, err := store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.CanonicalUsage == nil || frozen.ProviderUsageReportDigest == "" {
		t.Fatalf("precondition: canonical must be frozen with report A: %+v", frozen)
	}
	frozenCanonical := *frozen.CanonicalUsage
	frozenReportDigest := frozen.ProviderUsageReportDigest

	// canonical 已冻结后换 digest 的报告：latest report 槽拒改，legacy 列仍覆盖。
	late := sealedUsageReport(t, source.ID, source.AgentProfileID, source.AdapterID, "",
		domain.UsageBasisPerRun, fullUsageCounters(400, 160, 140, 100, 90))
	if err := svc.RecordRunUsage(ctx, source.ID, atwruntime.Usage{
		Basis: atwruntime.UsagePerRun, ProviderReport: late,
		InputTokens: 400, OutputTokens: 90,
	}); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ProviderUsageReportDigest != frozenReportDigest || stored.ProviderUsageReportSeq != 1 {
		t.Fatalf("frozen canonical must refuse report rewrite: seq=%d digest=%s",
			stored.ProviderUsageReportSeq, stored.ProviderUsageReportDigest)
	}
	if stored.CanonicalUsage == nil || stored.CanonicalUsage.Digest != frozenCanonical.Digest {
		t.Fatalf("canonical must stay frozen: %+v", stored.CanonicalUsage)
	}
	if !reflect.DeepEqual(*stored.CanonicalUsage, frozenCanonical) {
		t.Fatalf("canonical value mutated after freeze: %+v vs %+v", *stored.CanonicalUsage, frozenCanonical)
	}
	if stored.UsageIn != 400 || stored.UsageOut != 90 {
		t.Fatalf("legacy usage columns must keep late-arrival overwrite semantics: in=%d out=%d",
			stored.UsageIn, stored.UsageOut)
	}
}

func seedCumulativeTaskEnv(t *testing.T) (context.Context, *application.Service, *sqlstore.Store, *captureDispatcher, *domain.WorkItem, string) {
	t.Helper()
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	wi, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "cumulative usage anchor", RecordKind: domain.RecordKindTask, AgentProfileID: workerID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.runs) != 0 {
		t.Fatalf("plain task must not auto-coordinate: %d", len(dispatcher.runs))
	}
	return ctx, svc, store, dispatcher, wi, workerID
}

func usageCreatePlainRun(t *testing.T, ctx context.Context, svc *application.Service,
	wi *domain.WorkItem, workerID, instruction string) *domain.ExecutionRun {
	t.Helper()
	run, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: workerID, Instruction: instruction,
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func usageSessionAnchor(t *testing.T, ctx context.Context, store *sqlstore.Store,
	wi *domain.WorkItem, workerID string) *domain.TaskSession {
	t.Helper()
	session, err := store.TaskSessions().Get(ctx, wi.WorkspaceID, workerID, "mock", wi.ID)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

// usageMarkSessionResumed 把 run.SessionBefore 置为 provider 会话 ref（规格允许的
// harness 取舍：不走完整 resume 创建链，等价 resume 后 Run 的 canonicalize 输入）。
func usageMarkSessionResumed(t *testing.T, ctx context.Context, store *sqlstore.Store, runID, sessionRef string) {
	t.Helper()
	run, err := store.Runs().Get(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	expected := run.Version
	run.SessionBefore = sessionRef
	if err := store.Runs().Update(ctx, run, expected); err != nil {
		t.Fatal(err)
	}
}

func TestCumulativeUsageAnchorsAcrossSequentialRuns(t *testing.T) {
	ctx, svc, store, _, wi, workerID := seedCumulativeTaskEnv(t)
	const sessionRef = "mock://cumulative-session"

	c1 := fullUsageCounters(100, 60, 25, 15, 50)
	run1 := usageCreatePlainRun(t, ctx, svc, wi, workerID, "cumulative turn 1")
	if run1.SessionBefore != "" {
		t.Fatalf("first run must be a fresh provider session: %q", run1.SessionBefore)
	}
	usageDriveSourceRunning(t, ctx, svc, run1.ID)
	recordProviderUsage(t, ctx, svc, store, run1.ID, sessionRef, domain.UsageBasisSessionCumulative, c1)
	usageDriveSourceSucceeded(t, ctx, svc, run1.ID)

	stored1, err := store.Runs().Get(ctx, run1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored1.CanonicalUsage == nil {
		t.Fatal("fresh cumulative run must freeze canonical usage with an explicit zero baseline")
	}
	if err := stored1.CanonicalUsage.VerifyDigest(); err != nil {
		t.Fatalf("run1 canonical digest mismatch: %v", err)
	}
	// 防 bug #1 回归：全 resolved 的 kind 列表必须序列化为空数组而非 JSON null
	//（0028 触发器要求 array；null 会让累计 canonical 永远写不进 execution_runs）。
	if stored1.CanonicalUsage.UnresolvedKinds == nil || len(stored1.CanonicalUsage.UnresolvedKinds) != 0 {
		t.Fatalf("unresolved_kinds must be a non-nil empty slice: %#v", stored1.CanonicalUsage.UnresolvedKinds)
	}
	rawCanonical, err := json.Marshal(stored1.CanonicalUsage)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawCanonical), `"unresolved_kinds":[]`) ||
		strings.Contains(string(rawCanonical), `"unresolved_kinds":null`) {
		t.Fatalf("persisted canonical must serialize unresolved_kinds as []: %s", rawCanonical)
	}
	wantFullyResolved := []domain.QuotaKind{
		domain.QuotaCacheReadTokens, domain.QuotaCacheWriteTokens,
		domain.QuotaInputTokensTotal, domain.QuotaInputUncachedTokens, domain.QuotaOutputTokens,
	}
	if !reflect.DeepEqual(stored1.CanonicalUsage.ResolvedKinds, wantFullyResolved) {
		t.Fatalf("fully known cumulative report must resolve all five kinds: %+v", stored1.CanonicalUsage.ResolvedKinds)
	}
	if !usageCountersEqual(stored1.CanonicalUsage.Counters, c1) {
		t.Fatalf("run1 canonical must equal the cumulative report (zero baseline): %+v", stored1.CanonicalUsage.Counters)
	}
	if stored1.CanonicalUsage.Provenance.AnchorBefore == nil {
		t.Fatal("zero baseline must be explicit in provenance.anchor_before")
	}
	if !usageCountersEqual(*stored1.CanonicalUsage.Provenance.AnchorBefore, fullUsageCounters(0, 0, 0, 0, 0)) {
		t.Fatalf("anchor_before must be explicit zeros: %+v", *stored1.CanonicalUsage.Provenance.AnchorBefore)
	}
	if stored1.CanonicalUsage.Provenance.AnchorAfter == nil ||
		!usageCountersEqual(*stored1.CanonicalUsage.Provenance.AnchorAfter, c1) {
		t.Fatalf("anchor_after must equal report C1: %+v", stored1.CanonicalUsage.Provenance.AnchorAfter)
	}
	session := usageSessionAnchor(t, ctx, store, wi, workerID)
	if session.ProviderUsageAnchorSeq != 1 || session.ProviderUsageAnchor == nil ||
		!usageCountersEqual(session.ProviderUsageAnchor.Counters, c1) {
		t.Fatalf("task_sessions anchor must advance to C1: seq=%d anchor=%+v",
			session.ProviderUsageAnchorSeq, session.ProviderUsageAnchor)
	}

	// run2：SessionBefore 非空（等价 resume）后上报更高的累计值，canonical = C2−C1。
	c2 := fullUsageCounters(180, 100, 45, 35, 90)
	run2 := usageCreatePlainRun(t, ctx, svc, wi, workerID, "cumulative turn 2")
	usageMarkSessionResumed(t, ctx, store, run2.ID, sessionRef)
	usageDriveSourceRunning(t, ctx, svc, run2.ID)
	recordProviderUsage(t, ctx, svc, store, run2.ID, sessionRef, domain.UsageBasisSessionCumulative, c2)
	usageDriveSourceSucceeded(t, ctx, svc, run2.ID)

	stored2, err := store.Runs().Get(ctx, run2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored2.CanonicalUsage == nil {
		t.Fatal("run2 must canonicalize against the persisted anchor")
	}
	if err := stored2.CanonicalUsage.VerifyDigest(); err != nil {
		t.Fatalf("run2 canonical digest mismatch: %v", err)
	}
	wantDelta := fullUsageCounters(80, 40, 20, 20, 40)
	if !usageCountersEqual(stored2.CanonicalUsage.Counters, wantDelta) {
		t.Fatalf("run2 canonical must be C2−C1: total=%d uncached=%d read=%d write=%v output=%d (session_before=%q)",
			usageCounterValue(stored2.CanonicalUsage.Counters.InputTokensTotal),
			usageCounterValue(stored2.CanonicalUsage.Counters.InputUncachedTokens),
			usageCounterValue(stored2.CanonicalUsage.Counters.CacheReadTokens),
			stored2.CanonicalUsage.Counters.CacheWriteTokens,
			usageCounterValue(stored2.CanonicalUsage.Counters.OutputTokens),
			stored2.SessionBefore)
	}
	if stored2.CanonicalUsage.Provenance.AnchorBefore == nil ||
		!usageCountersEqual(*stored2.CanonicalUsage.Provenance.AnchorBefore, c1) {
		t.Fatalf("run2 anchor_before must equal C1: %+v", stored2.CanonicalUsage.Provenance.AnchorBefore)
	}
	if stored2.CanonicalUsage.Provenance.AnchorAfter == nil ||
		!usageCountersEqual(*stored2.CanonicalUsage.Provenance.AnchorAfter, c2) {
		t.Fatalf("run2 anchor_after must equal C2: %+v", stored2.CanonicalUsage.Provenance.AnchorAfter)
	}
	session = usageSessionAnchor(t, ctx, store, wi, workerID)
	if session.ProviderUsageAnchorSeq != 2 || session.ProviderUsageAnchor == nil ||
		!usageCountersEqual(session.ProviderUsageAnchor.Counters, c2) {
		t.Fatalf("task_sessions anchor must advance to C2: seq=%d anchor=%+v",
			session.ProviderUsageAnchorSeq, session.ProviderUsageAnchor)
	}
}

func TestCumulativeCounterRegressionMarksKindUnresolved(t *testing.T) {
	ctx, svc, store, _, wi, workerID := seedCumulativeTaskEnv(t)
	const sessionRef = "mock://regression-session"

	c1 := fullUsageCounters(100, 60, 25, 15, 50)
	run1 := usageCreatePlainRun(t, ctx, svc, wi, workerID, "regression turn 1")
	usageDriveSourceRunning(t, ctx, svc, run1.ID)
	recordProviderUsage(t, ctx, svc, store, run1.ID, sessionRef, domain.UsageBasisSessionCumulative, c1)
	usageDriveSourceSucceeded(t, ctx, svc, run1.ID)

	c2 := fullUsageCounters(180, 100, 45, 35, 90)
	run2 := usageCreatePlainRun(t, ctx, svc, wi, workerID, "regression turn 2")
	usageMarkSessionResumed(t, ctx, store, run2.ID, sessionRef)
	usageDriveSourceRunning(t, ctx, svc, run2.ID)
	recordProviderUsage(t, ctx, svc, store, run2.ID, sessionRef, domain.UsageBasisSessionCumulative, c2)
	usageDriveSourceSucceeded(t, ctx, svc, run2.ID)

	// run3：output 计数器低于锚点水位（回归/underflow），其余计数器正常增长。
	c3 := fullUsageCounters(260, 150, 60, 50, 30)
	run3 := usageCreatePlainRun(t, ctx, svc, wi, workerID, "regression turn 3")
	usageMarkSessionResumed(t, ctx, store, run3.ID, sessionRef)
	usageDriveSourceRunning(t, ctx, svc, run3.ID)
	recordProviderUsage(t, ctx, svc, store, run3.ID, sessionRef, domain.UsageBasisSessionCumulative, c3)
	usageDriveSourceSucceeded(t, ctx, svc, run3.ID)

	stored3, err := store.Runs().Get(ctx, run3.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored3.CanonicalUsage == nil {
		t.Fatal("run3 must canonicalize with a per-kind unresolved gap")
	}
	if err := stored3.CanonicalUsage.VerifyDigest(); err != nil {
		t.Fatalf("run3 canonical digest mismatch: %v", err)
	}
	found := false
	for _, kind := range stored3.CanonicalUsage.UnresolvedKinds {
		if kind == domain.QuotaOutputTokens {
			found = true
		}
	}
	if !found {
		t.Fatalf("regressed output_tokens must be unresolved: %+v", stored3.CanonicalUsage.UnresolvedKinds)
	}
	if !strings.Contains(stored3.CanonicalUsage.UnresolvedReason, "regressed") ||
		!strings.Contains(stored3.CanonicalUsage.UnresolvedReason, "underflow") {
		t.Fatalf("regression reason must explain underflow: %q", stored3.CanonicalUsage.UnresolvedReason)
	}
	if stored3.CanonicalUsage.Counters.OutputTokens != nil {
		t.Fatalf("unresolved kind must not fabricate a value: %+v", stored3.CanonicalUsage.Counters.OutputTokens)
	}
	wantResolved := domain.UsageCountersV1{
		InputTokensTotal: usageInt64(80), InputUncachedTokens: usageInt64(50),
		CacheReadTokens: usageInt64(15), CacheWriteTokens: usageInt64(15),
	}
	if !usageCountersEqual(stored3.CanonicalUsage.Counters, wantResolved) {
		t.Fatalf("non-regressed kinds must still resolve as C3−C2: %+v", stored3.CanonicalUsage.Counters)
	}
	for _, kind := range []domain.QuotaKind{
		domain.QuotaInputTokensTotal, domain.QuotaInputUncachedTokens,
		domain.QuotaCacheReadTokens, domain.QuotaCacheWriteTokens,
	} {
		for _, unresolved := range stored3.CanonicalUsage.UnresolvedKinds {
			if unresolved == kind {
				t.Fatalf("healthy kind %s must stay resolved: %+v", kind, stored3.CanonicalUsage.UnresolvedKinds)
			}
		}
	}
	// C3 的 output 回退不应阻止健康 input 水位推进；否则 C4 会再次从 C2
	// 做差，把 C3 已结算的健康 input 重复计入。
	session := usageSessionAnchor(t, ctx, store, wi, workerID)
	mergedC3Anchor := fullUsageCounters(260, 150, 60, 50, 90)
	if session.ProviderUsageAnchorSeq != 3 || session.ProviderUsageAnchor == nil ||
		!usageCountersEqual(session.ProviderUsageAnchor.Counters, mergedC3Anchor) {
		t.Fatalf("C3 must persist per-kind merged anchor: seq=%d anchor=%+v want=%+v",
			session.ProviderUsageAnchorSeq, session.ProviderUsageAnchor, mergedC3Anchor)
	}

	// run4：output 仍低于保留的 C2 output 水位，健康 input 从合并后的 C3
	// 水位计算，证明不会把 C3 的健康增量再次算入。
	c4 := fullUsageCounters(320, 180, 80, 60, 70)
	run4 := usageCreatePlainRun(t, ctx, svc, wi, workerID, "regression turn 4")
	usageMarkSessionResumed(t, ctx, store, run4.ID, sessionRef)
	usageDriveSourceRunning(t, ctx, svc, run4.ID)
	recordProviderUsage(t, ctx, svc, store, run4.ID, sessionRef, domain.UsageBasisSessionCumulative, c4)
	usageDriveSourceSucceeded(t, ctx, svc, run4.ID)

	stored4, err := store.Runs().Get(ctx, run4.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored4.CanonicalUsage == nil {
		t.Fatal("run4 must canonicalize against the merged C3 anchor")
	}
	if err := stored4.CanonicalUsage.VerifyDigest(); err != nil {
		t.Fatalf("run4 canonical digest mismatch: %v", err)
	}
	wantC4Delta := domain.UsageCountersV1{
		InputTokensTotal:    usageInt64(60),
		InputUncachedTokens: usageInt64(30),
		CacheReadTokens:     usageInt64(20),
		CacheWriteTokens:    usageInt64(10),
	}
	if !usageCountersEqual(stored4.CanonicalUsage.Counters, wantC4Delta) {
		t.Fatalf("run4 healthy kinds must be C4−mergedC3: got=%+v want=%+v",
			stored4.CanonicalUsage.Counters, wantC4Delta)
	}
	if stored4.CanonicalUsage.Counters.OutputTokens != nil {
		t.Fatalf("run4 regressed output must remain unresolved: %+v", stored4.CanonicalUsage.Counters)
	}
	found = false
	for _, kind := range stored4.CanonicalUsage.UnresolvedKinds {
		if kind == domain.QuotaOutputTokens {
			found = true
		}
	}
	if !found {
		t.Fatalf("run4 output regression must remain unresolved: %+v", stored4.CanonicalUsage.UnresolvedKinds)
	}
	mergedC4Anchor := fullUsageCounters(320, 180, 80, 60, 90)
	session = usageSessionAnchor(t, ctx, store, wi, workerID)
	if session.ProviderUsageAnchorSeq != 4 || session.ProviderUsageAnchor == nil ||
		!usageCountersEqual(session.ProviderUsageAnchor.Counters, mergedC4Anchor) {
		t.Fatalf("C4 must preserve regressed output watermark while advancing healthy kinds: seq=%d anchor=%+v want=%+v",
			session.ProviderUsageAnchorSeq, session.ProviderUsageAnchor, mergedC4Anchor)
	}
}

func TestCumulativeInputCounterRegressionKeepsPerKindAnchor(t *testing.T) {
	ctx, svc, store, _, wi, workerID := seedCumulativeTaskEnv(t)
	const sessionRef = "mock://input-regression-session"

	c1 := fullUsageCounters(100, 60, 25, 15, 50)
	run1 := usageCreatePlainRun(t, ctx, svc, wi, workerID, "input regression turn 1")
	usageDriveSourceRunning(t, ctx, svc, run1.ID)
	recordProviderUsage(t, ctx, svc, store, run1.ID, sessionRef, domain.UsageBasisSessionCumulative, c1)
	usageDriveSourceSucceeded(t, ctx, svc, run1.ID)

	c2 := fullUsageCounters(180, 100, 45, 35, 90)
	run2 := usageCreatePlainRun(t, ctx, svc, wi, workerID, "input regression turn 2")
	usageMarkSessionResumed(t, ctx, store, run2.ID, sessionRef)
	usageDriveSourceRunning(t, ctx, svc, run2.ID)
	recordProviderUsage(t, ctx, svc, store, run2.ID, sessionRef, domain.UsageBasisSessionCumulative, c2)
	usageDriveSourceSucceeded(t, ctx, svc, run2.ID)

	// C3 中只有 cache_read 回退；其余 input 水位和 output 继续增长。
	// 该 report 本身仍守恒，但逐 kind 合并后的 anchor 不再是单一观测快照。
	c3 := fullUsageCounters(190, 110, 40, 40, 100)
	run3 := usageCreatePlainRun(t, ctx, svc, wi, workerID, "input regression turn 3")
	usageMarkSessionResumed(t, ctx, store, run3.ID, sessionRef)
	usageDriveSourceRunning(t, ctx, svc, run3.ID)
	recordProviderUsage(t, ctx, svc, store, run3.ID, sessionRef, domain.UsageBasisSessionCumulative, c3)
	usageDriveSourceSucceeded(t, ctx, svc, run3.ID)

	stored3, err := store.Runs().Get(ctx, run3.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored3.CanonicalUsage == nil {
		t.Fatal("run3 must canonicalize with a per-kind unresolved gap")
	}
	if stored3.CanonicalUsage.Counters.CacheReadTokens != nil {
		t.Fatalf("regressed cache_read must remain unresolved: %+v", stored3.CanonicalUsage.Counters)
	}
	assertUnresolvedUsageKind(t, stored3.CanonicalUsage.UnresolvedKinds, domain.QuotaCacheReadTokens)
	assertUnresolvedUsageKind(t, stored3.CanonicalUsage.UnresolvedKinds, domain.QuotaInputTokensTotal)
	wantC3Delta := domain.UsageCountersV1{
		InputUncachedTokens: usageInt64(10),
		CacheWriteTokens:    usageInt64(5),
		OutputTokens:        usageInt64(10),
	}
	if !usageCountersEqual(stored3.CanonicalUsage.Counters, wantC3Delta) {
		t.Fatalf("run3 healthy kinds must resolve while cache_read stays unresolved: got=%+v want=%+v",
			stored3.CanonicalUsage.Counters, wantC3Delta)
	}

	// total=190、uncached=110、read=45（旧水位）、write=40 是跨观测合并态，
	// 专用 anchor 校验必须允许它持久化。
	mergedC3 := fullUsageCounters(190, 110, 45, 40, 100)
	session := usageSessionAnchor(t, ctx, store, wi, workerID)
	if session.ProviderUsageAnchorSeq != 3 || session.ProviderUsageAnchor == nil ||
		!usageCountersEqual(session.ProviderUsageAnchor.Counters, mergedC3) {
		t.Fatalf("C3 merged input anchor must persist: seq=%d anchor=%+v want=%+v",
			session.ProviderUsageAnchorSeq, session.ProviderUsageAnchor, mergedC3)
	}

	// C4 cache_read 恢复并继续增长。健康 input 从 C3 合并水位计算，不能
	// 再把 C3 的 uncached/write 增量重复计入；cache_read 则从保留的旧水位
	// 45 计算本次可证明 delta。
	c4 := fullUsageCounters(240, 140, 60, 40, 130)
	run4 := usageCreatePlainRun(t, ctx, svc, wi, workerID, "input regression turn 4")
	usageMarkSessionResumed(t, ctx, store, run4.ID, sessionRef)
	usageDriveSourceRunning(t, ctx, svc, run4.ID)
	recordProviderUsage(t, ctx, svc, store, run4.ID, sessionRef, domain.UsageBasisSessionCumulative, c4)
	usageDriveSourceSucceeded(t, ctx, svc, run4.ID)

	stored4, err := store.Runs().Get(ctx, run4.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored4.CanonicalUsage == nil {
		t.Fatal("run4 must canonicalize against the merged input anchor")
	}
	wantC4Delta := domain.UsageCountersV1{
		InputUncachedTokens: usageInt64(30),
		CacheReadTokens:     usageInt64(15),
		CacheWriteTokens:    usageInt64(0),
		OutputTokens:        usageInt64(30),
	}
	if !usageCountersEqual(stored4.CanonicalUsage.Counters, wantC4Delta) {
		t.Fatalf("run4 must use merged per-kind watermarks: got=%+v want=%+v",
			stored4.CanonicalUsage.Counters, wantC4Delta)
	}
	if stored4.CanonicalUsage.Counters.InputTokensTotal != nil {
		t.Fatalf("mixed input watermarks must keep aggregate total unresolved: %+v", stored4.CanonicalUsage.Counters)
	}
	if len(stored4.CanonicalUsage.UnresolvedKinds) != 1 ||
		stored4.CanonicalUsage.UnresolvedKinds[0] != domain.QuotaInputTokensTotal {
		t.Fatalf("only aggregate total should remain unresolved in run4: %+v", stored4.CanonicalUsage.UnresolvedKinds)
	}
	mergedC4 := c4
	session = usageSessionAnchor(t, ctx, store, wi, workerID)
	if session.ProviderUsageAnchorSeq != 4 || session.ProviderUsageAnchor == nil ||
		!usageCountersEqual(session.ProviderUsageAnchor.Counters, mergedC4) {
		t.Fatalf("C4 anchor must converge to the recovered report: seq=%d anchor=%+v want=%+v",
			session.ProviderUsageAnchorSeq, session.ProviderUsageAnchor, mergedC4)
	}
}

func TestLateCumulativeReportFromOldRunCannotAdvanceNewAnchorOwner(t *testing.T) {
	ctx, svc, store, _, wi, workerID := seedCumulativeTaskEnv(t)
	const sessionRef = "mock://late-owner-session"

	first := usageCreatePlainRun(t, ctx, svc, wi, workerID, "owner baseline")
	usageDriveSourceRunning(t, ctx, svc, first.ID)
	baseline := fullUsageCounters(100, 60, 25, 15, 50)
	recordProviderUsage(t, ctx, svc, store, first.ID, sessionRef, domain.UsageBasisSessionCumulative, baseline)
	usageDriveSourceSucceeded(t, ctx, svc, first.ID)

	old := usageCreatePlainRun(t, ctx, svc, wi, workerID, "old run")
	usageDriveSourceRunning(t, ctx, svc, old.ID)
	usageDriveSourceSucceeded(t, ctx, svc, old.ID)

	newOwner := usageCreatePlainRun(t, ctx, svc, wi, workerID, "new owner")
	session := usageSessionAnchor(t, ctx, store, wi, workerID)
	if session.LastRunID != newOwner.ID {
		t.Fatalf("new Run must own the provider anchor before late report: %+v", session)
	}

	late := fullUsageCounters(180, 100, 45, 35, 90)
	recordProviderUsage(t, ctx, svc, store, old.ID, sessionRef, domain.UsageBasisSessionCumulative, late)
	stored, err := store.Runs().Get(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CanonicalUsage == nil || stored.CanonicalUsage.Counters.AnyKnown() ||
		!strings.Contains(stored.CanonicalUsage.UnresolvedReason, "current anchor owner") {
		t.Fatalf("late non-owner cumulative report must be unresolved: %+v", stored.CanonicalUsage)
	}
	session = usageSessionAnchor(t, ctx, store, wi, workerID)
	if session.LastRunID != newOwner.ID || session.ProviderUsageAnchorSeq != 1 ||
		!usageCountersEqual(session.ProviderUsageAnchor.Counters, baseline) {
		t.Fatalf("late old Run must not advance the new owner anchor: %+v", session)
	}
}

// P1-4（复审裁决 #4）：受管无 report Run 的 absent canonical 延迟到关闭时刻
// 合成——终态钩子只做进行式 pass，canonical 保持缺失让迟到 report 可正常
// 首写；只有关闭性触发源（StartCoordinator 恢复面）才合成 absent evidence。
func TestGovernedTerminalRunWithoutReportSynthesizesAbsentCanonical(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "absent evidence", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"governed runs leave explicit usage gaps"},
	})
	if err != nil {
		t.Fatal(err)
	}
	source := dispatcher.runs[0]
	// usage 政策先行：没有 usage reservation 的 Turn 不归 sweep 管，absent
	// evidence 也就没有关闭时刻可言。
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	goal = setGoalQuotaPolicies(t, ctx, store, goal,
		domain.QuotaPolicy{Kind: domain.QuotaOutputTokens, Limit: 100_000, Enforcement: domain.QuotaEnforcementEnforce})
	usageDriveSourceRunning(t, ctx, svc, source.ID)
	if err := svc.RecordRunEvent(ctx, source.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": `{"schema_version":"plan-decision/v2","kind":"plan","reason":"dispatch absent usage probe","next_action":"wait for worker","steps":[{"verb":"dispatch","agent_id":"` + workerID + `","title":"work","instruction":"do work","acceptance":["done"]},{"verb":"join","children":"all"}]}`}); err != nil {
		t.Fatal(err)
	}
	usageDriveSourceSucceeded(t, ctx, svc, source.ID)
	if len(dispatcher.runs) != 2 {
		t.Fatalf("precondition: expected one governed Worker, got %d", len(dispatcher.runs))
	}

	// 非 governed source（进行式 pass）：无 report 不合成 canonical。
	sourceStored, err := store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sourceStored.CanonicalUsage != nil {
		t.Fatalf("progressive pass must not synthesize canonical for a report-less run: %+v", sourceStored.CanonicalUsage)
	}

	worker := dispatcher.runs[1]
	usageStartRun(t, ctx, svc, worker.ID)
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, worker.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}

	// P1-4 红线：受管无 report Run 终态后 canonical 保持缺失（此前在这里就
	// 被当场冻结，迟到 report 永远被拒、真实用量永久丢失）。
	workerStored, err := store.Runs().Get(ctx, worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if workerStored.CanonicalUsage != nil {
		t.Fatalf("absent evidence must stay absent until a close trigger: %+v", workerStored.CanonicalUsage)
	}
	if _, err := store.Quotas().GetSpend(ctx, domain.QuotaSpendKey{
		TurnKey: domain.TurnKey{GoalID: goal.ID, TodoID: goal.CurrentTodoID, TurnSeq: 1},
		Kind:    domain.QuotaOutputTokens, RunID: worker.ID}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("absent run must not produce spend before close: %v", err)
	}

	// 关闭性触发源：StartCoordinator 恢复面（allowAbsentClose=true）。
	if err := svc.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}

	workerStored, err = store.Runs().Get(ctx, worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if workerStored.CanonicalUsage == nil {
		t.Fatal("close trigger must synthesize absent evidence canonical for the governed run")
	}
	if err := workerStored.CanonicalUsage.VerifyDigest(); err != nil {
		t.Fatalf("absent canonical digest mismatch: %v", err)
	}
	if workerStored.CanonicalUsage.Basis != domain.UsageBasisPerRun || workerStored.CanonicalUsage.RunID != worker.ID {
		t.Fatalf("absent canonical identity mismatch: %+v", workerStored.CanonicalUsage)
	}
	if len(workerStored.CanonicalUsage.ResolvedKinds) != 0 {
		t.Fatalf("absent evidence must resolve nothing: %+v", workerStored.CanonicalUsage.ResolvedKinds)
	}
	wantUnresolved := []domain.QuotaKind{
		domain.QuotaCacheReadTokens, domain.QuotaCacheWriteTokens, domain.QuotaCostMicroUSD,
		domain.QuotaInputTokensTotal, domain.QuotaInputUncachedTokens, domain.QuotaOutputTokens,
	}
	if !reflect.DeepEqual(workerStored.CanonicalUsage.UnresolvedKinds, wantUnresolved) {
		t.Fatalf("all five token kinds (plus unpriced governed cost) must be unresolved: %+v",
			workerStored.CanonicalUsage.UnresolvedKinds)
	}
	if strings.TrimSpace(workerStored.CanonicalUsage.UnresolvedReason) == "" {
		t.Fatal("absent evidence requires an explicit reason")
	}
	// source 同属 phase1 受管集合：关闭时刻同样获得 absent evidence。
	sourceStored, err = store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sourceStored.CanonicalUsage == nil {
		t.Fatal("close trigger must also cover the phase1 source run's missing evidence")
	}
}

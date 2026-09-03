// runner_usage_events_test.go 覆盖 Runner v2 usage.updated 帧的 provider
// 原生报告透传（外部复审 P1-3）：远程 Runner 链路上 sealed
// ProviderUsageReportV1 经 provider_report 键到达控制面——落 Run 行、seq/digest
// 一致、终态后生成 report-derived canonical usage 而非 absent/unresolved 证据；
// 缺席保持 legacy 行为；畸形（类型不对/digest 失配）按毒帧收口。
package application_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

// sealedRemoteUsageReport 构造与远程 adapter 同构的 sealed provider 原生用量
// 报告（计数满足守恒恒等式 total = uncached + read + write）。
func sealedRemoteUsageReport(t *testing.T, runID, agentID, adapterID string) *domain.ProviderUsageReportV1 {
	t.Helper()
	total, uncached, read, write, out := int64(300), int64(200), int64(50), int64(50), int64(80)
	report := &domain.ProviderUsageReportV1{
		SchemaVersion: domain.ProviderUsageReportSchemaVersionV1,
		RunID:         runID,
		Basis:         domain.UsageBasisPerRun,
		Counters: domain.UsageCountersV1{
			InputTokensTotal: &total, InputUncachedTokens: &uncached,
			CacheReadTokens: &read, CacheWriteTokens: &write, OutputTokens: &out,
		},
		Provenance: domain.UsageProvenanceV1{
			AdapterID: adapterID, Protocol: "dsh-web-gateway", ProtocolVersion: "1",
			Source: "final", ReportedBasis: domain.UsageBasisPerRun,
			AgentID: agentID, SessionRef: "dsh://sess_remote", Mapping: "native_accumulators",
		},
	}
	if err := report.Seal(); err != nil {
		t.Fatalf("sealed provider usage report 构造失败: %v", err)
	}
	return report
}

// wireRoundTrip 模拟网关对 eventPayload 的 JSON 解码：data 经序列化往返后
// 数值为 float64、嵌套对象为 map[string]any——与真实远程链路一致。
func wireRoundTrip(t *testing.T, data map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// usageEventInput 组装一条合法 framing 的 usage.updated 事件命令。
func usageEventInput(runID, leaseID string, fencing, seq int64, eventID string, data map[string]any) application.RunnerEventInput {
	return application.RunnerEventInput{
		RunID: runID, LeaseID: leaseID, RunnerID: "runner_ctx", FencingToken: fencing,
		EventID: eventID, ProducerSeq: seq, Kind: domain.EventUsageUpdated, Data: data,
	}
}

// remoteRunningRun 建一个 running 状态、带活动租约的 Run（远程事件驱动前置）。
func remoteRunningRun(t *testing.T, ctx context.Context, svc *application.Service, store *sqlstore.Store, wsID, agentID, leaseID string) *domain.ExecutionRun {
	t.Helper()
	wi := mustTask(t, ctx, svc, wsID, "远程用量 "+leaseID)
	run, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agentID, Instruction: "远程执行"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunRunning, nil); err != nil {
		t.Fatal(err)
	}
	return run
}

// 远程链路正向：带 sealed provider_report 的 usage.updated + 终态事件 →
// run 行绑定报告（seq=1、digest 一致）且 canonical usage 从报告派生，
// 不落 absent/unresolved 证据。
func TestApplyRunnerUsageEventCarriesProviderReport(t *testing.T) {
	ctx := context.Background()
	svc, store, _, wsID, agentID := newContextTestSvc(t, "ws_runner_usage")
	run := remoteRunningRun(t, ctx, svc, store, wsID, agentID, "lease_usage_report")
	fencing := leaseFor(t, ctx, store, wsID, run.ID, "lease_usage_report")

	report := sealedRemoteUsageReport(t, run.ID, agentID, "mock")
	data := wireRoundTrip(t, map[string]any{
		"input_tokens": 300, "output_tokens": 80, "cached_tokens": 50, "basis": "per_run",
		"provider_report": report,
	})
	ack, err := svc.ApplyRunnerEvent(ctx, usageEventInput(run.ID, "lease_usage_report", fencing, 1, "revt_usage_1", data))
	if err != nil || ack.Outcome != application.RunnerEventApplied {
		t.Fatalf("usage.updated 应 applied: ack=%+v err=%v", ack, err)
	}

	stored, err := store.Runs().Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ProviderUsageReport == nil {
		t.Fatal("provider_report 未绑定 Run 行——远程 Run 丢证据（P1-3 复发）")
	}
	if stored.ProviderUsageReport.Digest != report.Digest {
		t.Fatalf("report digest 失真: %q != %q", stored.ProviderUsageReport.Digest, report.Digest)
	}
	if stored.ProviderUsageReportSeq != 1 {
		t.Fatalf("首次上报 seq 应为 1，实际 %d", stored.ProviderUsageReportSeq)
	}
	if stored.ProviderUsageReportDigest != report.Digest {
		t.Fatalf("report digest 列失真: %q", stored.ProviderUsageReportDigest)
	}
	if stored.UsageIn != 300 || stored.UsageOut != 80 || stored.UsageCached != 50 {
		t.Fatalf("legacy 用量列未更新: in=%d out=%d cached=%d", stored.UsageIn, stored.UsageOut, stored.UsageCached)
	}

	// 终态事件：canonical usage 应从 report 派生（计数 resolved），而非 absent。
	if _, err := svc.ApplyRunnerEvent(ctx, application.RunnerEventInput{
		RunID: run.ID, LeaseID: "lease_usage_report", RunnerID: "runner_ctx", FencingToken: fencing,
		EventID: "revt_usage_2", ProducerSeq: 2, Kind: domain.EventRunStatusChanged,
		Data: map[string]any{"status": "succeeded"},
	}); err != nil {
		t.Fatal(err)
	}
	final, err := store.Runs().Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.CanonicalUsage == nil {
		t.Fatal("终态后 canonical usage 未落——远程受管 Run 只能被 sweep 补 absent")
	}
	canonical := final.CanonicalUsage
	if canonical.Counters.InputTokensTotal == nil || *canonical.Counters.InputTokensTotal != 300 ||
		canonical.Counters.OutputTokens == nil || *canonical.Counters.OutputTokens != 80 {
		t.Fatalf("canonical 计数未从 report 派生: %+v", canonical.Counters)
	}
	if canonical.Provenance.AgentID != agentID {
		t.Fatalf("canonical 身份失真: agent=%s", canonical.Provenance.AgentID)
	}
	for _, kind := range []domain.QuotaKind{
		domain.QuotaInputTokensTotal, domain.QuotaInputUncachedTokens, domain.QuotaCacheReadTokens,
		domain.QuotaCacheWriteTokens, domain.QuotaOutputTokens,
	} {
		for _, unresolved := range canonical.UnresolvedKinds {
			if unresolved == kind {
				t.Fatalf("kind %q 不应 unresolved（report 已提供）", kind)
			}
		}
	}
}

// legacy 兼容：不带 provider_report 的 usage.updated 正常应用（老 runner /
// 无 report adapter），Run 行不绑定报告。
func TestApplyRunnerUsageEventLegacyWithoutReport(t *testing.T) {
	ctx := context.Background()
	svc, store, _, wsID, agentID := newContextTestSvc(t, "ws_runner_usage_legacy")
	run := remoteRunningRun(t, ctx, svc, store, wsID, agentID, "lease_usage_legacy")
	fencing := leaseFor(t, ctx, store, wsID, run.ID, "lease_usage_legacy")

	data := wireRoundTrip(t, map[string]any{
		"input_tokens": 12, "output_tokens": 3, "cached_tokens": 0, "basis": "per_run",
	})
	ack, err := svc.ApplyRunnerEvent(ctx, usageEventInput(run.ID, "lease_usage_legacy", fencing, 1, "revt_usage_l1", data))
	if err != nil || ack.Outcome != application.RunnerEventApplied {
		t.Fatalf("legacy usage.updated 应 applied: ack=%+v err=%v", ack, err)
	}
	stored, err := store.Runs().Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ProviderUsageReport != nil {
		t.Fatalf("legacy 帧不得绑定 report: %+v", stored.ProviderUsageReport)
	}
	if stored.UsageIn != 12 || stored.UsageOut != 3 {
		t.Fatalf("legacy 用量列未更新: in=%d out=%d", stored.UsageIn, stored.UsageOut)
	}
}

// 畸形 provider_report（类型不对 / digest 失配）：毒帧收口——Run 落
// failed(runner_event_invalid)、lease 同事务释放，不静默丢证据。
func TestApplyRunnerUsageEventMalformedProviderReportPoisons(t *testing.T) {
	ctx := context.Background()
	svc, store, _, wsID, agentID := newContextTestSvc(t, "ws_runner_usage_poison")

	poisonedRun := func(t *testing.T, title, leaseID, eventID string, data map[string]any) {
		t.Helper()
		run := remoteRunningRun(t, ctx, svc, store, wsID, agentID, leaseID)
		fencing := leaseFor(t, ctx, store, wsID, run.ID, leaseID)
		ack, err := svc.ApplyRunnerEvent(ctx, usageEventInput(run.ID, leaseID, fencing, 1, eventID, wireRoundTrip(t, data)))
		if err != nil || ack.Outcome != application.RunnerEventApplied {
			t.Fatalf("毒帧应提交 failed 后 ACK: ack=%+v err=%v", ack, err)
		}
		stored, err := store.Runs().Get(ctx, run.ID)
		if err != nil || stored.Status != domain.RunFailed || stored.Failure == nil || stored.Failure.Code != "runner_event_invalid" {
			t.Fatalf("畸形 provider_report 未毒帧收口: %v %+v %+v", err, stored.Status, stored.Failure)
		}
		lease, err := store.Runners().ActiveLease(ctx, run.ID)
		if err != nil || !lease.Released {
			t.Fatalf("毒帧必须同事务释放 lease: %v %+v", err, lease)
		}
	}

	t.Run("provider_report type", func(t *testing.T) {
		poisonedRun(t, "report 类型畸形", "lease_usage_p1", "revt_usage_p1", map[string]any{
			"input_tokens": 1, "output_tokens": 1, "cached_tokens": 0, "basis": "per_run",
			"provider_report": "not-an-object",
		})
	})

	t.Run("provider_report digest", func(t *testing.T) {
		// sealed 报告被篡改计数后再上线：解析可还原、VerifyDigest 必败 → poison。
		report := sealedRemoteUsageReport(t, "run_poison_digest", agentID, "mock")
		rawReport, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		var tampered map[string]any
		if err := json.Unmarshal(rawReport, &tampered); err != nil {
			t.Fatal(err)
		}
		counters, ok := tampered["counters"].(map[string]any)
		if !ok {
			t.Fatal("报告 counters 形态异常")
		}
		counters["output_tokens"] = 999
		poisonedRun(t, "report digest 失配", "lease_usage_p2", "revt_usage_p2", map[string]any{
			"input_tokens": 300, "output_tokens": 80, "cached_tokens": 50, "basis": "per_run",
			"provider_report": tampered,
		})
	})
}

// ── 终态后迟到 usage（终态观测例外，P1 回归钉）──────────────────────

// terminalRunWithReleasedLease 建一个经 ApplyRunnerEvent 落终态的 Run：终态
// 事件同事务释放 lease（步骤⑤），返回 run 与 fencing token。
func terminalRunWithReleasedLease(t *testing.T, ctx context.Context, svc *application.Service, store *sqlstore.Store, wsID, agentID, leaseID string) (*domain.ExecutionRun, int64) {
	t.Helper()
	run := remoteRunningRun(t, ctx, svc, store, wsID, agentID, leaseID)
	fencing := leaseFor(t, ctx, store, wsID, run.ID, leaseID)
	ack, err := svc.ApplyRunnerEvent(ctx, application.RunnerEventInput{
		RunID: run.ID, LeaseID: leaseID, RunnerID: "runner_ctx", FencingToken: fencing,
		EventID: "revt_terminal_" + leaseID, ProducerSeq: 1, Kind: domain.EventRunStatusChanged,
		Data: map[string]any{"status": "succeeded"},
	})
	if err != nil || ack.Outcome != application.RunnerEventApplied {
		t.Fatalf("终态事件应 applied: ack=%+v err=%v", ack, err)
	}
	lease, err := store.Runners().ActiveLease(ctx, run.ID)
	if err != nil || lease == nil || !lease.Released {
		t.Fatalf("终态应同事务释放 lease: %v %+v", err, lease)
	}
	return run, fencing
}

// 终态后同身份迟到 usage.updated（带 sealed provider_report）必须 applied：
// legacy 列更新、report 绑定、canonical 从 report 派生；同帧重放 duplicate
// 不重复应用；例外不扩面——迟到 message.delta 仍 stale。
func TestApplyRunnerLateUsageAfterTerminalApplies(t *testing.T) {
	ctx := context.Background()
	svc, store, _, wsID, agentID := newContextTestSvc(t, "ws_late_usage")
	run, fencing := terminalRunWithReleasedLease(t, ctx, svc, store, wsID, agentID, "lease_late_usage")

	report := sealedRemoteUsageReport(t, run.ID, agentID, "mock")
	data := wireRoundTrip(t, map[string]any{
		"input_tokens": 300, "output_tokens": 80, "cached_tokens": 50, "basis": "per_run",
		"provider_report": report,
	})
	ack, err := svc.ApplyRunnerEvent(ctx, usageEventInput(run.ID, "lease_late_usage", fencing, 2, "revt_late_1", data))
	if err != nil || ack.Outcome != application.RunnerEventApplied {
		t.Fatalf("终态后迟到 usage 应 applied: ack=%+v err=%v", ack, err)
	}
	stored, err := store.Runs().Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.UsageIn != 300 || stored.UsageOut != 80 || stored.UsageCached != 50 {
		t.Fatalf("迟到 usage legacy 列未更新: in=%d out=%d cached=%d", stored.UsageIn, stored.UsageOut, stored.UsageCached)
	}
	if stored.ProviderUsageReport == nil || stored.ProviderUsageReport.Digest != report.Digest || stored.ProviderUsageReportSeq != 1 {
		t.Fatalf("迟到 report 未绑定: report=%v seq=%d", stored.ProviderUsageReport, stored.ProviderUsageReportSeq)
	}
	// 终态（无 report 非受管）时 canonical 未落；迟到 report 到达后应从 report 派生。
	if stored.CanonicalUsage == nil {
		t.Fatal("迟到 usage 后 canonical 未从 report 派生")
	}
	if stored.CanonicalUsage.Counters.OutputTokens == nil || *stored.CanonicalUsage.Counters.OutputTokens != 80 {
		t.Fatalf("canonical 计数未从迟到 report 派生: %+v", stored.CanonicalUsage.Counters)
	}

	// 同帧重放：dedup 键不变 → duplicate，不重复应用。
	dup, err := svc.ApplyRunnerEvent(ctx, usageEventInput(run.ID, "lease_late_usage", fencing, 2, "revt_late_1", data))
	if err != nil || dup.Outcome != application.RunnerEventDuplicate {
		t.Fatalf("迟到 usage 重放应 duplicate: ack=%+v err=%v", dup, err)
	}
	again, err := store.Runs().Get(ctx, run.ID)
	if err != nil || again.ProviderUsageReportSeq != 1 {
		t.Fatalf("重放不得重复应用: %v seq=%d", err, again.ProviderUsageReportSeq)
	}

	// 例外只覆盖 usage.updated：终态后迟到的 message.delta 仍 stale。
	nonUsage, err := svc.ApplyRunnerEvent(ctx, application.RunnerEventInput{
		RunID: run.ID, LeaseID: "lease_late_usage", RunnerID: "runner_ctx", FencingToken: fencing,
		EventID: "revt_late_3", ProducerSeq: 3, Kind: domain.EventMessageDelta,
		Data: map[string]any{"role": "assistant", "text": "late"},
	})
	if err != nil || nonUsage.Outcome != application.RunnerEventStale {
		t.Fatalf("非 usage 帧不得走终态观测例外: ack=%+v err=%v", nonUsage, err)
	}
}

// legacy 迟到 usage（无 provider_report）在终态后同样可应用：legacy 投影
// 语义不变。
func TestApplyRunnerLateUsageLegacyAfterTerminalApplies(t *testing.T) {
	ctx := context.Background()
	svc, store, _, wsID, agentID := newContextTestSvc(t, "ws_late_usage_legacy")
	run, fencing := terminalRunWithReleasedLease(t, ctx, svc, store, wsID, agentID, "lease_late_legacy")

	data := wireRoundTrip(t, map[string]any{
		"input_tokens": 42, "output_tokens": 7, "cached_tokens": 0, "basis": "per_run",
	})
	ack, err := svc.ApplyRunnerEvent(ctx, usageEventInput(run.ID, "lease_late_legacy", fencing, 2, "revt_late_l1", data))
	if err != nil || ack.Outcome != application.RunnerEventApplied {
		t.Fatalf("legacy 迟到 usage 应 applied: ack=%+v err=%v", ack, err)
	}
	stored, err := store.Runs().Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.UsageIn != 42 || stored.UsageOut != 7 {
		t.Fatalf("legacy 迟到用量列未更新: in=%d out=%d", stored.UsageIn, stored.UsageOut)
	}
	if stored.ProviderUsageReport != nil {
		t.Fatalf("legacy 迟到帧不得绑定 report: %+v", stored.ProviderUsageReport)
	}
}

// 终态观测例外的严格身份：fencing 失配 / 错 runner 的迟到 usage 仍 stale，
// 不应用、不改 run 行。
func TestApplyRunnerLateUsageStrictIdentityStaysStale(t *testing.T) {
	ctx := context.Background()
	svc, store, _, wsID, agentID := newContextTestSvc(t, "ws_late_usage_identity")
	run, fencing := terminalRunWithReleasedLease(t, ctx, svc, store, wsID, agentID, "lease_late_id")

	data := wireRoundTrip(t, map[string]any{
		"input_tokens": 300, "output_tokens": 80, "cached_tokens": 50, "basis": "per_run",
	})
	for name, in := range map[string]application.RunnerEventInput{
		"fencing mismatch": {RunID: run.ID, LeaseID: "lease_late_id", RunnerID: "runner_ctx",
			FencingToken: fencing + 1, EventID: "revt_id_1", ProducerSeq: 2,
			Kind: domain.EventUsageUpdated, Data: data},
		"runner mismatch": {RunID: run.ID, LeaseID: "lease_late_id", RunnerID: "runner_other",
			FencingToken: fencing, EventID: "revt_id_2", ProducerSeq: 2,
			Kind: domain.EventUsageUpdated, Data: data},
		"unknown lease": {RunID: run.ID, LeaseID: "lease_missing", RunnerID: "runner_ctx",
			FencingToken: fencing, EventID: "revt_id_3", ProducerSeq: 2,
			Kind: domain.EventUsageUpdated, Data: data},
	} {
		ack, err := svc.ApplyRunnerEvent(ctx, in)
		if err != nil || ack.Outcome != application.RunnerEventStale {
			t.Fatalf("%s 的迟到 usage 应 stale: ack=%+v err=%v", name, ack, err)
		}
	}
	stored, err := store.Runs().Get(ctx, run.ID)
	if err != nil || stored.UsageIn != 0 || stored.UsageOut != 0 || stored.ProviderUsageReport != nil {
		t.Fatalf("stale 帧不得改动 run 行: %v in=%d out=%d report=%v",
			err, stored.UsageIn, stored.UsageOut, stored.ProviderUsageReport)
	}
}

// 防御：lease 已释放但 Run 未终态（如 sweep 收走租约）→ 迟到 usage 仍 stale。
func TestApplyRunnerLateUsageNonTerminalRunStaysStale(t *testing.T) {
	ctx := context.Background()
	svc, store, _, wsID, agentID := newContextTestSvc(t, "ws_late_usage_nonterm")
	run := remoteRunningRun(t, ctx, svc, store, wsID, agentID, "lease_nonterm")
	fencing := leaseFor(t, ctx, store, wsID, run.ID, "lease_nonterm")
	// 构造：非终态 Run 的租约被直接释放（sweep/接管语义）。
	if err := store.Runners().ReleaseLease(ctx, "lease_nonterm", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	data := wireRoundTrip(t, map[string]any{
		"input_tokens": 300, "output_tokens": 80, "cached_tokens": 50, "basis": "per_run",
	})
	ack, err := svc.ApplyRunnerEvent(ctx, usageEventInput(run.ID, "lease_nonterm", fencing, 1, "revt_nonterm_1", data))
	if err != nil || ack.Outcome != application.RunnerEventStale {
		t.Fatalf("非终态 Run 的已释放租约 usage 应 stale: ack=%+v err=%v", ack, err)
	}
	stored, err := store.Runs().Get(ctx, run.ID)
	if err != nil || stored.UsageIn != 0 || stored.UsageOut != 0 {
		t.Fatalf("stale 帧不得改动 run 行: %v in=%d out=%d", err, stored.UsageIn, stored.UsageOut)
	}
}

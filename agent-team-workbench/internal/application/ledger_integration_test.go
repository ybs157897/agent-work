package application_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// digestOf 读取任务台账摘要（失败即 Fatal）。
func digestOf(t *testing.T, ctx context.Context, store *sqlstore.Store, wiID string) string {
	t.Helper()
	wi, err := store.WorkItems().Get(ctx, wiID)
	if err != nil {
		t.Fatal(err)
	}
	return wi.RollingDigest
}

// countSubstring 子串出现次数（防重复追加断言用）。
func countSubstring(s, sub string) int {
	return strings.Count(s, sub)
}

// TestRollingDigestRefreshOnTerminalSegment 防回归（S2 摘要）：run 终态触发
// rolling_digest 确定性重算（状态行 + 轮数 + 最近摘录）；同一 run 重放终态被
// 状态机拒绝且摘要逐字节不变（不得重复追加）；后续轮次重算自然并入。
func TestRollingDigestRefreshOnTerminalSegment(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wi := seedRunEnv(t, ctx, svc, store)
	first, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: wi.AgentProfileID, Instruction: "第一轮：记住暗号 ECHO-9",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, first.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, first.ID, "暗号已记住"); err != nil {
		t.Fatal(err)
	}
	digest := digestOf(t, ctx, store, wi.ID)
	if !strings.Contains(digest, "第一轮：记住暗号 ECHO-9") || !strings.Contains(digest, "暗号已记住") {
		t.Fatalf("摘要应含当轮指令与回复: %q", digest)
	}
	if !strings.Contains(digest, "已执行 1 轮") || !strings.Contains(digest, "【任务台账】回归") {
		t.Fatalf("摘要状态行缺失: %q", digest)
	}

	// 第二轮：重算并入，且不重复第一轮内容。
	second, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: wi.AgentProfileID, Instruction: "第二轮：报暗号",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, second.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, second.ID, "ECHO-9"); err != nil {
		t.Fatal(err)
	}
	digest = digestOf(t, ctx, store, wi.ID)
	if !strings.Contains(digest, "第二轮：报暗号") || !strings.Contains(digest, "已执行 2 轮") {
		t.Fatalf("重算应并入第二轮: %q", digest)
	}
	if countSubstring(digest, "【任务台账】") != 1 || countSubstring(digest, "第一轮：记住暗号 ECHO-9") != 1 {
		t.Fatalf("重算覆盖写不得产生重复追加: %q", digest)
	}

	// 同一 run 重放终态：状态机拒绝，摘要逐字节不变。
	if err := svc.RecordRunStatus(ctx, first.ID, domain.RunSucceeded, nil); err == nil {
		t.Fatal("终态 run 重放终态应被拒绝")
	}
	if after := digestOf(t, ctx, store, wi.ID); after != digest {
		t.Fatalf("重放终态不得改动摘要:\n前=%q\n后=%q", digest, after)
	}
}

// conflictOnceWorkItems 前 N 次 UpdateRollingDigest 注入版本冲突的替身仓储。
type conflictOnceWorkItems struct {
	application.WorkItemRepo
	remaining *atomic.Int32
}

func (w *conflictOnceWorkItems) UpdateRollingDigest(ctx context.Context, workItemID, digest string, expectedVersion int) error {
	if w.remaining.Add(-1) >= 0 {
		return domain.ErrVersionConflict
	}
	return w.WorkItemRepo.UpdateRollingDigest(ctx, workItemID, digest, expectedVersion)
}

// conflictOnceStore 测试替身：注入确定性的台账写版本冲突，钉住终态摘要钩子
// 的乐观锁重试收敛路径（真实多写者 = PG 的冲突形态；单连接 sqlite 不产生冲突）。
type conflictOnceStore struct {
	application.Store
	remaining *atomic.Int32
}

func (s *conflictOnceStore) WorkItems() application.WorkItemRepo {
	return &conflictOnceWorkItems{WorkItemRepo: s.Store.WorkItems(), remaining: s.remaining}
}

// TestRollingDigestConcurrentRefresh 防回归：并发终态钩子的读改写安全——
// Go 侧两 goroutine 并发收尾（-race 覆盖共享状态），台账写注入两次确定性
// 版本冲突，断言有界重试收敛、最终摘要含全部已关闭轮次且无重复追加。
func TestRollingDigestConcurrentRefresh(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	base := sqlstore.New(db, sqlstore.SQLiteDialect())
	counter := &atomic.Int32{}
	counter.Store(2)
	store := &conflictOnceStore{Store: base, remaining: counter}
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wi := seedRunEnv(t, ctx, svc, base)
	runA, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: wi.AgentProfileID, Instruction: "并发轮 A",
	})
	if err != nil {
		t.Fatal(err)
	}
	runB, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: wi.AgentProfileID, Instruction: "并发轮 B",
	})
	if err != nil {
		t.Fatal(err)
	}

	// queued → starting 先行；并发段走 starting→succeeding→succeeded（零事件空
	// turn 的合法边，不经 running 任务锁——同任务双 run 并发 running 本就被 F1
	// 锁拒绝），只让终态写与台账摘要钩子真正并发。
	for _, run := range []*domain.ExecutionRun{runA, runB} {
		if err := svc.RecordRunStatus(ctx, run.ID, domain.RunStarting, nil); err != nil {
			t.Fatal(err)
		}
	}

	converge := func(runID, text string) error {
		if err := svc.RecordRunStatus(ctx, runID, domain.RunSucceeding, nil); err != nil {
			return err
		}
		if err := svc.RecordRunEvent(ctx, runID, domain.EventMessageCompleted,
			map[string]any{"role": "assistant", "text": text}); err != nil {
			return err
		}
		return svc.RecordRunStatus(ctx, runID, domain.RunSucceeded, nil)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs <- converge(runA.ID, "甲轮结论")
	}()
	go func() {
		defer wg.Done()
		errs <- converge(runB.ID, "乙轮结论")
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("并发终态不应泄漏错误: %v", err)
		}
	}
	if store.remaining.Load() > 0 {
		t.Fatalf("注入的版本冲突应被消耗殆尽: %d", store.remaining.Load())
	}

	digest := digestOf(t, ctx, base, wi.ID)
	if !strings.Contains(digest, "甲轮结论") || !strings.Contains(digest, "乙轮结论") {
		t.Fatalf("冲突重试后摘要应收敛到全部轮次: %q", digest)
	}
	if !strings.Contains(digest, "已执行 2 轮") || countSubstring(digest, "【任务台账】") != 1 {
		t.Fatalf("冲突重试后摘要形状异常: %q", digest)
	}
}

// TestRecordDecisionValidation 防回归（S2 决策台账）：quote trim 后非空、
// source_run_id 必须属于该 work item（不存在/跨任务均 400）、写入与
// decision.created 事件同事务、列表升序。
func TestRecordDecisionValidation(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wi := seedRunEnv(t, ctx, svc, store)
	run, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: wi.AgentProfileID, Instruction: "选型讨论",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, run.ID, "结论出来了"); err != nil {
		t.Fatal(err)
	}

	// 正常写入：quote 原文保真（仅 trim），来源轮次与片段位置落库。
	entry, err := svc.RecordDecision(ctx, wi.ID, application.RecordDecisionParams{
		Quote: "  用 PostgreSQL，不引入第二套数据库  ", SourceRunID: run.ID, SourceRef: "msg:2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Quote != "用 PostgreSQL，不引入第二套数据库" || entry.SourceRunID != run.ID || entry.SourceRef != "msg:2" {
		t.Fatalf("决策行内容失真: %+v", entry)
	}

	// quote 空/纯空白 → 校验拒绝。
	for _, q := range []string{"", "   "} {
		if _, err := svc.RecordDecision(ctx, wi.ID, application.RecordDecisionParams{Quote: q}); !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("空 quote 应 ErrValidation: %v", err)
		}
	}
	// source_run_id 不存在 / 跨任务 → 校验拒绝。
	if _, err := svc.RecordDecision(ctx, wi.ID, application.RecordDecisionParams{
		Quote: "x", SourceRunID: "run_missing"}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("不存在的 source_run_id 应 ErrValidation: %v", err)
	}
	other, err := svc.CreateWorkItem(ctx, "ws_"+t.Name(), application.CreateWorkItemParams{Title: "别的任务"})
	if err != nil {
		t.Fatal(err)
	}
	otherRun, err := svc.CreateRun(ctx, other.ID, application.CreateRunParams{
		AgentProfileID: wi.AgentProfileID, Instruction: "别的任务首跑",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, otherRun.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, otherRun.ID, "另一任务的回复"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RecordDecision(ctx, wi.ID, application.RecordDecisionParams{
		Quote: "x", SourceRunID: otherRun.ID}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("跨任务 source_run_id 应 ErrValidation: %v", err)
	}
	// 未知任务 → NotFound。
	if _, err := svc.RecordDecision(ctx, "wi_missing", application.RecordDecisionParams{Quote: "x"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("未知任务应 ErrNotFound: %v", err)
	}

	// 台账行 + decision.created 事件。
	entries, err := svc.DecisionsByWorkItem(ctx, wi.ID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("台账应有 1 条决策: %v %#v", err, entries)
	}
	events, err := store.Events().Since(ctx, "ws_"+t.Name(), 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range events {
		if ev.Type != domain.EventDecisionCreated {
			continue
		}
		found = true
		if ev.Aggregate.Type != domain.AggregateDecision || ev.Aggregate.ID != entry.ID {
			t.Fatalf("decision.created 信封异常: %+v", ev)
		}
		if ev.Data["work_item_id"] != wi.ID || ev.Data["quote"] != entry.Quote {
			t.Fatalf("decision.created 载荷异常: %+v", ev.Data)
		}
	}
	if !found {
		t.Fatal("decision.created 未进入事件流")
	}

	// 第二条决策：列表升序。
	second, err := svc.RecordDecision(ctx, wi.ID, application.RecordDecisionParams{Quote: "第二条"})
	if err != nil {
		t.Fatal(err)
	}
	entries, err = svc.DecisionsByWorkItem(ctx, wi.ID)
	if err != nil || len(entries) != 2 {
		t.Fatalf("台账应有 2 条决策: %v %#v", err, entries)
	}
	if entries[0].ID != entry.ID || entries[1].ID != second.ID {
		t.Fatalf("列表应按创建时间升序: %#v", entries)
	}
}

package application_test

import (
	"context"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// TestRecordRunUsageEmitsUsageUpdated：OnUsage 过程帧逐次进 RecordRunUsage →
// run 行四列末次覆盖 + stream_events 每次恰好一条 usage.updated（data 四字段
// 与 runDTO 契约一致，aggregate=execution_run）——run 进行中用量即达前端。
func TestRecordRunUsageEmitsUsageUpdated(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	ws := &domain.Workspace{ID: "ws_usage", Name: "usage", Timezone: "UTC", Version: 1}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	wi, err := svc.CreateWorkItem(ctx, ws.ID, application.CreateWorkItemParams{Title: "usage progress"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{Instruction: "跑一轮"})
	if err != nil {
		t.Fatal(err)
	}

	// 模拟两帧 OnUsage 过程观测（adapter 发各自累计口径，覆盖语义）。
	first := atwruntime.Usage{InputTokens: 100, OutputTokens: 20, CachedTokens: 10, Basis: atwruntime.UsagePerRun}
	last := atwruntime.Usage{InputTokens: 250, OutputTokens: 80, CachedTokens: 40, Basis: atwruntime.UsagePerRun}
	for _, u := range []atwruntime.Usage{first, last} {
		if err := svc.RecordRunUsage(ctx, run.ID, u); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.Runs().Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.UsageIn != last.InputTokens || got.UsageOut != last.OutputTokens ||
		got.UsageCached != last.CachedTokens || got.UsageBasis != string(last.Basis) {
		t.Fatalf("run 行四列应为末次覆盖值: in=%d out=%d cached=%d basis=%s",
			got.UsageIn, got.UsageOut, got.UsageCached, got.UsageBasis)
	}

	events, err := store.Events().Since(ctx, ws.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var usageEvents []*domain.CanonicalEvent
	for _, ev := range events {
		if ev.Type == domain.EventUsageUpdated {
			usageEvents = append(usageEvents, ev)
		}
	}
	if len(usageEvents) != 2 {
		t.Fatalf("期望恰好 2 条 usage.updated（每帧一条），得到 %d", len(usageEvents))
	}
	for i, want := range []atwruntime.Usage{first, last} {
		ev := usageEvents[i]
		if ev.AggregateType != domain.AggregateExecutionRun || ev.AggregateID != run.ID {
			t.Fatalf("usage.updated 聚合不符: %+v", ev.Aggregate)
		}
		// stream_events payload 经 JSON 往返，数值读回为 float64。
		if numData(ev.Data["usage_in"]) != want.InputTokens || numData(ev.Data["usage_out"]) != want.OutputTokens ||
			numData(ev.Data["usage_cached"]) != want.CachedTokens || ev.Data["usage_basis"] != string(want.Basis) {
			t.Fatalf("第 %d 条 usage.updated data 不符: %+v", i+1, ev.Data)
		}
	}
}

// numData 取事件 data 数值字段（JSON 往返后为 float64）的 int64 值；缺失返回 -1。
func numData(v any) int64 {
	if f, ok := v.(float64); ok {
		return int64(f)
	}
	return -1
}

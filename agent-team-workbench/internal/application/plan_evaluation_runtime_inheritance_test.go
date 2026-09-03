package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// seedEvaluationRuntimeEnv 在 seedCoordinatorEnv 之上配置非 mock 的 Coordinator
// runtime（codex_local，binding provider/model 与配置 ModelRef 故意不同，用于证明
// 评估 run 的模型快照来自 config 而非 binding），并显式配置成对回退。
func seedEvaluationRuntimeEnv(t *testing.T) (context.Context, *application.Service, *sqlstore.Store,
	*captureDispatcher, *domain.TaskCoordinatorConfig, string, string) {
	t.Helper()
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	now := time.Now().UTC()
	if err := store.Bindings().Create(ctx, &domain.RuntimeBinding{
		ID: "rb_coordinator_codex", WorkspaceID: wsID, RuntimeLabel: "codex_local",
		AdapterID: "codex-appserver", Provider: "codex", Model: "binding-model",
		Status:       domain.BindingReady,
		Capabilities: map[string]string{"resume": string(atwruntime.CapSupported)},
		Version:      1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	config, err := store.TaskCoordinators().EnsureConfig(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeLabel = "codex_local"
	config.FallbackRuntimeLabel = "mock"
	config.ModelRef = domain.ModelRef{Provider: "codex", Model: "coord-main-model"}
	config.FallbackModelRef = domain.ModelRef{Provider: "mock-provider", Model: "coord-fallback-model",
		ReasoningEffort: "low"}
	config.ReasoningEffort = "high"
	if err := store.TaskCoordinators().UpdateConfig(ctx, config, config.Version); err != nil {
		t.Fatal(err)
	}
	config, err = store.TaskCoordinators().GetConfig(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, svc, store, dispatcher, config, wsID, workerID
}

func submitFinishEvaluationDecision(t *testing.T, ctx context.Context, svc *application.Service,
	store *sqlstore.Store, dispatcher *captureDispatcher, sourceRunID string) *domain.ExecutionRun {
	t.Helper()
	decision := `{"schema_version":"plan-decision/v2","kind":"plan","reason":"all evidence is complete",` +
		`"next_action":"evaluate before user acceptance","steps":[{"verb":"finish","evaluation":true}]}`
	completeCoordinatorPlanDecision(t, ctx, svc, sourceRunID, decision)
	if len(dispatcher.runs) != 2 {
		t.Fatalf("finish{evaluation:true} 必须建成恰好一个评估 run: runs=%d", len(dispatcher.runs))
	}
	evaluation, err := store.Runs().Get(ctx, dispatcher.runs[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	return evaluation
}

func requireEvaluationRuntimeSnapshot(t *testing.T, evaluation *domain.ExecutionRun,
	wantLabel, wantPreferred string, wantFallbacks []string, wantModel domain.ModelRef) {
	t.Helper()
	if evaluation.RuntimeLabel != wantLabel {
		t.Fatalf("评估 run runtime 继承失配: want=%s got=%s", wantLabel, evaluation.RuntimeLabel)
	}
	preference, ok := evaluation.Input["runtime_preference"].(map[string]any)
	if !ok {
		t.Fatalf("评估 run 缺少 runtime_preference 快照: %#v", evaluation.Input["runtime_preference"])
	}
	if preference["preferred"] != wantPreferred {
		t.Fatalf("runtime_preference.preferred 失配: want=%s got=%v", wantPreferred, preference["preferred"])
	}
	if preference["mode"] != "plan" {
		t.Fatalf("runtime_preference.mode 必须为 plan: %v", preference["mode"])
	}
	fallbacks, _ := preference["fallbacks"].([]any)
	if len(fallbacks) != len(wantFallbacks) {
		t.Fatalf("runtime_preference.fallbacks 失配: want=%v got=%v", wantFallbacks, fallbacks)
	}
	for i, want := range wantFallbacks {
		if fallbacks[i] != want {
			t.Fatalf("runtime_preference.fallbacks[%d] 失配: want=%s got=%v", i, want, fallbacks[i])
		}
	}
	model, ok := evaluation.Input["model"].(map[string]any)
	if !ok {
		t.Fatalf("评估 run 缺少模型快照: %#v", evaluation.Input["model"])
	}
	if model["provider"] != wantModel.Provider || model["model"] != wantModel.Model ||
		model["reasoning_effort"] != wantModel.ReasoningEffort {
		t.Fatalf("评估 run 模型快照与 Coordinator 配置不成对: want=%+v got=%#v", wantModel, model)
	}
}

// F1 防回归：finish{evaluation:true} 的评估 run 必须与 Coordinator 轮次同源继承
// workspace 配置的主 runtime/模型（use_fallback=false），而不是回落 mock 撞
// 「禁止静默回退」守卫。
func TestEvaluationRunInheritsCoordinatorConfiguredRuntime(t *testing.T) {
	ctx, svc, store, dispatcher, config, wsID, _ := seedEvaluationRuntimeEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "评估 runtime 继承", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	evaluation := submitFinishEvaluationDecision(t, ctx, svc, store, dispatcher, dispatcher.runs[0].ID)
	requireEvaluationRuntimeSnapshot(t, evaluation, config.RuntimeLabel, config.RuntimeLabel,
		[]string{config.FallbackRuntimeLabel}, config.ModelRef)
	control, _ := evaluation.Input["task_coordinator"].(map[string]any)
	if control["use_fallback"] != false {
		t.Fatalf("评估 control context 必须携带 use_fallback=false: %#v", control)
	}
	if evaluation.WorkItemID != root.ID {
		t.Fatalf("评估 run 必须挂在主任务上: %+v", evaluation)
	}
}

// F1 防回归：coordinator state use_fallback=true 时评估 run 成对切换到
// FallbackRuntimeLabel + FallbackModelRef（runtime 与模型不允许分叉）。
func TestEvaluationRunFollowsCoordinatorFallbackPairing(t *testing.T) {
	ctx, svc, store, dispatcher, config, wsID, _ := seedEvaluationRuntimeEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "评估回退成对切换", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := state.Version
	if state.Data == nil {
		state.Data = map[string]any{}
	}
	state.Data["use_fallback"] = true
	if err := store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
		t.Fatal(err)
	}
	evaluation := submitFinishEvaluationDecision(t, ctx, svc, store, dispatcher, dispatcher.runs[0].ID)
	requireEvaluationRuntimeSnapshot(t, evaluation, config.FallbackRuntimeLabel,
		config.FallbackRuntimeLabel, []string{config.RuntimeLabel}, config.FallbackModelRef)
	control, _ := evaluation.Input["task_coordinator"].(map[string]any)
	if control["use_fallback"] != true {
		t.Fatalf("评估 control context 必须携带 use_fallback=true: %#v", control)
	}
}

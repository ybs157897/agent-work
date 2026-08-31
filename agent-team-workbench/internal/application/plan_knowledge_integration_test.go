// plan_knowledge_integration_test.go M2 consult_knowledge 动词测试（设计 note §5）：
// 预取注入（dispatch knowledge_from 拼参考条目）与检索器缺失的响亮失败。
package application_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/knowledge"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// writeKnowledgeCorpus 在 root/prd 下落一条测试语料条目。
func writeKnowledgeCorpus(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "prd")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nid: prd-login\ntitle: 登录需求\nversion: 1\nstatus: effective\n---\n用户必须能用账号密码登录，失败三次后锁定十分钟。\n"
	if err := os.WriteFile(filepath.Join(dir, "prd-login.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestConsultKnowledgeInjectsDispatch 验收 8a：consult_knowledge（tmp 语料 +
// FileRetriever）→ 检索结果落 step payload；dispatch knowledge_from 引用的
// 子任务 instruction 含「## 参考条目」节与条目 ID/全文。
func TestConsultKnowledgeInjectsDispatch(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())

	root := t.TempDir()
	writeKnowledgeCorpus(t, root)
	retriever, err := knowledge.NewFileRetriever(root)
	if err != nil {
		t.Fatal(err)
	}
	svc.Knowledge = retriever

	wsID, leadID, workerID := seedM2Env(t, ctx, store)
	main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "主任务"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: leadID,
		Steps: []application.PlanStepInput{
			{Verb: "consult_knowledge", Payload: map[string]any{
				"corpus": "prd", "terms": []any{"登录"},
			}},
			dispatchStep(workerID, "子任务A", "实现 A"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanFinished {
		t.Fatalf("plan 应 finished，实际 %s", plan.Status)
	}
	stored, err := store.Plans().Get(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Steps[0].Status != domain.PlanStepExecuted {
		t.Fatalf("consult step 状态 = %s，应为 executed", stored.Steps[0].Status)
	}
	results, ok := stored.Steps[0].Payload["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("检索结果未落 step payload.results: %#v", stored.Steps[0].Payload["results"])
	}
	entry, _ := results[0].(map[string]any)
	if entry["id"] != "prd-login" {
		t.Fatalf("检索条目 id = %v，应为 prd-login", entry["id"])
	}
	// dispatch 未带 knowledge_from：不注入。
	firstRun, err := store.Runs().Get(ctx, stored.Steps[1].ResultRunID)
	if err != nil {
		t.Fatal(err)
	}
	if instr, _ := firstRun.Input["instruction"].(string); strings.Contains(instr, "参考条目") {
		t.Fatal("未引用 knowledge_from 的 dispatch 不应注入参考条目")
	}

	// 第二份 plan：dispatch knowledge_from=0 → instruction 注入参考条目全文。
	plan2, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: leadID,
		Steps: []application.PlanStepInput{
			{Verb: "consult_knowledge", Payload: map[string]any{"corpus": "prd", "terms": []any{"登录"}}},
			{Verb: "dispatch", Payload: map[string]any{
				"agent_id": workerID, "title": "子任务B", "instruction": "实现 B",
				"knowledge_from": 0,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	stored2, err := store.Plans().Get(ctx, plan2.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Runs().Get(ctx, stored2.Steps[1].ResultRunID)
	if err != nil {
		t.Fatal(err)
	}
	instruction, _ := run.Input["instruction"].(string)
	for _, want := range []string{"## 参考条目", "prd-login", "登录需求", "账号密码登录"} {
		if !strings.Contains(instruction, want) {
			t.Fatalf("dispatch instruction 缺少 %q:\n%s", want, instruction)
		}
	}
}

// TestConsultKnowledgeWithoutRetrieverFails 验收 8b：retriever=nil → step failed
// error=no_retriever 且 plan failed（响亮失败，余下步骤 skipped、无子任务产生）。
func TestConsultKnowledgeWithoutRetrieverFails(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wsID, leadID, workerID := seedM2Env(t, ctx, store)
	main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "主任务"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: leadID,
		Steps: []application.PlanStepInput{
			{Verb: "consult_knowledge", Payload: map[string]any{"corpus": "prd", "terms": []any{"登录"}}},
			dispatchStep(workerID, "子任务A", "实现 A"),
		},
	})
	if err != nil {
		t.Fatalf("检索器缺失应步骤级失败而非提交报错: %v", err)
	}
	stored, err := store.Plans().Get(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.PlanFailed {
		t.Fatalf("plan 应 failed，实际 %s", stored.Status)
	}
	if stored.Steps[0].Status != domain.PlanStepFailed || stored.Steps[0].Error != "no_retriever" {
		t.Fatalf("consult step 应 failed+no_retriever: %#v", stored.Steps[0])
	}
	if stored.Steps[1].Status != domain.PlanStepSkipped {
		t.Fatalf("失败后的步骤应 skipped，实际 %s", stored.Steps[1].Status)
	}
	if children, err := store.WorkItems().ListByParent(ctx, main.ID); err != nil || len(children) != 0 {
		t.Fatalf("失败 plan 不应产生子任务: %v %#v", err, children)
	}
}

// TestConsultKnowledgeValidation 校验面：corpus 必填且为单层目录名；
// knowledge_from 必须指向更早的 consult_knowledge 步骤。
func TestConsultKnowledgeValidation(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wsID, leadID, workerID := seedM2Env(t, ctx, store)
	main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "主任务"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		steps []application.PlanStepInput
		want  string
	}{
		{
			name:  "corpus 缺失",
			steps: []application.PlanStepInput{{Verb: "consult_knowledge", Payload: map[string]any{"terms": []any{"登录"}}}},
			want:  "corpus",
		},
		{
			name:  "corpus 带路径分隔符",
			steps: []application.PlanStepInput{{Verb: "consult_knowledge", Payload: map[string]any{"corpus": "../etc"}}},
			want:  "corpus",
		},
		{
			name: "knowledge_from 指向 dispatch",
			steps: []application.PlanStepInput{
				dispatchStep(workerID, "子任务A", "实现 A"),
				{Verb: "dispatch", Payload: map[string]any{
					"agent_id": workerID, "title": "子任务B", "instruction": "实现 B", "knowledge_from": 0,
				}},
			},
			want: "consult_knowledge",
		},
		{
			name: "knowledge_from 前向引用",
			steps: []application.PlanStepInput{
				{Verb: "dispatch", Payload: map[string]any{
					"agent_id": workerID, "title": "子任务B", "instruction": "实现 B", "knowledge_from": 1,
				}},
			},
			want: "更早",
		},
	}
	for _, c := range cases {
		_, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
			WorkItemID: main.ID, AgentProfileID: leadID, Steps: c.steps,
		})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: 期望校验错误含 %q，实际 %v", c.name, c.want, err)
		}
	}
}

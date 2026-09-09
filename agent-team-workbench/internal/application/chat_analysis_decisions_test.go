package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/chatsources"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/orchestrator"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

func TestChatAnalysisDecisionHistoryActorCASAndNewMaterialReopen(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	base := seedRunEnv(t, ctx, svc, store)
	chat, err := svc.CreateWorkItem(ctx, base.WorkspaceID, application.CreateWorkItemParams{Title: "U05", RecordKind: domain.RecordKindChat, AgentProfileID: base.AgentProfileID})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.CreateRun(ctx, chat.ID, application.CreateRunParams{AgentProfileID: chat.AgentProfileID, Instruction: "分析", OutputContract: orchestrator.OutputContractChatAnalysisV1})
	if err != nil {
		t.Fatal(err)
	}
	ref, sha := analysisConversationCatalog(t, run)
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, run.ID, analysisEnvelope(ref, sha, "", "", false)); err != nil {
		t.Fatal(err)
	}
	view, err := svc.GetChatAnalysis(ctx, chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	decisionParams := application.SaveChatAnalysisDecisionParams{
		ChatWorkItemID: chat.ID, ExpectedVersion: view.Projection.Version, Revision: view.Projection.Revision,
		ItemID: "scope", Outcome: domain.ChatAnalysisDecisionConfirmed, Conclusion: "确认范围",
		Basis: "产品沟通记录", ProductVersion: "web-idea-v1", ClientKey: "decision:scope:1", ActorID: "user_demo",
	}
	decided, replayed, err := svc.SaveChatAnalysisDecision(ctx, decisionParams)
	if err != nil || replayed || len(decided.Decisions) != 1 || decided.Decisions[0].Status != domain.ChatAnalysisDecisionValid || decided.Decisions[0].UpdatedAt.IsZero() {
		t.Fatalf("decision projection=%+v replayed=%v err=%v", decided, replayed, err)
	}
	if decided.Decisions[0].ItemID == chat.AgentProfileID {
		t.Fatal("product actor/item identity must not be the Agent owner")
	}
	decisionParams.ExpectedVersion = 1
	replayedView, replayed, err := svc.SaveChatAnalysisDecision(ctx, decisionParams)
	if err != nil || !replayed || len(replayedView.Decisions) != 1 {
		t.Fatalf("decision retry should replay: view=%+v replayed=%v err=%v", replayedView, replayed, err)
	}
	decisionParams.Revision++
	if _, _, err := svc.SaveChatAnalysisDecision(ctx, decisionParams); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("same client key across analysis revisions must conflict: %v", err)
	}
	history, err := svc.ChatAnalysisHistory(ctx, chat.ID, 100)
	if err != nil || len(history.Decisions) != 1 || len(history.Revisions) != 1 {
		t.Fatalf("history missing immutable decision/revision: %+v err=%v", history, err)
	}
	contextRun, err := svc.CreateRun(ctx, chat.ID, application.CreateRunParams{AgentProfileID: chat.AgentProfileID, Instruction: "继续分析", OutputContract: orchestrator.OutputContractChatAnalysisV1})
	if err != nil {
		t.Fatal(err)
	}
	analysisContext, _ := contextRun.Input["analysis_context"].(string)
	analysisInput, _ := contextRun.Input["analysis"].(map[string]any)
	if baseRevision, ok := analysisInput["base_revision"].(int64); !ok || baseRevision != 1 {
		if baseRevisionFloat, floatOK := analysisInput["base_revision"].(float64); !floatOK || baseRevisionFloat != 1 {
			t.Fatalf("new analysis Run must freeze base_revision=1: %#v", analysisInput["base_revision"])
		}
	}
	if !strings.Contains(analysisContext, "此前产品回填结论") || !strings.Contains(analysisContext, "确认范围") || !strings.Contains(analysisContext, "product_version=web-idea-v1") || !strings.Contains(analysisContext, "模型不能自行批准") {
		t.Fatalf("analysis context must carry product decision history: %q", analysisContext)
	}
	svc.SetChatSourceStore(chatsources.NewStore(t.TempDir()))
	if _, _, err := svc.CreateChatSource(ctx, application.ChatSourceUpload{ChatWorkItemID: chat.ID, Filename: "change.md", Data: []byte("new material"), ClientKey: "new-material"}); err != nil {
		t.Fatal(err)
	}
	stale, err := svc.GetChatAnalysis(ctx, chat.ID)
	if err != nil || stale.Projection.Status != domain.ChatAnalysisStale || stale.Decisions[0].Status != domain.ChatAnalysisDecisionValid {
		t.Fatalf("new material must stale only product state: projection=%+v decisions=%+v err=%v", stale.Projection, stale.Decisions, err)
	}
}

func TestChatAnalysisRetryKeepsParentFrozenContextAfterCurrentRevisionChanges(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	base := seedRunEnv(t, ctx, svc, store)
	chat, err := svc.CreateWorkItem(ctx, base.WorkspaceID, application.CreateWorkItemParams{Title: "retry lineage", RecordKind: domain.RecordKindChat, AgentProfileID: base.AgentProfileID})
	if err != nil {
		t.Fatal(err)
	}
	run1, err := svc.CreateRun(ctx, chat.ID, application.CreateRunParams{AgentProfileID: chat.AgentProfileID, Instruction: "首轮分析", OutputContract: orchestrator.OutputContractChatAnalysisV1})
	if err != nil {
		t.Fatal(err)
	}
	ref1, sha1 := analysisConversationCatalog(t, run1)
	if err := svc.RecordRunStatus(ctx, run1.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, run1.ID, analysisEnvelope(ref1, sha1, "", "", false)); err != nil {
		t.Fatal(err)
	}
	parent, err := store.Runs().Get(ctx, run1.ID)
	if err != nil {
		t.Fatal(err)
	}
	parentContext, _ := parent.Input["analysis_context"].(string)
	run2, err := svc.CreateRun(ctx, chat.ID, application.CreateRunParams{AgentProfileID: chat.AgentProfileID, Instruction: "第二轮分析", OutputContract: orchestrator.OutputContractChatAnalysisV1})
	if err != nil {
		t.Fatal(err)
	}
	ref2, sha2 := analysisConversationCatalog(t, run2)
	if err := svc.RecordRunStatus(ctx, run2.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, run2.ID, analysisEnvelope(ref2, sha2, "", "", false)); err != nil {
		t.Fatal(err)
	}
	retry, err := svc.RetryRun(ctx, run1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := retry.Input["analysis_context"].(string); got != parentContext {
		t.Fatalf("retry must preserve parent analysis_context across current revision change: got=%q want=%q", got, parentContext)
	}
	analysisInput, _ := retry.Input["analysis"].(map[string]any)
	if baseRevision, ok := analysisInput["base_revision"].(float64); ok && baseRevision != 0 {
		t.Fatalf("retry must preserve parent base_revision=0, got=%v", baseRevision)
	}
}

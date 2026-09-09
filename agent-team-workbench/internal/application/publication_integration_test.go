package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/orchestrator"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

func testPublicationBaseline(head, content string) domain.ProjectBaseline {
	return domain.ProjectBaseline{RepositoryIdentity: "repo_web_idea", RefKind: domain.RefRoot, CheckoutRef: "root", Head: head,
		StagedDigest: content, UnstagedDigest: content, UntrackedDigest: content, ContentDigest: content}
}

func TestPublicationDraftSelectedConfirmedCASAndBaselineDrift(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	base := seedRunEnv(t, ctx, svc, store)
	chat, err := svc.CreateWorkItem(ctx, base.WorkspaceID, application.CreateWorkItemParams{Title: "publish chat", RecordKind: domain.RecordKindChat, AgentProfileID: base.AgentProfileID})
	if err != nil {
		t.Fatal(err)
	}
	baseline := testPublicationBaseline("head-1", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	svc.SetPublicationBaselineResolver(func(context.Context, *domain.ExecutionContextSnapshot) (domain.ProjectBaseline, error) {
		return baseline, nil
	})
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
	analysis, err := svc.GetChatAnalysis(ctx, chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	decision, _, err := svc.SaveChatAnalysisDecision(ctx, application.SaveChatAnalysisDecisionParams{ChatWorkItemID: chat.ID, ExpectedVersion: analysis.Projection.Version, Revision: analysis.Projection.Revision, ItemID: "scope", Outcome: domain.ChatAnalysisDecisionConfirmed, Conclusion: "确认范围", Basis: "产品沟通", ProductVersion: "v1", ClientKey: "decision-scope", ActorID: "user_demo"})
	if err != nil {
		t.Fatal(err)
	}
	draft, _, err := svc.CreatePublicationDraft(ctx, application.CreatePublicationDraftParams{ChatWorkItemID: chat.ID, ExpectedVersion: decision.Projection.Version, Revision: decision.Projection.Revision, ItemIDs: []string{"scope"}, Title: "Java 能力", ClientKey: "draft-1"})
	if err != nil || draft.Draft.Status != domain.PublicationDraftReady {
		t.Fatalf("draft=%+v err=%v", draft, err)
	}
	if _, _, err := svc.CreatePublicationDraft(ctx, application.CreatePublicationDraftParams{ChatWorkItemID: chat.ID, ExpectedVersion: decision.Projection.Version - 1, Revision: decision.Projection.Revision, ItemIDs: []string{"scope"}, Title: "old version", ClientKey: "draft-old"}); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("stale analysis expected_version must conflict: %v", err)
	}
	if _, _, err := svc.CreatePublicationDraft(ctx, application.CreatePublicationDraftParams{ChatWorkItemID: chat.ID, ExpectedVersion: decision.Projection.Version, Revision: decision.Projection.Revision, ItemIDs: []string{"scope"}, Title: "different", ClientKey: "draft-1"}); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("same draft key with different payload must conflict: %v", err)
	}
	baseline = testPublicationBaseline("head-2", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if _, err := svc.PublishPublicationDraft(ctx, chat.ID, draft.Draft.ID, draft.Draft.Version, "publish-1"); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("baseline drift must block publish: %v", err)
	}
	baseline = testPublicationBaseline("head-1", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	task, err := svc.PublishPublicationDraft(ctx, chat.ID, draft.Draft.ID, draft.Draft.Version, "publish-1")
	if err != nil {
		t.Fatal(err)
	}
	if task.RecordKind != domain.RecordKindTask || task.WorkspaceID != base.WorkspaceID {
		t.Fatalf("published task scope=%+v", task)
	}
	contextRow, err := store.WorkItemContexts().Get(ctx, task.ID)
	if err != nil || contextRow.BaseRevision != baseline.Head {
		t.Fatalf("published Task context must freeze captured baseline HEAD: context=%+v err=%v", contextRow, err)
	}
	if replay, err := svc.PublishPublicationDraft(ctx, chat.ID, draft.Draft.ID, draft.Draft.Version, "publish-different-key"); !errors.Is(err, domain.ErrIdempotencyConflict) || replay != nil {
		t.Fatalf("published draft with different publish key must conflict: replay=%+v err=%v", replay, err)
	}
	replay, err := svc.PublishPublicationDraft(ctx, chat.ID, draft.Draft.ID, draft.Draft.Version, "publish-1")
	if err != nil || replay.ID != task.ID {
		t.Fatalf("same publish key must replay same Task: replay=%+v task=%+v err=%v", replay, task, err)
	}
	if _, err := svc.PublishPublicationDraft(ctx, chat.ID, draft.Draft.ID, draft.Draft.Version+1, "publish-1"); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("same publish key with changed expected_version must conflict: %v", err)
	}
}

func TestPublicationFirstCoordinatorRunBaselineDriftDoesNotDispatch(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	dispatcher := &captureDispatcher{}
	store := sqlstore.New(db)
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())
	base := seedRunEnv(t, ctx, svc, store)
	chat, err := svc.CreateWorkItem(ctx, base.WorkspaceID, application.CreateWorkItemParams{Title: "first run gate", RecordKind: domain.RecordKindChat, AgentProfileID: base.AgentProfileID})
	if err != nil {
		t.Fatal(err)
	}
	baseline1 := testPublicationBaseline("head-gate-1", "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	baseline2 := testPublicationBaseline("head-gate-2", "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
	calls := 0
	svc.SetPublicationBaselineResolver(func(context.Context, *domain.ExecutionContextSnapshot) (domain.ProjectBaseline, error) {
		calls++
		if calls == 4 {
			return baseline2, nil
		}
		return baseline1, nil
	})
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
	analysis, err := svc.GetChatAnalysis(ctx, chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	decision, _, err := svc.SaveChatAnalysisDecision(ctx, application.SaveChatAnalysisDecisionParams{ChatWorkItemID: chat.ID, ExpectedVersion: analysis.Projection.Version, Revision: analysis.Projection.Revision, ItemID: "scope", Outcome: domain.ChatAnalysisDecisionConfirmed, Conclusion: "范围", Basis: "产品", ProductVersion: "v1", ClientKey: "gate-decision", ActorID: "user_demo"})
	if err != nil {
		t.Fatal(err)
	}
	draft, _, err := svc.CreatePublicationDraft(ctx, application.CreatePublicationDraftParams{ChatWorkItemID: chat.ID, ExpectedVersion: decision.Projection.Version, Revision: decision.Projection.Revision, ItemIDs: []string{"scope"}, Title: "gate", ClientKey: "gate-draft"})
	if err != nil {
		t.Fatal(err)
	}
	dispatchCountBeforePublish := len(dispatcher.runs)
	task, err := svc.PublishPublicationDraft(ctx, chat.ID, draft.Draft.ID, draft.Draft.Version, "gate-publish")
	if err != nil || task == nil {
		t.Fatalf("publish should retain durable Task when first Run gate drifts: task=%+v err=%v", task, err)
	}
	if len(dispatcher.runs) != dispatchCountBeforePublish {
		t.Fatalf("baseline drift before first Coordinator Run must not dispatch: calls=%d runs=%+v", calls, dispatcher.runs)
	}
}

package application_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/knowledge"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

func finishKnowledgeTestRun(t *testing.T, svc *application.Service, runID, text string) {
	t.Helper()
	ctx := context.Background()
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, runID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.RecordRunEvent(ctx, runID, domain.EventMessageCompleted, map[string]any{"role": "assistant", "text": text}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, runID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestKnowledgeTaskCaptureCurationPublicationRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())
	ws, manager, worker := seedM2Env(t, ctx, store)
	if _, err := svc.ConfigureKnowledgeLibrarian(ctx, ws, domain.KnowledgeLibrarianConfig{LibrarianAgentID: manager, Enabled: true, AutoCollect: true}); err != nil {
		t.Fatal(err)
	}
	root, err := svc.CreateWorkItem(ctx, ws, application.CreateWorkItemParams{Title: "知识写入闭环"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := svc.SubmitPlan(ctx, ws, application.SubmitPlanParams{WorkItemID: root.ID, AgentProfileID: manager, Steps: []application.PlanStepInput{dispatchStep(worker, "功能A任务", "完成A并提交新知识")}})
	if err != nil {
		t.Fatal(err)
	}
	workerRun := plan.Steps[0].ResultRunID
	source := domain.KnowledgeSourceInput{Kind: domain.KnowledgeSourceDocument, Ref: "specification@v1", Locator: "A-B规则", Excerpt: "功能A依赖功能B；B提供必要的后续处理。"}
	changes := []domain.KnowledgeChange{
		{Title: "功能A", Kind: "fact", Body: "功能A依赖下游功能B。", Sources: []domain.KnowledgeSourceInput{source}, Relations: []domain.KnowledgeRelationInput{{ToItemID: "@change:1", Kind: domain.KnowledgeRelationDependsOn, Condition: "处理A时", Rationale: "同一份规格明确要求B"}}},
		{Title: "功能B", Kind: "fact", Body: "提供必要的后续处理。", Sources: []domain.KnowledgeSourceInput{source}},
	}
	raw, _ := json.Marshal(map[string]any{"changes": changes})
	finishKnowledgeTestRun(t, svc, workerRun, "任务完成。\n```knowledge-submission/v1\n"+string(raw)+"\n```")
	pending, err := store.Knowledge().ListPendingRunCaptures(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("terminal capture not atomic: %+v %v", pending, err)
	}
	if _, err = svc.DrainKnowledgeCaptures(ctx); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.KnowledgeJobs().List(ctx, ws, "", "", 20)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("auto curation not dispatched: %+v %v", jobs, err)
	}
	job := jobs[0]
	if job.Mode != domain.KnowledgeJobCuration || job.SubmissionID == "" {
		t.Fatalf("curation lost source receipt: %+v", job)
	}
	origin := job.SubmissionID
	finishKnowledgeTestRun(t, svc, job.CurrentRunID, `{"schema_version":"knowledge-librarian/v1","action":"search","search":{"terms":["功能A"]}}`)
	job, err = svc.GetKnowledgeJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Used.Searches != 1 || job.TurnSeq != 2 {
		t.Fatalf("curator did not check existing library: %+v", job)
	}
	// Native models may deliver the relation only at finish level. It must
	// still become a versioned relation in the published library.
	curatedChanges := append([]domain.KnowledgeChange(nil), changes...)
	curatedChanges[0].Relations = nil
	finishedRelation := changes[0].Relations[0]
	finishedRelation.FromItemID = "@change:0"
	decision, _ := json.Marshal(map[string]any{"schema_version": "knowledge-librarian/v1", "action": "finish", "finish": map[string]any{"status": "partial", "summary": "已整理A与B，未授权外部源码调查。", "gaps": []string{"仅核实已提交规格，未检查外部代码"}, "revised_changes": curatedChanges, "relations": []domain.KnowledgeRelationInput{finishedRelation}}})
	finishKnowledgeTestRun(t, svc, job.CurrentRunID, string(decision))
	job, err = svc.GetKnowledgeJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	curatedID, _ := job.Result["submission_id"].(string)
	if curatedID == "" || curatedID == origin {
		events, _ := store.Events().ListRunEventsIncludeInternal(ctx, job.CurrentRunID)
		for _, event := range events {
			if event.EventType == "run.phase_closed" {
				t.Logf("terminal hook: %+v", event.Payload)
			}
		}
		t.Fatalf("curation failed to preserve original and prepare revision: status=%s error=%s result=%+v", job.Status, job.LastError, job.Result)
	}
	curated, err := svc.GetKnowledgeSubmission(ctx, ws, manager, curatedID)
	if err != nil {
		t.Fatal(err)
	}
	if curated.Status != domain.KnowledgeSubmissionNeedsReview || len(curated.ResultVersionIDs) != 2 {
		t.Fatalf("missing reviewable prepared versions: %+v", curated)
	}
	if _, err = svc.PublishKnowledgeSubmission(ctx, ws, manager, curatedID); err != nil {
		t.Fatal(err)
	}
	snapshot, err := knowledge.NewEngine(store.Knowledge()).Query(ctx, &domain.KnowledgeQuery{WorkspaceID: ws, RequesterAgentID: worker, Question: "功能A", Terms: []string{"功能A"}})
	if err != nil {
		t.Fatal(err)
	}
	foundB := false
	for _, hit := range snapshot.Results {
		if hit.Version.Title == "功能B" {
			foundB = true
			if len(hit.Sources) == 0 || len(hit.Relations) == 0 {
				t.Fatal("B lost evidence or relationship")
			}
		}
	}
	if !foundB {
		t.Fatalf("published A did not recall related B: %+v", snapshot)
	}
	before := len(dispatcher.runs)
	if _, err = svc.DrainKnowledgeCaptures(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.RecoverKnowledgeLibrarianJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.runs) != before {
		t.Fatal("librarian output triggered another ingestion loop")
	}
	if _, err = svc.PublishKnowledgeSubmission(ctx, ws, manager, curatedID); err != nil {
		t.Fatalf("publication replay failed: %v", err)
	}
	noChange := domain.KnowledgeSubmitCandidate{WorkspaceID: ws, AgentID: worker, ClientKey: "explicit-no-change", NoChange: true}
	ack, err := svc.SubmitKnowledgeCandidate(ctx, noChange)
	if err != nil || ack.Status != domain.KnowledgeSubmissionMerged {
		t.Fatalf("no_change must close without curation: %+v %v", ack, err)
	}
	replay, err := svc.SubmitKnowledgeCandidate(ctx, noChange)
	if err != nil || replay.ID != ack.ID || replay.Status != domain.KnowledgeSubmissionMerged {
		t.Fatalf("no_change replay changed receipt: %+v %v", replay, err)
	}
}

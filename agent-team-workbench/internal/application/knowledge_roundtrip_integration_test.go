package application_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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
	librarian, err := svc.EnsureBuiltinKnowledgeLibrarian(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateAgent(ctx, librarian.ID, application.AgentPatch{
		RuntimePreference: &domain.RuntimePreference{Preferred: "codex_local"}, ExpectedVersion: librarian.Version,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfigureKnowledgeLibrarian(ctx, ws, domain.KnowledgeLibrarianConfig{LibrarianAgentID: librarian.ID, Enabled: true, AutoCollect: true}); err != nil {
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
	curated, err := svc.GetKnowledgeSubmission(ctx, ws, librarian.ID, curatedID)
	if err != nil {
		t.Fatal(err)
	}
	if curated.Status != domain.KnowledgeSubmissionNeedsReview || len(curated.ResultVersionIDs) != 2 {
		t.Fatalf("missing reviewable prepared versions: %+v", curated)
	}
	if _, err = svc.PublishKnowledgeSubmission(ctx, ws, librarian.ID, curatedID); err != nil {
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
	if _, err = svc.PublishKnowledgeSubmission(ctx, ws, librarian.ID, curatedID); err != nil {
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

func TestKnowledgeAgentRecordPartialCurationCreatesReviewableCandidate(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())
	ws, legacyManager, producer := seedM2Env(t, ctx, store)
	if err := store.KnowledgeJobs().CreateConfig(ctx, &domain.KnowledgeLibrarianConfig{WorkspaceID: ws, LibrarianAgentID: legacyManager, Enabled: true, Version: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	builtin, err := svc.EnsureBuiltinKnowledgeLibrarian(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	manager := builtin.ID
	if _, err := svc.UpdateAgent(ctx, manager, application.AgentPatch{
		RuntimePreference: &domain.RuntimePreference{Preferred: "mock"}, ExpectedVersion: builtin.Version,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetKnowledgeLibrarianConfig(ctx, ws); err != nil {
		t.Fatal(err)
	}
	callerItem, err := svc.CreateWorkItem(ctx, ws, application.CreateWorkItemParams{Title: "Agent knowledge record", AgentProfileID: producer})
	if err != nil {
		t.Fatal(err)
	}
	sourceRun, err := svc.CreateRun(ctx, callerItem.ID, application.CreateRunParams{AgentProfileID: producer, Instruction: "record knowledge"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, sourceRun.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, sourceRun.ID, domain.RunRunning, nil); err != nil {
		t.Fatal(err)
	}
	intake, err := svc.SubmitKnowledgeAgentRecord(ctx, application.KnowledgeAgentRecordParams{
		RunID: sourceRun.ID, Content: "用户已确认退款必须附订单号。", Title: "退款约定",
		ClientKey: "agent-record-partial",
	})
	if err != nil {
		t.Fatalf("submit Agent record: %v", err)
	}
	if intake.Job == nil || len(dispatcher.runs) != 2 {
		t.Fatalf("Agent record did not start exactly one librarian Run: %+v runs=%d", intake, len(dispatcher.runs))
	}
	job := intake.Job
	if job.RequestingAgentID != producer || job.AgentProfileID != manager {
		t.Fatalf("record result scope leaked librarian identity or lost producer scope: %+v", job)
	}
	if len(job.EvidenceIDs) != 1 {
		t.Fatalf("Agent record source seed missing: %+v", job)
	}
	decision := `{"schema_version":"knowledge-librarian/v1","action":"finish","finish":{"status":"partial","summary":"根据 Agent 提供的确认内容形成待复核候选","coverage":[{"subject":"rules","status":"complete","evidence_ids":["` + job.EvidenceIDs[0] + `"]}],"evidence_ids":["` + job.EvidenceIDs[0] + `"],"gaps":["未提供上下游依赖、共享约束和历史核验材料"],"revised_changes":[{"base_version":0,"title":"退款约定","body":"用户已确认退款必须附订单号。","kind":"requirement","sources":[{"kind":"agent","ref":"` + producer + `","excerpt":"用户已确认退款必须附订单号。","metadata":{"origin_agent_id":"model-spoof","origin_run_id":"model-spoof","record_intent":"confirmed_requirement"}}]}]}}`
	finishKnowledgeTestRun(t, svc, job.CurrentRunID, decision)
	updated, err := svc.GetKnowledgeJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domain.KnowledgeJobIncomplete {
		t.Fatalf("partial Agent record was falsely completed: %+v", updated)
	}
	curatedID, _ := updated.Result["curated_submission_id"].(string)
	if curatedID == "" {
		t.Fatalf("partial Agent record did not produce a reviewable curation submission: %+v", updated.Result)
	}
	curated, err := svc.GetKnowledgeSubmission(ctx, ws, manager, curatedID)
	if err != nil {
		t.Fatal(err)
	}
	if curated.Status != domain.KnowledgeSubmissionNeedsReview || len(curated.ResultVersionIDs) != 1 {
		t.Fatalf("partial Agent record candidate bypassed review state: %+v", curated)
	}
	version, err := store.Knowledge().GetVersion(ctx, ws, manager, curated.ResultVersionIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if version.Status != domain.KnowledgeStatusDraft || version.Kind != "requirement" || version.BodyMarkdown != "用户已确认退款必须附订单号。" {
		t.Fatalf("curated Agent record was not preserved as a draft requirement: %+v", version)
	}
	sources, err := store.Knowledge().ListVersionSources(ctx, ws, manager, version.ID)
	if err != nil || len(sources) != 1 || sources[0].Kind != domain.KnowledgeSourceAgent || sources[0].Ref != producer || sources[0].Metadata["origin_agent_id"] != producer || sources[0].Metadata["origin_run_id"] != sourceRun.ID || sources[0].Metadata["record_intent"] != application.KnowledgeRecordIntentAgentRecord {
		t.Fatalf("Agent record provenance was lost: sources=%+v err=%v", sources, err)
	}
}

func TestKnowledgeAgentRecordConfirmedRequirementAutoPublishes(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())
	ws, legacyManager, producer := seedM2Env(t, ctx, store)
	if err := store.KnowledgeJobs().CreateConfig(ctx, &domain.KnowledgeLibrarianConfig{WorkspaceID: ws, LibrarianAgentID: legacyManager, Enabled: true, Version: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	builtin, err := svc.EnsureBuiltinKnowledgeLibrarian(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	manager := builtin.ID
	if _, err := svc.UpdateAgent(ctx, manager, application.AgentPatch{
		RuntimePreference: &domain.RuntimePreference{Preferred: "mock"}, ExpectedVersion: builtin.Version,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetKnowledgeLibrarianConfig(ctx, ws); err != nil {
		t.Fatal(err)
	}
	callerItem, err := svc.CreateWorkItem(ctx, ws, application.CreateWorkItemParams{Title: "Confirmed requirement", AgentProfileID: producer})
	if err != nil {
		t.Fatal(err)
	}
	sourceRun, err := svc.CreateRun(ctx, callerItem.ID, application.CreateRunParams{AgentProfileID: producer, Instruction: "record confirmed requirement"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, sourceRun.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, sourceRun.ID, domain.RunRunning, nil); err != nil {
		t.Fatal(err)
	}
	intake, err := svc.SubmitKnowledgeAgentRecord(ctx, application.KnowledgeAgentRecordParams{
		RunID: sourceRun.ID, Content: "用户确认退款必须附订单号。", Title: "退款约定",
		PublishIntent: application.KnowledgeRecordPublishIntentConfirmedRequirement,
		ClientKey:     "agent-record-confirmed",
	})
	if err != nil || intake.Job == nil {
		t.Fatalf("submit confirmed Agent record: result=%+v err=%v", intake, err)
	}
	job := intake.Job
	decision := `{"schema_version":"knowledge-librarian/v1","action":"finish","finish":{"status":"partial","summary":"用户已确认的退款约定","coverage":[{"subject":"rules","status":"complete","evidence_ids":["` + job.EvidenceIDs[0] + `"]}],"evidence_ids":["` + job.EvidenceIDs[0] + `"],"gaps":["未提供实现验证材料"],"revised_changes":[{"base_version":0,"title":"退款约定","body":"退款必须附订单号。","kind":"requirement","sources":[{"kind":"agent","ref":"` + producer + `","excerpt":"用户确认退款必须附订单号。"}]}]}}`
	finishKnowledgeTestRun(t, svc, job.CurrentRunID, decision)
	updated, err := svc.GetKnowledgeJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domain.KnowledgeJobIncomplete || updated.Result["publication_status"] != "published" {
		t.Fatalf("confirmed requirement did not publish through controlled path: %+v", updated)
	}
	curatedID, _ := updated.Result["published_submission_id"].(string)
	if curatedID == "" {
		t.Fatalf("published submission identity missing: %+v", updated.Result)
	}
	curated, err := svc.GetKnowledgeSubmission(ctx, ws, manager, curatedID)
	if err != nil || curated.Status != domain.KnowledgeSubmissionAccepted {
		t.Fatalf("confirmed requirement was not accepted: submission=%+v err=%v", curated, err)
	}
	if len(updated.Result["published_version_ids"].([]any)) != 1 {
		t.Fatalf("published version references missing: %+v", updated.Result)
	}
	query, err := knowledge.NewEngine(store.Knowledge()).Query(ctx, &domain.KnowledgeQuery{
		WorkspaceID: ws, RequesterAgentID: producer, Question: "退款约定", Terms: []string{"退款", "订单号"},
	})
	if err != nil || len(query.Results) != 1 || query.Results[0].Version.Status != domain.KnowledgeStatusEffective {
		t.Fatalf("published requirement is not visible to the originating Agent: query=%+v err=%v", query, err)
	}
	sources := query.Results[0].Sources
	if len(sources) != 1 || sources[0].Kind != domain.KnowledgeSourceAgent || sources[0].Metadata["verification"] != "agent_identity_only" {
		t.Fatalf("published requirement lost non-implementation evidence status: %+v", sources)
	}
	updated.Result["publication_status"] = "pending"
	if err := store.KnowledgeJobs().Update(ctx, updated, updated.Version); err != nil {
		t.Fatal(err)
	}
	acted, err := svc.RecoverKnowledgeLibrarianJobs(ctx)
	if err != nil || acted == 0 {
		t.Fatalf("terminal pending publication was not finalized by recovery: acted=%d err=%v", acted, err)
	}
	recovered, err := svc.GetKnowledgeJob(ctx, job.ID)
	if err != nil || recovered.Result["publication_status"] != "published" {
		t.Fatalf("terminal pending publication remained unresolved: job=%+v err=%v", recovered, err)
	}
}

func TestKnowledgeAgentRecordObservationAutoPublishesWithUnverifiedEvidence(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())
	ws, _, producer := seedM2Env(t, ctx, store)
	librarian, err := svc.EnsureBuiltinKnowledgeLibrarian(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateAgent(ctx, librarian.ID, application.AgentPatch{
		RuntimePreference: &domain.RuntimePreference{Preferred: "codex_local"}, ExpectedVersion: librarian.Version,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfigureKnowledgeLibrarian(ctx, ws, domain.KnowledgeLibrarianConfig{LibrarianAgentID: librarian.ID, Enabled: true, Version: 0}); err != nil {
		t.Fatal(err)
	}
	callerItem, err := svc.CreateWorkItem(ctx, ws, application.CreateWorkItemParams{Title: "Agent observation", AgentProfileID: producer})
	if err != nil {
		t.Fatal(err)
	}
	sourceRun, err := svc.CreateRun(ctx, callerItem.ID, application.CreateRunParams{AgentProfileID: producer, Instruction: "record an observation"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, sourceRun.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, sourceRun.ID, domain.RunRunning, nil); err != nil {
		t.Fatal(err)
	}
	intake, err := svc.SubmitKnowledgeAgentRecord(ctx, application.KnowledgeAgentRecordParams{
		RunID: sourceRun.ID, Content: "开发 Agent 观察到退款页面需要订单号。", Title: "退款页面观察", ClientKey: "agent-observation",
	})
	if err != nil || intake.Job == nil {
		t.Fatalf("submit observation record: result=%+v err=%v", intake, err)
	}
	job := intake.Job
	decision := `{"schema_version":"knowledge-librarian/v1","action":"finish","finish":{"status":"partial","summary":"保留 Agent 观察","coverage":[{"subject":"rules","status":"complete","evidence_ids":["` + job.EvidenceIDs[0] + `"]}],"evidence_ids":["` + job.EvidenceIDs[0] + `"],"gaps":["尚未进行实现验证"],"revised_changes":[{"base_version":0,"title":"退款页面观察","body":"退款页面需要订单号。","kind":"observation","sources":[{"kind":"agent","ref":"` + producer + `","excerpt":"开发 Agent 观察到退款页面需要订单号。"}]}]}}`
	finishKnowledgeTestRun(t, svc, job.CurrentRunID, decision)
	updated, err := svc.GetKnowledgeJob(ctx, job.ID)
	if err != nil || updated.Result["publication_status"] != "published" {
		t.Fatalf("ordinary Agent record was not auto-published: job=%+v err=%v", updated, err)
	}
	publishedID, _ := updated.Result["published_submission_id"].(string)
	if publishedID == "" {
		t.Fatalf("ordinary Agent record publication receipt missing: %+v", updated.Result)
	}
	query, err := knowledge.NewEngine(store.Knowledge()).Query(ctx, &domain.KnowledgeQuery{
		WorkspaceID: ws, RequesterAgentID: producer, Question: "退款页面", Terms: []string{"退款", "订单号"},
	})
	if err != nil || len(query.Results) != 1 || query.Results[0].Version.Status != domain.KnowledgeStatusEffective || query.Results[0].Version.Kind != "observation" {
		t.Fatalf("ordinary Agent observation is not visible as effective unverified knowledge: query=%+v err=%v", query, err)
	}
	if len(query.Results[0].Sources) != 1 || query.Results[0].Sources[0].Metadata["verification"] != "agent_identity_only" {
		t.Fatalf("ordinary Agent observation lost unverified provenance: %+v", query.Results[0].Sources)
	}
}

func TestKnowledgeAgentRecordConfirmedNoChangeReturnsFinalReceipt(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())
	ws, _, producer := seedM2Env(t, ctx, store)
	librarian, err := svc.EnsureBuiltinKnowledgeLibrarian(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateAgent(ctx, librarian.ID, application.AgentPatch{
		RuntimePreference: &domain.RuntimePreference{Preferred: "codex_local"}, ExpectedVersion: librarian.Version,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfigureKnowledgeLibrarian(ctx, ws, domain.KnowledgeLibrarianConfig{LibrarianAgentID: librarian.ID, Enabled: true, Version: 0}); err != nil {
		t.Fatal(err)
	}
	callerItem, err := svc.CreateWorkItem(ctx, ws, application.CreateWorkItemParams{Title: "Confirmed no change", AgentProfileID: producer})
	if err != nil {
		t.Fatal(err)
	}
	sourceRun, err := svc.CreateRun(ctx, callerItem.ID, application.CreateRunParams{AgentProfileID: producer, Instruction: "record confirmed no change"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, sourceRun.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, sourceRun.ID, domain.RunRunning, nil); err != nil {
		t.Fatal(err)
	}
	params := application.KnowledgeAgentRecordParams{
		RunID: sourceRun.ID, Content: "用户确认现有退款规则无需变化。", Title: "无变化确认",
		PublishIntent: application.KnowledgeRecordPublishIntentConfirmedRequirement, ClientKey: "agent-confirmed-no-change",
	}
	intake, err := svc.SubmitKnowledgeAgentRecord(ctx, params)
	if err != nil || intake.Job == nil {
		t.Fatalf("submit confirmed no-change record: result=%+v err=%v", intake, err)
	}
	job := intake.Job
	evidence := job.EvidenceIDs[0]
	coverage := make([]map[string]any, 0, 6)
	for _, subject := range []string{"function", "rules", "upstream_dependencies", "downstream_impacts", "shared_constraints", "history_validation"} {
		coverage = append(coverage, map[string]any{"subject": subject, "status": "complete", "evidence_ids": []string{evidence}})
	}
	decisionBytes, _ := json.Marshal(map[string]any{
		"schema_version": "knowledge-librarian/v1", "action": "finish",
		"finish": map[string]any{"status": "complete", "summary": "已确认无需变化", "coverage": coverage, "evidence_ids": []string{evidence}, "no_change": true},
	})
	finishKnowledgeTestRun(t, svc, job.CurrentRunID, string(decisionBytes))
	updated, err := svc.GetKnowledgeJob(ctx, job.ID)
	if err != nil || updated.Result["publication_status"] != "unchanged" {
		t.Fatalf("confirmed no-change did not produce unchanged receipt: job=%+v err=%v", updated, err)
	}
	replay, err := svc.SubmitKnowledgeAgentRecord(ctx, params)
	if err != nil || replay.PublicationStatus != "unchanged" || replay.Job == nil || replay.Job.Status != domain.KnowledgeJobCompleted {
		t.Fatalf("record replay still reported pending after no-change completion: result=%+v err=%v", replay, err)
	}
}

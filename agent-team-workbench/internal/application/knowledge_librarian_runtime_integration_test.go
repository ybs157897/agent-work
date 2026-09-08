package application_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

func TestKnowledgeInquirySearchDecisionAdvancesBoundedRun(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_knowledge_runtime", Name: "knowledge runtime", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	seedCtx(t, store, ctx, ws.ID)
	requester := &domain.AgentProfile{ID: "agent_knowledge_requester", WorkspaceID: ws.ID, Name: "requester", Role: "worker", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Agents().Create(ctx, requester); err != nil {
		t.Fatal(err)
	}
	if err := store.Bindings().Create(ctx, &domain.RuntimeBinding{ID: "binding_knowledge_mock", WorkspaceID: ws.ID, RuntimeLabel: "mock", AdapterID: "mock", Capabilities: map[string]string{"resume": "supported"}, Status: domain.BindingReady, Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	librarian, err := svc.EnsureBuiltinKnowledgeLibrarian(ctx, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateAgent(ctx, librarian.ID, application.AgentPatch{
		RuntimePreference: &domain.RuntimePreference{Preferred: "mock"}, ExpectedVersion: librarian.Version,
	}); err != nil {
		t.Fatal(err)
	}
	item := &domain.KnowledgeItem{ID: domain.NewID(domain.PrefixKnowledgeItem), WorkspaceID: ws.ID, Visibility: domain.KnowledgeVisibilityWorkspace, Kind: "fact", Title: "runtime fact", Version: 1, Status: domain.KnowledgeStatusCandidate, CreatedAt: now, UpdatedAt: now}
	if err := store.Knowledge().CreateItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	version := &domain.KnowledgeVersion{ID: domain.NewID(domain.PrefixKnowledgeVersion), ItemID: item.ID, BaseVersion: 0, Status: domain.KnowledgeStatusCandidate, Kind: "fact", Title: item.Title, BodyMarkdown: "the runtime fact", CreatedByAgentID: requester.ID, CreatedAt: now}
	source := &domain.KnowledgeSource{ID: domain.NewID(domain.PrefixKnowledgeSource), WorkspaceID: ws.ID, SubmittedByAgentID: requester.ID, Kind: domain.KnowledgeSourceCode, Ref: "runtime.go", Excerpt: "the runtime fact", CreatedAt: now}
	if err := store.Knowledge().CreateVersionBundle(ctx, version, []*domain.KnowledgeSource{source}, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().PublishVersion(ctx, item.ID, version.ID, 0, now); err != nil {
		t.Fatal(err)
	}
	job, err := svc.StartKnowledgeInquiry(ctx, application.StartKnowledgeInquiryParams{WorkspaceID: ws.ID, RequestingAgentID: requester.ID, Question: "runtime fact", ClientKey: "runtime-inquiry", Budget: domain.KnowledgeJobBudget{MaxTurns: 3}})
	if err != nil {
		t.Fatal(err)
	}
	if job.CurrentRunID == "" || len(dispatcher.runs) != 1 {
		t.Fatalf("initial librarian Run missing: job=%+v dispatch=%d", job, len(dispatcher.runs))
	}
	first := job.CurrentRunID
	firstRun := dispatcher.runs[0]
	if prompt, _ := firstRun.Input["system_prompt"].(string); prompt == "" || !containsKnowledgePromptMarker(prompt) {
		t.Fatalf("librarian Run missing per-Run protocol system prompt: %q", prompt)
	}
	if prompt, _ := firstRun.Input["system_prompt"].(string); !strings.Contains(prompt, "KNOWLEDGE_LIBRARIAN_SCHEMA_V1_LENGTH:") || !strings.Contains(prompt, `"conflicts_with"`) || !strings.Contains(prompt, `"action":"search"`) || !strings.Contains(prompt, "contiguous literal substring") || !strings.Contains(prompt, "reviewable revised_changes candidate") {
		t.Fatalf("librarian prompt missing canonical schema/complete action example: %q", prompt)
	}
	policy, _ := firstRun.Input["policy"].(map[string]any)
	if policy["sandbox"] != "read-only" {
		t.Fatalf("librarian Run sandbox policy = %#v, want read-only", policy["sandbox"])
	}
	decision := `{"schema_version":"knowledge-librarian/v1","action":"search","search":{"question":"runtime fact","item_ids":["` + item.ID + `"]}}`
	if err := svc.RecordRunStatus(ctx, first, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, first, domain.RunRunning, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunEvent(ctx, first, domain.EventMessageCompleted, map[string]any{"role": "assistant", "text": decision}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, first, domain.RunSucceeding, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, first, domain.RunSucceeded, nil); err != nil {
		t.Fatal(err)
	}
	updated, err := svc.GetKnowledgeJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domain.KnowledgeJobRunning || updated.TurnSeq != 2 || updated.CurrentRunID == first {
		t.Fatalf("search decision did not create next bounded turn: %+v", updated)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("search decision dispatched %d Runs, want exactly 2", len(dispatcher.runs))
	}
	if updated.Used.Searches != 1 || len(updated.EvidenceIDs) != 1 || updated.Coverage.VisitedNodes != 1 || updated.Coverage.VisitedRelations != 0 {
		t.Fatalf("search observation/usage not persisted: %+v", updated)
	}
	if len(updated.SnapshotIDs) != 1 {
		t.Fatalf("search did not persist an immutable query snapshot: %+v", updated)
	}
	snapshot, err := store.Knowledge().GetQuerySnapshot(ctx, ws.ID, requester.ID, updated.SnapshotIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.IndexRevision != updated.IndexRevision || snapshot.Scope == nil || len(snapshot.Results) != 1 || snapshot.Results[0].Version.ID != version.ID {
		t.Fatalf("query snapshot lost scope/version/revision: %+v", snapshot)
	}
	if marker, ok := updatedResultMarker(dispatcher.runs[1]); !ok || marker != updated.ID {
		t.Fatalf("continuation is not linked to job: marker=%q ok=%t run=%+v", marker, ok, dispatcher.runs[1].Input)
	}
	// A publication between turns changes the workspace index revision. The
	// next terminal decision must stop with an explicit incomplete result
	// instead of combining the old search snapshot with the new head.
	newVersion := &domain.KnowledgeVersion{ID: domain.NewID(domain.PrefixKnowledgeVersion), ItemID: item.ID,
		BaseVersion: 1, Status: domain.KnowledgeStatusCandidate, Kind: "fact", Title: item.Title,
		BodyMarkdown: "the revised runtime fact", CreatedByAgentID: requester.ID, CreatedAt: now}
	if err := store.Knowledge().CreateVersion(ctx, newVersion); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().PublishVersion(ctx, item.ID, newVersion.ID, 1, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	terminalizeKnowledgeRun(t, ctx, svc, dispatcher.runs[1].ID,
		`{"schema_version":"knowledge-librarian/v1","action":"read","read":{"item_ids":["`+item.ID+`"]}}`)
	updated, err = svc.GetKnowledgeJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domain.KnowledgeJobIncomplete || !strings.Contains(updated.LastError, "索引版本") {
		t.Fatalf("index drift between turns was not fenced: %+v", updated)
	}
}

func TestKnowledgeInquiryUsesProvisionedBuiltinLibrarian(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())
	ws, _, requester := seedM2Env(t, ctx, store)
	now := time.Now().UTC()
	if err := store.Bindings().Create(ctx, &domain.RuntimeBinding{
		ID: "rb_m2_builtin_mock", WorkspaceID: ws, RuntimeLabel: "mock", AdapterID: "mock",
		Provider: "mock", Model: "mock", Status: domain.BindingReady,
		Capabilities: map[string]string{"resume": "supported"}, Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	librarian, err := svc.EnsureBuiltinKnowledgeLibrarian(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateAgent(ctx, librarian.ID, application.AgentPatch{
		RuntimePreference: &domain.RuntimePreference{Preferred: "mock"}, ExpectedVersion: librarian.Version,
	}); err != nil {
		t.Fatal(err)
	}
	job, err := svc.StartKnowledgeInquiry(ctx, application.StartKnowledgeInquiryParams{
		WorkspaceID: ws, RequestingAgentID: requester, Question: "builtin librarian inquiry", ClientKey: "builtin-inquiry",
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.AgentProfileID != librarian.ID || job.RequestingAgentID != requester {
		t.Fatalf("knowledge inquiry did not separate builtin executor from requester scope: %+v", job)
	}
	if len(dispatcher.runs) != 1 || dispatcher.runs[0].AgentProfileID != librarian.ID {
		t.Fatalf("knowledge inquiry was not dispatched to builtin librarian: %+v", dispatcher.runs)
	}
	prompt, _ := dispatcher.runs[0].Input["system_prompt"].(string)
	if !strings.Contains(prompt, "knowledge-librarian/v1") || prompt == application.KnowledgeLibrarianChatPrompt {
		t.Fatalf("builtin curation Run did not use the private protocol prompt: %q", prompt)
	}
	if !strings.Contains(prompt, "In inquiry mode, never include revised_changes or no_change") {
		t.Fatalf("inquiry prompt did not forbid curation-only finish fields: %q", prompt)
	}
}

func TestKnowledgeInquiryRepairsCurationFieldsBeforeApplyingDecision(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())
	ws, _, requester := seedM2Env(t, ctx, store)
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
	job, err := svc.StartKnowledgeInquiry(ctx, application.StartKnowledgeInquiryParams{
		WorkspaceID: ws, RequestingAgentID: requester, Question: "repair inquiry", ClientKey: "repair-inquiry",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstRunID := job.CurrentRunID
	terminalizeKnowledgeRun(t, ctx, svc, firstRunID,
		`{"schema_version":"knowledge-librarian/v1","action":"finish","finish":{"status":"complete","answer":"已有证据","no_change":true}}`)
	updated, err := svc.GetKnowledgeJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domain.KnowledgeJobRunning || updated.TurnSeq != 2 || updated.CurrentRunID == firstRunID {
		t.Fatalf("inquiry curation fields were not repaired into a bounded continuation: %+v", updated)
	}
	if _, err := store.KnowledgeJobs().GetAction(ctx, job.ID, 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("invalid inquiry finish must not create an applied action: %v", err)
	}
	continuation, err := store.Runs().Get(ctx, updated.CurrentRunID)
	if err != nil {
		t.Fatal(err)
	}
	if prompt, _ := continuation.Input["instruction"].(string); !strings.Contains(prompt, "inquiry finish cannot contain curation changes") {
		t.Fatalf("repair continuation did not explain the curation-only field violation: %q", prompt)
	}

	terminalizeKnowledgeRun(t, ctx, svc, updated.CurrentRunID,
		`{"schema_version":"knowledge-librarian/v1","action":"finish","finish":{"status":"missing","answer":"证据不足","gaps":["缺少完成调查所需证据"]}}`)
	finished, err := svc.GetKnowledgeJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != domain.KnowledgeJobIncomplete {
		t.Fatalf("legal inquiry finish did not terminate the bounded job: %+v", finished)
	}
	action, err := store.KnowledgeJobs().GetAction(ctx, job.ID, 2)
	if err != nil || action.Status != domain.KnowledgeJobActionApplied {
		t.Fatalf("legal inquiry finish action was not applied: action=%+v err=%v", action, err)
	}
}

func containsKnowledgePromptMarker(prompt string) bool {
	return len(prompt) > 0 && strings.Contains(prompt, "Knowledge Librarian") && strings.Contains(prompt, "knowledge-librarian/v1")
}

func updatedResultMarker(run *domain.ExecutionRun) (string, bool) {
	if run == nil {
		return "", false
	}
	marker, ok := run.Input["knowledge_librarian"].(map[string]any)
	if !ok {
		return "", false
	}
	id, ok := marker["job_id"].(string)
	return id, ok && id != ""
}

func TestKnowledgeInquirySourceRunTerminalSemantics(t *testing.T) {
	tests := []struct {
		name             string
		sourceStatus     domain.RunStatus
		wantJobStatus    domain.KnowledgeJobStatus
		wantContinuation bool
	}{
		{name: "succeeded_keeps_inquiry_running", sourceStatus: domain.RunSucceeded, wantJobStatus: domain.KnowledgeJobRunning, wantContinuation: true},
		{name: "failed_cancels_inquiry", sourceStatus: domain.RunFailed, wantJobStatus: domain.KnowledgeJobCancelled},
		{name: "cancelled_cancels_inquiry", sourceStatus: domain.RunCancelled, wantJobStatus: domain.KnowledgeJobCancelled},
		{name: "interrupted_cancels_inquiry", sourceStatus: domain.RunInterrupted, wantJobStatus: domain.KnowledgeJobCancelled},
		{name: "lost_cancels_inquiry", sourceStatus: domain.RunLost, wantJobStatus: domain.KnowledgeJobCancelled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db := openTestDB(t)
			defer db.Close()
			store := sqlstore.New(db)
			dispatcher := &captureDispatcher{}
			svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())
			ws, _, requester := seedM2Env(t, ctx, store)
			librarian, err := svc.EnsureBuiltinKnowledgeLibrarian(ctx, ws)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.UpdateAgent(ctx, librarian.ID, application.AgentPatch{
				RuntimePreference: &domain.RuntimePreference{Preferred: "codex_local"}, ExpectedVersion: librarian.Version,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ConfigureKnowledgeLibrarian(ctx, ws, domain.KnowledgeLibrarianConfig{LibrarianAgentID: librarian.ID, Enabled: true}); err != nil {
				t.Fatal(err)
			}
			callerItem, err := svc.CreateWorkItem(ctx, ws, application.CreateWorkItemParams{Title: "knowledge caller", AgentProfileID: requester})
			if err != nil {
				t.Fatal(err)
			}
			sourceRun, err := svc.CreateRun(ctx, callerItem.ID, application.CreateRunParams{AgentProfileID: requester, Instruction: "ask knowledge in background"})
			if err != nil {
				t.Fatal(err)
			}
			job, err := svc.StartKnowledgeInquiry(ctx, application.StartKnowledgeInquiryParams{
				WorkspaceID: ws, RequestingAgentID: requester, SourceRunID: sourceRun.ID,
				Question: "background knowledge inquiry", ClientKey: "source-terminal-" + tc.name,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := terminalizeKnowledgeSourceRun(t, ctx, svc, sourceRun.ID, tc.sourceStatus); err != nil {
				t.Fatal(err)
			}
			if len(dispatcher.runs) != 2 {
				t.Fatalf("expected caller and librarian Runs, got %d: %+v", len(dispatcher.runs), dispatcher.runs)
			}
			decision := `{"schema_version":"knowledge-librarian/v1","action":"search","search":{"terms":["background knowledge inquiry"]}}`
			terminalizeKnowledgeRun(t, ctx, svc, job.CurrentRunID, decision)
			updated, err := svc.GetKnowledgeJob(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Status != tc.wantJobStatus {
				t.Fatalf("source status %s produced job status %s, want %s: %+v", tc.sourceStatus, updated.Status, tc.wantJobStatus, updated)
			}
			if tc.wantContinuation {
				if updated.TurnSeq != 2 || updated.CurrentRunID == job.CurrentRunID || len(dispatcher.runs) != 3 {
					t.Fatalf("successful source Run did not continue the inquiry: job=%+v dispatches=%d", updated, len(dispatcher.runs))
				}
			} else if updated.TurnSeq != 1 || len(dispatcher.runs) != 2 {
				t.Fatalf("non-success source Run unexpectedly continued the inquiry: job=%+v dispatches=%d", updated, len(dispatcher.runs))
			}
		})
	}
}

func TestKnowledgeInquiryRecoveryDoesNotCancelSuccessfulSource(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())
	ws, _, requester := seedM2Env(t, ctx, store)
	librarian, err := svc.EnsureBuiltinKnowledgeLibrarian(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateAgent(ctx, librarian.ID, application.AgentPatch{
		RuntimePreference: &domain.RuntimePreference{Preferred: "codex_local"}, ExpectedVersion: librarian.Version,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfigureKnowledgeLibrarian(ctx, ws, domain.KnowledgeLibrarianConfig{LibrarianAgentID: librarian.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	callerItem, err := svc.CreateWorkItem(ctx, ws, application.CreateWorkItemParams{Title: "recoverable knowledge caller", AgentProfileID: requester})
	if err != nil {
		t.Fatal(err)
	}
	sourceRun, err := svc.CreateRun(ctx, callerItem.ID, application.CreateRunParams{AgentProfileID: requester, Instruction: "ask knowledge and return handle"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := svc.StartKnowledgeInquiry(ctx, application.StartKnowledgeInquiryParams{
		WorkspaceID: ws, RequestingAgentID: requester, SourceRunID: sourceRun.ID,
		Question: "recoverable inquiry", ClientKey: "recovery-success-source",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := terminalizeKnowledgeSourceRun(t, ctx, svc, sourceRun.ID, domain.RunSucceeded); err != nil {
		t.Fatal(err)
	}
	acted, err := svc.RecoverKnowledgeLibrarianJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := svc.GetKnowledgeJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Status.Valid() || updated.Status != domain.KnowledgeJobRunning || acted == 0 {
		t.Fatalf("successful source Run was cancelled by recovery: acted=%d job=%+v", acted, updated)
	}
}

func TestKnowledgeAgentRecordCancellationStopsCurationAndPublication(t *testing.T) {
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
	callerItem, err := svc.CreateWorkItem(ctx, ws, application.CreateWorkItemParams{Title: "Cancelled Agent record", AgentProfileID: producer})
	if err != nil {
		t.Fatal(err)
	}
	sourceRun, err := svc.CreateRun(ctx, callerItem.ID, application.CreateRunParams{AgentProfileID: producer, Instruction: "record a requirement"})
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
		RunID: sourceRun.ID, Content: "用户确认退款必须附订单号。", Title: "取消前记录",
		PublishIntent: application.KnowledgeRecordPublishIntentConfirmedRequirement, ClientKey: "cancelled-agent-record",
	})
	if err != nil || intake.Job == nil {
		t.Fatalf("submit Agent record: result=%+v err=%v", intake, err)
	}
	job := intake.Job
	if err := svc.RecordRunStatus(ctx, sourceRun.ID, domain.RunCancelling, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, sourceRun.ID, domain.RunCancelled, nil); err != nil {
		t.Fatal(err)
	}
	terminalizeKnowledgeRun(t, ctx, svc, job.CurrentRunID, "this output must be ignored after source cancellation")
	updated, err := svc.GetKnowledgeJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domain.KnowledgeJobCancelled || updated.Result["publication_status"] == "published" {
		t.Fatalf("cancelled source record continued or published: %+v", updated)
	}
	origin, err := store.Knowledge().GetSubmissionForWorkspace(ctx, ws, job.SubmissionID)
	if err != nil || origin.Status != domain.KnowledgeSubmissionNeedsReview {
		t.Fatalf("cancelled source record did not remain reviewable: submission=%+v err=%v", origin, err)
	}
}

func terminalizeKnowledgeSourceRun(t *testing.T, ctx context.Context, svc *application.Service, runID string, status domain.RunStatus) error {
	if err := svc.RecordRunStatus(ctx, runID, domain.RunStarting, nil); err != nil {
		return err
	}
	if err := svc.RecordRunStatus(ctx, runID, domain.RunRunning, nil); err != nil {
		return err
	}
	switch status {
	case domain.RunSucceeded:
		if err := svc.RecordRunStatus(ctx, runID, domain.RunSucceeding, nil); err != nil {
			return err
		}
		return svc.RecordRunStatus(ctx, runID, domain.RunSucceeded, nil)
	case domain.RunFailed:
		return svc.RecordRunStatus(ctx, runID, domain.RunFailed, map[string]any{"code": "source_failed", "message": "caller failed", "retryable": false})
	case domain.RunCancelled:
		if err := svc.RecordRunStatus(ctx, runID, domain.RunCancelling, nil); err != nil {
			return err
		}
		return svc.RecordRunStatus(ctx, runID, domain.RunCancelled, nil)
	case domain.RunInterrupted:
		if err := svc.RecordRunStatus(ctx, runID, domain.RunInterrupting, nil); err != nil {
			return err
		}
		return svc.RecordRunStatus(ctx, runID, domain.RunInterrupted, nil)
	case domain.RunLost:
		if err := svc.RecordRunStatus(ctx, runID, domain.RunReconnecting, nil); err != nil {
			return err
		}
		return svc.RecordRunStatus(ctx, runID, domain.RunLost, nil)
	default:
		return fmt.Errorf("unsupported source test status %s", status)
	}
}

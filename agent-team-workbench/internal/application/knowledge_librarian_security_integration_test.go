package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

func TestKnowledgeConfigReadRemainsRepairableWhenLibrarianDisabled(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, nil, nil, atwruntime.NewRegistry())
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_knowledge_config", Name: "knowledge config", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	seedCtx(t, store, ctx, ws.ID)
	disabled := &domain.AgentProfile{ID: "agent_disabled_librarian", WorkspaceID: ws.ID, Name: "disabled", Role: "librarian", Availability: domain.AgentDisabled, Presence: domain.PresenceOffline, Version: 1, CreatedAt: now, UpdatedAt: now}
	active := &domain.AgentProfile{ID: "agent_active_librarian", WorkspaceID: ws.ID, Name: "active", Role: "librarian", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now}
	for _, agent := range []*domain.AgentProfile{disabled, active} {
		if err := store.Agents().Create(ctx, agent); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.KnowledgeJobs().CreateConfig(ctx, &domain.KnowledgeLibrarianConfig{
		WorkspaceID: ws.ID, LibrarianAgentID: disabled.ID, Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := svc.GetKnowledgeLibrarianConfig(ctx, ws.ID)
	if err != nil || cfg.Version != 1 || cfg.LibrarianAgentID != disabled.ID {
		t.Fatalf("stale config must remain readable for repair: cfg=%+v err=%v", cfg, err)
	}
	cfg.LibrarianAgentID = active.ID
	updated, err := svc.ConfigureKnowledgeLibrarian(ctx, ws.ID, *cfg)
	if err != nil {
		t.Fatalf("valid replacement of disabled librarian rejected: %v", err)
	}
	if updated.Version != 2 || updated.LibrarianAgentID != active.ID {
		t.Fatalf("replacement config = %+v, want active agent/version 2", updated)
	}
}

func TestKnowledgeCandidateDefaultsOwnerAndEnforcesMaintenanceAuthority(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, nil, nil, atwruntime.NewRegistry())
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_knowledge_candidate", Name: "knowledge candidate", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	seedCtx(t, store, ctx, ws.ID)
	producer := &domain.AgentProfile{ID: "agent_candidate_producer", WorkspaceID: ws.ID, Name: "producer", Role: "worker", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now}
	manager := &domain.AgentProfile{ID: "agent_candidate_manager", WorkspaceID: ws.ID, Name: "manager", Role: "librarian", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now}
	for _, agent := range []*domain.AgentProfile{producer, manager} {
		if err := store.Agents().Create(ctx, agent); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.ConfigureKnowledgeLibrarian(ctx, ws.ID, domain.KnowledgeLibrarianConfig{LibrarianAgentID: manager.ID, Enabled: true, Version: 0}); err != nil {
		t.Fatal(err)
	}
	item := &domain.KnowledgeItem{ID: domain.NewID(domain.PrefixKnowledgeItem), WorkspaceID: ws.ID, OwnerAgentID: producer.ID, Visibility: domain.KnowledgeVisibilityWorkspace, Kind: "fact", Title: "owned fact", Version: 1, Status: domain.KnowledgeStatusCandidate, CreatedAt: now, UpdatedAt: now}
	if err := store.Knowledge().CreateItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	claimOtherOwner := domain.KnowledgeSubmitCandidate{WorkspaceID: ws.ID, AgentID: producer.ID, ClientKey: "candidate-other-owner", Changes: []domain.KnowledgeChange{{ItemID: item.ID, OwnerAgentID: manager.ID, BaseVersion: 0, Title: "rewrite", Body: "body", Kind: "fact"}}}
	if _, err := svc.SubmitKnowledgeCandidate(ctx, claimOtherOwner); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("ordinary producer claiming manager owner err=%v, want validation", err)
	}
	newCandidate := domain.KnowledgeSubmitCandidate{WorkspaceID: ws.ID, AgentID: producer.ID, ClientKey: "candidate-new", Changes: []domain.KnowledgeChange{{Title: "new fact", Body: "body", Kind: "fact"}}}
	result, err := svc.SubmitKnowledgeCandidate(ctx, newCandidate)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Request.Changes) != 1 || result.Request.Changes[0].OwnerAgentID != producer.ID || result.Request.Changes[0].Visibility != domain.KnowledgeVisibilityWorkspace {
		t.Fatalf("candidate defaults not sealed: %+v", result.Request.Changes)
	}
	modifyOtherOwner := domain.KnowledgeSubmitCandidate{WorkspaceID: ws.ID, AgentID: manager.ID, ClientKey: "candidate-manager-revision", Changes: []domain.KnowledgeChange{{ItemID: item.ID, OwnerAgentID: producer.ID, BaseVersion: 0, Title: "manager rewrite", Body: "body", Kind: "fact"}}}
	if _, err := svc.SubmitKnowledgeCandidate(ctx, modifyOtherOwner); err != nil {
		t.Fatalf("configured librarian should be allowed to curate owner item: %v", err)
	}
}

func TestKnowledgeCurationMovesSourceReceiptToProcessingAtomically(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_knowledge_curation", Name: "knowledge curation", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	seedCtx(t, store, ctx, ws.ID)
	producer := &domain.AgentProfile{ID: "agent_curation_producer", WorkspaceID: ws.ID, Name: "producer", Role: "worker", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now}
	manager := &domain.AgentProfile{ID: "agent_curation_manager", WorkspaceID: ws.ID, Name: "manager", Role: "librarian", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now}
	for _, agent := range []*domain.AgentProfile{producer, manager} {
		if err := store.Agents().Create(ctx, agent); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Bindings().Create(ctx, &domain.RuntimeBinding{ID: "binding_knowledge_curation", WorkspaceID: ws.ID, RuntimeLabel: "mock", AdapterID: "mock", Capabilities: map[string]string{"resume": "supported"}, Status: domain.BindingReady, Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfigureKnowledgeLibrarian(ctx, ws.ID, domain.KnowledgeLibrarianConfig{LibrarianAgentID: manager.ID, Enabled: true, Version: 0}); err != nil {
		t.Fatal(err)
	}
	submission, err := svc.SubmitKnowledgeCandidate(ctx, domain.KnowledgeSubmitCandidate{WorkspaceID: ws.ID, AgentID: producer.ID, ClientKey: "curation-source", Changes: []domain.KnowledgeChange{{Title: "candidate", Body: "candidate body", Kind: "fact", Sources: []domain.KnowledgeSourceInput{{Kind: domain.KnowledgeSourceDocument, Ref: "spec@v1", Excerpt: "provided source excerpt"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := svc.StartKnowledgeCuration(ctx, application.StartKnowledgeCurationParams{WorkspaceID: ws.ID, RequestingAgentID: manager.ID, SubmissionID: submission.ID, ClientKey: "curation-job"})
	if err != nil {
		t.Fatal(err)
	}
	if job.SubmissionID != submission.ID || job.CurrentRunID == "" {
		t.Fatalf("curation job identity = %+v", job)
	}
	if len(job.EvidenceIDs) != 1 || !strings.HasPrefix(job.EvidenceIDs[0], "submission:"+submission.ID+":change:0:source:0") {
		t.Fatalf("curation did not seed deterministic receipt evidence: %+v", job.EvidenceIDs)
	}
	if len(dispatcher.runs) != 1 || !strings.Contains(dispatcher.runs[0].Input["instruction"].(string), job.EvidenceIDs[0]) || !strings.Contains(dispatcher.runs[0].Input["instruction"].(string), "provided source excerpt") {
		t.Fatalf("curation Run did not receive controlled source seed: %+v", dispatcher.runs)
	}
	terminalizeKnowledgeRun(t, ctx, svc, job.CurrentRunID, `{"schema_version":"knowledge-librarian/v1","action":"read","read":{"source_ids":["`+job.EvidenceIDs[0]+`"]}}`)
	updatedJob, err := svc.GetKnowledgeJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedJob.Used.Reads != 1 || len(updatedJob.Observations) == 0 || !strings.Contains(updatedJob.Observations[0], "supplied_unverified") {
		t.Fatalf("receipt source read did not return unverified evidence: %+v", updatedJob)
	}
	updated, err := store.Knowledge().GetSubmissionForWorkspace(ctx, ws.ID, submission.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domain.KnowledgeSubmissionProcessing {
		t.Fatalf("source receipt status = %s, want processing", updated.Status)
	}
}

func TestKnowledgeCurationMalformedFinishDoesNotLeaveProcessingReceipt(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_knowledge_curation_failure", Name: "knowledge curation failure", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	seedCtx(t, store, ctx, ws.ID)
	producer := &domain.AgentProfile{ID: "agent_curation_failure_producer", WorkspaceID: ws.ID, Name: "producer", Role: "worker", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now}
	manager := &domain.AgentProfile{ID: "agent_curation_failure_manager", WorkspaceID: ws.ID, Name: "manager", Role: "librarian", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now}
	for _, agent := range []*domain.AgentProfile{producer, manager} {
		if err := store.Agents().Create(ctx, agent); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Bindings().Create(ctx, &domain.RuntimeBinding{ID: "binding_knowledge_curation_failure", WorkspaceID: ws.ID, RuntimeLabel: "mock", AdapterID: "mock", Capabilities: map[string]string{"resume": "supported"}, Status: domain.BindingReady, Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfigureKnowledgeLibrarian(ctx, ws.ID, domain.KnowledgeLibrarianConfig{LibrarianAgentID: manager.ID, Enabled: true, Version: 0}); err != nil {
		t.Fatal(err)
	}
	submission, err := svc.SubmitKnowledgeCandidate(ctx, domain.KnowledgeSubmitCandidate{WorkspaceID: ws.ID, AgentID: producer.ID, ClientKey: "curation-failure-source", Changes: []domain.KnowledgeChange{{Title: "candidate", Body: "candidate body", Kind: "fact"}}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := svc.StartKnowledgeCuration(ctx, application.StartKnowledgeCurationParams{WorkspaceID: ws.ID, RequestingAgentID: manager.ID, SubmissionID: submission.ID, ClientKey: "curation-failure-job"})
	if err != nil {
		t.Fatal(err)
	}
	current := job.CurrentRunID
	for attempt := 0; attempt < 3; attempt++ {
		terminalizeKnowledgeRun(t, ctx, svc, current, "this is not JSON")
		updated, getErr := svc.GetKnowledgeJob(ctx, job.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if attempt < 2 {
			if updated.CurrentRunID == current || updated.RepairAttempt != attempt+1 {
				t.Fatalf("repair attempt %d did not advance: %+v", attempt+1, updated)
			}
			current = updated.CurrentRunID
		} else if updated.Status != domain.KnowledgeJobFailed {
			t.Fatalf("malformed curation output status = %s, want failed", updated.Status)
		}
	}
	origin, err := store.Knowledge().GetSubmissionForWorkspace(ctx, ws.ID, submission.ID)
	if err != nil {
		t.Fatal(err)
	}
	if origin.Status != domain.KnowledgeSubmissionNeedsReview {
		t.Fatalf("failed curation source status = %s, want needs_review", origin.Status)
	}
}

func TestKnowledgeCurationMissingEvidenceWithoutProposalNeedsReview(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_knowledge_curation_missing", Name: "knowledge curation missing", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	seedCtx(t, store, ctx, ws.ID)
	producer := &domain.AgentProfile{ID: "agent_curation_missing_producer", WorkspaceID: ws.ID, Name: "producer", Role: "worker", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now}
	manager := &domain.AgentProfile{ID: "agent_curation_missing_manager", WorkspaceID: ws.ID, Name: "manager", Role: "librarian", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now}
	for _, agent := range []*domain.AgentProfile{producer, manager} {
		if err := store.Agents().Create(ctx, agent); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Bindings().Create(ctx, &domain.RuntimeBinding{ID: "binding_knowledge_curation_missing", WorkspaceID: ws.ID, RuntimeLabel: "mock", AdapterID: "mock", Capabilities: map[string]string{"resume": "supported"}, Status: domain.BindingReady, Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfigureKnowledgeLibrarian(ctx, ws.ID, domain.KnowledgeLibrarianConfig{LibrarianAgentID: manager.ID, Enabled: true, Version: 0}); err != nil {
		t.Fatal(err)
	}
	submission, err := svc.SubmitKnowledgeCandidate(ctx, domain.KnowledgeSubmitCandidate{WorkspaceID: ws.ID, AgentID: producer.ID, ClientKey: "curation-missing-source", Changes: []domain.KnowledgeChange{{Title: "candidate", Body: "candidate body", Kind: "fact"}}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := svc.StartKnowledgeCuration(ctx, application.StartKnowledgeCurationParams{WorkspaceID: ws.ID, RequestingAgentID: manager.ID, SubmissionID: submission.ID, ClientKey: "curation-missing-job"})
	if err != nil {
		t.Fatal(err)
	}
	terminalizeKnowledgeRun(t, ctx, svc, job.CurrentRunID, `{"schema_version":"knowledge-librarian/v1","action":"finish","finish":{"status":"missing","answer":"无法核实","gaps":["缺少可读来源"]}}`)
	updated, err := svc.GetKnowledgeJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domain.KnowledgeJobIncomplete {
		t.Fatalf("missing curation result status = %s, want incomplete", updated.Status)
	}
	if answer, _ := updated.Result["answer"].(string); answer != "无法核实" {
		t.Fatalf("missing curation answer was not preserved: %+v", updated.Result)
	}
	origin, err := store.Knowledge().GetSubmissionForWorkspace(ctx, ws.ID, submission.ID)
	if err != nil {
		t.Fatal(err)
	}
	if origin.Status != domain.KnowledgeSubmissionNeedsReview {
		t.Fatalf("missing curation source status = %s, want needs_review", origin.Status)
	}
}

func TestKnowledgeSemanticFailureAccountsRolledBackTerminalUsage(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_knowledge_curation_usage", Name: "knowledge curation usage", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	seedCtx(t, store, ctx, ws.ID)
	producer := &domain.AgentProfile{ID: "agent_curation_usage_producer", WorkspaceID: ws.ID, Name: "producer", Role: "worker", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now}
	manager := &domain.AgentProfile{ID: "agent_curation_usage_manager", WorkspaceID: ws.ID, Name: "manager", Role: "librarian", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now}
	for _, agent := range []*domain.AgentProfile{producer, manager} {
		if err := store.Agents().Create(ctx, agent); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Bindings().Create(ctx, &domain.RuntimeBinding{ID: "binding_knowledge_curation_usage", WorkspaceID: ws.ID, RuntimeLabel: "mock", AdapterID: "mock", Capabilities: map[string]string{"resume": "supported"}, Status: domain.BindingReady, Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfigureKnowledgeLibrarian(ctx, ws.ID, domain.KnowledgeLibrarianConfig{LibrarianAgentID: manager.ID, Enabled: true, Version: 0}); err != nil {
		t.Fatal(err)
	}
	submission, err := svc.SubmitKnowledgeCandidate(ctx, domain.KnowledgeSubmitCandidate{WorkspaceID: ws.ID, AgentID: producer.ID, ClientKey: "curation-usage-source", Changes: []domain.KnowledgeChange{{Title: "candidate", Body: "candidate body", Kind: "fact", Sources: []domain.KnowledgeSourceInput{{Kind: domain.KnowledgeSourceDocument, Ref: "spec", Excerpt: "provided evidence"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := svc.StartKnowledgeCuration(ctx, application.StartKnowledgeCurationParams{WorkspaceID: ws.ID, RequestingAgentID: manager.ID, SubmissionID: submission.ID, ClientKey: "curation-usage-job"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunUsage(ctx, job.CurrentRunID, atwruntime.Usage{OutputTokens: 537, Basis: atwruntime.UsagePerRun}); err != nil {
		t.Fatal(err)
	}
	decision := `{"schema_version":"knowledge-librarian/v1","action":"finish","finish":{"status":"partial","summary":"提出了未经核实的修改","revised_changes":[{"base_version":0,"title":"forged","body":"body","kind":"fact","sources":[{"kind":"document","ref":"spec-real","excerpt":"forged evidence"}]}]}}`
	if _, decodeErr := application.DecodeKnowledgeLibrarianDecision([]byte(decision)); decodeErr != nil {
		t.Fatalf("test semantic decision unexpectedly failed schema decode: %v", decodeErr)
	}
	terminalizeKnowledgeRun(t, ctx, svc, job.CurrentRunID, decision)
	updated, err := svc.GetKnowledgeJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domain.KnowledgeJobIncomplete || updated.Used.Turns != 1 || updated.Used.OutputTokens != 537 {
		t.Fatalf("semantic failure lost terminal usage: mode=%s status=%s turn=%d current=%s used=%+v error=%s result=%+v", updated.Mode, updated.Status, updated.TurnSeq, updated.CurrentRunID, updated.Used, updated.LastError, updated.Result)
	}
	origin, err := store.Knowledge().GetSubmissionForWorkspace(ctx, ws.ID, submission.ID)
	if err != nil {
		t.Fatal(err)
	}
	if origin.Status != domain.KnowledgeSubmissionNeedsReview {
		t.Fatalf("semantic failure source status = %s, want needs_review", origin.Status)
	}
}

func terminalizeKnowledgeRun(t *testing.T, ctx context.Context, svc *application.Service, runID, text string) {
	t.Helper()
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

package application_test

import (
	"context"
	"errors"
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
	submission, err := svc.SubmitKnowledgeCandidate(ctx, domain.KnowledgeSubmitCandidate{WorkspaceID: ws.ID, AgentID: producer.ID, ClientKey: "curation-source", Changes: []domain.KnowledgeChange{{Title: "candidate", Body: "candidate body", Kind: "fact"}}})
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

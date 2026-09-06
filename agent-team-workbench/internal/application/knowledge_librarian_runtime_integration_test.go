package application_test

import (
	"context"
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
	librarian := &domain.AgentProfile{ID: "agent_knowledge_runtime_librarian", WorkspaceID: ws.ID, Name: "librarian", Role: "librarian", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now}
	for _, agent := range []*domain.AgentProfile{requester, librarian} {
		if err := store.Agents().Create(ctx, agent); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Bindings().Create(ctx, &domain.RuntimeBinding{ID: "binding_knowledge_mock", WorkspaceID: ws.ID, RuntimeLabel: "mock", AdapterID: "mock", Capabilities: map[string]string{"resume": "supported"}, Status: domain.BindingReady, Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfigureKnowledgeLibrarian(ctx, ws.ID, domain.KnowledgeLibrarianConfig{LibrarianAgentID: librarian.ID, Enabled: true, Version: 0}); err != nil {
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
	if prompt, _ := firstRun.Input["system_prompt"].(string); !strings.Contains(prompt, "KNOWLEDGE_LIBRARIAN_SCHEMA_V1_LENGTH:") || !strings.Contains(prompt, `"conflicts_with"`) || !strings.Contains(prompt, `"action":"search"`) || !strings.Contains(prompt, "contiguous literal substring") {
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

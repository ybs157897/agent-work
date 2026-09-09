package application_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/hostregistry"
	"github.com/ybs/agent-team-workbench/internal/orchestrator"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

type realPublicationFixture struct {
	ctx        context.Context
	svc        *application.Service
	store      *sqlstore.Store
	dispatcher *captureDispatcher
	chat       *domain.WorkItem
	draft      *domain.TaskPublicationDraft
	registry   *hostregistry.Registry
	root       string
	head       string
}

func newRealPublicationFixture(t *testing.T) *realPublicationFixture {
	t.Helper()
	ctx := context.Background()
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	store := sqlstore.New(db)
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())
	base := seedRunEnv(t, ctx, svc, store)
	root, registry, head := prepareRealProject(t)
	bindRealWorkspaceProject(t, ctx, store, base.WorkspaceID, registry)
	svc.SetProjectCanonicalKeyResolver(func(ctx context.Context, alias, generation, repository string) (string, error) {
		return registry.ProjectCanonicalKey(ctx, alias, generation, repository)
	})
	chat, err := svc.CreateWorkItem(ctx, base.WorkspaceID, application.CreateWorkItemParams{
		Title: "真实 Git 发布 Chat", RecordKind: domain.RecordKindChat, AgentProfileID: base.AgentProfileID,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetPublicationBaselineResolver(func(ctx context.Context, snapshot *domain.ExecutionContextSnapshot) (domain.ProjectBaseline, error) {
		return registry.ResolveProjectBaseline(ctx, snapshot)
	})
	run, err := svc.CreateRun(ctx, chat.ID, application.CreateRunParams{
		AgentProfileID: chat.AgentProfileID, Instruction: "分析已确认事项", OutputContract: orchestrator.OutputContractChatAnalysisV1,
	})
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
	decision, _, err := svc.SaveChatAnalysisDecision(ctx, application.SaveChatAnalysisDecisionParams{
		ChatWorkItemID: chat.ID, ExpectedVersion: analysis.Projection.Version, Revision: analysis.Projection.Revision,
		ItemID: "scope", Outcome: domain.ChatAnalysisDecisionConfirmed, Conclusion: "真实 Git 发布范围",
		Basis: "隔离测试中的人类确认", ProductVersion: "u06-real-git", ClientKey: "real-git-decision", ActorID: "user_demo",
	})
	if err != nil {
		t.Fatal(err)
	}
	draft, _, err := svc.CreatePublicationDraft(ctx, application.CreatePublicationDraftParams{
		ChatWorkItemID: chat.ID, ExpectedVersion: decision.Projection.Version, Revision: decision.Projection.Revision,
		ItemIDs: []string{"scope"}, Title: "真实 Git 发布任务", ClientKey: "real-git-draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &realPublicationFixture{ctx: ctx, svc: svc, store: store, dispatcher: dispatcher,
		chat: chat, draft: draft.Draft, registry: registry, root: root, head: head}
}

func TestPublicationRealGitFreezesTaskContextAndFirstRunSnapshot(t *testing.T) {
	fixture := newRealPublicationFixture(t)
	chatRevision, err := fixture.store.ChatAnalyses().Get(fixture.ctx, fixture.chat.WorkspaceID, fixture.chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	chatSnapshot, err := fixture.store.ContextSnapshots().GetByRun(fixture.ctx, chatRevision.CurrentRunID)
	if err != nil {
		t.Fatal(err)
	}

	task, err := fixture.svc.PublishPublicationDraft(fixture.ctx, fixture.chat.ID, fixture.draft.ID, fixture.draft.Version, "real-git-publish")
	if err != nil {
		t.Fatal(err)
	}
	contextRow, err := fixture.store.WorkItemContexts().Get(fixture.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if contextRow.WorkItemID != task.ID || contextRow.WorkspaceLocationID != chatSnapshot.WorkspaceLocationID ||
		contextRow.RefKind != domain.RefRoot || contextRow.BaseRevision != fixture.head {
		t.Fatalf("published Task context did not freeze real project identity: context=%+v head=%s chatSnapshot=%+v", contextRow, fixture.head, chatSnapshot)
	}

	runs, err := fixture.store.Runs().ListByWorkItem(fixture.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 {
		t.Fatalf("published Task did not create its first Coordinator Run: task=%s", task.ID)
	}
	firstSnapshot, err := fixture.store.ContextSnapshots().GetByRun(fixture.ctx, runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if runs[0].WorkItemID != task.ID || firstSnapshot.WorkspaceID != task.WorkspaceID ||
		firstSnapshot.BaseRevision != fixture.head || firstSnapshot.ID == chatSnapshot.ID || firstSnapshot.RunID == chatSnapshot.RunID {
		t.Fatalf("first Task snapshot reused Chat identity or lost baseline: first=%+v chat=%+v", firstSnapshot, chatSnapshot)
	}
	if countRunsForWorkItem(fixture.dispatcher.runs, task.ID) != 1 {
		t.Fatalf("expected exactly one dispatched first Task Run, got=%+v", fixture.dispatcher.runs)
	}
}

func TestPublicationRealGitDriftBeforeFirstRunLeavesDurableBlockedTask(t *testing.T) {
	fixture := newRealPublicationFixture(t)
	var calls int
	mutated := false
	fixture.svc.SetPublicationBaselineResolver(func(ctx context.Context, snapshot *domain.ExecutionContextSnapshot) (domain.ProjectBaseline, error) {
		calls++
		if calls == 3 && !mutated {
			// The third baseline observation is the first-Coordinator gate after
			// draft creation and publish preflight. Mutate only this test checkout.
			if err := os.WriteFile(filepath.Join(fixture.root, "README.md"), []byte("changed between publish and first Run"), 0o644); err != nil {
				return domain.ProjectBaseline{}, err
			}
			mutated = true
		}
		return fixture.registry.ResolveProjectBaseline(ctx, snapshot)
	})
	task, err := fixture.svc.PublishPublicationDraft(fixture.ctx, fixture.chat.ID, fixture.draft.ID, fixture.draft.Version, "real-git-drift-publish")
	if err != nil || task == nil {
		t.Fatalf("publish should retain a durable Task when first-run baseline drifts: task=%+v err=%v calls=%d", task, err, calls)
	}
	if !mutated || calls < 3 {
		t.Fatalf("test did not reach the first-run baseline gate: mutated=%t calls=%d", mutated, calls)
	}
	if countRunsForWorkItem(fixture.dispatcher.runs, task.ID) != 0 {
		t.Fatalf("baseline drift must not dispatch a Task Run: runs=%+v", fixture.dispatcher.runs)
	}
	state, err := fixture.store.TaskCoordinators().GetStateForWorkItem(fixture.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorBlocked || state.BlockerCode == "" {
		t.Fatalf("baseline drift must leave a durable Coordinator blocker: state=%+v", state)
	}
}

func countRunsForWorkItem(runs []*domain.ExecutionRun, workItemID string) int {
	count := 0
	for _, run := range runs {
		if run.WorkItemID == workItemID {
			count++
		}
	}
	return count
}

func prepareRealProject(t *testing.T) (string, *hostregistry.Registry, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "web-idea-fixture")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	realGitRun(t, root, "init", "-q")
	realGitRun(t, root, "config", "user.email", "u06@example.com")
	realGitRun(t, root, "config", "user.name", "U06")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("initial baseline"), 0o644); err != nil {
		t.Fatal(err)
	}
	realGitRun(t, root, "add", "README.md")
	realGitRun(t, root, "commit", "-q", "-m", "initial")
	head := strings.TrimSpace(realGitOutput(t, root, "rev-parse", "--verify", "HEAD^{commit}"))
	registryPath := filepath.Join(t.TempDir(), "host-registry.yaml")
	registryYAML := fmt.Sprintf("version: 1\nmounts:\n  - alias: default\n    root: %q\n    repository_identity: repo-real-git\n", root)
	if err := os.WriteFile(registryPath, []byte(registryYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	registry, err := hostregistry.Load(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	return root, registry, head
}

func bindRealWorkspaceProject(t *testing.T, ctx context.Context, store *sqlstore.Store, workspaceID string, registry *hostregistry.Registry) {
	t.Helper()
	advertised := registry.Advertise()[0]
	now := time.Now().UTC()
	if err := store.ExecutionHosts().UpsertMount(ctx, &domain.HostMount{
		ExecutionHostID: domain.LocalHostID, Alias: advertised.Alias, RepositoryIdentity: advertised.RepositoryIdentity,
		RegistryGeneration: advertised.RegistryGeneration, Status: domain.MountStatusReady,
		SupportedRefKinds: advertised.SupportedRefKinds, Checkouts: advertised.Checkouts, LastSeenAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	location, err := store.WorkspaceLocations().DefaultFor(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	location.MountAlias = advertised.Alias
	location.MountGeneration = advertised.RegistryGeneration
	location.RepositoryIdentity = advertised.RepositoryIdentity
	if err := store.WorkspaceLocations().Update(ctx, location, location.Version); err != nil {
		t.Fatal(err)
	}
	location, err = store.WorkspaceLocations().Get(ctx, location.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WorkspaceProjects().Create(ctx, &domain.WorkspaceProject{
		WorkspaceID: workspaceID, ExecutionHostID: domain.LocalHostID, MountAlias: advertised.Alias,
		MountGeneration: advertised.RegistryGeneration, RepositoryIdentity: advertised.RepositoryIdentity,
		CanonicalKey: domain.WorkspaceProjectCanonicalKey(domain.LocalHostID, advertised.Alias), LocationID: location.ID,
		Status: domain.WorkspaceProjectReady, Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func realGitRun(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=U06", "GIT_AUTHOR_EMAIL=u06@example.com", "GIT_COMMITTER_NAME=U06", "GIT_COMMITTER_EMAIL=u06@example.com")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func realGitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := cmd.Output(); err != nil {
		t.Fatalf("git %v: %v", args, err)
	} else {
		return string(output)
	}
	return ""
}

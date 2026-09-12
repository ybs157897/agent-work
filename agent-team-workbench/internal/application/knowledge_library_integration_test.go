package application_test

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/httpapi"
	"github.com/ybs/agent-team-workbench/internal/knowledgelib"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// These tests exercise the whole library write path against real git
// repositories: snapshot capture, FIFO ordering, staging validation,
// program-collected evidence, atomic publish and release-pinned reads. The
// librarian model turn is simulated by writing the staging directory the
// brief asks for, which is exactly the contract the harness consumes; a real
// CLI runtime is exercised separately by the live end-to-end run.

type libraryFixture struct {
	root     string
	repos    map[string]string
	cleanSHA map[string]string
}

func generateLibraryFixture(t *testing.T) *libraryFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for the multi-repo library fixture")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "testdata", "knowledge-fixtures", "generate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Skipf("fixture generator missing: %v", err)
	}
	root := filepath.Join(t.TempDir(), "fixtures")
	cmd := exec.Command("bash", script, root)
	cmd.Dir = filepath.Dir(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate fixture: %v\n%s", err, out)
	}
	fixture := &libraryFixture{root: root, repos: map[string]string{}, cleanSHA: map[string]string{}}
	for _, name := range []string{"order-service", "device-service", "common"} {
		fixture.repos[name] = filepath.Join(root, name)
		out, err := exec.Command("git", "-C", fixture.repos[name], "rev-parse", "HEAD").Output()
		if err != nil {
			t.Fatalf("rev-parse %s: %v", name, err)
		}
		fixture.cleanSHA[name] = strings.TrimSpace(string(out))
	}
	return fixture
}

type libraryHarness struct {
	svc      *application.Service
	store    *sqlstore.Store
	db       *sql.DB
	wsID     string
	libRoot  string
	fixture  *libraryFixture
	agentRun *domain.ExecutionRun
}

func newLibraryHarness(t *testing.T) *libraryHarness {
	t.Helper()
	ctx := context.Background()
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	store := sqlstore.New(db)
	harness := &libraryHarness{store: store, db: db, wsID: "ws_knowledge_library", fixture: generateLibraryFixture(t)}
	now := time.Now().UTC()
	if err := store.Workspaces().Create(ctx, &domain.Workspace{
		ID: harness.wsID, Name: "library", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.SeedWorkspaceLocation(ctx, store, harness.wsID); err != nil {
		t.Fatal(err)
	}
	if err := store.Bindings().Create(ctx, &domain.RuntimeBinding{
		ID: "rb_library_mock", WorkspaceID: harness.wsID, RuntimeLabel: "mock", AdapterID: "mock",
		Provider: "mock", Model: "mock", Status: domain.BindingReady, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	harness.libRoot = filepath.Join(harness.fixture.root, "..", "library")
	harness.libRoot = filepath.Clean(harness.libRoot)
	harness.svc = application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	harness.svc.SetKnowledgeWorkspaceRootResolver(func(context.Context, string) (string, error) {
		return harness.fixture.root, nil
	})
	librarian, err := harness.svc.EnsureBuiltinKnowledgeLibrarian(ctx, harness.wsID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.svc.UpdateAgent(ctx, librarian.ID, application.AgentPatch{
		RuntimePreference: &domain.RuntimePreference{Preferred: "mock", Mode: "default"},
		ExpectedVersion:   librarian.Version,
	}); err != nil {
		t.Fatal(err)
	}
	lib, err := harness.svc.EnsureKnowledgeLibrary(ctx, harness.wsID)
	if err != nil {
		t.Fatal(err)
	}
	harness.libRoot = lib.RootPath
	return harness
}

func (h *libraryHarness) registerSource(t *testing.T, name string, kind domain.KnowledgeSourceKind, usages ...domain.KnowledgeSourceUsage) {
	t.Helper()
	if _, err := h.svc.CreateKnowledgeSource(context.Background(), h.wsID, application.KnowledgeLibrarySourceInput{
		Name: name, Kind: kind, RepoPath: h.fixture.repos[name], DefaultRef: "main", Usages: usages,
	}); err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
}

func (h *libraryHarness) registerAllSources(t *testing.T) {
	t.Helper()
	// A service repository is one implicit usage: its binding is named after
	// the source itself.
	h.registerSource(t, "order-service", domain.SourceKindService)
	h.registerSource(t, "device-service", domain.SourceKindService)
	// One logical common repository, two consumers at two artifact versions:
	// this must become two bindings of the same source, not two sources.
	h.registerSource(t, "common", domain.SourceKindCommon,
		domain.KnowledgeSourceUsage{Consumer: "order-service", Artifact: "com.example:common:1.4.2"},
		domain.KnowledgeSourceUsage{Consumer: "device-service", Artifact: "com.example:common:2.0.0"})
}

// tick runs one worker pass.
func (h *libraryHarness) tick(t *testing.T) {
	t.Helper()
	if err := h.svc.ProcessKnowledgeLibraryOnce(context.Background()); err != nil {
		t.Fatalf("worker tick: %v", err)
	}
}

// headTask returns the current queue head, if any.
func (h *libraryHarness) headTask(t *testing.T) *domain.KnowledgeWriteTask {
	t.Helper()
	task, err := h.svc.KnowledgeLibraryHeadTask(context.Background(), h.wsID)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// completeAgentTurn marks the dispatched librarian Run terminal, which is what
// a finished CLI runtime reports.
func (h *libraryHarness) completeAgentTurn(t *testing.T, status domain.RunStatus) {
	t.Helper()
	ctx := context.Background()
	task := h.headTask(t)
	if task == nil || task.CurrentRunID == "" {
		t.Fatal("no dispatched agent run to complete")
	}
	run, err := h.store.Runs().Get(ctx, task.CurrentRunID)
	if err != nil {
		t.Fatal(err)
	}
	run.Status = status
	if err := h.store.Runs().Update(ctx, run, run.Version); err != nil {
		t.Fatal(err)
	}
	h.agentRun = run
}

func (h *libraryHarness) stagingDir(t *testing.T) string {
	t.Helper()
	task := h.headTask(t)
	if task == nil || task.StagingPath == "" {
		t.Fatal("no staging directory")
	}
	return task.StagingPath
}

// writeStaging simulates the librarian agent's file output.
func writeStaging(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		target := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// libraryDoc renders one topic document with a single assertion.
func libraryDoc(docID, title, assertionID, statement, evidenceKey string, about []string) string {
	return "---\n" +
		"schema_version: kb-note/0.2-draft\n" +
		"id: " + docID + "\n" +
		"kind: components.service\n" +
		"title: " + title + "\n" +
		"summary: 由合成夹具生成的服务说明。\n" +
		"about: [" + strings.Join(about, ", ") + "]\n" +
		"domains: [order]\n" +
		"---\n\n" +
		"# " + title + "\n\n## 知识条目\n\n### " + title + "职责\n\n" +
		"```yaml\n" +
		"kind: assertion\n" +
		"id: " + assertionID + "\n" +
		"about: [" + strings.Join(about, ", ") + "]\n" +
		"perspective: descriptive\n" +
		"basis: code_static\n" +
		"scope:\n  conditions: []\n  environments: []\n" +
		"evidence:\n  - evidence_id: " + evidenceKey + "\n    role: supports\n" +
		"```\n\n" +
		"#### 陈述\n\n" + statement + "\n\n" +
		"#### 说明与未知\n\n部署环境尚未核实。\n"
}

func orderServiceAssertionEvidence(t *testing.T, repoPath string) map[string]string {
	t.Helper()
	span := lineSpan(t, repoPath, "src/main/java/com/example/order/OrderService.java")
	return map[string]string{
		"evidence.yaml": "evidence:\n" +
			"  - key: ev-order-cancel\n" +
			"    binding: order-service\n" +
			"    path: src/main/java/com/example/order/OrderService.java\n" +
			"    locator:\n" +
			"      kind: source_text\n" +
			"      interval: half_open\n" +
			"      start: {line: 0, column: 0}\n" +
			"      end: {line: " + span + ", column: 0}\n" +
			"    note: 取消分支\n",
	}
}

// lineSpan returns the exact line count of a fixture source file so the
// evidence locator names a real, in-range span.
func lineSpan(t *testing.T, repoPath, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoPath, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read fixture source: %v", err)
	}
	lines := strings.Count(strings.TrimRight(string(raw), "\n"), "\n") + 1
	return strconv.Itoa(lines)
}

// TestKnowledgeLibraryInitializePublishesAndAnswers covers A2, A3 (contract),
// A6 and A8 on a real multi-repo fixture: initialize produces a release whose
// documents, assertions and evidence are all readable, and a query answers
// pinned to that release.
func TestKnowledgeLibraryInitializePublishesAndAnswers(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	h.registerAllSources(t)

	receipt, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "test", ClientKey: "init-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Accepted || receipt.TaskID == "" || receipt.QueueSeq != 1 {
		t.Fatalf("initialize receipt must durably accept and enqueue one task: %+v", receipt)
	}

	h.tick(t)
	task := h.headTask(t)
	if task == nil || task.Status != domain.KnowledgeTaskRunning || task.SnapshotID == "" {
		t.Fatalf("head task should be running with a frozen snapshot: %+v", task)
	}
	snapshot, err := h.store.Library().GetSnapshot(ctx, task.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	// Three logical sources, four bindings: common participates in two usages.
	if len(snapshot.Bindings) != 4 {
		t.Fatalf("snapshot should carry one binding per usage, got %d", len(snapshot.Bindings))
	}
	for _, b := range snapshot.Bindings {
		if b.CommitSHA == "" {
			t.Fatalf("binding %s has no pinned commit", b.SourceName)
		}
		if b.SourceName == "device-service" && (!b.Dirty || len(b.Untracked) != 1) {
			t.Fatalf("device-service must be frozen dirty with one untracked file: %+v", b)
		}
	}

	staging := h.stagingDir(t)
	if _, err := os.Stat(filepath.Join(staging, "brief.md")); err != nil {
		t.Fatalf("brief.md must be written for the agent: %v", err)
	}
	files := orderServiceAssertionEvidence(t, h.fixture.repos["order-service"])
	files["content/components/order-service.md"] = libraryDoc(
		"doc:order-service", "订单服务", "assertion:order-cancel-publishes", "订单服务在取消分支发布取消事件。",
		"ev-order-cancel", []string{"entity:service:order-service"})
	files["entities.yaml"] = "entities:\n  - id: entity:event:order-cancelled\n    kind: event\n    name: OrderCancelledEvent\n"
	files["plan.json"] = `{"summary":"初始化订单服务说明","documents":["content/components/order-service.md"],` +
		`"removals":[],"renames":[],"coverage":{"sources_read":["order-service"],"sources_missed":["device-service"],` +
		`"gaps":["未核实生产环境投递"],"notes":""}}`
	writeStaging(t, staging, files)

	h.completeAgentTurn(t, domain.RunSucceeded)
	h.tick(t)

	task = h.headTask(t)
	if task != nil {
		t.Fatalf("task should have completed, still head: %+v (%s)", task, task.LastError)
	}
	release, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, "")
	if err != nil {
		t.Fatal(err)
	}
	if release.Seq != 1 || release.DocumentCount != 1 || release.AssertionCount != 1 || release.EvidenceCount != 1 {
		t.Fatalf("release counts wrong: %+v", release)
	}

	// Evidence was collected by the program, never reported by the model.
	docs, _, err := h.svc.ListKnowledgeDocuments(ctx, h.wsID, release.ID, "", "", 0)
	if err != nil || len(docs) != 1 {
		t.Fatalf("document browse failed: %v %+v", err, docs)
	}
	detail, err := h.svc.GetKnowledgeDocument(ctx, h.wsID, docs[0].ID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Assertions) != 1 {
		t.Fatalf("assertion not materialized: %+v", detail.Assertions)
	}
	evID := evidenceIDOf(t, detail.Assertions[0].EvidenceJSON)
	ev, binding, rep, err := h.svc.GetKnowledgeEvidence(ctx, h.wsID, evID)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ContentDigest == "" || ev.ExcerptDigest == "" || ev.Excerpt == "" {
		t.Fatalf("evidence must carry a program-computed digest and excerpt: %+v %+v", ev, rep)
	}
	if binding.CommitSHA != h.fixture.cleanSHA["order-service"] {
		t.Fatalf("evidence binding must pin the frozen commit: %s", binding.CommitSHA)
	}

	answer, err := h.svc.QueryKnowledgeLibrary(ctx, application.KnowledgeLibraryQuery{
		WorkspaceID: h.wsID, Question: "订单服务是否发布取消事件",
	})
	if err != nil {
		t.Fatal(err)
	}
	if answer.Release == nil || answer.Release.ID != release.ID {
		t.Fatalf("query must pin the published release: %+v", answer.Release)
	}
	if len(answer.Hits) == 0 {
		t.Fatalf("query should find the published assertion: %+v", answer)
	}
	if len(answer.Unknowns) == 0 {
		t.Fatal("coverage gaps from plan.json must surface as unknowns")
	}

	// Markdown is materialized beside the ledger.
	if _, err := os.Stat(filepath.Join(h.libRoot, "content", "components", "order-service.md")); err != nil {
		t.Fatalf("published markdown missing from the library root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.libRoot, "INDEX.md")); err != nil {
		t.Fatalf("library index missing: %v", err)
	}

	// The search projection is derived: the queued rebuild reproduces the same
	// answers from the published versions.
	reindexReceipt, err := h.svc.ReindexKnowledgeLibrary(ctx, h.wsID, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reindexReceipt.Accepted || reindexReceipt.Status != domain.KnowledgeTaskQueued {
		t.Fatalf("reindex must be enqueued, not executed inline: %+v", reindexReceipt)
	}
	h.tick(t)
	reindexTask, _, err := h.svc.GetKnowledgeWriteTask(ctx, h.wsID, reindexReceipt.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if reindexTask.Status != domain.KnowledgeTaskCompleted {
		t.Fatalf("queued reindex did not run: %+v (%s)", reindexTask, reindexTask.LastError)
	}
	again, err := h.svc.QueryKnowledgeLibrary(ctx, application.KnowledgeLibraryQuery{
		WorkspaceID: h.wsID, Question: "订单服务是否发布取消事件",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Hits) != len(answer.Hits) {
		t.Fatalf("reindex changed the answer set: %d vs %d", len(again.Hits), len(answer.Hits))
	}
}

func evidenceIDOf(t *testing.T, raw string) string {
	t.Helper()
	if !strings.Contains(raw, "evidence:") {
		t.Fatalf("assertion carries no evidence reference: %s", raw)
	}
	start := strings.Index(raw, "evidence:")
	rest := raw[start:]
	end := strings.IndexAny(rest, "\"")
	if end < 0 {
		t.Fatal("malformed evidence reference")
	}
	return rest[:end]
}

// TestKnowledgeLibraryFIFOAndIdempotency covers A4: events are accepted
// cheaply, processed strictly in order, never concurrently, and a repeated
// delivery never creates a second task.
func TestKnowledgeLibraryFIFOAndIdempotency(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	h.registerAllSources(t)

	first, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "test", ClientKey: "ev-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "test", ClientKey: "ev-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Duplicate || duplicate.TaskID != first.TaskID || duplicate.QueueSeq != first.QueueSeq {
		t.Fatalf("a repeated client_key must return the original receipt: %+v vs %+v", duplicate, first)
	}
	second, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "code.changed", Source: "git-hook", ClientKey: "ev-2",
		Subject: map[string]any{"changed_paths": []any{"src/main/java/com/example/order/OrderService.java"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	third, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "requirement.imported", Source: "business-harness", ClientKey: "ev-3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.QueueSeq != 2 || third.QueueSeq != 3 {
		t.Fatalf("queue sequence must follow acceptance order: %d %d %d", first.QueueSeq, second.QueueSeq, third.QueueSeq)
	}

	// The queue accepts everything without generating anything.
	tasks, err := h.svc.ListKnowledgeWriteTasks(ctx, h.wsID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Fatalf("all three events must be queued: %d", len(tasks))
	}
	if tasks[0].Status != domain.KnowledgeTaskQueued {
		t.Fatalf("acceptance alone must not publish: %+v", tasks[0])
	}

	// One worker pass may only ever run the oldest task.
	h.tick(t)
	head := h.headTask(t)
	if head == nil || head.Seq != 1 {
		t.Fatalf("FIFO head must be seq 1: %+v", head)
	}
	all, err := h.store.Library().ListTasks(ctx, h.mustLibraryID(t), "", 10)
	if err != nil {
		t.Fatal(err)
	}
	running := 0
	for _, task := range all {
		if task.Status == domain.KnowledgeTaskRunning {
			running++
		}
	}
	if running != 1 {
		for _, task := range all {
			t.Logf("task seq=%d status=%s err=%s blocked=%s", task.Seq, task.Status, task.LastError, task.BlockedReason)
		}
		t.Fatalf("at most one write task may run: %d", running)
	}
	// A second concurrent claim on the same head must not create a second
	// writer, because the database enforces it.
	if ok, err := h.store.Library().ClaimTask(ctx, h.mustLibraryID(t), "ktask-not-head", "other"); err != nil || ok {
		t.Fatalf("a non-head task must not be claimable: ok=%v err=%v", ok, err)
	}
}

// mustLibraryID resolves the library row of the harness workspace.
func mustLibraryID(t *testing.T, h *libraryHarness) string {
	return h.mustLibraryID(t)
}

// buildProjectionForTest exposes the harness's own validation step so a test
// can construct the exact projection a crashed publish would have used.
func (h *libraryHarness) buildProjectionForTest(ctx context.Context, task *domain.KnowledgeWriteTask) (*knowledgelib.Projection, *application.PublishInput, error) {
	staged, err := h.svc.ReadStagingForTest(ctx, task)
	if err != nil {
		return nil, nil, err
	}
	return h.svc.BuildProjectionForTest(ctx, task, staged)
}

// rewindCompletedTaskForTest puts a finished task back into the state a crash
// after the publish transaction would leave: release committed, task not.
func (h *libraryHarness) rewindCompletedTaskForTest(t *testing.T, ctx context.Context, taskID, releaseID string) error {
	task, err := h.store.Library().GetTask(ctx, h.mustLibraryID(t), taskID)
	if err != nil {
		return err
	}
	task.Status = domain.KnowledgeTaskRunning
	task.TargetReleaseID = ""
	task.FinishedAt = nil
	task.UpdatedAt = time.Now().UTC()
	return h.store.Library().UpdateTask(ctx, task)
}

func (h *libraryHarness) mustLibraryID(t *testing.T) string {
	t.Helper()
	lib, err := h.store.Library().GetLibrary(context.Background(), h.wsID)
	if err != nil {
		t.Fatal(err)
	}
	return lib.ID
}

// TestKnowledgeLibraryRejectsUnverifiableEvidence covers A3/A8 negative
// control: a model that points at a location that does not exist cannot get
// its work published, and the queue head is retained rather than skipped.
func TestKnowledgeLibraryRejectsUnverifiableEvidence(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	h.registerAllSources(t)
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "test", ClientKey: "bad-1",
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	staging := h.stagingDir(t)
	writeStaging(t, staging, map[string]string{
		"evidence.yaml": "evidence:\n" +
			"  - key: ev-missing\n" +
			"    binding: order-service\n" +
			"    path: src/main/java/com/example/order/DoesNotExist.java\n" +
			"    locator:\n" +
			"      kind: source_text\n" +
			"      interval: half_open\n" +
			"      start: {line: 0, column: 0}\n" +
			"      end: {line: 5, column: 0}\n",
		"content/components/order-service.md": libraryDoc(
			"doc:order-service", "订单服务", "assertion:ghost", "订单服务发布取消事件。",
			"ev-missing", []string{"entity:service:order-service"}),
		"plan.json": `{"summary":"坏证据","documents":["content/components/order-service.md"],"removals":[],"renames":[],"coverage":{"sources_read":[],"sources_missed":[],"gaps":[],"notes":""}}`,
	})
	h.completeAgentTurn(t, domain.RunSucceeded)
	h.tick(t)

	task := h.headTask(t)
	if task == nil {
		t.Fatal("a rejected task must keep the queue head")
	}
	if task.RepairAttempt != 1 || task.Status != domain.KnowledgeTaskRunning {
		t.Fatalf("first rejection should request one bounded repair turn: %+v", task)
	}
	release, err := h.store.Library().CurrentRelease(ctx, h.mustLibraryID(t))
	if err == nil || release != nil {
		t.Fatalf("nothing may be published from unverifiable evidence: %+v", release)
	}
}

// TestKnowledgeLibraryRepairBudgetBlocks covers A7: when the model cannot fix
// its output, the head is explicitly blocked with a diagnosis instead of being
// silently skipped or counted as success.
func TestKnowledgeLibraryRepairBudgetBlocks(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	h.registerAllSources(t)
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "test", ClientKey: "block-1",
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	// One dispatch plus the bounded repair turns.
	for attempt := 0; attempt < 8; attempt++ {
		task := h.headTask(t)
		if task == nil || task.Status == domain.KnowledgeTaskBlocked {
			break
		}
		// The agent keeps producing an assertion with no evidence at all.
		writeStaging(t, h.stagingDir(t), map[string]string{
			"evidence.yaml": "evidence: []\n",
			"content/components/order-service.md": libraryDoc(
				"doc:order-service", "订单服务", "assertion:no-evidence", "订单服务发布取消事件。",
				"ev-undocumented", []string{"entity:service:order-service"}),
			"plan.json": `{"summary":"无证据","documents":["content/components/order-service.md"],"removals":[],"renames":[],"coverage":{"sources_read":[],"sources_missed":[],"gaps":[],"notes":""}}`,
		})
		h.completeAgentTurn(t, domain.RunSucceeded)
		h.tick(t)
	}
	task := h.headTask(t)
	if task == nil || task.Status != domain.KnowledgeTaskBlocked {
		t.Fatalf("task should end blocked after the repair budget: %+v", task)
	}
	if task.BlockedReason == "" || task.LastError == "" {
		t.Fatalf("a blocked task must carry a diagnosis: %+v", task)
	}
	status, err := h.svc.KnowledgeLibraryStatus(ctx, h.wsID)
	if err != nil {
		t.Fatal(err)
	}
	if status.HeadTask == nil || len(status.BlockedTasks) != 1 || len(status.RecentErrors) == 0 {
		t.Fatalf("the admin status view must expose the block: %+v", status)
	}
}

// TestKnowledgeLibraryIncrementalDiscoversNewConsumer covers A5: changing one
// source updates its documents, a newly added consumer is discovered, and
// unrelated documents are carried forward instead of being rewritten.
func TestKnowledgeLibraryIncrementalDiscoversNewConsumer(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	h.registerAllSources(t)
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "test", ClientKey: "inc-1",
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	files := orderServiceAssertionEvidence(t, h.fixture.repos["order-service"])
	files["content/components/order-service.md"] = libraryDoc(
		"doc:order-service", "订单服务", "assertion:order-cancel-publishes", "订单服务在取消分支发布取消事件。",
		"ev-order-cancel", []string{"entity:service:order-service"})
	files["content/components/device-service.md"] = libraryDoc(
		"doc:device-service", "设备服务", "assertion:device-consumes-cancel", "设备服务消费取消事件。",
		"ev-order-cancel", []string{"entity:service:device-service"})
	files["plan.json"] = `{"summary":"初始化两个服务","documents":["content/components/order-service.md","content/components/device-service.md"],"removals":[],"renames":[],"coverage":{"sources_read":["order-service","device-service"],"sources_missed":[],"gaps":[],"notes":""}}`
	writeStaging(t, h.stagingDir(t), files)
	h.completeAgentTurn(t, domain.RunSucceeded)
	h.tick(t)

	first, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, "")
	if err != nil {
		t.Fatal(err)
	}
	docsBefore, _, err := h.svc.ListKnowledgeDocuments(ctx, h.wsID, first.ID, "", "", 0)
	if err != nil || len(docsBefore) != 2 {
		t.Fatalf("initial release should carry two documents: %v %+v", err, docsBefore)
	}

	// A new consumer is added to device-service: the incremental task must
	// discover it and must not rewrite the untouched order-service document.
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "code.changed", Source: "git-hook", ClientKey: "inc-2",
		Subject: map[string]any{"changed_paths": []any{"DeviceReservationConsumer.java"}},
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	task := h.headTask(t)
	if task == nil || task.BaseReleaseID != first.ID {
		t.Fatalf("the incremental task must rebase on the published release: %+v", task)
	}
	if task.Kind != "incremental" {
		t.Fatalf("a code.changed event must be an incremental task, got %q", task.Kind)
	}
	writeStaging(t, h.stagingDir(t), map[string]string{
		"evidence.yaml": "evidence:\n" +
			"  - key: ev-device-consumer\n" +
			"    binding: device-service\n" +
			"    path: src/main/java/com/example/device/DeviceReservationConsumer.java\n" +
			"    locator:\n      kind: source_text\n      interval: half_open\n" +
			"      start: {line: 0, column: 0}\n      end: {line: " +
			lineSpan(t, h.fixture.repos["device-service"], "src/main/java/com/example/device/DeviceReservationConsumer.java") + ", column: 0}\n",
		"content/components/device-service.md": libraryDoc(
			"doc:device-service", "设备服务", "assertion:device-consumes-cancel",
			"设备服务新增消费者：消费取消事件并释放占用。", "ev-device-consumer",
			[]string{"entity:service:device-service"}),
		"plan.json": `{"summary":"设备服务新增消费者","documents":["content/components/device-service.md"],"removals":[],"renames":[],"coverage":{"sources_read":["device-service"],"sources_missed":[],"gaps":[],"notes":""}}`,
	})
	h.completeAgentTurn(t, domain.RunSucceeded)
	h.tick(t)

	second, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, "")
	if err != nil {
		t.Fatal(err)
	}
	if second.Seq != 2 || second.ParentReleaseID != first.ID {
		t.Fatalf("incremental release must chain to its parent: %+v", second)
	}
	docsAfter, _, err := h.svc.ListKnowledgeDocuments(ctx, h.wsID, second.ID, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(docsAfter) != 2 {
		t.Fatalf("the untouched document must be carried forward: %+v", docsAfter)
	}
	carried := false
	for _, d := range docsAfter {
		if strings.Contains(d.Path, "order-service") {
			detail, err := h.svc.GetKnowledgeDocument(ctx, h.wsID, d.ID, "", 0)
			if err != nil {
				t.Fatal(err)
			}
			versions, err := h.store.Library().ListDocumentVersions(ctx, d.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(versions) != 1 {
				t.Fatalf("an untouched document must not be rewritten: %d versions", len(versions))
			}
			if len(detail.Relations) != 0 && detail.Document.CurrentVersion != 1 {
				t.Fatal("carried-forward document changed unexpectedly")
			}
			carried = true
		}
	}
	if !carried {
		t.Fatal("order-service document missing from the incremental release")
	}
	// The new consumer is discoverable as a relation endpoint.
	graph, err := h.svc.GetKnowledgeGraph(ctx, h.wsID, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Documents) != 2 {
		t.Fatalf("graph must show both service documents: %+v", graph.Documents)
	}
}

// TestKnowledgeLibraryCommonMultiVersionBindings covers F1: one logical common
// source used by two services at two artifact versions produces two bindings
// of the same source in one snapshot, and evidence collected for one usage is
// never silently satisfied by the other usage's binding.
func TestKnowledgeLibraryCommonMultiVersionBindings(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	h.registerAllSources(t)

	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "test", ClientKey: "multi-1",
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	task := h.headTask(t)
	if task == nil || task.SnapshotID == "" {
		t.Fatalf("initialize task should freeze a snapshot: %+v", task)
	}
	snapshot, err := h.store.Library().GetSnapshot(ctx, task.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	// One source_id, two bindings, two artifact versions.
	var commonSourceID string
	byUsage := map[string]domain.KnowledgeSnapshotBinding{}
	for _, b := range snapshot.Bindings {
		if b.SourceName == "common" {
			commonSourceID = b.SourceID
			byUsage[b.Consumer] = b
		}
	}
	if commonSourceID == "" {
		t.Fatalf("common source missing from the snapshot: %+v", snapshot.Bindings)
	}
	if len(byUsage) != 2 {
		t.Fatalf("common must produce one binding per usage, got %d", len(byUsage))
	}
	order, device := byUsage["order-service"], byUsage["device-service"]
	if order.SourceID != commonSourceID || device.SourceID != commonSourceID {
		t.Fatalf("both usages must share one logical source: %+v %+v", order, device)
	}
	if order.Artifact != "com.example:common:1.4.2" || device.Artifact != "com.example:common:2.0.0" {
		t.Fatalf("artifact versions were not isolated per usage: %+v %+v", order, device)
	}
	if order.ID == device.ID {
		t.Fatalf("usages must have distinct binding identities: %s", order.ID)
	}
	// The agent is told to read the frozen export of each binding.
	staging := h.stagingDir(t)
	brief, err := os.ReadFile(filepath.Join(staging, "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(brief), "common@order-service") || !strings.Contains(string(brief), "common@device-service") {
		t.Fatalf("brief must name both usage bindings:\n%s", string(brief))
	}
	if !strings.Contains(string(brief), "/snapshots/") {
		t.Fatalf("brief must point the agent at the frozen snapshot tree, not the live repository:\n%s", string(brief))
	}
}

// TestKnowledgeLibraryEvidenceBindingsAreIsolated proves the collected
// evidence of one usage cannot be reused for the other: distinct bindings
// produce distinct evidence identities even for the same file and locator.
func TestKnowledgeLibraryEvidenceBindingsAreIsolated(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	h.registerAllSources(t)
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "test", ClientKey: "iso-1",
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	staging := h.stagingDir(t)
	span := lineSpan(t, h.fixture.repos["common"], "src/main/java/com/example/common/messaging/EventPublisher.java")
	writeStaging(t, staging, map[string]string{
		"evidence.yaml": "evidence:\n" +
			"  - key: ev-order-common\n" +
			"    binding: common@order-service\n" +
			"    path: src/main/java/com/example/common/messaging/EventPublisher.java\n" +
			"    locator:\n      kind: source_text\n      interval: half_open\n" +
			"      start: {line: 0, column: 0}\n      end: {line: " + span + ", column: 0}\n" +
			"  - key: ev-device-common\n" +
			"    binding: common@device-service\n" +
			"    path: src/main/java/com/example/common/messaging/EventPublisher.java\n" +
			"    locator:\n      kind: source_text\n      interval: half_open\n" +
			"      start: {line: 0, column: 0}\n      end: {line: " + span + ", column: 0}\n",
		"entities.yaml": "entities:\n  - id: entity:service:order-service\n    kind: service\n    name: order-service\n  - id: entity:service:device-service\n    kind: service\n    name: device-service\n",
		"content/components/order-service.md": libraryDoc(
			"doc:order-service", "订单服务", "assertion:order-uses-common", "订单服务依赖公共事件发布接口。",
			"ev-order-common", []string{"entity:service:order-service"}),
		"content/components/device-service.md": libraryDoc(
			"doc:device-service", "设备服务", "assertion:device-uses-common", "设备服务依赖公共事件发布接口。",
			"ev-device-common", []string{"entity:service:device-service"}),
		"plan.json": `{"summary":"两个使用方各自取证","documents":["content/components/order-service.md","content/components/device-service.md"],"removals":[],"renames":[],"coverage":{"sources_read":["common"],"sources_missed":[],"gaps":[],"notes":""}}`,
	})
	h.completeAgentTurn(t, domain.RunSucceeded)
	h.tick(t)

	release, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, "")
	if err != nil {
		t.Fatal(err)
	}
	docs, _, err := h.svc.ListKnowledgeDocuments(ctx, h.wsID, release.ID, "", "", 0)
	if err != nil || len(docs) != 2 {
		t.Fatalf("both usage documents must publish: %v %+v", err, docs)
	}
	evidenceByDoc := map[string]domain.KnowledgeEvidence{}
	for _, d := range docs {
		detail, err := h.svc.GetKnowledgeDocument(ctx, h.wsID, d.ID, "", 0)
		if err != nil || len(detail.Assertions) != 1 {
			t.Fatalf("document %s has no assertion: %v", d.ID, err)
		}
		ev, binding, _, err := h.svc.GetKnowledgeEvidence(ctx, h.wsID, evidenceIDOf(t, detail.Assertions[0].EvidenceJSON))
		if err != nil {
			t.Fatal(err)
		}
		evidenceByDoc[d.Title] = *ev
		if binding.SourceID != "" && binding.Consumer == "" {
			t.Fatalf("evidence must record which usage binding it came from: %+v", binding)
		}
	}
	order, device := evidenceByDoc["订单服务"], evidenceByDoc["设备服务"]
	if order.ID == "" || device.ID == "" {
		t.Fatalf("both evidences must exist: %+v", evidenceByDoc)
	}
	if order.BindingID == device.BindingID {
		t.Fatalf("the two usages must not share one binding: %s", order.BindingID)
	}
	if order.ID == device.ID {
		t.Fatalf("evidence identity must be scoped to the binding, got one id %s", order.ID)
	}
}

// TestKnowledgeLibraryAgentReadsFrozenSnapshot covers F2: the library agent is
// pointed at the frozen export of the pinned commit, and mutating the source
// repository after capture changes neither that export nor the evidence.
func TestKnowledgeLibraryAgentReadsFrozenSnapshot(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	h.registerAllSources(t)
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "test", ClientKey: "frozen-1",
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	task := h.headTask(t)
	if task == nil || task.SnapshotID == "" {
		t.Fatalf("no frozen snapshot: %+v", task)
	}
	// The brief must hand the agent the frozen export, never the live path.
	brief, err := os.ReadFile(filepath.Join(h.stagingDir(t), "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(brief), "/snapshots/") || !strings.Contains(string(brief), "/tree/") {
		t.Fatalf("brief must point at the frozen tree:\n%s", string(brief))
	}
	snapshotForTree, err := h.store.Library().GetSnapshot(ctx, task.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	kl, err := knowledgelib.NewLibrary(h.libRoot)
	if err != nil {
		t.Fatal(err)
	}
	frozenRoot := filepath.Join(h.libRoot, "_system", "snapshots", task.SnapshotID, "tree")
	const rel = "src/main/java/com/example/order/OrderService.java"
	frozenFile := ""
	for _, b := range snapshotForTree.Bindings {
		if b.SourceName != "order-service" {
			continue
		}
		candidate := filepath.Join(kl.SnapshotTreeDirFor(b.SnapshotID, b.ID), filepath.FromSlash(rel))
		if _, statErr := os.Stat(candidate); statErr == nil {
			frozenFile = candidate
		}
	}
	if frozenFile == "" {
		entries, _ := os.ReadDir(frozenRoot)
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("frozen tree for order-service missing under %s (entries: %v)", frozenRoot, names)
	}
	before, err := os.ReadFile(frozenFile)
	if err != nil {
		t.Fatalf("frozen file missing: %v", err)
	}
	// Now mutate the live repository. The frozen copy must not follow.
	liveFile := filepath.Join(h.fixture.repos["order-service"], filepath.FromSlash(rel))
	if err := os.WriteFile(liveFile, []byte("// live edit after capture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("git", "-C", h.fixture.repos["order-service"], "checkout", "--", rel).Run() })
	after, err := os.ReadFile(frozenFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("frozen tree followed a live edit; the agent would read moving input")
	}

	// Evidence collected now must come from the frozen snapshot, not the edit.
	span := lineSpan(t, h.fixture.repos["order-service"], rel)
	writeStaging(t, h.stagingDir(t), map[string]string{
		"evidence.yaml": "evidence:\n" +
			"  - key: ev-frozen\n" +
			"    binding: order-service\n" +
			"    path: " + rel + "\n" +
			"    locator:\n      kind: source_text\n      interval: half_open\n" +
			"      start: {line: 0, column: 0}\n      end: {line: " + span + ", column: 0}\n",
		"content/components/order-service.md": libraryDoc(
			"doc:order-service", "订单服务", "assertion:frozen-read", "订单服务在取消分支发布取消事件。",
			"ev-frozen", []string{"entity:service:order-service"}),
		"plan.json": `{"summary":"冻结读取","documents":["content/components/order-service.md"],"removals":[],"renames":[],"coverage":{"sources_read":["order-service"],"sources_missed":[],"gaps":[],"notes":""}}`,
	})
	h.completeAgentTurn(t, domain.RunSucceeded)
	h.tick(t)

	release, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, "")
	if err != nil {
		t.Fatalf("publish must succeed even though the worktree moved on: %v", err)
	}
	docs, _, err := h.svc.ListKnowledgeDocuments(ctx, h.wsID, release.ID, "", "", 0)
	if err != nil || len(docs) != 1 {
		t.Fatalf("document missing: %v", err)
	}
	detail, err := h.svc.GetKnowledgeDocument(ctx, h.wsID, docs[0].ID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	ev, _, rep, err := h.svc.GetKnowledgeEvidence(ctx, h.wsID, evidenceIDOf(t, detail.Assertions[0].EvidenceJSON))
	if err != nil {
		t.Fatal(err)
	}
	// The published Markdown must cite the canonical evidence ID, not the
	// staging key, so a reader can resolve it without the staging directory.
	if strings.Contains(detail.Version.ContentMarkdown, "evidence_id: ev-order-cancel\n") {
		t.Fatalf("published markdown still cites the staging key:\n%s", detail.Version.ContentMarkdown)
	}
	if !strings.Contains(detail.Version.ContentMarkdown, "evidence_id: "+ev.ID) {
		t.Fatalf("published markdown must cite the canonical evidence id %s", ev.ID)
	}
	if detail.Version.ContentDigest != knowledgelib.DigestMarkdown([]byte(detail.Version.ContentMarkdown)) {
		t.Fatalf("the published digest must cover the canonical markdown")
	}
	if strings.Contains(ev.Excerpt, "live edit after capture") {
		t.Fatalf("evidence came from the mutated worktree, not the frozen snapshot: %q", ev.Excerpt)
	}
	if rep.ContentDigest == "" || rep.Origin != "committed" {
		t.Fatalf("evidence must be pinned to the committed representation: %+v", rep)
	}
}

// TestKnowledgeLibraryLateRunCannotOverwrite covers F2: a superseded run that
// reports terminal after the task already moved on cannot change published
// content, because ingestion reads the task's current run only.
func TestKnowledgeLibraryLateRunCannotOverwrite(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	h.registerAllSources(t)
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "test", ClientKey: "late-1",
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	staleRun := h.headTask(t).CurrentRunID
	staleStaging := h.stagingDir(t)
	files := orderServiceAssertionEvidence(t, h.fixture.repos["order-service"])
	files["content/components/order-service.md"] = libraryDoc(
		"doc:order-service", "订单服务", "assertion:first-version", "第一版结论。",
		"ev-order-cancel", []string{"entity:service:order-service"})
	files["plan.json"] = `{"summary":"第一版","documents":["content/components/order-service.md"],"removals":[],"renames":[],"coverage":{"sources_read":[],"sources_missed":[],"gaps":[],"notes":""}}`
	writeStaging(t, h.stagingDir(t), files)
	h.completeAgentTurn(t, domain.RunSucceeded)
	h.tick(t)

	first, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, "")
	if err != nil {
		t.Fatal(err)
	}
	// Rewrite the staging directory the way a stale run would, then replay the
	// old run's terminal hook. Nothing may change.
	writeStaging(t, staleStaging, map[string]string{
		"evidence.yaml": "evidence: []\n",
		"content/components/order-service.md": libraryDoc(
			"doc:order-service", "订单服务", "assertion:late-overwrite", "迟到的结论。",
			"ev-none", []string{"entity:service:order-service"}),
		"plan.json": `{"summary":"迟到覆盖","documents":["content/components/order-service.md"],"removals":[],"renames":[],"coverage":{"sources_read":[],"sources_missed":[],"gaps":[],"notes":""}}`,
	})
	if err := h.svc.NotifyKnowledgeLibraryTerminalForTest(ctx, h.wsID, staleRun); err != nil {
		t.Fatal(err)
	}
	h.tick(t)

	releases, err := h.svc.ListKnowledgeReleases(ctx, h.wsID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 1 || releases[0].ID != first.ID {
		t.Fatalf("a late run created a second release: %+v", releases)
	}
	docs, _, err := h.svc.ListKnowledgeDocuments(ctx, h.wsID, first.ID, "", "", 0)
	if err != nil || len(docs) != 1 {
		t.Fatalf("documents changed: %v %+v", err, docs)
	}
	detail, err := h.svc.GetKnowledgeDocument(ctx, h.wsID, docs[0].ID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Assertions) != 1 || detail.Assertions[0].ID != "assertion:first-version" {
		t.Fatalf("late content overwrote published knowledge: %+v", detail.Assertions)
	}
}

// TestKnowledgeLibraryPublishRecovery covers F2: an interrupted publish
// recovers from durable records to the same content version, and never
// publishes twice.
func TestKnowledgeLibraryPublishRecovery(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	h.registerAllSources(t)
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "test", ClientKey: "recover-1",
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	task := h.headTask(t)
	staging := h.stagingDir(t)
	files := orderServiceAssertionEvidence(t, h.fixture.repos["order-service"])
	files["content/components/order-service.md"] = libraryDoc(
		"doc:order-service", "订单服务", "assertion:recover", "中断恢复验证。",
		"ev-order-cancel", []string{"entity:service:order-service"})
	files["plan.json"] = `{"summary":"恢复","documents":["content/components/order-service.md"],"removals":[],"renames":[],"coverage":{"sources_read":[],"sources_missed":[],"gaps":[],"notes":""}}`
	writeStaging(t, staging, files)
	h.completeAgentTurn(t, domain.RunSucceeded)

	// Simulate a crash between "publish prepared" and the publish transaction:
	// the journal says prepared, but no release exists yet.
	projection, publishInput, err := h.buildProjectionForTest(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.Library().CreatePublication(ctx, &domain.KnowledgePublication{
		ID: "kpub_crashed", LibraryID: h.mustLibraryID(t), TaskID: task.ID,
		Status: "prepared", ProjectionDigest: publishInput.ProjectionDigest,
		PreparedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)

	rel, err := h.store.Library().ReleaseByProjectionDigest(ctx, h.mustLibraryID(t), projection.Digest)
	if err != nil {
		t.Fatalf("recovery did not publish the same projection: %v", err)
	}
	if rel.ProjectionDigest != publishInput.ProjectionDigest {
		t.Fatalf("recovered release digest mismatch: %s vs %s", rel.ProjectionDigest, publishInput.ProjectionDigest)
	}
	releases, err := h.svc.ListKnowledgeReleases(ctx, h.wsID, 10)
	if err != nil || len(releases) != 1 {
		t.Fatalf("recovery must publish exactly once: %v %+v", err, releases)
	}

	// Now simulate a crash after the publish transaction: the release and the
	// committed journal exist, but the task never recorded completion.
	head := h.headTask(t)
	if head != nil {
		t.Fatalf("task should be complete after recovery, got %+v", head)
	}
	if err := h.rewindCompletedTaskForTest(t, ctx, task.ID, rel.ID); err != nil {
		t.Fatal(err)
	}
	h.tick(t)

	releasesAfter, err := h.svc.ListKnowledgeReleases(ctx, h.wsID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(releasesAfter) != 1 || releasesAfter[0].ID != rel.ID {
		t.Fatalf("re-committing an already published task must not create a second release: %+v", releasesAfter)
	}
	final, err := h.store.Library().GetTask(ctx, h.mustLibraryID(t), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != domain.KnowledgeTaskCompleted || final.TargetReleaseID != rel.ID {
		t.Fatalf("recovered task must point at its original release: %+v", final)
	}
}

// TestKnowledgeLibraryBindingIdentityIsComplete covers F1b: the full usage
// identity (consumer + artifact + environment) decides binding identity and
// the frozen directory, so two artifacts or two environments never share one
// frozen copy.
func TestKnowledgeLibraryBindingIdentityIsComplete(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	h.registerSource(t, "order-service", domain.SourceKindService)
	// One consumer, two artifact versions, two environments: four bindings of
	// one logical repository.
	h.registerSource(t, "common", domain.SourceKindCommon,
		domain.KnowledgeSourceUsage{Consumer: "order-service", Artifact: "com.example:common:1.4.2", Environment: "prod"},
		domain.KnowledgeSourceUsage{Consumer: "order-service", Artifact: "com.example:common:2.0.0", Environment: "prod"},
		domain.KnowledgeSourceUsage{Consumer: "order-service", Artifact: "com.example:common:1.4.2", Environment: "staging"},
		domain.KnowledgeSourceUsage{Consumer: "order-service", Artifact: "com.example:common:2.0.0", Environment: "staging"},
	)
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "test", ClientKey: "identity-1",
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	task := h.headTask(t)
	if task == nil {
		t.Fatal("no task after tick")
	}
	if task.SnapshotID == "" {
		t.Fatalf("task did not freeze a snapshot: status=%s err=%s", task.Status, task.LastError)
	}
	snapshot, err := h.store.Library().GetSnapshot(ctx, task.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	var common []domain.KnowledgeSnapshotBinding
	for _, b := range snapshot.Bindings {
		if b.SourceName == "common" {
			common = append(common, b)
		}
	}
	if len(common) != 4 {
		t.Fatalf("one consumer x two artifacts x two environments must be four bindings, got %d", len(common))
	}
	seenID := map[string]bool{}
	seenDir := map[string]bool{}
	kl, err := knowledgelib.NewLibrary(h.libRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range common {
		if seenID[b.ID] {
			t.Fatalf("binding identity collided: %s", b.ID)
		}
		seenID[b.ID] = true
		dir := kl.SnapshotTreeDirFor(b.SnapshotID, b.ID)
		if seenDir[dir] {
			t.Fatalf("two bindings share one frozen directory: %s", dir)
		}
		seenDir[dir] = true
		if _, statErr := os.Stat(dir); statErr != nil {
			t.Fatalf("frozen directory missing for %s: %v", b.ID, statErr)
		}
		if b.ArtifactResolution != "declared" {
			t.Fatalf("a registered artifact must be reported as declared, got %q", b.ArtifactResolution)
		}
	}
}

// TestKnowledgeLibraryEvidenceRejectsCrossBinding proves the database refuses
// evidence whose snapshot/binding/representation triple is inconsistent.
func TestKnowledgeLibraryEvidenceRejectsCrossBinding(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	h.registerAllSources(t)
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "test", ClientKey: "xref-1",
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	task := h.headTask(t)
	snapshot, err := h.store.Library().GetSnapshot(ctx, task.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	var common []domain.KnowledgeSnapshotBinding
	for _, b := range snapshot.Bindings {
		if b.SourceName == "common" {
			common = append(common, b)
		}
	}
	if len(common) != 2 {
		t.Fatalf("common must have two usage bindings, got %d", len(common))
	}
	now := time.Now().UTC()
	// Freeze one representation for the first usage.
	rep := &domain.KnowledgeRepresentation{
		ID: "repr_cross", BindingID: common[0].ID, Path: "src/main/java/com/example/common/messaging/EventPublisher.java",
		MediaType: "text/x-java", Encoding: "utf-8", DigestAlgo: "sha256",
		ContentDigest: "sha256:deadbeef", ByteSize: 10, Coverage: "full", Transform: "",
		StoredPath: "sha256/deadbeef.bin", Origin: "committed", CreatedAt: now,
	}
	stored, err := h.store.Library().EnsureRepresentation(ctx, rep)
	if err != nil {
		t.Fatal(err)
	}
	// Citing that representation from the OTHER usage must be refused.
	crossErr := h.store.Library().InsertEvidence(ctx, &domain.KnowledgeEvidence{
		ID: "evidence:cross", LibraryID: h.mustLibraryID(t), SnapshotID: snapshot.ID,
		BindingID: common[1].ID, RepresentationID: stored.ID, LocatorJSON: "{}", LocatorKind: "source_text",
		Excerpt: "x", ExcerptDigest: "sha256:x", MatchCount: 1, Availability: "available", CollectedAt: now,
	})
	if crossErr == nil {
		t.Fatal("evidence citing another binding's representation must be refused")
	}
	// Citing it from its own binding is accepted.
	if err := h.store.Library().InsertEvidence(ctx, &domain.KnowledgeEvidence{
		ID: "evidence:own", LibraryID: h.mustLibraryID(t), SnapshotID: snapshot.ID,
		BindingID: common[0].ID, RepresentationID: stored.ID, LocatorJSON: "{}", LocatorKind: "source_text",
		Excerpt: "x", ExcerptDigest: "sha256:x", MatchCount: 1, Availability: "available", CollectedAt: now,
	}); err != nil {
		t.Fatalf("consistent evidence must be accepted: %v", err)
	}
}

// TestKnowledgeLibraryCoverageState covers F4: an unfinished task must not be
// presented as a verified "no gaps" result.
func TestKnowledgeLibraryCoverageState(t *testing.T) {
	cases := []struct {
		name     string
		coverage string
		terminal bool
		want     string
	}{
		{name: "running_empty", coverage: "", terminal: false, want: "pending"},
		{name: "running_reported", coverage: `{"sources_read":["a"]}`, terminal: false, want: "in_progress"},
		{name: "finished_empty", coverage: "{}", terminal: true, want: "not_computed"},
		{name: "finished_reported", coverage: `{"sources_read":["a"],"gaps":[]}`, terminal: true, want: "reported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := httpapi.CoverageStateForTest(tc.coverage, tc.terminal); got != tc.want {
				t.Fatalf("coverage state = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestKnowledgeLibraryQueuedIncrementsStackOnLiveBaseline covers F5.1: two
// increments queued from the same release, each editing a different document,
// must both survive. The second task must build on the first one's release,
// not on the stale baseline captured when it was enqueued.
func TestKnowledgeLibraryQueuedIncrementsStackOnLiveBaseline(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	h.registerAllSources(t)
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "test", ClientKey: "stack-init",
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	files := orderServiceAssertionEvidence(t, h.fixture.repos["order-service"])
	files["content/components/order-service.md"] = libraryDoc(
		"doc:order-service", "订单服务", "assertion:order-v1", "订单服务第一版结论。",
		"ev-order-cancel", []string{"entity:service:order-service"})
	files["content/components/device-service.md"] = libraryDoc(
		"doc:device-service", "设备服务", "assertion:device-v1", "设备服务第一版结论。",
		"ev-order-cancel", []string{"entity:service:device-service"})
	files["plan.json"] = `{"summary":"初始两篇","documents":["content/components/order-service.md","content/components/device-service.md"],"removals":[],"renames":[],"coverage":{"sources_read":[],"sources_missed":[],"gaps":[],"notes":""}}`
	writeStaging(t, h.stagingDir(t), files)
	h.completeAgentTurn(t, domain.RunSucceeded)
	h.tick(t)
	r0, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, "")
	if err != nil {
		t.Fatal(err)
	}

	// Two increments are accepted while nothing is running: both see R0.
	first, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "code.changed", Source: "git-hook", ClientKey: "stack-t1",
		Subject: map[string]any{"changed_paths": []any{"OrderService.java"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "code.changed", Source: "git-hook", ClientKey: "stack-t2",
		Subject: map[string]any{"changed_paths": []any{"DeviceReservationConsumer.java"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.QueueSeq != 2 || second.QueueSeq != 3 {
		t.Fatalf("queue order wrong: %d %d", first.QueueSeq, second.QueueSeq)
	}

	// T1 edits only the order-service document.
	h.tick(t)
	t1 := h.headTask(t)
	if t1.BaseReleaseID != r0.ID {
		t.Fatalf("T1 baseline should be R0: %s", t1.BaseReleaseID)
	}
	ev := orderServiceAssertionEvidence(t, h.fixture.repos["order-service"])
	ev["content/components/order-service.md"] = libraryDoc(
		"doc:order-service", "订单服务", "assertion:order-v2", "订单服务第二版结论。",
		"ev-order-cancel", []string{"entity:service:order-service"})
	ev["plan.json"] = `{"summary":"T1 只改订单服务","documents":["content/components/order-service.md"],"removals":[],"renames":[],"coverage":{"sources_read":[],"sources_missed":[],"gaps":[],"notes":""}}`
	writeStaging(t, h.stagingDir(t), ev)
	h.completeAgentTurn(t, domain.RunSucceeded)
	h.tick(t)
	r1, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, "")
	if err != nil {
		t.Fatal(err)
	}
	if r1.Seq != 2 || r1.ParentReleaseID != r0.ID {
		t.Fatalf("R1 must chain to R0: %+v", r1)
	}

	// T2 edits only the device-service document. It was enqueued when R0 was
	// current, but it must carry forward R1's order-service document.
	h.tick(t)
	t2 := h.headTask(t)
	if t2 == nil || t2.Seq != 3 {
		t.Fatalf("T2 should be the head: %+v", t2)
	}
	if t2.BaseReleaseID != r1.ID {
		t.Fatalf("T2 must re-resolve its baseline to the live release R1, got %s", t2.BaseReleaseID)
	}
	ev2 := orderServiceAssertionEvidence(t, h.fixture.repos["order-service"])
	ev2["content/components/device-service.md"] = libraryDoc(
		"doc:device-service", "设备服务", "assertion:device-v2", "设备服务第二版结论。",
		"ev-order-cancel", []string{"entity:service:device-service"})
	ev2["plan.json"] = `{"summary":"T2 只改设备服务","documents":["content/components/device-service.md"],"removals":[],"renames":[],"coverage":{"sources_read":[],"sources_missed":[],"gaps":[],"notes":""}}`
	writeStaging(t, h.stagingDir(t), ev2)
	h.completeAgentTurn(t, domain.RunSucceeded)
	h.tick(t)
	r2, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, "")
	if err != nil {
		t.Fatal(err)
	}
	if r2.Seq != 3 || r2.ParentReleaseID != r1.ID {
		t.Fatalf("R2 must chain to R1: %+v", r2)
	}
	docs, _, err := h.svc.ListKnowledgeDocuments(ctx, h.wsID, r2.ID, "", "", 0)
	if err != nil || len(docs) != 2 {
		t.Fatalf("R2 must still carry both documents: %v %+v", err, docs)
	}
	byPath := map[string]domain.KnowledgeDocument{}
	for _, d := range docs {
		byPath[d.Path] = *d
	}
	orderDoc, ok := byPath["content/components/order-service.md"]
	if !ok {
		t.Fatalf("T1's document was dropped by T2: %+v", byPath)
	}
	detail, err := h.svc.GetKnowledgeDocument(ctx, h.wsID, orderDoc.ID, r2.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Assertions) != 1 || detail.Assertions[0].ID != "assertion:order-v2" {
		t.Fatalf("T2 lost T1's edit: %+v", detail.Assertions)
	}
}

// TestKnowledgeLibraryMissingBaselineDoesNotWipeContent covers F5.2: when a
// non-initial task has no resolvable baseline it must stop instead of
// publishing an empty-parent release that would clear the content zone.
func TestKnowledgeLibraryMissingBaselineDoesNotWipeContent(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	h.registerAllSources(t)
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "code.changed", Source: "git-hook", ClientKey: "nobase-1",
	}); err != nil {
		t.Fatal(err)
	}
	// First publish a real release, then point the library at a dangling
	// baseline, which is what a corrupted or partially cleaned state looks like.
	h.tick(t)
	ev := orderServiceAssertionEvidence(t, h.fixture.repos["order-service"])
	ev["content/components/order-service.md"] = libraryDoc(
		"doc:order-service", "订单服务", "assertion:baseline", "基线内容。",
		"ev-order-cancel", []string{"entity:service:order-service"})
	ev["plan.json"] = `{"summary":"基线","documents":["content/components/order-service.md"],"removals":[],"renames":[],"coverage":{"sources_read":[],"sources_missed":[],"gaps":[],"notes":""}}`
	writeStaging(t, h.stagingDir(t), ev)
	h.completeAgentTurn(t, domain.RunSucceeded)
	h.tick(t)
	if _, err := h.store.Library().CurrentRelease(ctx, h.mustLibraryID(t)); err != nil {
		t.Fatalf("baseline release missing: %v", err)
	}
	// The library now claims a baseline release that does not exist.
	if _, err := h.db.ExecContext(ctx, `UPDATE knowledge_libraries SET current_release_id='rel_missing' WHERE id=?`,
		h.mustLibraryID(t)); err != nil {
		t.Fatal(err)
	}
	// A new event must still be accepted, and the worker must refuse to write
	// because the library claims a baseline that cannot be resolved.
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "code.changed", Source: "git-hook", ClientKey: "nobase-2",
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	task := h.headTask(t)
	if task == nil {
		t.Fatal("no task")
	}
	if task.Status != domain.KnowledgeTaskBlocked {
		t.Fatalf("a task with an unresolvable baseline must block, got status=%s err=%s", task.Status, task.LastError)
	}
	if task.BlockedReason == "" {
		t.Fatal("the block must carry a reason")
	}
	releases, err := h.svc.ListKnowledgeReleases(ctx, h.wsID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 1 || releases[0].Seq != 1 {
		t.Fatalf("a blocked task must not publish anything new: %+v", releases)
	}
}

// TestKnowledgeLibraryHistoricalReleaseReadIsPinned covers F5.3: after a
// rename and a content change, opening a document from an older release must
// show that release's path, title and body — never the current ones.
func TestKnowledgeLibraryHistoricalReleaseReadIsPinned(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	h.registerAllSources(t)
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "test", ClientKey: "pin-1",
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	files := orderServiceAssertionEvidence(t, h.fixture.repos["order-service"])
	files["content/components/order-service.md"] = libraryDoc(
		"doc:order-service", "订单服务（旧标题）", "assertion:pinned", "旧版本的陈述。",
		"ev-order-cancel", []string{"entity:service:order-service"})
	files["plan.json"] = `{"summary":"旧版本","documents":["content/components/order-service.md"],"removals":[],"renames":[],"coverage":{"sources_read":[],"sources_missed":[],"gaps":[],"notes":""}}`
	writeStaging(t, h.stagingDir(t), files)
	h.completeAgentTurn(t, domain.RunSucceeded)
	h.tick(t)
	old, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, "")
	if err != nil {
		t.Fatal(err)
	}
	docsOld, _, err := h.svc.ListKnowledgeDocuments(ctx, h.wsID, old.ID, "", "", 0)
	if err != nil || len(docsOld) != 1 {
		t.Fatalf("old release documents: %v %+v", err, docsOld)
	}
	oldDocID := docsOld[0].ID
	oldPath := docsOld[0].Path

	// Rename the document and change its content in a second release.
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "document.revised", Source: "business-harness", ClientKey: "pin-2",
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	moved := orderServiceAssertionEvidence(t, h.fixture.repos["order-service"])
	moved["content/decisions/order-service.md"] = libraryDoc(
		"doc:order-service", "订单服务（新标题）", "assertion:pinned", "新版本的陈述。",
		"ev-order-cancel", []string{"entity:service:order-service"})
	moved["plan.json"] = `{"summary":"改名与改内容","documents":["content/decisions/order-service.md"],"removals":[],"renames":[{"document_id":"doc:order-service","from_path":"` + oldPath + `","to_path":"content/decisions/order-service.md","note":"归档到 decisions"}],"coverage":{"sources_read":[],"sources_missed":[],"gaps":[],"notes":""}}`
	writeStaging(t, h.stagingDir(t), moved)
	h.completeAgentTurn(t, domain.RunSucceeded)
	h.tick(t)
	second := h.headTask(t)
	if second != nil {
		t.Fatalf("second task did not publish: status=%s err=%s", second.Status, second.LastError)
	}
	current, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, "")
	if err != nil {
		t.Fatal(err)
	}
	if current.Seq != 2 {
		t.Fatalf("second release missing: %+v", current)
	}

	// The historical release must still show the old path and the old body.
	oldDetail, err := h.svc.GetKnowledgeDocument(ctx, h.wsID, oldDocID, old.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !oldDetail.Pinned || oldDetail.ReleaseID != old.ID {
		t.Fatalf("historical read must be pinned: %+v", oldDetail)
	}
	if oldDetail.Document.Path != oldPath {
		t.Fatalf("historical path leaked the current value: %s vs %s", oldDetail.Document.Path, oldPath)
	}
	if !strings.Contains(oldDetail.Version.ContentMarkdown, "旧版本的陈述") {
		t.Fatalf("historical read returned current content")
	}
	if oldDetail.Document.Title != "订单服务（旧标题）" {
		t.Fatalf("historical title leaked the current value: %s", oldDetail.Document.Title)
	}
	// The current release shows the new path and body.
	newDetail, err := h.svc.GetKnowledgeDocument(ctx, h.wsID, oldDocID, current.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if newDetail.Document.Path != "content/decisions/order-service.md" {
		t.Fatalf("current release path wrong: %s", newDetail.Document.Path)
	}
	if !strings.Contains(newDetail.Version.ContentMarkdown, "新版本的陈述") {
		t.Fatalf("current release content wrong")
	}
	// Expanding an assertion is scoped to the release it came from.
	pinned, _, err := h.svc.ExpandKnowledgeAssertion(ctx, h.wsID, old.ID, "assertion:pinned")
	if err != nil {
		t.Fatal(err)
	}
	if pinned.DocumentVersionID != oldDetail.Version.ID {
		t.Fatalf("expand must resolve inside the pinned release")
	}
}

// TestKnowledgeSourceResolvedSurvivesNormalEdit covers F8's second half: a
// genuine, evidence-backed resolution record must not be destroyed by editing
// an unrelated field.
func TestKnowledgeSourceResolvedSurvivesNormalEdit(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	created, err := h.svc.CreateKnowledgeSource(ctx, h.wsID, application.KnowledgeLibrarySourceInput{
		Name: "common", Kind: domain.SourceKindCommon, RepoPath: h.fixture.repos["common"], DefaultRef: "main",
		Usages: []domain.KnowledgeSourceUsage{
			{Consumer: "order-service", Artifact: "com.example:common:1.4.2", ArtifactResolution: "resolved", ResolutionRef: "mvn-dependency-tree:order/target/tree.txt"},
			{Consumer: "device-service", Artifact: "com.example:common:2.0.0", ArtifactResolution: "resolved"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Usages) != 2 {
		t.Fatalf("usages lost on create: %+v", created.Usages)
	}
	// No resolver exists in this build, so even a well-formed reference stays a
	// claim: the effective state is a declaration and the reference is kept as
	// an unverified note.
	if created.Usages[0].ArtifactState() != "declared" {
		t.Fatalf("no claim may become a verified state: %+v", created.Usages[0])
	}
	if created.Usages[0].ResolutionRef != "mvn-dependency-tree:order/target/tree.txt" {
		t.Fatalf("the caller's reference must be preserved: %+v", created.Usages[0])
	}
	if !created.Usages[0].ResolutionDowngraded() {
		t.Fatalf("the downgrade must be reported: %+v", created.Usages[0])
	}
	// An unsupported claim keeps its raw text but its effective state is the
	// declaration it really is, and the downgrade stays visible.
	if created.Usages[1].ArtifactState() != "declared" {
		t.Fatalf("a claim without a basis must resolve to declared: %+v", created.Usages[1])
	}
	if !created.Usages[1].ResolutionDowngraded() {
		t.Fatalf("the record must keep saying the claim lacked a basis: %+v", created.Usages[1])
	}

	// Edit only default_ref, sending the loaded usages back unchanged, which is
	// exactly what the admin form does.
	updated, err := h.svc.UpdateKnowledgeSource(ctx, h.wsID, created.ID, created.Version,
		application.KnowledgeLibrarySourceInput{
			Name: created.Name, Kind: created.Kind, RepoPath: created.RepoPath, DefaultRef: "main",
			Usages: created.Usages,
		})
	if err != nil {
		t.Fatal(err)
	}
	if updated.DefaultRef != "main" {
		t.Fatalf("the edited field did not change: %+v", updated)
	}
	if len(updated.Usages) != 2 || updated.Usages[0].ArtifactState() != "declared" ||
		updated.Usages[0].ResolutionRef != "mvn-dependency-tree:order/target/tree.txt" {
		t.Fatalf("editing an unrelated field destroyed the recorded declaration: %+v", updated.Usages)
	}
	// And the binding carries the basis forward.
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "test", ClientKey: "f8-1",
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	task := h.headTask(t)
	snapshot, err := h.store.Library().GetSnapshot(ctx, task.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range snapshot.Bindings {
		if b.SourceName != "common" {
			continue
		}
		if b.Consumer == "order-service" {
			if b.ArtifactResolution != "declared" || b.ArtifactResolutionRef == "" {
				t.Fatalf("the binding must carry the declared state plus the caller's note: %+v", b)
			}
		}
		if b.Consumer == "device-service" && b.ArtifactResolution != "declared" {
			t.Fatalf("an unsupported claim reached the binding as %q", b.ArtifactResolution)
		}
	}
}

// TestKnowledgeLibraryFrozenWorktreePathIsShared covers the bug the acceptance
// agent found: the writer and the reader must agree on the frozen worktree
// directory, or a frozen untracked file looks missing and its evidence can
// never be collected.
func TestKnowledgeLibraryFrozenWorktreePathIsShared(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	h.registerAllSources(t)
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "test", ClientKey: "wt-1",
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	task := h.headTask(t)
	snapshot, err := h.store.Library().GetSnapshot(ctx, task.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	kl, err := knowledgelib.NewLibrary(h.libRoot)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, b := range snapshot.Bindings {
		if len(b.Untracked) == 0 {
			continue
		}
		dir := kl.SnapshotWorktreeDirFor(b.SnapshotID, b.ID)
		for _, rel := range b.Untracked {
			if _, statErr := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); statErr != nil {
				t.Fatalf("frozen untracked file %s of binding %s is not readable at %s: %v", rel, b.ID, dir, statErr)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("the fixture should have frozen at least one untracked file")
	}
}

// TestKnowledgeLibraryRestartReplaysInsteadOfConflicting covers recovery: a
// task that was interrupted mid-turn must be resumable after a restart without
// minting a second WorkItem or failing on the run idempotency key.
func TestKnowledgeLibraryRestartReplaysInsteadOfConflicting(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	h.registerAllSources(t)
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "test", ClientKey: "restart-1",
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	first := h.headTask(t)
	if first == nil || first.CurrentRunID == "" {
		t.Fatalf("first dispatch missing: %+v", first)
	}
	workItemID := first.WorkItemID

	// Simulate a crash: the process dies while the turn is in flight, and the
	// restarted worker puts the head back in the queue.
	if _, err := h.db.ExecContext(ctx, `UPDATE knowledge_write_tasks
		SET status='queued', current_run_id=NULL, owner_token='', updated_at=? WHERE id=?`,
		time.Now().UTC(), first.ID); err != nil {
		t.Fatal(err)
	}
	h.tick(t)

	resumed := h.headTask(t)
	if resumed == nil {
		t.Fatal("task disappeared after restart")
	}
	if resumed.Status != domain.KnowledgeTaskRunning {
		t.Fatalf("a restarted task must resume, got status=%s err=%s", resumed.Status, resumed.LastError)
	}
	if resumed.WorkItemID != workItemID {
		t.Fatalf("recovery must reuse the task's WorkItem: %s vs %s", resumed.WorkItemID, workItemID)
	}
	runs, err := h.store.Runs().ListByWorkItem(ctx, workItemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("recovery must replay the same turn, not mint a second run: %d", len(runs))
	}
}

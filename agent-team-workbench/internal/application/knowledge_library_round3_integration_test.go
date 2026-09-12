package application_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/httpapi"
	"github.com/ybs/agent-team-workbench/internal/knowledgelib"
)

// execGit runs one git command in a fixture repository.
func execGit(t *testing.T, repo string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// publishInitializeRelease drives one real initialize task through the whole
// write path and returns the published release. Round-3 tests build on a
// published library rather than reaching into staging by hand.
func publishInitializeRelease(t *testing.T, h *libraryHarness) *domain.KnowledgeRelease {
	t.Helper()
	ctx := context.Background()
	h.registerAllSources(t)
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "test", ClientKey: "init-round3",
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	staging := h.stagingDir(t)
	files := orderServiceAssertionEvidence(t, h.fixture.repos["order-service"])
	files["content/components/order-service.md"] = libraryDoc(
		"doc:order-service", "订单服务", "assertion:order-cancel-publishes", "订单服务在取消分支发布取消事件。",
		"ev-order-cancel", []string{"entity:service:order-service"})
	files["entities.yaml"] = "entities: []\n"
	files["plan.json"] = `{"summary":"初始化","documents":["content/components/order-service.md"],` +
		`"removals":[],"renames":[],"coverage":{"sources_read":["order-service"],"sources_missed":[],` +
		`"gaps":[],"notes":""}}`
	writeStaging(t, staging, files)
	h.completeAgentTurn(t, domain.RunSucceeded)
	h.tick(t)
	release, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, "")
	if err != nil {
		t.Fatal(err)
	}
	return release
}

// TestKnowledgeRequirementTextReachesTheBrief covers the requirement-import
// boundary: what an external system submitted as the requirement body must be
// in the agent's staging directory, verbatim, together with the rule that a
// requirement is a declared ask and never proof of implementation.
func TestKnowledgeRequirementTextReachesTheBrief(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	h.registerAllSources(t)

	// The referenced document is the authority over a payload summary.
	refPath := filepath.Join(h.fixture.root, "req-kb-accept-01-v2.md")
	if err := os.WriteFile(refPath, []byte("REQ-KB-ACCEPT-01 v2：受理超时时间由 17分钟 调整为 23分钟。\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "requirement.imported", Source: "business-harness", ClientKey: "req-v2",
		ContentRef: refPath,
		Subject: map[string]any{
			"requirement_id": "REQ-KB-ACCEPT-01", "requirement_version": "v2",
			"changed_paths": []any{"content/business/rules/acceptance.md"},
		},
		Payload: map[string]any{
			"title": "受理时限调整", "text": "摘要：受理时限调整为 23 分钟。",
			"acceptance_criteria": []any{"老版本仍可查询到 17 分钟"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)

	task := h.headTask(t)
	if task == nil || task.StagingPath == "" {
		t.Fatalf("requirement task was not prepared: %+v", task)
	}
	reqRaw, err := os.ReadFile(filepath.Join(task.StagingPath, knowledgelib.RequirementFileName))
	if err != nil {
		t.Fatalf("the requirement body must be handed over as a staging file: %v", err)
	}
	if !strings.Contains(string(reqRaw), "23分钟") || strings.Contains(string(reqRaw), "摘要：受理时限调整为 23 分钟。") {
		t.Fatalf("the referenced document, not the payload summary, is the requirement body: %s", reqRaw)
	}
	briefRaw, err := os.ReadFile(filepath.Join(task.StagingPath, "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	brief := string(briefRaw)
	for _, want := range []string{"本次需求原文", "REQ-KB-ACCEPT-01", "23分钟", "acceptance_criteria", refPath} {
		if !strings.Contains(brief, want) {
			t.Fatalf("brief must carry %q; got:\n%s", want, brief)
		}
	}
	// A requirement is an ask, not a status: the brief must forbid both
	// claiming implementation and writing approval state.
	for _, want := range []string{"不是「现状」", "已实现", "会被整篇拒绝"} {
		if !strings.Contains(brief, want) {
			t.Fatalf("brief must state the requirement/implementation boundary (%q)", want)
		}
	}
	if !strings.Contains(task.FocusJSON, "REQ-KB-ACCEPT-01") {
		t.Fatalf("the requirement identity must survive into the task focus: %s", task.FocusJSON)
	}
}

// TestKnowledgeRequirementWithoutReadableTextSaysSo: a requirement event whose
// body cannot be read must produce an explicit gap, never an invented one.
func TestKnowledgeRequirementWithoutReadableTextSaysSo(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	h.registerAllSources(t)
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "requirement.imported", Source: "business-harness", ClientKey: "req-missing",
		ContentRef: filepath.Join(h.fixture.root, "does-not-exist.md"),
		Subject:    map[string]any{"requirement_id": "REQ-KB-ACCEPT-99"},
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	task := h.headTask(t)
	briefRaw, err := os.ReadFile(filepath.Join(task.StagingPath, "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	brief := string(briefRaw)
	if !strings.Contains(brief, "这段需求的原文没有取到") || !strings.Contains(brief, "coverage.gaps") {
		t.Fatalf("an unreadable requirement must be reported, not guessed:\n%s", brief)
	}
	if _, err := os.Stat(filepath.Join(task.StagingPath, knowledgelib.RequirementFileName)); !os.IsNotExist(err) {
		t.Fatal("no requirement.md may be written when there is no text")
	}
}

// TestKnowledgeReindexIsQueuedBehindTheHead covers the ordering rule for the
// index rebuild: it is a knowledge write, so it enters the FIFO queue behind
// any pending publish instead of rebuilding from a version about to change.
func TestKnowledgeReindexIsQueuedBehindTheHead(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	h.registerAllSources(t)

	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "test", ClientKey: "init-queue",
	}); err != nil {
		t.Fatal(err)
	}
	receipt, err := h.svc.ReindexKnowledgeLibrary(ctx, h.wsID)
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Accepted || receipt.Status != domain.KnowledgeTaskQueued || receipt.QueueSeq != 2 {
		t.Fatalf("reindex must return a queue receipt behind the pending initialize: %+v", receipt)
	}
	if head := h.headTask(t); head == nil || head.Kind == knowledgelib.TaskReindex {
		t.Fatalf("reindex must not jump the queue: %+v", head)
	}

	// Finish the initialize task; the queued reindex then runs at the head.
	h.tick(t)
	staging := h.stagingDir(t)
	files := orderServiceAssertionEvidence(t, h.fixture.repos["order-service"])
	files["content/components/order-service.md"] = libraryDoc(
		"doc:order-service", "订单服务", "assertion:order-cancel-publishes", "订单服务在取消分支发布取消事件。",
		"ev-order-cancel", []string{"entity:service:order-service"})
	files["entities.yaml"] = "entities: []\n"
	files["plan.json"] = `{"summary":"初始化","documents":["content/components/order-service.md"],` +
		`"removals":[],"renames":[],"coverage":{"sources_read":["order-service"],"sources_missed":[],` +
		`"gaps":[],"notes":""}}`
	writeStaging(t, staging, files)
	h.completeAgentTurn(t, domain.RunSucceeded)
	h.tick(t)

	release, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, "")
	if err != nil {
		t.Fatal(err)
	}
	// A crash during the reindex leaves it running with no model run; the next
	// tick must re-execute the derived work instead of blocking the queue.
	if _, err := h.db.ExecContext(ctx, `UPDATE knowledge_write_tasks
		SET status='running', owner_token='' WHERE id=?`, receipt.TaskID); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	task, _, err := h.svc.GetKnowledgeWriteTask(ctx, h.wsID, receipt.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.KnowledgeTaskCompleted {
		t.Fatalf("an interrupted reindex must be re-executed, got %s (%s)", task.Status, task.LastError)
	}
	if task.TargetReleaseID != "" {
		t.Fatalf("a reindex must not publish a release: %s", task.TargetReleaseID)
	}
	after, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, "")
	if err != nil || after.ID != release.ID || after.Seq != release.Seq {
		t.Fatalf("reindex must not create a new release: %+v vs %+v", after, release)
	}
	answer, err := h.svc.QueryKnowledgeLibrary(ctx, application.KnowledgeLibraryQuery{
		WorkspaceID: h.wsID, Question: "订单服务是否发布取消事件",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(answer.Hits) == 0 {
		t.Fatalf("the rebuilt index must still answer: %+v", answer)
	}
}

// TestKnowledgeNonActiveViewIsRejected: this build keeps one active view, and
// it must say so instead of silently folding a second view into the baseline.
func TestKnowledgeNonActiveViewIsRejected(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	publishInitializeRelease(t, h)

	_, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "branch.switched", Source: "git-hook", ClientKey: "view-1",
		Subject: map[string]any{"view_id": "feature/kb-accept", "branch": "feature/kb-accept"},
	})
	if err == nil {
		t.Fatal("a second view must be refused, not merged into the baseline")
	}
	if !strings.Contains(err.Error(), "feature/kb-accept") || !strings.Contains(err.Error(), knowledgelib.DefaultViewID) {
		t.Fatalf("the refusal must name both views: %v", err)
	}
	if head := h.headTask(t); head != nil {
		t.Fatalf("a refused event must not enqueue work: %+v", head)
	}
	// A branch switch inside the one active view is a normal incremental read.
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "branch.switched", Source: "git-hook", ClientKey: "view-2",
		Payload: map[string]any{"branch": "main", "head_sha": "0f1e2d"},
	}); err != nil {
		t.Fatalf("a branch switch inside the active view is a normal incremental read: %v", err)
	}
	h.tick(t)
	task := h.headTask(t)
	if task == nil || task.Kind != knowledgelib.TaskIncremental {
		t.Fatalf("a branch switch in the active view is an incremental task: %+v", task)
	}
	if !strings.Contains(task.FocusJSON, "0f1e2d") || !strings.Contains(task.FocusJSON, "main") {
		t.Fatalf("payload commit facts must reach the task focus: %s", task.FocusJSON)
	}
	briefRaw, err := os.ReadFile(filepath.Join(task.StagingPath, "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(briefRaw), "0f1e2d") {
		t.Fatal("the reported commit must be visible in the brief")
	}
}

// TestKnowledgeRegisteredRefMustResolve: a ref the server can already disprove
// is refused when the source is saved, and a ref that disappears afterwards
// stops the task with a message naming it.
func TestKnowledgeRegisteredRefMustResolve(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	repo := h.fixture.repos["order-service"]
	if _, err := h.svc.CreateKnowledgeSource(ctx, h.wsID, application.KnowledgeLibrarySourceInput{
		Name: "order-service", Kind: domain.SourceKindService, RepoPath: repo, DefaultRef: "release/9.9",
	}); err == nil || !strings.Contains(err.Error(), "release/9.9") {
		t.Fatalf("an unresolvable ref must be refused at save time: %v", err)
	}
	// A ref that exists now but is gone later must stop the task, not silently
	// fall back to the checked-out commit.
	git := func(args ...string) string {
		t.Helper()
		out, err := execGit(t, repo, args...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return out
	}
	git("branch", "kb-temp", "HEAD")
	if _, err := h.svc.CreateKnowledgeSource(ctx, h.wsID, application.KnowledgeLibrarySourceInput{
		Name: "order-service", Kind: domain.SourceKindService, RepoPath: repo, DefaultRef: "kb-temp",
	}); err != nil {
		t.Fatal(err)
	}
	git("branch", "-D", "kb-temp")
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "test", ClientKey: "gone-1",
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	task := h.headTask(t)
	if task == nil || task.SnapshotID != "" {
		t.Fatalf("a vanished ref must not produce a snapshot: %+v", task)
	}
	if !strings.Contains(task.LastError, "kb-temp") {
		t.Fatalf("the failure must name the ref: %+v", task)
	}
}

// TestKnowledgeDocumentHistoryKeepsItsOwnPaths: a historical read reports the
// path and title pinned in that version, so a later rename cannot rewrite the
// past, and an old release can still resolve its own evidence citations.
func TestKnowledgeDocumentHistoryKeepsItsOwnPaths(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	first := publishInitializeRelease(t, h)

	docs, _, err := h.svc.ListKnowledgeDocuments(ctx, h.wsID, first.ID, "", "", 0)
	if err != nil || len(docs) != 1 {
		t.Fatalf("published document missing: %v", err)
	}
	docID := docs[0].ID

	// Second release: same document moved and retitled.
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "document.revised", Source: "business-harness", ClientKey: "rename-1",
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	staging := h.stagingDir(t)
	files := orderServiceAssertionEvidence(t, h.fixture.repos["order-service"])
	files["content/business/rules/order-service-rules.md"] = libraryDoc(
		"doc:order-service", "订单服务规则（改名后）", "assertion:order-cancel-publishes", "订单服务在取消分支发布取消事件。",
		"ev-order-cancel", []string{"entity:service:order-service"})
	files["entities.yaml"] = "entities: []\n"
	files["plan.json"] = `{"summary":"改名","documents":["content/business/rules/order-service-rules.md"],` +
		`"renames":[{"document_id":"doc:order-service","from_path":"content/components/order-service.md",` +
		`"to_path":"content/business/rules/order-service-rules.md"}],` +
		`"removals":[],"coverage":{"sources_read":["order-service"],"sources_missed":[],"gaps":[],"notes":""}}`
	writeStaging(t, staging, files)
	h.completeAgentTurn(t, domain.RunSucceeded)
	h.tick(t)

	history, err := h.svc.GetKnowledgeDocument(ctx, h.wsID, docID, first.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !history.Pinned || history.ReleaseID != first.ID {
		t.Fatalf("a release-pinned read must report the release it pinned: %+v", history)
	}
	if history.Document.Path != "content/components/order-service.md" ||
		history.Document.Title != "订单服务" {
		t.Fatalf("history must keep the pinned path and title: %+v", history.Document)
	}
	current, err := h.svc.GetKnowledgeDocument(ctx, h.wsID, docID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if current.Document.Path != "content/business/rules/order-service-rules.md" {
		t.Fatalf("the current read must follow the move: %+v", current.Document)
	}
	// The rename must not have destroyed the old release's own citations.
	if len(history.Assertions) != 1 {
		t.Fatalf("pinned read lost its assertions: %+v", history.Assertions)
	}
	oldEvidence := evidenceIDOf(t, history.Assertions[0].EvidenceJSON)
	if _, _, _, err := h.svc.GetKnowledgeEvidence(ctx, h.wsID, oldEvidence); err != nil {
		t.Fatalf("a historical citation must still resolve: %v", err)
	}
}

// TestKnowledgeWriteSnapshotsTheDeclaredRef: the registered ref, not the
// checked-out branch, decides what a task reads, and the input records it.
func TestKnowledgeWriteSnapshotsTheDeclaredRef(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	repo := h.fixture.repos["order-service"]
	git := func(args ...string) string {
		t.Helper()
		out, err := execGit(t, repo, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	git("checkout", "-q", "-b", "kb-accept")
	if err := os.WriteFile(filepath.Join(repo, "kb-accept-only.txt"), []byte("branch content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "kb-accept-only.txt")
	git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "kb accept branch")
	featureSHA := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(repo, "untracked-wip.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.CreateKnowledgeSource(ctx, h.wsID, application.KnowledgeLibrarySourceInput{
		Name: "order-service", Kind: domain.SourceKindService, RepoPath: repo, DefaultRef: "kb-accept",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "test", ClientKey: "ref-1",
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	task := h.headTask(t)
	snapshot, err := h.store.Library().GetSnapshot(ctx, task.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Bindings) != 1 {
		t.Fatalf("one source, one binding: %+v", snapshot.Bindings)
	}
	binding := snapshot.Bindings[0]
	if binding.CommitSHA != featureSHA || binding.GitRef != "kb-accept" {
		t.Fatalf("the registered ref must decide the pinned commit: %+v", binding)
	}
	if binding.Dirty {
		t.Fatalf("work from another checkout must not be overlaid onto the registered ref: %+v", binding)
	}
}

// TestKnowledgeReindexAPIReturnsQueueReceipt: the HTTP surface must not claim
// the index was rebuilt when it was only enqueued.
func TestKnowledgeReindexAPIReturnsQueueReceipt(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	h.registerAllSources(t)
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "test", ClientKey: "api-1",
	}); err != nil {
		t.Fatal(err)
	}
	mux := httpapi.NewServer(h.svc, h.store, nil).Routes()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+h.wsID+"/library/reindex", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("reindex must answer 202 with a receipt, got %d: %s", rec.Code, rec.Body.String())
	}
	body := map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("receipt is not JSON: %v", err)
	}
	if body["accepted"] != true || body["task_id"] == nil || body["status"] != string(domain.KnowledgeTaskQueued) {
		t.Fatalf("receipt must describe queue admission: %v", body)
	}
	if seq, ok := body["queue_seq"].(float64); !ok || seq != 2 {
		t.Fatalf("receipt must carry the queue position: %v", body)
	}
	if _, claimed := body["indexed_versions"]; claimed {
		t.Fatalf("the response must not claim the index was rebuilt: %v", body)
	}
	if _, ok := body["enqueued_at"]; !ok {
		t.Fatalf("receipt must record when it was accepted: %v", body)
	}
	// The enqueued task really is a queue entry, not a completed execution.
	head := h.headTask(t)
	if head == nil || head.Seq != 1 {
		t.Fatalf("the publish must still hold the head: %+v", head)
	}
}

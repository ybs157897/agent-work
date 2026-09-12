package application_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

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
	receipt, err := h.svc.ReindexKnowledgeLibrary(ctx, h.wsID, "")
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

// TestKnowledgeVersionResolvesItsOwnEvidenceAliases: a version stored the way
// pre-canonicalization releases were — Markdown citing the staged key, the
// canonical ID only in its alias map — must still open its evidence instead of
// reporting a broken citation.
func TestKnowledgeVersionResolvesItsOwnEvidenceAliases(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	release := publishInitializeRelease(t, h)

	docs, _, err := h.svc.ListKnowledgeDocuments(ctx, h.wsID, release.ID, "", "", 0)
	if err != nil || len(docs) != 1 {
		t.Fatalf("published document missing: %v", err)
	}
	docID := docs[0].ID
	var (
		markdown, frontmatter, snapshotID, aliasJSON, assertionID, basis string
	)
	if err := h.db.QueryRowContext(ctx, `SELECT content_markdown, frontmatter_json, snapshot_id,
		evidence_alias_json FROM knowledge_document_versions WHERE document_id=? AND version=1`,
		docID).Scan(&markdown, &frontmatter, &snapshotID, &aliasJSON); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRowContext(ctx, `SELECT assertion_id, basis FROM knowledge_assertions
		WHERE document_id=? ORDER BY ordinal LIMIT 1`, docID).Scan(&assertionID, &basis); err != nil {
		t.Fatal(err)
	}
	if aliasJSON == "" || aliasJSON == "{}" {
		t.Fatalf("a published version must keep the alias map it was written with: %q", aliasJSON)
	}
	aliases := map[string]string{}
	if err := json.Unmarshal([]byte(aliasJSON), &aliases); err != nil {
		t.Fatal(err)
	}
	stagedKey, canonicalID := "", ""
	for staged, id := range aliases {
		stagedKey, canonicalID = staged, id
	}
	if stagedKey == "" {
		t.Fatal("no alias pair to exercise")
	}
	lib, err := h.svc.EnsureKnowledgeLibrary(ctx, h.wsID)
	if err != nil {
		t.Fatal(err)
	}
	legacyMarkdown := strings.ReplaceAll(markdown, canonicalID, stagedKey)
	if legacyMarkdown == markdown {
		t.Fatalf("aliases must name the IDs the markdown cites: %v", aliases)
	}
	now := time.Now().UTC()
	const versionID = "kdocv_accept_legacy"
	if _, err := h.db.ExecContext(ctx, `INSERT INTO knowledge_document_versions
		(id, document_id, library_id, version, title, path, content_markdown, frontmatter_json,
		 content_digest, snapshot_id, release_id, evidence_alias_json, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		versionID, docID, lib.ID, 2, "订单服务", "content/components/order-service.md",
		legacyMarkdown, frontmatter, "sha256:legacy", snapshotID, release.ID, aliasJSON, now); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.ExecContext(ctx, `INSERT INTO knowledge_assertions
		(row_id, assertion_id, library_id, document_id, document_version_id, heading, about_json,
		 perspective, basis, statement, scope_json, evidence_json, content_digest, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"kasrt_accept_legacy", assertionID, lib.ID, docID, versionID, "职责",
		`["entity:service:order-service"]`, "descriptive", basis, "订单服务在取消分支发布取消事件。",
		`{"conditions":[],"environments":[]}`,
		`[{"evidence_id":"`+stagedKey+`","role":"supports"}]`, "sha256:legacy", now); err != nil {
		t.Fatal(err)
	}

	if _, err := h.svc.ReindexKnowledgeLibrary(ctx, h.wsID, ""); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	var evidenceJSON string
	if err := h.db.QueryRowContext(ctx, `SELECT evidence_json FROM knowledge_assertions
		WHERE document_version_id=? AND assertion_id=?`, versionID, assertionID).Scan(&evidenceJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(evidenceJSON, canonicalID) || strings.Contains(evidenceJSON, stagedKey) {
		t.Fatalf("the version alias map must resolve the staged key %q: %s", stagedKey, evidenceJSON)
	}
}

// libraryIndexRelease reads the release ID the official INDEX.md advertises.
func libraryIndexRelease(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "INDEX.md"))
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile("`(rel_[A-Za-z0-9]+)`").FindStringSubmatch(string(raw))
	if len(match) != 2 {
		t.Fatalf("INDEX.md names no release:\n%s", raw)
	}
	return match[1]
}

// TestKnowledgePublishMaterializesImmediately is the F14 regression: the
// official Markdown must describe the release that was just published, not the
// one before it. The defect was a stale in-memory library pointer, so it only
// shows up on the second consecutive publish.
func TestKnowledgePublishMaterializesImmediately(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	first := publishInitializeRelease(t, h)
	if got := libraryIndexRelease(t, h.libRoot); got != first.ID {
		t.Fatalf("INDEX.md must name the first release %s, got %s", first.ID, got)
	}

	// Second publish through the same code path, one release later.
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "code.changed", Source: "git-hook", ClientKey: "f14-2",
		Subject: map[string]any{"changed_paths": []any{"src/main/java/com/example/order/OrderService.java"}},
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	staging := h.stagingDir(t)
	files := orderServiceAssertionEvidence(t, h.fixture.repos["order-service"])
	files["content/components/order-service.md"] = libraryDoc(
		"doc:order-service", "订单服务", "assertion:order-cancel-publishes",
		"订单服务在取消分支发布取消事件（第二版）。", "ev-order-cancel", []string{"entity:service:order-service"})
	files["entities.yaml"] = "entities: []\n"
	files["plan.json"] = `{"summary":"增量","documents":["content/components/order-service.md"],` +
		`"removals":[],"renames":[],"coverage":{"sources_read":["order-service"],"sources_missed":[],` +
		`"gaps":[],"notes":""}}`
	writeStaging(t, staging, files)
	h.completeAgentTurn(t, domain.RunSucceeded)
	h.tick(t)

	second, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, "")
	if err != nil {
		t.Fatal(err)
	}
	if second.Seq != 2 {
		t.Fatalf("expected a second release: %+v", second)
	}
	if got := libraryIndexRelease(t, h.libRoot); got != second.ID {
		t.Fatalf("INDEX.md must name the release just published %s, got %s (official files lag one version)",
			second.ID, got)
	}
	body, err := os.ReadFile(filepath.Join(h.libRoot, "content", "components", "order-service.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "第二版") {
		t.Fatalf("content must be the version just published:\n%s", body)
	}
}

// TestKnowledgeMaterializeFailureIsNotCompleted: a task may not report success
// while the official files disagree with the release it published, and the
// retry must not publish a second copy.
func TestKnowledgeMaterializeFailureIsNotCompleted(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	firstRelease := publishInitializeRelease(t, h)

	// Obstruct the next write: the document target becomes a directory, so
	// materialization fails while the database publish succeeds.
	obstruction := filepath.Join(h.libRoot, "content", "components", "order-service.md")
	if err := os.Remove(obstruction); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(obstruction, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "code.changed", Source: "git-hook", ClientKey: "f14-fail",
		Subject: map[string]any{"changed_paths": []any{"src/main/java/com/example/order/OrderService.java"}},
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	staging := h.stagingDir(t)
	files := orderServiceAssertionEvidence(t, h.fixture.repos["order-service"])
	files["content/components/order-service.md"] = libraryDoc(
		"doc:order-service", "订单服务", "assertion:order-cancel-publishes",
		"订单服务在取消分支发布取消事件（物化失败版）。", "ev-order-cancel", []string{"entity:service:order-service"})
	files["entities.yaml"] = "entities: []\n"
	files["plan.json"] = `{"summary":"增量","documents":["content/components/order-service.md"],` +
		`"removals":[],"renames":[],"coverage":{"sources_read":["order-service"],"sources_missed":[],` +
		`"gaps":[],"notes":""}}`
	writeStaging(t, staging, files)
	h.completeAgentTurn(t, domain.RunSucceeded)
	h.tick(t)

	task := h.headTask(t)
	if task == nil {
		t.Fatal("the task must stay in the queue while the official files are inconsistent")
	}
	if task.Status == domain.KnowledgeTaskCompleted {
		t.Fatalf("a task must not be completed while materialization fails: %+v", task)
	}
	if !strings.Contains(task.LastError, "不一致") {
		t.Fatalf("the failure must be recorded on the task: %q", task.LastError)
	}
	// The publish is not visible as a version until its files exist: the
	// journal row is still prepared, and every reader-facing lookup must
	// answer with the previous release.
	var pubStatus string
	var committedAt *string
	if err := h.db.QueryRowContext(ctx, `SELECT status, committed_at FROM knowledge_publications
		WHERE task_id=?`, task.ID).Scan(&pubStatus, &committedAt); err != nil {
		t.Fatal(err)
	}
	if pubStatus != "prepared" || committedAt != nil {
		t.Fatalf("an unmaterialized publish must stay prepared with no commit time: %s %v", pubStatus, committedAt)
	}
	lib, err := h.svc.EnsureKnowledgeLibrary(ctx, h.wsID)
	if err != nil {
		t.Fatal(err)
	}
	if lib.CurrentReleaseID != firstRelease.ID {
		t.Fatalf("the library must still point at the previous release %s, got %s", firstRelease.ID, lib.CurrentReleaseID)
	}
	visible, err := h.svc.ListKnowledgeReleases(ctx, h.wsID, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range visible {
		if rel.Seq > 1 {
			t.Fatalf("a half-published release must not be listed as a version: %+v", rel)
		}
	}
	current, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, "")
	if err != nil || current.ID != firstRelease.ID {
		t.Fatalf("the current release must stay %s: %+v %v", firstRelease.ID, current, err)
	}
	answer, err := h.svc.QueryKnowledgeLibrary(ctx, application.KnowledgeLibraryQuery{
		WorkspaceID: h.wsID, Question: "订单服务是否发布取消事件",
	})
	if err != nil {
		t.Fatal(err)
	}
	if answer.Release == nil || answer.Release.ID != firstRelease.ID {
		t.Fatalf("a query must answer from the last complete release: %+v", answer.Release)
	}
	for _, hit := range answer.Hits {
		if strings.Contains(hit.Assertion.Statement, "物化失败版") {
			t.Fatalf("an unmaterialized version must not be queryable: %+v", hit.Assertion)
		}
	}
	pending := pendingReleaseID(t, h, task)
	if _, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, pending); err == nil {
		t.Fatal("a prepared release must not be readable by its ID")
	}
	// The retry budget must be spent on the harness step, not on a new model
	// turn, and the release must not be published twice.
	for i := 0; i < 6; i++ {
		if _, err := h.db.ExecContext(ctx, `UPDATE knowledge_write_tasks SET next_attempt_at=NULL WHERE id=?`, task.ID); err != nil {
			t.Fatal(err)
		}
		h.tick(t)
		current := h.headTask(t)
		if current == nil || current.Status == domain.KnowledgeTaskBlocked {
			break
		}
	}
	blocked := h.headTask(t)
	if blocked == nil || blocked.Status != domain.KnowledgeTaskBlocked {
		t.Fatalf("a permanent materialization failure must block with a reason: %+v", blocked)
	}
	if blocked.BlockedReason == "" || !strings.Contains(blocked.LastError, "不一致") {
		t.Fatalf("the block must explain itself: %+v", blocked)
	}
	// The release row exists, but only because the publish happened: it must
	// not be reachable, and the retries must not create a second one.
	var releaseRows int
	if err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_releases WHERE library_id=?`,
		lib.ID).Scan(&releaseRows); err != nil {
		t.Fatal(err)
	}
	if releaseRows != 2 {
		t.Fatalf("retrying the harness step must not publish extra releases: %d", releaseRows)
	}
	if visible, err := h.svc.ListKnowledgeReleases(ctx, h.wsID, 10); err != nil {
		t.Fatal(err)
	} else if len(visible) != 1 {
		t.Fatalf("only the last complete release may be listed: %+v", visible)
	}

	// A blocked task is still the queue head's business: recovery must not
	// spend more materialization attempts behind its back, so nothing changes
	// until an operator retries it.
	attemptsWhenBlocked := blocked.Attempt
	for i := 0; i < 3; i++ {
		h.tick(t)
	}
	stillBlocked := h.headTask(t)
	if stillBlocked == nil || stillBlocked.Status != domain.KnowledgeTaskBlocked {
		t.Fatalf("a blocked task must stay blocked until it is retried: %+v", stillBlocked)
	}
	if stillBlocked.Attempt != attemptsWhenBlocked {
		t.Fatalf("recovery must not spend the task's retry budget: %d vs %d",
			stillBlocked.Attempt, attemptsWhenBlocked)
	}
	// The official files still describe the last complete release: the planted
	// obstruction is untouched and INDEX.md has not moved.
	if info, err := os.Stat(obstruction); err != nil || !info.IsDir() {
		t.Fatalf("recovery must not materialize a blocked task's publication: %v %v", info, err)
	}
	if got := libraryIndexRelease(t, h.libRoot); got != firstRelease.ID {
		t.Fatalf("the official index must still name %s, got %s", firstRelease.ID, got)
	}
	if pub, err := h.store.Library().GetPublicationByTask(ctx, blocked.ID); err != nil {
		t.Fatal(err)
	} else if pub.Status != "prepared" {
		t.Fatalf("the publication must stay prepared while the task is blocked: %+v", pub)
	}

	// Once the obstruction is gone and the operator retries, the task's own
	// state machine finishes the same release: no new release, and the
	// official files catch up.
	if err := os.RemoveAll(obstruction); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.RetryKnowledgeWriteTask(ctx, h.wsID, blocked.ID); err != nil {
		t.Fatalf("a blocked head must be retryable: %v", err)
	}
	h.tick(t)
	body, err := os.ReadFile(filepath.Join(h.libRoot, "content", "components", "order-service.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "物化失败版") {
		t.Fatalf("recovery must write the published version:\n%s", body)
	}
	after, err := h.svc.ListKnowledgeReleases(ctx, h.wsID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 {
		t.Fatalf("recovery must commit the release it already published, not add one: %+v", after)
	}
	committed, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, "")
	if err != nil {
		t.Fatal(err)
	}
	if committed.ID != pending {
		t.Fatalf("recovery must switch to the same release %s, got %s", pending, committed.ID)
	}
	if got := libraryIndexRelease(t, h.libRoot); got != pending {
		t.Fatalf("recovered INDEX.md must name %s, got %s", pending, got)
	}
	var pubAfter string
	var committedAtAfter *string
	if err := h.db.QueryRowContext(ctx, `SELECT status, committed_at FROM knowledge_publications
		WHERE task_id=?`, blocked.ID).Scan(&pubAfter, &committedAtAfter); err != nil {
		t.Fatal(err)
	}
	if pubAfter != "committed" || committedAtAfter == nil {
		t.Fatalf("recovery must commit the publication: %s %v", pubAfter, committedAtAfter)
	}
	// Publication, task and event must reach their terminal states together.
	settled, _, err := h.svc.GetKnowledgeWriteTask(ctx, h.wsID, blocked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != domain.KnowledgeTaskCompleted || settled.TargetReleaseID != pending {
		t.Fatalf("the retried task must finish against its own release: %+v", settled)
	}
	if settled.EventID != "" {
		event, err := h.store.Library().GetEvent(ctx, lib.ID, settled.EventID)
		if err != nil {
			t.Fatal(err)
		}
		if event.Status != domain.KnowledgeEventCompleted || event.TaskID != settled.ID {
			t.Fatalf("the event must agree with the task: %+v", event)
		}
	}
}

// requirementDoc renders a normative statement whose evidence is the frozen
// requirement text, which is what the brief asks the librarian to produce.
func requirementDoc(statement, evidenceKey, requirementBinding string) string {
	return "---\n" +
		"schema_version: kb-note/0.2-draft\n" +
		"id: doc:order-cancel-acceptance-sla\n" +
		"kind: business.rule\n" +
		"title: 订单取消受理时限\n" +
		"summary: 外部提交的受理时限要求。\n" +
		"about: [entity:service:order-service]\n" +
		"domains: [order]\n" +
		"---\n\n" +
		"# 订单取消受理时限\n\n## 知识条目\n\n### 受理时限上限\n\n" +
		"```yaml\n" +
		"kind: assertion\n" +
		"id: assertion:order-cancel-acceptance-time-limit\n" +
		"about: [entity:service:order-service]\n" +
		"perspective: normative\n" +
		"basis: source_statement\n" +
		"scope:\n  conditions: []\n  environments: []\n" +
		"evidence:\n  - evidence_id: " + evidenceKey + "\n    role: supports\n" +
		"```\n\n" +
		"#### 陈述\n\n" + statement + "\n\n" +
		"#### 说明与未知\n\n需求是否已在代码中实现尚未核实。\n"
}

// TestKnowledgeRequirementTextIsFrozenAndCitable covers the requirement-import
// boundary end to end: the accepted text is frozen at acceptance, registered
// as a source binding, cited by the librarian and collected as evidence.
func TestKnowledgeRequirementTextIsFrozenAndCitable(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	lib, err := h.svc.EnsureKnowledgeLibrary(ctx, h.wsID)
	if err != nil {
		t.Fatal(err)
	}
	reqPath := filepath.Join(h.fixture.root, "REQ-KB-ACCEPT-01-v1.md")
	v1 := "REQ-KB-ACCEPT-01 v1：订单取消受理时限上限为 17分钟；超时转人工处理。\n"
	if err := os.WriteFile(reqPath, []byte(v1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "requirement.imported", Source: "business-harness",
		ClientKey: "req-evidence-v1", ContentRef: reqPath,
		Subject: map[string]any{"requirement_id": "REQ-KB-ACCEPT-01", "requirement_version": "v1"},
		Payload: map[string]any{"title": "订单取消受理时限", "owner": "业务运营"},
	}); err != nil {
		t.Fatal(err)
	}
	task := h.headTask(t)
	if task == nil || task.RequirementInputID == "" {
		t.Fatalf("the accepted requirement must be frozen and linked to the task: %+v", task)
	}
	input, err := h.store.Library().GetRequirementInput(ctx, lib.ID, task.RequirementInputID)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := os.ReadFile(input.StoredPath)
	if err != nil {
		t.Fatalf("the frozen requirement must exist on disk: %v", err)
	}
	if string(frozen) != v1 {
		t.Fatalf("the frozen copy must be the accepted bytes:\n%s", frozen)
	}
	if input.ContentDigest != "sha256:"+requirementDigestOf(v1) {
		t.Fatalf("digest must cover the accepted text exactly: %s", input.ContentDigest)
	}
	source, err := h.store.Library().GetSource(ctx, lib.ID, input.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if source.Kind != domain.SourceKindRequirement {
		t.Fatalf("the requirement must be a registered source: %+v", source)
	}

	// The referenced path changes before the task runs: the frozen copy, not
	// the new content, is what the task documents.
	if err := os.WriteFile(reqPath, []byte("REQ-KB-ACCEPT-01 v2：23分钟。\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	task = h.headTask(t)
	snapshot, err := h.store.Library().GetSnapshot(ctx, task.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	requirementBinding := ""
	for _, b := range snapshot.Bindings {
		if b.SourceKind == string(domain.SourceKindRequirement) {
			requirementBinding = b.SourceName
			if b.CommitSHA != input.ContentDigest {
				t.Fatalf("the binding must pin the frozen digest: %+v", b)
			}
		}
	}
	if requirementBinding == "" {
		t.Fatalf("the frozen requirement must appear as a binding: %+v", snapshot.Bindings)
	}
	briefRaw, err := os.ReadFile(filepath.Join(task.StagingPath, "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	brief := string(briefRaw)
	if !strings.Contains(brief, requirementBinding) || !strings.Contains(brief, "requirement.md") {
		t.Fatalf("the brief must name the requirement binding and its path:\n%s", brief)
	}
	if !strings.Contains(brief, "17分钟") {
		t.Fatal("the brief must carry the frozen text that was accepted")
	}

	// The agent cites the frozen binding; the program collects the evidence.
	files := map[string]string{
		"content/business/rules/order-cancel-acceptance-sla.md": requirementDoc(
			"订单取消受理时限上限为 17 分钟。", "ev-req-limit", requirementBinding),
		"entities.yaml": "entities:\n  - id: entity:service:order-service\n    kind: service\n    name: order-service\n",
		"evidence.yaml": "evidence:\n" +
			"  - key: ev-req-limit\n" +
			"    binding: " + requirementBinding + "\n" +
			"    path: requirement.md\n" +
			"    locator:\n      kind: source_text\n      interval: half_open\n" +
			"      start: {line: 0, column: 0}\n      end: {line: 1, column: 0}\n" +
			"    note: 需求正文的时限条款\n",
		"plan.json": `{"summary":"需求导入","documents":["content/business/rules/order-cancel-acceptance-sla.md"],` +
			`"removals":[],"renames":[],"coverage":{"sources_read":["` + requirementBinding + `"],"sources_missed":[],` +
			`"gaps":[],"notes":""}}`,
	}
	writeStaging(t, task.StagingPath, files)
	h.completeAgentTurn(t, domain.RunSucceeded)
	h.tick(t)
	if head := h.headTask(t); head != nil {
		t.Fatalf("the requirement publish must finish: %+v err=%q", head, head.LastError)
	}
	release, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, "")
	if err != nil {
		t.Fatal(err)
	}
	docs, _, err := h.svc.ListKnowledgeDocuments(ctx, h.wsID, release.ID, "", "", 0)
	if err != nil || len(docs) != 1 {
		t.Fatalf("published document missing: %v %+v", err, docs)
	}
	detail, err := h.svc.GetKnowledgeDocument(ctx, h.wsID, docs[0].ID, release.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Assertions) != 1 {
		t.Fatalf("the requirement assertion must be published: %+v", detail.Assertions)
	}
	evidenceID := evidenceIDOf(t, detail.Assertions[0].EvidenceJSON)
	ev, binding, rep, err := h.svc.GetKnowledgeEvidence(ctx, h.wsID, evidenceID)
	if err != nil {
		t.Fatalf("the requirement evidence must be readable: %v", err)
	}
	if binding.SourceKind != string(domain.SourceKindRequirement) {
		t.Fatalf("evidence must come from the requirement binding: %+v", binding)
	}
	if !strings.Contains(ev.Excerpt, "17分钟") {
		t.Fatalf("the excerpt must be the frozen original text: %q", ev.Excerpt)
	}
	if rep.ContentDigest != input.ContentDigest {
		t.Fatalf("the representation must be the accepted bytes: %s vs %s", rep.ContentDigest, input.ContentDigest)
	}
	if ev.Availability != "available" {
		t.Fatalf("a frozen requirement representation stays available: %+v", ev)
	}
}

// TestKnowledgeRequirementRefIsAuthorized: an import may not read outside the
// workspace root, follow a symlink out of it, or smuggle in an oversized file.
func TestKnowledgeRequirementRefIsAuthorized(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside the workspace\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "requirement.imported", Source: "business-harness",
		ClientKey: "req-outside", ContentRef: outside,
		Subject: map[string]any{"requirement_id": "REQ-OUTSIDE"},
	}); err == nil || !strings.Contains(err.Error(), "授权范围") {
		t.Fatalf("a content_ref outside the workspace root must be refused: %v", err)
	}
	link := filepath.Join(h.fixture.root, "escape.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "requirement.imported", Source: "business-harness",
		ClientKey: "req-symlink", ContentRef: link,
		Subject: map[string]any{"requirement_id": "REQ-SYMLINK"},
	}); err == nil {
		t.Fatal("a symlink escaping the workspace root must be refused")
	}
	big := filepath.Join(h.fixture.root, "big-requirement.md")
	if err := os.WriteFile(big, bytes.Repeat([]byte("x"), 2*1024*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "requirement.imported", Source: "business-harness",
		ClientKey: "req-big", ContentRef: big,
		Subject: map[string]any{"requirement_id": "REQ-BIG"},
	}); err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("an oversized requirement must be refused, not truncated: %v", err)
	}
}

func requirementDigestOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// TestKnowledgeOrdinaryEventsDoNotInventRequirements: a code or workspace
// notification carries no requirement, so it must not produce a "requirement
// not obtained" gap.
func TestKnowledgeOrdinaryEventsDoNotInventRequirements(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	h.registerAllSources(t)
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "code.changed", Source: "git-hook", ClientKey: "plain-code",
		Subject: map[string]any{"changed_paths": []any{"src/main/java/com/example/order/OrderService.java"}},
	}); err != nil {
		t.Fatal(err)
	}
	if head := h.headTask(t); head == nil || strings.Contains(head.FocusJSON, "requirement") {
		t.Fatalf("a code change must not be parsed as a requirement: %+v", head)
	}
	h.tick(t)
	task := h.headTask(t)
	if task == nil || task.StagingPath == "" {
		t.Fatalf("the code change must still be queued: %+v", task)
	}
	if strings.Contains(task.FocusJSON, "requirement") {
		t.Fatalf("a code change must not be parsed as a requirement: %s", task.FocusJSON)
	}
	briefRaw, err := os.ReadFile(filepath.Join(task.StagingPath, "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	brief := string(briefRaw)
	for _, forbidden := range []string{"本次需求原文", "需求的原文没有取到", "requirement_unresolved"} {
		if strings.Contains(brief, forbidden) {
			t.Fatalf("a code change must not invent a requirement gap (%q):\n%s", forbidden, brief)
		}
	}
	if !strings.Contains(brief, "本次变更范围") {
		t.Fatal("the code change must still reach the brief as a change scope")
	}
	// A workspace notification behaves the same way.
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "admin", ClientKey: "plain-ws",
	}); err != nil {
		t.Fatal(err)
	}
	if head := h.headTask(t); strings.Contains(head.FocusJSON, "requirement") {
		t.Fatalf("a workspace notification must not be parsed as a requirement: %s", head.FocusJSON)
	}
}

// TestKnowledgeCodeEventsKeepTheirReferencesAsContext covers the two variants
// the protocol already ships: a code pull whose content_ref is a commit, and a
// code change whose payload carries a summary. Neither is a requirement
// import, and both keep their reference as context for the change.
func TestKnowledgeCodeEventsKeepTheirReferencesAsContext(t *testing.T) {
	cases := []struct {
		name     string
		event    application.KnowledgeLibraryEventInput
		contexts []string
	}{
		{
			name: "commit reference",
			event: application.KnowledgeLibraryEventInput{
				EventType: "code.pulled", Source: "git-hook", ClientKey: "variant-pull",
				ContentRef: "commit:9f2c1a",
				Subject:    map[string]any{"changed_paths": []any{"src/main/java/com/example/order/OrderService.java"}},
			},
			contexts: []string{"commit:9f2c1a"},
		},
		{
			name: "change summary",
			event: application.KnowledgeLibraryEventInput{
				EventType: "code.changed", Source: "git-hook", ClientKey: "variant-summary",
				Subject: map[string]any{"changed_paths": []any{"src/main/java/com/example/order/OrderService.java"}},
				Payload: map[string]any{"summary": "拉取代码更新"},
			},
			contexts: []string{"拉取代码更新"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			h := newLibraryHarness(t)
			h.registerAllSources(t)
			lib, err := h.svc.EnsureKnowledgeLibrary(ctx, h.wsID)
			if err != nil {
				t.Fatal(err)
			}
			in := tc.event
			in.WorkspaceID = h.wsID
			if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, in); err != nil {
				t.Fatal(err)
			}
			task := h.headTask(t)
			if task == nil {
				t.Fatal("the notification must still be queued")
			}
			if task.RequirementInputID != "" {
				t.Fatalf("a code notification must not freeze a requirement body: %+v", task)
			}
			for _, key := range []string{"requirement_unresolved", "requirement_id"} {
				if strings.Contains(task.FocusJSON, key) {
					t.Fatalf("a code notification must not report a requirement (%s): %s", key, task.FocusJSON)
				}
			}
			h.tick(t)
			task = h.headTask(t)
			if task == nil || task.StagingPath == "" {
				t.Fatalf("the notification must reach the brief stage: %+v", task)
			}
			briefRaw, err := os.ReadFile(filepath.Join(task.StagingPath, "brief.md"))
			if err != nil {
				t.Fatal(err)
			}
			brief := string(briefRaw)
			for _, want := range tc.contexts {
				if !strings.Contains(brief, want) {
					t.Fatalf("the code event context %q must survive in the brief:\n%s", want, brief)
				}
			}
			for _, forbidden := range []string{"本次需求原文", "需求的原文没有取到", "requirement:"} {
				if strings.Contains(brief, forbidden) {
					t.Fatalf("a code notification must not become a requirement import (%q):\n%s", forbidden, brief)
				}
			}
			// No requirement input or source may exist for either variant.
			var inputs, sources int
			if err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_requirement_inputs WHERE library_id=?`, lib.ID).Scan(&inputs); err != nil {
				t.Fatal(err)
			}
			if err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_library_sources WHERE library_id=? AND kind='requirement'`, lib.ID).Scan(&sources); err != nil {
				t.Fatal(err)
			}
			if inputs != 0 || sources != 0 {
				t.Fatalf("code events must not register requirement inputs or sources: inputs=%d sources=%d", inputs, sources)
			}
		})
	}
}

// TestKnowledgeInlineRequirementKeepsItsGap: when the referenced document
// cannot be read but the payload carries text, the text is a separate inline
// source — the reference failure and the true origin must both survive.
func TestKnowledgeInlineRequirementKeepsItsGap(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	lib, err := h.svc.EnsureKnowledgeLibrary(ctx, h.wsID)
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(h.fixture.root, "REQ-KB-ACCEPT-03-missing.md")
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "requirement.imported", Source: "business-harness",
		ClientKey: "req-inline", ContentRef: missing,
		Subject: map[string]any{"requirement_id": "REQ-KB-ACCEPT-03", "requirement_version": "v1"},
		Payload: map[string]any{"title": "受理时限草稿", "text": "内联草稿：受理时限上限为 31 分钟。\n"},
	}); err != nil {
		t.Fatalf("an inline payload with an unreadable reference is still acceptable: %v", err)
	}
	task := h.headTask(t)
	if task == nil || task.RequirementInputID == "" {
		t.Fatalf("the inline body must be frozen as an input: %+v", task)
	}
	input, err := h.store.Library().GetRequirementInput(ctx, lib.ID, task.RequirementInputID)
	if err != nil {
		t.Fatal(err)
	}
	if input.InputOrigin != domain.RequirementOriginInlinePayload {
		t.Fatalf("the input must record that it is an inline copy: %+v", input)
	}
	if !strings.Contains(input.ReferenceError, "不可读") {
		t.Fatalf("the reference failure must be kept: %q", input.ReferenceError)
	}
	source, err := h.store.Library().GetSource(ctx, lib.ID, input.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if source.Name != "requirement-inline:REQ-KB-ACCEPT-03" {
		t.Fatalf("an inline copy is a separate source: %+v", source)
	}
	h.tick(t)
	task = h.headTask(t)
	briefRaw, err := os.ReadFile(filepath.Join(task.StagingPath, "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	brief := string(briefRaw)
	if !strings.Contains(brief, "引用的需求文档没有取到") {
		t.Fatalf("the reference failure must be reported:\n%s", brief)
	}
	if !strings.Contains(brief, "内联文本") || !strings.Contains(brief, "31 分钟") {
		t.Fatalf("the inline body must be handed over and labelled:\n%s", brief)
	}
	if !strings.Contains(brief, source.Name) {
		t.Fatalf("the brief must name the inline binding: %s", source.Name)
	}
	// The brief must not advertise a staging requirement.md: the inline case
	// keeps the text in the brief and points at the frozen binding instead.
	if strings.Contains(brief, filepath.Join(task.StagingPath, "requirement.md")) {
		t.Fatalf("the inline branch must not name an unwritten staging file:\n%s", brief)
	}
	if _, err := os.Stat(filepath.Join(task.StagingPath, "requirement.md")); !os.IsNotExist(err) {
		t.Fatal("the inline branch must not leave a requirement.md in staging")
	}
	// The snapshot binding must carry the inline source's own name, so the
	// evidence collected from it is visibly not the referenced document.
	snapshot, err := h.store.Library().GetSnapshot(ctx, task.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, b := range snapshot.Bindings {
		if b.SourceID != input.SourceID {
			continue
		}
		found = true
		if b.SourceName != source.Name || b.SourceKind != string(domain.SourceKindRequirement) {
			t.Fatalf("the inline binding must keep its own source identity: %+v", b)
		}
	}
	if !found {
		t.Fatalf("the frozen inline input must appear as a binding: %+v", snapshot.Bindings)
	}
}

// TestKnowledgeQueryCoverageIsHonest covers F4b: a search that returned every
// hit must still be reported as partial when the release it read carries gaps
// or when the matched statements have no evidence.
func TestKnowledgeQueryCoverageIsHonest(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	release := publishInitializeRelease(t, h)

	answer, err := h.svc.QueryKnowledgeLibrary(ctx, application.KnowledgeLibraryQuery{
		WorkspaceID: h.wsID, Question: "订单服务是否发布取消事件",
	})
	if err != nil {
		t.Fatal(err)
	}
	if answer.Coverage.Truncated {
		t.Fatalf("the fixture answer is not truncated: %+v", answer.Coverage)
	}
	// The initialize fixture documents an unknown note, so the answer carries
	// unknowns even though no hit was dropped.
	if answer.Coverage.Unknowns == 0 {
		t.Fatalf("unknown statements must be counted: %+v", answer.Coverage)
	}
	if answer.Coverage.Status != "partial" {
		t.Fatalf("a release with unknown statements must not report complete coverage: %+v", answer.Coverage)
	}
	if release.ID == "" {
		t.Fatal("fixture release missing")
	}
}

// TestKnowledgeReindexFailureIsBounded covers the queue's failure policy for a
// reindex: it must spend the same bounded retry budget as any other write and
// end up blocked with a reason, instead of resetting to queued forever.
func TestKnowledgeReindexFailureIsBounded(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	publishInitializeRelease(t, h)

	receipt, err := h.svc.ReindexKnowledgeLibrary(ctx, h.wsID, "reindex-bounded")
	if err != nil {
		t.Fatal(err)
	}
	// Idempotent acceptance: the same key returns the original task.
	again, err := h.svc.ReindexKnowledgeLibrary(ctx, h.wsID, "reindex-bounded")
	if err != nil {
		t.Fatal(err)
	}
	if !again.Duplicate || again.TaskID != receipt.TaskID || again.QueueSeq != receipt.QueueSeq {
		t.Fatalf("a repeated client key must return the original receipt: %+v vs %+v", again, receipt)
	}
	tasks, err := h.svc.ListKnowledgeWriteTasks(ctx, h.wsID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("a repeated key must not enqueue a second task: %d", len(tasks))
	}

	// Permanent failure: the library root cannot be created, so writing the
	// derived files always fails while every database step succeeds.
	if err := os.RemoveAll(h.libRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.libRoot, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := h.db.ExecContext(ctx, `UPDATE knowledge_write_tasks SET next_attempt_at=NULL WHERE id=?`, receipt.TaskID); err != nil {
			t.Fatal(err)
		}
		h.tick(t)
		task, _, err := h.svc.GetKnowledgeWriteTask(ctx, h.wsID, receipt.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status == domain.KnowledgeTaskBlocked {
			break
		}
		if task.Status != domain.KnowledgeTaskQueued && task.Status != domain.KnowledgeTaskRetryWait &&
			task.Status != domain.KnowledgeTaskRunning {
			t.Fatalf("unexpected reindex state %s", task.Status)
		}
		if task.Attempt == 0 && i > 0 {
			t.Fatalf("a failing reindex must spend its retry budget, attempt stayed 0")
		}
	}
	task, _, err := h.svc.GetKnowledgeWriteTask(ctx, h.wsID, receipt.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.KnowledgeTaskBlocked {
		t.Fatalf("a permanently failing reindex must be blocked: %+v", task)
	}
	if task.Attempt < 2 || task.BlockedReason == "" || task.LastError == "" {
		t.Fatalf("the block must record attempts and a reason: %+v", task)
	}
	// It is still the queue head: a blocked write holds the queue.
	if head := h.headTask(t); head == nil || head.ID != receipt.TaskID {
		t.Fatalf("a blocked reindex must keep the queue head: %+v", head)
	}
}

// pendingReleaseID reads the release a task published even though its
// publication is not committed yet, which is exactly what must stay invisible.
func pendingReleaseID(t *testing.T, h *libraryHarness, task *domain.KnowledgeWriteTask) string {
	t.Helper()
	var releaseID string
	if err := h.db.QueryRowContext(context.Background(),
		`SELECT release_id FROM knowledge_publications WHERE task_id=?`, task.ID).Scan(&releaseID); err != nil {
		t.Fatal(err)
	}
	return releaseID
}

// httpGetJSON issues one GET against the real router.
func httpGetJSON(t *testing.T, mux http.Handler, path string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	body := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

// TestKnowledgeCancelContract: cancelling takes an empty body, works for a task
// that is still waiting, and refuses the states where it would orphan a running
// turn or rewrite published history.
func TestKnowledgeCancelContract(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	h.registerAllSources(t)
	mux := httpapi.NewServer(h.svc, h.store, nil).Routes()
	base := "/api/v1/workspaces/" + h.wsID + "/library"

	submit := func(clientKey string) string {
		t.Helper()
		receipt, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
			WorkspaceID: h.wsID, EventType: "code.changed", Source: "git-hook", ClientKey: clientKey,
			Subject: map[string]any{"changed_paths": []any{"src/main/java/com/example/order/OrderService.java"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return receipt.TaskID
	}
	post := func(path, body string) (int, map[string]any) {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, reader)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		mux.ServeHTTP(rec, req)
		parsed := map[string]any{}
		_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
		return rec.Code, parsed
	}

	// An empty body is a valid cancel request: requiring JSON here produced
	// "EOF" for every caller that had nothing to add.
	queued := submit("cancel-empty")
	if status, body := post(base+"/tasks/"+queued+"/cancel", ""); status != http.StatusOK {
		t.Fatalf("an empty cancel body must be accepted: %d %v", status, body)
	}
	task, _, err := h.svc.GetKnowledgeWriteTask(ctx, h.wsID, queued)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.KnowledgeTaskCancelled {
		t.Fatalf("a queued task must be cancellable: %+v", task)
	}

	// A reason travels through the body.
	withReason := submit("cancel-reason")
	if status, _ := post(base+"/tasks/"+withReason+"/cancel", `{"reason":"重复事件"}`); status != http.StatusOK {
		t.Fatalf("a cancel with a reason must be accepted: %d", status)
	}
	task, _, err = h.svc.GetKnowledgeWriteTask(ctx, h.wsID, withReason)
	if err != nil {
		t.Fatal(err)
	}
	if task.BlockedReason != "重复事件" {
		t.Fatalf("the reason must be kept: %+v", task)
	}

	// A task whose model turn is in flight refuses, and says why.
	running := submit("cancel-running")
	h.tick(t)
	if status, body := post(base+"/tasks/"+running+"/cancel", "{}"); status != http.StatusConflict {
		t.Fatalf("a running task must refuse cancellation: %d %v", status, body)
	} else if detail, _ := body["detail"].(string); !strings.Contains(detail, "模型轮次") {
		t.Fatalf("the refusal must explain itself: %v", body)
	}
	// Published history is not cancellable either.
	task, _, err = h.svc.GetKnowledgeWriteTask(ctx, h.wsID, running)
	if err != nil {
		t.Fatal(err)
	}
	staging := task.StagingPath
	files := orderServiceAssertionEvidence(t, h.fixture.repos["order-service"])
	files["content/components/order-service.md"] = libraryDoc(
		"doc:order-service", "订单服务", "assertion:order-cancel-publishes", "订单服务在取消分支发布取消事件。",
		"ev-order-cancel", []string{"entity:service:order-service"})
	files["entities.yaml"] = "entities: []\n"
	files["plan.json"] = `{"summary":"增量","documents":["content/components/order-service.md"],` +
		`"removals":[],"renames":[],"coverage":{"sources_read":["order-service"],"sources_missed":[],` +
		`"gaps":[],"notes":""}}`
	writeStaging(t, staging, files)
	h.completeAgentTurn(t, domain.RunSucceeded)
	h.tick(t)
	if status, body := post(base+"/tasks/"+running+"/cancel", "{}"); status != http.StatusConflict {
		t.Fatalf("a completed task must refuse cancellation: %d %v", status, body)
	} else if detail, _ := body["detail"].(string); !strings.Contains(detail, "已经发布") {
		t.Fatalf("the refusal must explain itself: %v", body)
	}
}

// TestKnowledgeCancelAndRecoveryStateBoundary pins the boundary between the two
// state machines that can finish a publish: the task's own head retry policy,
// and the recovery pass that exists for tasks that no longer have an owner.
func TestKnowledgeCancelAndRecoveryStateBoundary(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	first := publishInitializeRelease(t, h)

	// A publish that fails to materialize blocks after spending its budget, and
	// then stays where it is: recovery must not spend attempts the task's own
	// policy already exhausted.
	obstruction := filepath.Join(h.libRoot, "content", "components", "order-service.md")
	if err := os.Remove(obstruction); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(obstruction, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "code.changed", Source: "git-hook", ClientKey: "boundary-1",
		Subject: map[string]any{"changed_paths": []any{"src/main/java/com/example/order/OrderService.java"}},
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	staging := h.stagingDir(t)
	files := orderServiceAssertionEvidence(t, h.fixture.repos["order-service"])
	files["content/components/order-service.md"] = libraryDoc(
		"doc:order-service", "订单服务", "assertion:order-cancel-publishes",
		"订单服务在取消分支发布取消事件（边界用例）。", "ev-order-cancel", []string{"entity:service:order-service"})
	files["entities.yaml"] = "entities: []\n"
	files["plan.json"] = `{"summary":"增量","documents":["content/components/order-service.md"],` +
		`"removals":[],"renames":[],"coverage":{"sources_read":["order-service"],"sources_missed":[],` +
		`"gaps":[],"notes":""}}`
	writeStaging(t, staging, files)
	h.completeAgentTurn(t, domain.RunSucceeded)
	h.tick(t)
	blocked := h.headTask(t)
	for i := 0; i < 8 && blocked != nil && blocked.Status != domain.KnowledgeTaskBlocked; i++ {
		if _, err := h.db.ExecContext(ctx, `UPDATE knowledge_write_tasks SET next_attempt_at=NULL WHERE id=?`, blocked.ID); err != nil {
			t.Fatal(err)
		}
		h.tick(t)
		blocked = h.headTask(t)
	}
	if blocked == nil || blocked.Status != domain.KnowledgeTaskBlocked {
		t.Fatalf("the task must block after its budget: %+v", blocked)
	}
	pending := pendingReleaseID(t, h, blocked)

	// 1. Repeated ticks on a blocked task neither advance its attempts nor
	//    publish its prepared release.
	attempts := blocked.Attempt
	headBefore := h.headTask(t).ID
	for i := 0; i < 5; i++ {
		h.tick(t)
	}
	still := h.headTask(t)
	if still == nil || still.ID != headBefore || still.Status != domain.KnowledgeTaskBlocked || still.Attempt != attempts {
		t.Fatalf("blocked must stay blocked within its budget: %+v (was attempt %d)", still, attempts)
	}
	pub, err := h.store.Library().GetPublicationByTask(ctx, blocked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pub.Status != "prepared" || pub.CommittedAt != nil {
		t.Fatalf("a blocked task's publication must stay prepared: %+v", pub)
	}
	if current, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, ""); err != nil || current.ID != first.ID {
		t.Fatalf("the visible release must not move while the task is blocked: %+v %v", current, err)
	}

	// 2. A task that entered the publish phase cannot be cancelled: cancelling
	//    it would hand its release to recovery and publish for a cancelled
	//    task.
	if _, err := h.svc.CancelKnowledgeWriteTask(ctx, h.wsID, blocked.ID, "试一试"); err == nil {
		t.Fatal("a task with a prepared publication must not be cancellable")
	} else if !strings.Contains(err.Error(), "发布阶段") {
		t.Fatalf("the refusal must point at recovery: %v", err)
	}

	// A task that never published can be cancelled, and stays unpublished.
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "code.changed", Source: "git-hook", ClientKey: "boundary-2",
		Subject: map[string]any{"changed_paths": []any{"src/main/java/com/example/order/OrderService.java"}},
	}); err != nil {
		t.Fatal(err)
	}
	// A blocked write holds the queue head, so the new task waits behind it.
	tasks, err := h.svc.ListKnowledgeWriteTasks(ctx, h.wsID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) == 0 || tasks[0].ID == blocked.ID || tasks[0].Status != domain.KnowledgeTaskQueued {
		t.Fatalf("expected a newly queued task behind the blocked head: %+v", tasks)
	}
	queued := tasks[0]
	if head := h.headTask(t); head == nil || head.ID != blocked.ID {
		t.Fatalf("the blocked task must still hold the head: %+v", head)
	}
	if _, err := h.svc.CancelKnowledgeWriteTask(ctx, h.wsID, queued.ID, "重复事件"); err != nil {
		t.Fatalf("an unpublished task must be cancellable: %v", err)
	}
	for i := 0; i < 3; i++ {
		h.tick(t)
	}
	cancelled, _, err := h.svc.GetKnowledgeWriteTask(ctx, h.wsID, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != domain.KnowledgeTaskCancelled || cancelled.TargetReleaseID != "" {
		t.Fatalf("a cancelled task must stay unpublished: %+v", cancelled)
	}
	if current, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, ""); err != nil || current.ID != first.ID {
		t.Fatalf("a cancelled task must never become current: %+v %v", current, err)
	}
	if _, err := h.store.Library().GetPublicationByTask(ctx, queued.ID); err == nil {
		t.Fatal("a cancelled task must not own a publication")
	}

	// 3. The legitimate path — the head is retried — settles publication, task
	//    and event together on the same release.
	if err := os.RemoveAll(obstruction); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.RetryKnowledgeWriteTask(ctx, h.wsID, blocked.ID); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	settled, _, err := h.svc.GetKnowledgeWriteTask(ctx, h.wsID, blocked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != domain.KnowledgeTaskCompleted {
		t.Fatalf("the retried task must finish: %+v", settled)
	}
	settledPub, err := h.store.Library().GetPublicationByTask(ctx, blocked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settledPub.Status != "committed" || settledPub.CommittedAt == nil || settledPub.ReleaseID != pending {
		t.Fatalf("the publication must commit to the same release: %+v", settledPub)
	}
	if settled.TargetReleaseID != pending {
		t.Fatalf("the task must point at that release: %+v", settled)
	}
	if settled.EventID != "" {
		event, err := h.store.Library().GetEvent(ctx, h.libID(t), settled.EventID)
		if err != nil {
			t.Fatal(err)
		}
		if event.Status != domain.KnowledgeEventCompleted || event.TaskID != settled.ID {
			t.Fatalf("the event must reach its terminal state with the task: %+v", event)
		}
	}
	current, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, "")
	if err != nil || current.ID != pending {
		t.Fatalf("the committed release must become current: %+v %v", current, err)
	}
}

// TestKnowledgePublicReadReleaseGate covers every public read path that can
// name a release: they must all resolve a committed release and check that the
// object they return is a member of it. A prepared publish is invisible
// everywhere, and an old release may not be pointed at an object that only the
// new publish created.
func TestKnowledgePublicReadReleaseGate(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	first := publishTwoDocumentRelease(t, h)

	docs, _, err := h.svc.ListKnowledgeDocuments(ctx, h.wsID, first.ID, "", "", 0)
	if err != nil || len(docs) != 2 {
		t.Fatalf("fixture documents missing: %v %+v", err, docs)
	}
	docID := "doc:order-service"
	carriedDocID := "doc:common"
	carriedEvidence := ""
	firstEvidence := evidenceOfDocument(t, h, first.ID, docID)
	carriedEvidence = evidenceOfDocument(t, h, first.ID, carriedDocID)
	if firstEvidence == "" || carriedEvidence == "" {
		t.Fatal("fixture evidence missing")
	}
	firstVersion := 0
	if err := h.db.QueryRowContext(ctx, `SELECT v.version FROM knowledge_release_documents rd
		JOIN knowledge_document_versions v ON v.id = rd.document_version_id
		WHERE rd.release_id=? AND rd.document_id=?`, first.ID, docID).Scan(&firstVersion); err != nil {
		t.Fatal(err)
	}

	// Second publish that fails to materialize: its rows exist, its files do
	// not, so no reader may see it.
	obstruction := filepath.Join(h.libRoot, "content", "components", "order-service.md")
	if err := os.Remove(obstruction); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(obstruction, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "code.changed", Source: "git-hook", ClientKey: "gate-2",
		Subject: map[string]any{"changed_paths": []any{"src/main/java/com/example/order/OrderService.java"}},
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	staging := h.stagingDir(t)
	files := orderServiceAssertionEvidence(t, h.fixture.repos["order-service"])
	files["content/components/order-service.md"] = libraryDoc(
		"doc:order-service", "订单服务", "assertion:order-cancel-publishes",
		"订单服务在取消分支发布取消事件（未物化版）。", "ev-order-cancel", []string{"entity:service:order-service"})
	files["content/components/order-service-notes.md"] = libraryDoc(
		"doc:order-service-notes", "订单服务补充说明", "assertion:order-service-unmaterialized",
		"这条知识只存在于未物化的发布中。", "ev-order-cancel", []string{"entity:service:order-service"})
	files["entities.yaml"] = "entities: []\n"
	files["plan.json"] = `{"summary":"增量","documents":["content/components/order-service.md",` +
		`"content/components/order-service-notes.md"],` +
		`"removals":[],"renames":[],"coverage":{"sources_read":["order-service"],"sources_missed":[],` +
		`"gaps":[],"notes":""}}`
	writeStaging(t, staging, files)
	h.completeAgentTurn(t, domain.RunSucceeded)
	h.tick(t)

	blocked := h.headTask(t)
	if blocked == nil || blocked.Status != domain.KnowledgeTaskBlocked {
		// Spend the retry budget so the task blocks and the release stays
		// visible nowhere.
		for i := 0; i < 6 && blocked != nil && blocked.Status != domain.KnowledgeTaskBlocked; i++ {
			if _, err := h.db.ExecContext(ctx, `UPDATE knowledge_write_tasks SET next_attempt_at=NULL WHERE id=?`, blocked.ID); err != nil {
				t.Fatal(err)
			}
			h.tick(t)
			blocked = h.headTask(t)
		}
	}
	pending := pendingReleaseID(t, h, blocked)
	pendingVersion := releaseVersionOfDocument(t, h, pending, docID)

	// Evidence created by the pending publish only: the new assertion's
	// citation, which the old release must not serve.
	newEvidence := ""
	{
		var raw string
		if err := h.db.QueryRowContext(ctx, `SELECT a.evidence_json FROM knowledge_assertions a
			JOIN knowledge_release_documents rd ON rd.document_version_id = a.document_version_id
			WHERE rd.release_id=? AND a.document_id=?`, pending, docID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var refs []struct {
			EvidenceID string `json:"evidence_id"`
		}
		if err := json.Unmarshal([]byte(raw), &refs); err != nil || len(refs) == 0 {
			t.Fatalf("pending publish recorded no evidence: %s %v", raw, err)
		}
		newEvidence = refs[0].EvidenceID
	}

	mux := httpapi.NewServer(h.svc, h.store, nil).Routes()
	base := "/api/v1/workspaces/" + h.wsID + "/library"
	matrix := []struct {
		name   string
		path   string
		status int
	}{
		{"发布列表不含半完成版本", base + "/releases", http.StatusOK},
		{"文档浏览-pending", base + "/documents?release_id=" + pending, http.StatusNotFound},
		// 省略 release_id 的默认读取同样必须落在已提交版本上：正文、断言与
		// 版本历史都不含 prepared 的那一版。
		{"文档详情-默认读", base + "/documents/" + docID, http.StatusOK},
		{"文档详情-默认读+prepared版本号", base + "/documents/" + docID + "?version=" + pendingVersion, http.StatusNotFound},
		{"文档详情-pending", base + "/documents/" + docID + "?release_id=" + pending, http.StatusNotFound},
		{"文档详情-pending+version", base + "/documents/" + docID + "?release_id=" + pending + "&version=" + pendingVersion, http.StatusNotFound},
		// 版本号与 release 固定的版本冲突时必须拒绝，而不是用旧 release 标注新版本。
		{"文档详情-旧release+新version", base + "/documents/" + docID + "?release_id=" + first.ID + "&version=" + pendingVersion, http.StatusUnprocessableEntity},
		{"断言展开-pending", base + "/expand?kind=assertion&id=assertion:order-cancel-publishes&release_id=" + pending, http.StatusNotFound},
		{"新文档-默认读", base + "/documents/doc:order-service-notes", http.StatusNotFound},
		{"新文档-默认读+prepared版本号", base + "/documents/doc:order-service-notes?version=1", http.StatusNotFound},
		{"新文档-指定release", base + "/documents/doc:order-service-notes?release_id=" + pending, http.StatusNotFound},
		{"断言展开-旧release", base + "/expand?kind=assertion&id=assertion:order-cancel-publishes&release_id=" + first.ID, http.StatusOK},
		// 未物化期间，连沿用的旧证据也不可见（该发布整体未提交）。
		{"证据展开-新release沿用的旧证据-未提交", base + "/expand?kind=evidence&id=" + carriedEvidence + "&release_id=" + pending, http.StatusNotFound},
		{"证据展开-旧release引用新证据", base + "/expand?kind=evidence&id=" + newEvidence + "&release_id=" + first.ID, http.StatusNotFound},
		{"证据展开-旧release自身证据", base + "/expand?kind=evidence&id=" + firstEvidence + "&release_id=" + first.ID, http.StatusOK},
		{"桥接-pending", base + "/bridges?release_id=" + pending, http.StatusNotFound},
		{"图-pending", base + "/graph?release_id=" + pending, http.StatusNotFound},
	}
	for _, tc := range matrix {
		status, body := httpGetJSON(t, mux, tc.path)
		if status != tc.status {
			t.Fatalf("%s: status=%d want=%d body=%v", tc.name, status, tc.status, body)
		}
	}
	// The default read serves the last committed version, and neither its body
	// nor its version history mentions the unmaterialized one.
	{
		status, body := httpGetJSON(t, mux, base+"/documents/"+docID)
		if status != http.StatusOK {
			t.Fatalf("the default read must serve the committed version: %d %v", status, body)
		}
		doc, _ := body["document"].(map[string]any)
		version, _ := body["version"].(map[string]any)
		if doc == nil || version == nil {
			t.Fatalf("default read shape changed: %v", body)
		}
		if got, _ := version["version"].(float64); int(got) != firstVersion {
			t.Fatalf("the default read must serve version %d, got %v", firstVersion, version["version"])
		}
		if markdown, _ := version["content_markdown"].(string); strings.Contains(markdown, "未物化版") {
			t.Fatalf("the default read leaked the unmaterialized version:\n%s", markdown)
		}
		for _, a := range asList(body["assertions"]) {
			statement, _ := a.(map[string]any)["statement"].(string)
			if strings.Contains(statement, "未物化版") {
				t.Fatalf("the default read leaked an unmaterialized assertion: %s", statement)
			}
		}
		history, _ := body["versions"].([]any)
		if len(history) != 1 {
			t.Fatalf("the version history must contain published versions only: %v", body["versions"])
		}
	}

	// The release list must name the complete release only.
	_, listBody := httpGetJSON(t, mux, base+"/releases")
	items, _ := listBody["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("the release list must expose only the complete release: %v", listBody)
	}
	if id, _ := items[0].(map[string]any)["id"].(string); id != first.ID {
		t.Fatalf("the listed release must be %s: %v", first.ID, items[0])
	}

	// The blocked head is retried by an operator (recovery must not spend its
	// budget), and that retry commits the same release: every path above flips
	// to it.
	if err := os.RemoveAll(obstruction); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.RetryKnowledgeWriteTask(ctx, h.wsID, blocked.ID); err != nil {
		t.Fatalf("the blocked head must be retryable: %v", err)
	}
	h.tick(t)
	committed, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, "")
	if err != nil {
		t.Fatal(err)
	}
	if committed.ID != pending {
		t.Fatalf("recovery must switch to %s, got %s", pending, committed.ID)
	}
	after := []struct {
		name string
		path string
	}{
		{"默认读切到新版本", base + "/documents/" + docID},
		{"新文档可读", base + "/documents/doc:order-service-notes"},
		// 正例：新发布沿用了未改文档的旧证据，提交后必须能展开。
		{"证据展开-新release沿用的旧证据", base + "/expand?kind=evidence&id=" + carriedEvidence + "&release_id=" + pending},
		{"文档详情", base + "/documents/" + docID + "?release_id=" + pending},
		{"文档详情+正确版本", base + "/documents/" + docID + "?release_id=" + pending + "&version=" + pendingVersion},
		{"断言展开", base + "/expand?kind=assertion&id=assertion:order-cancel-publishes&release_id=" + pending},
		{"证据展开", base + "/expand?kind=evidence&id=" + newEvidence + "&release_id=" + pending},
		{"桥接", base + "/bridges?release_id=" + pending},
	}
	for _, tc := range after {
		if status, body := httpGetJSON(t, mux, tc.path); status != http.StatusOK {
			t.Fatalf("%s must succeed once committed: status=%d body=%v", tc.name, status, body)
		}
	}
}

// publishTwoDocumentRelease publishes one release containing two documents, so
// a later incremental publish can carry one of them forward.
func publishTwoDocumentRelease(t *testing.T, h *libraryHarness) *domain.KnowledgeRelease {
	t.Helper()
	ctx := context.Background()
	h.registerAllSources(t)
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "workspace.connected", Source: "test", ClientKey: "init-two-docs",
	}); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	staging := h.stagingDir(t)
	files := orderServiceAssertionEvidence(t, h.fixture.repos["order-service"])
	span := lineSpan(t, h.fixture.repos["common"], "src/main/java/com/example/common/messaging/EventPublisher.java")
	files["evidence.yaml"] = files["evidence.yaml"] +
		"  - key: ev-common-publisher\n" +
		"    binding: common@order-service#com.example:common:1.4.2\n" +
		"    path: src/main/java/com/example/common/messaging/EventPublisher.java\n" +
		"    locator:\n      kind: source_text\n      interval: half_open\n" +
		"      start: {line: 0, column: 0}\n      end: {line: " + span + ", column: 0}\n" +
		"    note: 共享事件发布接口\n"
	files["content/components/order-service.md"] = libraryDoc(
		"doc:order-service", "订单服务", "assertion:order-cancel-publishes", "订单服务在取消分支发布取消事件。",
		"ev-order-cancel", []string{"entity:service:order-service"})
	files["content/components/common.md"] = libraryDoc(
		"doc:common", "common 共享契约库", "assertion:common-publisher-interface",
		"common 提供 EventPublisher 接口供各服务发布事件。", "ev-common-publisher", []string{"entity:common:common"})
	files["entities.yaml"] = "entities:\n  - id: entity:common:common\n    kind: common\n    name: common\n"
	files["plan.json"] = `{"summary":"初始化","documents":["content/components/order-service.md","content/components/common.md"],` +
		`"removals":[],"renames":[],"coverage":{"sources_read":["order-service","common"],"sources_missed":[],` +
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

// evidenceOfDocument returns one evidence ID an assertion of that document
// cites inside one release.
func evidenceOfDocument(t *testing.T, h *libraryHarness, releaseID, documentID string) string {
	t.Helper()
	var raw string
	if err := h.db.QueryRowContext(context.Background(), `SELECT a.evidence_json
		FROM knowledge_assertions a
		JOIN knowledge_release_documents rd ON rd.document_version_id = a.document_version_id
		WHERE rd.release_id=? AND a.document_id=? ORDER BY a.ordinal LIMIT 1`, releaseID, documentID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`evidence:[0-9a-f]+`).FindString(raw)
	if match == "" {
		t.Fatalf("no evidence cited by %s in %s: %s", documentID, releaseID, raw)
	}
	return match
}

// libID returns the library id of a harness workspace.
func (h *libraryHarness) libID(t *testing.T) string {
	t.Helper()
	lib, err := h.svc.EnsureKnowledgeLibrary(context.Background(), h.wsID)
	if err != nil {
		t.Fatal(err)
	}
	return lib.ID
}

// asList tolerates the JSON shapes a decoded body may use for a list.
func asList(value any) []any {
	if list, ok := value.([]any); ok {
		return list
	}
	return nil
}

// releaseVersionOfDocument reads the version number a release pins.
func releaseVersionOfDocument(t *testing.T, h *libraryHarness, releaseID, documentID string) string {
	t.Helper()
	var version int
	if err := h.db.QueryRowContext(context.Background(), `SELECT v.version
		FROM knowledge_release_documents rd JOIN knowledge_document_versions v ON v.id = rd.document_version_id
		WHERE rd.release_id=? AND rd.document_id=?`, releaseID, documentID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return strconv.Itoa(version)
}

// TestKnowledgeIncrementalReleaseCountsWhatItContains: an incremental release
// inherits every unchanged document, so its counts must describe the whole
// release rather than only the version rows this publish rewrote.
func TestKnowledgeIncrementalReleaseCountsWhatItContains(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	first := publishInitializeRelease(t, h)
	if first.DocumentCount != 1 || first.AssertionCount != 1 {
		t.Fatalf("first release counts wrong: %+v", first)
	}
	if _, err := h.svc.SubmitKnowledgeLibraryEvent(ctx, application.KnowledgeLibraryEventInput{
		WorkspaceID: h.wsID, EventType: "code.changed", Source: "git-hook", ClientKey: "count-2",
		Subject: map[string]any{"changed_paths": []any{"src/main/java/com/example/order/OrderService.java"}},
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
	files["plan.json"] = `{"summary":"增量","documents":["content/components/order-service.md"],` +
		`"removals":[],"renames":[],"coverage":{"sources_read":["order-service"],"sources_missed":[],` +
		`"gaps":[],"notes":""}}`
	writeStaging(t, staging, files)
	h.completeAgentTurn(t, domain.RunSucceeded)
	h.tick(t)

	second, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, "")
	if err != nil {
		t.Fatal(err)
	}
	if second.Seq != 2 || second.DocumentCount != 1 {
		t.Fatalf("second release should carry the one document: %+v", second)
	}
	var versionCount, assertionCount int
	if err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_release_documents
		WHERE release_id=?`, second.ID).Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_assertions a
		JOIN knowledge_release_documents rd ON rd.document_version_id = a.document_version_id
		WHERE rd.release_id=?`, second.ID).Scan(&assertionCount); err != nil {
		t.Fatal(err)
	}
	if second.AssertionCount != assertionCount || assertionCount == 0 {
		t.Fatalf("release assertion count must describe the release: reported %d, contains %d",
			second.AssertionCount, assertionCount)
	}
	if second.EvidenceCount == 0 {
		t.Fatalf("release evidence count must describe the release: %+v", second)
	}
	// The counts are derived, so the rebuild path must be able to repair them
	// for a release that was published under older counting rules.
	if _, err := h.db.ExecContext(ctx, `UPDATE knowledge_releases SET assertion_count=999 WHERE id=?`, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.ReindexKnowledgeLibrary(ctx, h.wsID, ""); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	repaired, err := h.svc.GetKnowledgeRelease(ctx, h.wsID, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.AssertionCount != assertionCount {
		t.Fatalf("reindex must repair derived release counts: %d vs %d", repaired.AssertionCount, assertionCount)
	}
}

// TestKnowledgeExpandEvidenceHandleIsOpenable: a query hands the client an
// evidence handle, so that exact URL must open the evidence — including the
// consistency triple the client checks. The dedicated evidence route and the
// generic expand route must serve the same object.
func TestKnowledgeExpandEvidenceHandleIsOpenable(t *testing.T) {
	ctx := context.Background()
	h := newLibraryHarness(t)
	release := publishInitializeRelease(t, h)
	answer, err := h.svc.QueryKnowledgeLibrary(ctx, application.KnowledgeLibraryQuery{
		WorkspaceID: h.wsID, Question: "订单服务是否发布取消事件",
	})
	if err != nil {
		t.Fatal(err)
	}
	evidenceID := ""
	for _, handle := range answer.Expandables {
		if handle.Kind == "evidence" {
			evidenceID = handle.ID
			break
		}
	}
	if evidenceID == "" {
		t.Fatalf("the answer must offer an evidence handle: %+v", answer.Expandables)
	}
	mux := httpapi.NewServer(h.svc, h.store, nil).Routes()
	get := func(path string) (int, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		body := map[string]any{}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body
	}
	base := "/api/v1/workspaces/" + h.wsID + "/library"
	status, expanded := get(base + "/expand?kind=evidence&id=" + evidenceID)
	if status != http.StatusOK {
		t.Fatalf("the evidence handle from a query must be openable, got %d: %v", status, expanded)
	}
	if expanded["id"] != evidenceID || expanded["excerpt"] == nil {
		t.Fatalf("expanded evidence is not the one asked for: %v", expanded)
	}
	consistency, ok := expanded["consistency"].(map[string]any)
	if !ok || consistency["snapshot_id"] == nil || consistency["binding_id"] == nil ||
		consistency["representation_id"] == nil {
		t.Fatalf("expand must report the snapshot/binding/representation triple: %v", expanded)
	}
	directStatus, direct := get(base + "/evidence/" + evidenceID)
	if directStatus != http.StatusOK {
		t.Fatalf("the dedicated evidence route must keep working, got %d", directStatus)
	}
	if direct["id"] != expanded["id"] || direct["excerpt_digest"] != expanded["excerpt_digest"] {
		t.Fatalf("both routes must serve the same evidence: %v vs %v", direct, expanded)
	}
	_ = release
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
	// A pinned assertion expand reports the pinned version's metadata, not the
	// document row's current state.
	oldAssertion, oldDoc, err := h.svc.ExpandKnowledgeAssertion(ctx, h.wsID, first.ID, "assertion:order-cancel-publishes")
	if err != nil {
		t.Fatal(err)
	}
	if oldDoc.Path != "content/components/order-service.md" || oldDoc.Title != "订单服务" {
		t.Fatalf("pinned expand leaked current document metadata: %+v", oldDoc)
	}
	if oldDoc.CurrentVersion != 1 {
		t.Fatalf("pinned expand must report the pinned version number, got %d", oldDoc.CurrentVersion)
	}
	if oldAssertion.DocumentVersionID == "" {
		t.Fatalf("expanded assertion must name its version: %+v", oldAssertion)
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

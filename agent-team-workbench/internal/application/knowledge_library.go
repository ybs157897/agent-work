package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/knowledgelib"
)

// knowledgeLibraryMaxRepairAttempts bounds the task-level automatic repair
// turns. A real CLI model can need more than one pass to satisfy the record
// grammar on long documents, and each pass is a bounded, recorded turn; past
// this limit the head is explicitly blocked for an operator.
const knowledgeLibraryMaxRepairAttempts = 4

// KnowledgeLibrarianHarnessPromptVersion is the fixed prompt contract of the
// internal library agent. It is not user editable and is pinned by a SQLite
// trigger so a workspace cannot repurpose the system agent.
const KnowledgeLibrarianHarnessPromptVersion = "knowledge-harness/v2"

// KnowledgeLibrarianHarnessPrompt is the system prompt of the internal
// library agent. The concrete task lives in the staging brief, so this text
// stays stable across tasks.
const KnowledgeLibrarianHarnessPrompt = `你是资料库内部的资料整理智能体。资料库是唯一权威，你只负责按任务说明书读取固定来源、整理知识并写入本任务的暂存目录。
每一轮任务都会在暂存目录里给你 brief.md 与 task.json：先读它们，再读被点名的来源仓库，最后写出 content/ 下的知识文档、plan.json、evidence.yaml、entities.yaml。
只写暂存目录，不改来源仓库，不改资料库根目录下的发布内容。不要写版本号、发布状态、批准状态或任何摘要；证据的坐标、内容和摘要由资料程序采集。
结论必须能回到具体来源位置；不确定的内容写进「说明与未知」或 plan.json 的 coverage.gaps，不要伪装成已核实。
完成后直接用自然语言说明你做了什么即可，验收由资料程序执行。`

// ── Public request types ───────────────────────────────────────────────

// KnowledgeLibraryEventInput is one external event submission.
type KnowledgeLibraryEventInput struct {
	WorkspaceID     string
	ProtocolVersion string
	EventType       string
	Source          string
	Subject         map[string]any
	ContentRef      string
	Payload         map[string]any
	ClientKey       string
}

// KnowledgeLibraryEventReceipt is the lightweight acceptance answer. It never
// claims that knowledge has been updated.
type KnowledgeLibraryEventReceipt struct {
	EventID    string                      `json:"event_id"`
	Accepted   bool                        `json:"accepted"`
	Duplicate  bool                        `json:"duplicate"`
	TaskID     string                      `json:"task_id,omitempty"`
	QueueSeq   int                         `json:"queue_seq,omitempty"`
	Status     domain.KnowledgeEventStatus `json:"status"`
	ReceivedAt time.Time                   `json:"received_at"`
}

// KnowledgeTaskReceipt is the answer to a queue-only write request: it says
// the work was accepted and where it sits, never that it is done.
type KnowledgeTaskReceipt struct {
	TaskID   string `json:"task_id"`
	Accepted bool   `json:"accepted"`
	// Duplicate says this key was already accepted; TaskID is the original
	// task and no second write was enqueued.
	Duplicate  bool                       `json:"duplicate"`
	QueueSeq   int                        `json:"queue_seq"`
	Status     domain.KnowledgeTaskStatus `json:"status"`
	EnqueuedAt time.Time                  `json:"enqueued_at"`
}

// requirementEventTypes are the events that always describe a requirement
// document, even when the body is delivered elsewhere.
var requirementEventTypes = map[string]bool{
	"requirement.imported": true,
	"document.revised":     true,
}

// declaresRequirement reports whether an event claims to carry a requirement:
// an explicit content reference, an inline body, a declared requirement
// identity, or an event type that is a requirement import by definition.
// Everything else is an ordinary code or workspace notification.
func declaresRequirement(event *domain.KnowledgeLibraryEvent) bool {
	if event == nil {
		return false
	}
	if strings.TrimSpace(event.ContentRef) != "" {
		return true
	}
	if requirementEventTypes[strings.TrimSpace(event.EventType)] {
		return true
	}
	payload := decodeJSONObject(event.PayloadJSON)
	if firstString(payload, requirementTextKeys...) != "" {
		return true
	}
	if firstString(payload, "requirement_id", "req_id", "requirement", "requirement_version", "req_version") != "" {
		return true
	}
	return strings.TrimSpace(subjectString(event.SubjectJSON, "requirement_id")) != ""
}

// mergeFocusJSON merges extra keys into a focus object, keeping the existing
// fields and ignoring empty additions.
func mergeFocusJSON(raw string, extra map[string]any) string {
	focus := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &focus)
	}
	for key, value := range extra {
		text, isText := value.(string)
		if isText && strings.TrimSpace(text) == "" {
			continue
		}
		focus[key] = value
	}
	out, err := json.Marshal(focus)
	if err != nil {
		return raw
	}
	return string(out)
}

// validateSourceRef rejects a registered ref the server can already disprove.
// The capture path re-checks against the frozen repository, but an operator
// must learn about a typo when saving the source, not one queue turn later.
func validateSourceRef(ctx context.Context, repoPath, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	path := strings.TrimSpace(repoPath)
	if path == "" {
		return nil
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		// The capture path reports an unreadable repository with its own
		// message; there is nothing to verify here.
		return nil
	}
	runner := knowledgelib.ExecGitRunner{}
	if _, err := runner.Run(ctx, path, "rev-parse", "--git-dir"); err != nil {
		return nil
	}
	if _, err := runner.Run(ctx, path, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
		return fmt.Errorf("%w: ref %q 在 %s 中无法解析；请填写真实存在的分支、标签或提交", domain.ErrValidation, ref, path)
	}
	return nil
}

// KnowledgeLibrarySourceInput registers or updates one source.
type KnowledgeLibrarySourceInput struct {
	Name         string
	Kind         domain.KnowledgeSourceKind
	RepoPath     string
	DefaultRef   string
	IncludeGlobs []string
	ExcludeGlobs []string
	// Usages declares the concrete use bindings of this source. One common
	// library used by two services at different versions has two usages; an
	// empty list means one implicit usage.
	Usages  []domain.KnowledgeSourceUsage
	Enabled *bool
}

// ── Library provisioning ───────────────────────────────────────────────

// DefaultKnowledgeLibraryRoot returns the workspace-scoped default location.
// It stays inside the workspace root so a sandboxed Run may write its staging
// directory, while never writing into a business repository's tracked tree.
func DefaultKnowledgeLibraryRoot(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, ".agent-work", "knowledge")
}

// EnsureKnowledgeLibrary creates the library row on first use. The workspace
// root is resolved from the trusted mount registry, never from a caller path.
func (s *Service) EnsureKnowledgeLibrary(ctx context.Context, workspaceID string) (*domain.KnowledgeLibrary, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return nil, fmt.Errorf("%w: workspace_id required", domain.ErrValidation)
	}
	lib, err := s.store.Library().GetLibrary(ctx, workspaceID)
	if err == nil {
		if _, statErr := os.Stat(lib.RootPath); statErr != nil {
			if mkErr := os.MkdirAll(lib.RootPath, 0o755); mkErr != nil {
				return nil, mkErr
			}
		}
		if _, mkErr := knowledgelib.NewLibrary(lib.RootPath); mkErr != nil {
			return nil, mkErr
		}
		return lib, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}
	root, rootErr := s.workspaceRoot(ctx, workspaceID)
	if rootErr != nil {
		return nil, rootErr
	}
	rootPath := DefaultKnowledgeLibraryRoot(root)
	kl, mkErr := knowledgelib.NewLibrary(rootPath)
	if mkErr != nil {
		return nil, mkErr
	}
	if mkErr := kl.Ensure(); mkErr != nil {
		return nil, mkErr
	}
	librarian, libErr := s.EnsureBuiltinKnowledgeLibrarian(ctx, workspaceID)
	if libErr != nil {
		return nil, libErr
	}
	now := time.Now().UTC()
	lib = &domain.KnowledgeLibrary{
		ID: domain.NewID("klib_"), WorkspaceID: workspaceID, RootPath: kl.Root,
		LibrarianAgentID: librarian.ID, Enabled: true, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.Library().CreateLibrary(ctx, lib); err != nil {
		// A concurrent creator wins; read back instead of failing the caller.
		if existing, getErr := s.store.Library().GetLibrary(ctx, workspaceID); getErr == nil {
			return existing, nil
		}
		return nil, err
	}
	return lib, nil
}

// SetKnowledgeWorkspaceRootResolver installs the Host-local trust hook that
// maps a workspace to its authorized root directory.
func (s *Service) SetKnowledgeWorkspaceRootResolver(v func(ctx context.Context, workspaceID string) (string, error)) {
	s.knowledgeWorkspaceRoot = v
}

// workspaceRoot resolves the workspace's authorized root directory.
func (s *Service) workspaceRoot(ctx context.Context, workspaceID string) (string, error) {
	if s.knowledgeWorkspaceRoot == nil {
		return "", fmt.Errorf("%w: knowledge library requires a Host root resolver", domain.ErrCapabilityMissing)
	}
	root, err := s.knowledgeWorkspaceRoot(ctx, workspaceID)
	if err != nil {
		return "", fmt.Errorf("%w: workspace %s has no authorized root: %v", domain.ErrCapabilityMissing, workspaceID, err)
	}
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("%w: workspace %s authorized root is empty", domain.ErrCapabilityMissing, workspaceID)
	}
	return root, nil
}

// libraryRoot opens the library filesystem view.
func (s *Service) libraryRoot(lib *domain.KnowledgeLibrary) (knowledgelib.Library, error) {
	if lib == nil {
		return knowledgelib.Library{}, fmt.Errorf("%w: knowledge library is required", domain.ErrValidation)
	}
	return knowledgelib.NewLibrary(lib.RootPath)
}

// GetKnowledgeLibrary returns the library, provisioning it on first use.
func (s *Service) GetKnowledgeLibrary(ctx context.Context, workspaceID string) (*domain.KnowledgeLibrary, error) {
	return s.EnsureKnowledgeLibrary(ctx, workspaceID)
}

// ── Sources ────────────────────────────────────────────────────────────

// ListKnowledgeSources returns the registered sources of the library.
func (s *Service) ListKnowledgeSources(ctx context.Context, workspaceID string) ([]*domain.KnowledgeSource, error) {
	lib, err := s.EnsureKnowledgeLibrary(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return s.store.Library().ListSources(ctx, lib.ID)
}

// CreateKnowledgeSource registers one repository in the library.
func (s *Service) CreateKnowledgeSource(ctx context.Context, workspaceID string, in KnowledgeLibrarySourceInput) (*domain.KnowledgeSource, error) {
	lib, err := s.EnsureKnowledgeLibrary(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if err := validateSourceInput(in); err != nil {
		return nil, err
	}
	root, err := s.workspaceRoot(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	repoPath, err := resolveSourcePath(root, in.RepoPath)
	if err != nil {
		return nil, err
	}
	if err := validateSourceRef(ctx, repoPath, in.DefaultRef); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	src := &domain.KnowledgeSource{
		ID: domain.NewID("ksrc_"), LibraryID: lib.ID, Name: strings.TrimSpace(in.Name),
		Kind: in.Kind, RepoPath: repoPath, DefaultRef: strings.TrimSpace(in.DefaultRef),
		IncludeGlobs: in.IncludeGlobs, ExcludeGlobs: in.ExcludeGlobs,
		Usages:  normalizeUsages(in.Usages),
		Enabled: enabled, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.Library().CreateSource(ctx, src); err != nil {
		return nil, err
	}
	return src, nil
}

// UpdateKnowledgeSource changes one source with an optimistic version check.
func (s *Service) UpdateKnowledgeSource(ctx context.Context, workspaceID, sourceID string, expectedVersion int, in KnowledgeLibrarySourceInput) (*domain.KnowledgeSource, error) {
	lib, err := s.EnsureKnowledgeLibrary(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if err := validateSourceInput(in); err != nil {
		return nil, err
	}
	current, err := s.store.Library().GetSource(ctx, lib.ID, sourceID)
	if err != nil {
		return nil, err
	}
	root, err := s.workspaceRoot(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	repoPath, err := resolveSourcePath(root, in.RepoPath)
	if err != nil {
		return nil, err
	}
	if err := validateSourceRef(ctx, repoPath, in.DefaultRef); err != nil {
		return nil, err
	}
	next := *current
	next.Name = strings.TrimSpace(in.Name)
	next.Kind = in.Kind
	next.RepoPath = repoPath
	next.DefaultRef = strings.TrimSpace(in.DefaultRef)
	next.IncludeGlobs, next.ExcludeGlobs = in.IncludeGlobs, in.ExcludeGlobs
	next.Usages = normalizeUsages(in.Usages)
	if in.Enabled != nil {
		next.Enabled = *in.Enabled
	}
	next.UpdatedAt = time.Now().UTC()
	if err := s.store.Library().UpdateSource(ctx, &next, expectedVersion); err != nil {
		return nil, err
	}
	next.Version = expectedVersion + 1
	return &next, nil
}

// DeleteKnowledgeSource retires a source from future snapshots. The ledger
// keeps naming it, so historical evidence stays explainable.
func (s *Service) DeleteKnowledgeSource(ctx context.Context, workspaceID, sourceID string) error {
	lib, err := s.EnsureKnowledgeLibrary(ctx, workspaceID)
	if err != nil {
		return err
	}
	return s.store.Library().DeleteSource(ctx, lib.ID, sourceID)
}

func validateSourceInput(in KnowledgeLibrarySourceInput) error {
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("%w: source name required", domain.ErrValidation)
	}
	if !in.Kind.Valid() {
		return fmt.Errorf("%w: source kind %q invalid", domain.ErrValidation, in.Kind)
	}
	if strings.TrimSpace(in.RepoPath) == "" {
		return fmt.Errorf("%w: source repo_path required", domain.ErrValidation)
	}
	return nil
}

// normalizeUsages trims and de-duplicates declared usages, so repeating the
// same consumer/artifact binding is refused instead of creating a duplicate
// snapshot binding.
func normalizeUsages(in []domain.KnowledgeSourceUsage) []domain.KnowledgeSourceUsage {
	out := make([]domain.KnowledgeSourceUsage, 0, len(in))
	seen := map[string]bool{}
	for _, usage := range in {
		usage.Consumer = strings.TrimSpace(usage.Consumer)
		usage.Artifact = strings.TrimSpace(usage.Artifact)
		usage.Environment = strings.TrimSpace(usage.Environment)
		usage.ResolutionRef = strings.TrimSpace(usage.ResolutionRef)
		// The raw claim is stored as submitted so the record keeps saying what
		// the caller asserted; ArtifactState() is the single source of truth
		// for the effective state, and the read API reports both. Rewriting the
		// claim here would erase the fact that it lacked a basis.
		key := usage.Consumer + "\x00" + usage.Artifact + "\x00" + usage.Environment
		if seen[key] {
			// A repeated usage keeps the strongest verifiable claim rather than
			// silently dropping the second one.
			continue
		}
		seen[key] = true
		out = append(out, usage)
	}
	return out
}

// resolveSourcePath keeps every source inside the workspace root and rejects
// symlinked escapes, so a registered source can never point outside the
// workspace the control plane is authorized for.
func resolveSourcePath(workspaceRoot, candidate string) (string, error) {
	abs := candidate
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(workspaceRoot, candidate)
	}
	abs = filepath.Clean(abs)
	realRoot, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		realRoot = workspaceRoot
	}
	realPath, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// The path does not exist yet. Its deepest existing ancestor is still
		// resolved, so containment is decided on real paths: comparing a
		// lexical path against a symlink-resolved root rejects legitimate
		// paths on systems where the root itself is reached through a symlink.
		resolved, rest, ok := evalExistingAncestor(abs)
		if !ok {
			return "", fmt.Errorf("%w: source path %q cannot be resolved", domain.ErrValidation, candidate)
		}
		if resolved != realRoot && !strings.HasPrefix(resolved+string(filepath.Separator), realRoot+string(filepath.Separator)) {
			return "", fmt.Errorf("%w: source path %q is outside the workspace root", domain.ErrValidation, candidate)
		}
		return filepath.Join(resolved, rest), nil
	}
	if realPath != realRoot && !strings.HasPrefix(realPath+string(filepath.Separator), realRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: source path %q is outside the workspace root", domain.ErrValidation, candidate)
	}
	return realPath, nil
}

// evalExistingAncestor resolves the deepest existing ancestor of a path and
// returns it together with the not-yet-existing remainder.
func evalExistingAncestor(path string) (string, string, bool) {
	current := filepath.Clean(path)
	rest := ""
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return resolved, rest, true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", "", false
		}
		rest = filepath.Join(filepath.Base(current), rest)
		current = parent
	}
}

// ── Events: async acceptance ───────────────────────────────────────────

// SubmitKnowledgeLibraryEvent durably accepts one event and enqueues exactly
// one FIFO write task. The caller gets a receipt and continues; it never waits
// for analysis, writing or publication.
func (s *Service) SubmitKnowledgeLibraryEvent(ctx context.Context, in KnowledgeLibraryEventInput) (*KnowledgeLibraryEventReceipt, error) {
	if strings.TrimSpace(in.EventType) == "" {
		return nil, fmt.Errorf("%w: event_type required", domain.ErrValidation)
	}
	if strings.TrimSpace(in.Source) == "" {
		return nil, fmt.Errorf("%w: source required", domain.ErrValidation)
	}
	clientKey := strings.TrimSpace(in.ClientKey)
	if clientKey == "" {
		return nil, fmt.Errorf("%w: client_key required", domain.ErrValidation)
	}
	lib, err := s.EnsureKnowledgeLibrary(ctx, in.WorkspaceID)
	if err != nil {
		return nil, err
	}
	subjectJSON := jsonTextOrEmpty(in.Subject)
	payloadJSON := jsonTextOrEmpty(in.Payload)
	// One active view per library: a view the publish path cannot keep
	// separate must be refused here rather than silently folded into the
	// shared baseline.
	requestedView := eventViewFromJSON(subjectJSON, payloadJSON)
	activeView := s.activeViewID(ctx, lib)
	if requestedView != activeView {
		return nil, fmt.Errorf("%w: 本版本一个资料库只有一个活动视图 %q，事件请求的视图 %q 不受支持：分支视图需要独立的历史链，本版本尚未实现",
			domain.ErrValidation, activeView, requestedView)
	}
	protocol := strings.TrimSpace(in.ProtocolVersion)
	if protocol == "" {
		protocol = "1"
	}
	digest := sha256Hex(protocol, in.EventType, in.Source, subjectJSON, in.ContentRef, payloadJSON)
	now := time.Now().UTC()
	event := &domain.KnowledgeLibraryEvent{
		ID: domain.NewID("kevt_"), LibraryID: lib.ID, ProtocolVersion: protocol,
		EventType: strings.TrimSpace(in.EventType), Source: strings.TrimSpace(in.Source),
		SubjectJSON: subjectJSON, ContentRef: strings.TrimSpace(in.ContentRef), PayloadJSON: payloadJSON,
		ClientKey: clientKey, RequestDigest: digest,
		Status: domain.KnowledgeEventAccepted, ReceivedAt: now, UpdatedAt: now,
	}
	// A requirement body is only looked for when the caller actually declared
	// one. A code or workspace notification carries no requirement, and
	// treating it as a failed import would fill the brief with a gap nobody
	// reported.
	var draft *requirementDraft
	if declaresRequirement(event) {
		// The text is read and digested now, at acceptance: a queued task must
		// document the text that was accepted, not whatever the referenced
		// path happens to contain when the task reaches the head.
		draft, err = s.prepareRequirementDraft(ctx, in.WorkspaceID, event)
		if err != nil {
			return nil, err
		}
	}
	kl, err := s.libraryRoot(lib)
	if err != nil {
		return nil, err
	}
	var input *domain.KnowledgeRequirementInput
	var receipt *KnowledgeLibraryEventReceipt
	err = s.store.InTx(ctx, func(ctx context.Context) error {
		created, err := s.store.Library().InsertEvent(ctx, event)
		if err != nil {
			return err
		}
		if !created {
			existing, err := s.store.Library().GetEventByClientKey(ctx, lib.ID, clientKey)
			if err != nil {
				return err
			}
			task, _ := s.taskForEvent(ctx, lib.ID, existing.TaskID)
			receipt = &KnowledgeLibraryEventReceipt{
				EventID: existing.ID, Accepted: true, Duplicate: true, TaskID: existing.TaskID,
				QueueSeq: taskSeq(task), Status: existing.Status, ReceivedAt: existing.ReceivedAt,
			}
			return nil
		}
		if draft != nil && draft.Text != "" {
			input, err = s.freezeRequirementInput(ctx, lib, kl, event.ID, draft)
			if err != nil {
				return err
			}
		}
		task, err := s.enqueueTaskLocked(ctx, lib, event)
		if err != nil {
			return err
		}
		if input != nil {
			task.RequirementInputID = input.ID
		}
		if draft != nil && input == nil {
			// The accepted event declares a requirement whose text could not be
			// obtained: keep the identity and the reason on the task so the
			// brief reports the gap instead of dropping the requirement.
			task.FocusJSON = mergeFocusJSON(task.FocusJSON, map[string]any{
				"requirement_id": draft.RequirementID, "requirement_version": draft.Version,
				"title": draft.Title, "requirement_unresolved": draft.Unresolved,
			})
		}
		if input != nil || (draft != nil && draft.Unresolved != "") {
			if err := s.store.Library().UpdateTask(ctx, task); err != nil {
				return err
			}
		}
		event.Status = domain.KnowledgeEventQueued
		event.TaskID = task.ID
		receipt = &KnowledgeLibraryEventReceipt{
			EventID: event.ID, Accepted: true, TaskID: task.ID, QueueSeq: task.Seq,
			Status: event.Status, ReceivedAt: event.ReceivedAt,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if s.notifier != nil {
		s.notifier.Notify(lib.WorkspaceID)
	}
	return receipt, nil
}

func taskSeq(t *domain.KnowledgeWriteTask) int {
	if t == nil {
		return 0
	}
	return t.Seq
}

func (s *Service) taskForEvent(ctx context.Context, libraryID, taskID string) (*domain.KnowledgeWriteTask, error) {
	if taskID == "" {
		return nil, domain.ErrNotFound
	}
	return s.store.Library().GetTask(ctx, libraryID, taskID)
}

// enqueueTaskLocked appends one task at the tail of the library's FIFO. The
// kind is derived from the event, never chosen by the caller.
func (s *Service) enqueueTaskLocked(ctx context.Context, lib *domain.KnowledgeLibrary, event *domain.KnowledgeLibraryEvent) (*domain.KnowledgeWriteTask, error) {
	// The tail is the highest sequence ever allocated, not the head's
	// The queue position is allocated by the insert itself: several events may
	// be accepted while one task holds the head, and a concurrent reindex must
	// not be able to claim the same position.
	kind := taskKindForEvent(event.EventType, lib.CurrentReleaseID == "")
	// Only record a baseline the library can actually resolve. A dangling
	// current_release_id must surface as a clear block at head time rather than
	// as a foreign-key error, and must never be used as an empty parent.
	baseReleaseID := ""
	if lib.CurrentReleaseID != "" {
		if _, err := s.store.Library().GetRelease(ctx, lib.ID, lib.CurrentReleaseID); err == nil {
			baseReleaseID = lib.CurrentReleaseID
		}
	}
	now := time.Now().UTC()
	task := &domain.KnowledgeWriteTask{
		ID: domain.NewID("ktask_"), LibraryID: lib.ID, Kind: kind,
		EventID: event.ID, Status: domain.KnowledgeTaskQueued, BaseReleaseID: baseReleaseID,
		ViewID: viewForEvent(event), FocusJSON: focusForEvent(event),
		MaxAttempts: 3, MaxRepairAttempts: knowledgeLibraryMaxRepairAttempts, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.Library().CreateTaskWithEvent(ctx, task); err != nil {
		return nil, err
	}
	return task, nil
}

// taskKindForEvent maps an external fact onto the internal work the library
// decides it needs. The caller never selects the workflow.
func taskKindForEvent(eventType string, empty bool) string {
	switch strings.TrimSpace(eventType) {
	case "requirement.imported", "document.revised", "code.pulled", "code.changed", "workspace.connected":
	default:
	}
	if empty {
		return knowledgelib.TaskInitialize
	}
	switch strings.TrimSpace(eventType) {
	case "source.removed":
		return knowledgelib.TaskRemove
	case "source.renamed":
		return knowledgelib.TaskRename
	default:
		// branch.switched belongs here: the library has one active view, so a
		// switched branch is a new revision of the same input to re-read, not
		// a second knowledge line.
		return knowledgelib.TaskIncremental
	}
}

// eventViewFromJSON reads the declared view from either the subject or the
// payload, the same way focusForEvent does.
func eventViewFromJSON(subjectJSON, payloadJSON string) string {
	for _, raw := range []string{subjectJSON, payloadJSON} {
		if v, ok := decodeJSONObject(raw)["view_id"].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return knowledgelib.DefaultViewID
}

func viewForEvent(event *domain.KnowledgeLibraryEvent) string {
	if v := eventField(event, "view_id"); v != nil {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return knowledgelib.DefaultViewID
}

// activeViewID is the single view this library writes into. This build keeps
// one active view per library: branch overlays would need their own
// parent/current release chain, and pretending to isolate a view the publish
// path does not separate would be worse than refusing it.
func (s *Service) activeViewID(ctx context.Context, lib *domain.KnowledgeLibrary) string {
	if strings.TrimSpace(lib.CurrentSnapshotID) == "" {
		return knowledgelib.DefaultViewID
	}
	snap, err := s.store.Library().GetSnapshot(ctx, lib.CurrentSnapshotID)
	if err != nil || strings.TrimSpace(snap.ViewID) == "" {
		return knowledgelib.DefaultViewID
	}
	return snap.ViewID
}

// eventField reads one field from the event subject, falling back to the
// payload. Integrators legitimately put commit facts in either place; the
// library must not lose them just because it picked one location.
func eventField(event *domain.KnowledgeLibraryEvent, key string) any {
	var subject map[string]any
	if err := json.Unmarshal([]byte(event.SubjectJSON), &subject); err == nil {
		if v, ok := subject[key]; ok && v != nil {
			return v
		}
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err == nil {
		if v, ok := payload[key]; ok && v != nil {
			return v
		}
	}
	return nil
}

func focusForEvent(event *domain.KnowledgeLibraryEvent) string {
	focus := map[string]any{"event_type": event.EventType, "source": event.Source}
	for _, key := range []string{
		"changed_paths", "removed_paths", "renamed_paths", "source_names",
		"previous_version", "observed_version",
		// code.pulled / code.changed carry their commit facts in the payload.
		"branch", "ref", "head_sha", "previous_sha", "base_sha", "compare_url",
		// requirement.imported identifies the requirement being revised.
		"requirement_id", "requirement_version", "title",
	} {
		if v := eventField(event, key); v != nil {
			focus[key] = v
		}
	}
	raw, _ := json.Marshal(focus)
	return string(raw)
}

// ── Task and release reads ─────────────────────────────────────────────

// ListKnowledgeLibraryEvents returns recent events with their processing state.
func (s *Service) ListKnowledgeLibraryEvents(ctx context.Context, workspaceID, status string, limit int) ([]*domain.KnowledgeLibraryEvent, error) {
	lib, err := s.EnsureKnowledgeLibrary(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return s.store.Library().ListEvents(ctx, lib.ID, status, limit)
}

// ListKnowledgeWriteTasks returns the FIFO queue, newest first.
func (s *Service) ListKnowledgeWriteTasks(ctx context.Context, workspaceID, status string, limit int) ([]*domain.KnowledgeWriteTask, error) {
	lib, err := s.EnsureKnowledgeLibrary(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return s.store.Library().ListTasks(ctx, lib.ID, status, limit)
}

// GetKnowledgeWriteTask returns one task with its turns.
func (s *Service) GetKnowledgeWriteTask(ctx context.Context, workspaceID, taskID string) (*domain.KnowledgeWriteTask, []*domain.KnowledgeTaskTurn, error) {
	lib, err := s.EnsureKnowledgeLibrary(ctx, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	task, err := s.store.Library().GetTask(ctx, lib.ID, taskID)
	if err != nil {
		return nil, nil, err
	}
	turns, err := s.store.Library().ListTurns(ctx, taskID)
	if err != nil {
		return nil, nil, err
	}
	return task, turns, nil
}

// RetryKnowledgeWriteTask clears a blocked task so the worker picks the head
// up again. Only the head may be retried, because only the head holds the
// single-writer slot.
func (s *Service) RetryKnowledgeWriteTask(ctx context.Context, workspaceID, taskID string) (*domain.KnowledgeWriteTask, error) {
	lib, err := s.EnsureKnowledgeLibrary(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return s.reviveTask(ctx, lib, taskID, domain.KnowledgeTaskQueued, "")
}

// CancelKnowledgeWriteTask explicitly cancels a task that has not published.
// A published release is never rolled back by cancellation.
func (s *Service) CancelKnowledgeWriteTask(ctx context.Context, workspaceID, taskID, reason string) (*domain.KnowledgeWriteTask, error) {
	lib, err := s.EnsureKnowledgeLibrary(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	task, err := s.store.Library().GetTask(ctx, lib.ID, taskID)
	if err != nil {
		return nil, err
	}
	if task.Status == domain.KnowledgeTaskCompleted {
		return nil, fmt.Errorf("%w: task %s already published; submit a new knowledge change instead", domain.ErrStateConflict, taskID)
	}
	if task.Status == domain.KnowledgeTaskRunning {
		return nil, fmt.Errorf("%w: task %s is running; wait for it to reach a terminal state", domain.ErrStateConflict, taskID)
	}
	if pub, err := s.store.Library().GetPublicationByTask(ctx, taskID); err == nil && pub.Status == "committed" {
		return nil, fmt.Errorf("%w: task %s already published release %s", domain.ErrStateConflict, taskID, pub.ReleaseID)
	}
	now := time.Now().UTC()
	task.Status = domain.KnowledgeTaskCancelled
	task.BlockedReason = strings.TrimSpace(reason)
	task.UpdatedAt = now
	task.FinishedAt = &now
	if err := s.store.Library().UpdateTask(ctx, task); err != nil {
		return nil, err
	}
	if task.EventID != "" {
		if err := s.store.Library().UpdateEventStatus(ctx, task.EventID, domain.KnowledgeEventFailed, task.ID); err != nil {
			return nil, err
		}
	}
	return task, nil
}

func (s *Service) reviveTask(ctx context.Context, lib *domain.KnowledgeLibrary, taskID string, status domain.KnowledgeTaskStatus, reason string) (*domain.KnowledgeWriteTask, error) {
	task, err := s.store.Library().GetTask(ctx, lib.ID, taskID)
	if err != nil {
		return nil, err
	}
	head, err := s.store.Library().HeadTask(ctx, lib.ID)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}
	if head == nil || head.ID != taskID {
		return nil, fmt.Errorf("%w: only the queue head can be retried", domain.ErrStateConflict)
	}
	task.Status = status
	task.BlockedReason = ""
	task.LastError = reason
	task.NextAttemptAt = nil
	task.Attempt = 0
	task.UpdatedAt = time.Now().UTC()
	if err := s.store.Library().UpdateTask(ctx, task); err != nil {
		return nil, err
	}
	return task, nil
}

// IsKnowledgeLibraryWorkItem reports whether a WorkItem is an internal
// library write task. The persisted task relation is authoritative; titles and
// display strings are never used to classify a public conversation.
func (s *Service) IsKnowledgeLibraryWorkItem(ctx context.Context, workItemID string) (bool, error) {
	if strings.TrimSpace(workItemID) == "" {
		return false, fmt.Errorf("%w: work_item_id required", domain.ErrValidation)
	}
	_, err := s.store.Library().TaskByWorkItem(ctx, workItemID)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ListKnowledgeReleases returns published releases, newest first.
func (s *Service) ListKnowledgeReleases(ctx context.Context, workspaceID string, limit int) ([]*domain.KnowledgeRelease, error) {
	lib, err := s.EnsureKnowledgeLibrary(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return s.store.Library().ListReleases(ctx, lib.ID, limit)
}

// GetKnowledgeRelease returns one release.
func (s *Service) GetKnowledgeRelease(ctx context.Context, workspaceID, releaseID string) (*domain.KnowledgeRelease, error) {
	lib, err := s.EnsureKnowledgeLibrary(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if releaseID == "" {
		rel, err := s.store.Library().CurrentRelease(ctx, lib.ID)
		if err != nil {
			return nil, err
		}
		return rel, nil
	}
	return s.store.Library().GetRelease(ctx, lib.ID, releaseID)
}

// ListKnowledgeDocuments browses the documents pinned in one release.
func (s *Service) ListKnowledgeDocuments(ctx context.Context, workspaceID, releaseID, query, kind string, limit int) ([]*domain.KnowledgeDocument, *domain.KnowledgeRelease, error) {
	lib, err := s.EnsureKnowledgeLibrary(ctx, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	rel, err := s.resolveRelease(ctx, lib, releaseID)
	if err != nil {
		return nil, nil, err
	}
	if rel == nil {
		return nil, nil, nil
	}
	docs, err := s.store.Library().ReleaseDocuments(ctx, rel.ID, query, kind, limit)
	if err != nil {
		return nil, nil, err
	}
	return docs, rel, nil
}

func (s *Service) resolveRelease(ctx context.Context, lib *domain.KnowledgeLibrary, releaseID string) (*domain.KnowledgeRelease, error) {
	if strings.TrimSpace(releaseID) == "" {
		rel, err := s.store.Library().CurrentRelease(ctx, lib.ID)
		if errors.Is(err, domain.ErrNotFound) {
			return nil, nil
		}
		return rel, err
	}
	return s.store.Library().GetRelease(ctx, lib.ID, releaseID)
}

// KnowledgeDocumentDetail is one pinned document with its records.
type KnowledgeDocumentDetail struct {
	Document   *domain.KnowledgeDocument           `json:"document"`
	Version    *domain.KnowledgeDocumentVersion    `json:"version"`
	Assertions []domain.KnowledgeAssertion         `json:"assertions"`
	Relations  []domain.KnowledgeAssertionRelation `json:"relations"`
	Versions   []*domain.KnowledgeDocumentVersion  `json:"versions"`
	// ReleaseID and Pinned say whether this read was pinned to one release
	// rather than to the document's current version.
	ReleaseID string `json:"release_id,omitempty"`
	Pinned    bool   `json:"pinned"`
	// EvidenceAliases maps staged evidence keys to canonical IDs for versions
	// published before canonical rewriting; the client resolves a citation
	// through it instead of reporting a broken link.
	EvidenceAliases map[string]string `json:"-"`
}

// decodeAliasMap tolerates the empty and malformed values older rows carry.
func decodeAliasMap(raw string) map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]string{}
	}
	return out
}

// GetKnowledgeDocument returns a document's records. A non-zero version pins
// that historical version; a release pin selects the version that release
// published, so opening a document from an old release never silently reads
// the current text or the current path.
func (s *Service) GetKnowledgeDocument(ctx context.Context, workspaceID, documentID, releaseID string, version int) (*KnowledgeDocumentDetail, error) {
	lib, err := s.EnsureKnowledgeLibrary(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	doc, err := s.store.Library().GetDocument(ctx, lib.ID, documentID)
	if err != nil {
		return nil, err
	}
	pinnedReleaseID := strings.TrimSpace(releaseID)
	if version <= 0 && pinnedReleaseID != "" {
		pinned, err := s.store.Library().ReleaseDocumentVersion(ctx, pinnedReleaseID, documentID)
		if err != nil {
			return nil, err
		}
		version = pinned.Version
	}
	v, assertions, relations, err := s.store.Library().DocumentVersionDetail(ctx, documentID, version)
	if err != nil {
		return nil, err
	}
	versions, err := s.store.Library().ListDocumentVersions(ctx, documentID)
	if err != nil {
		return nil, err
	}
	// The version row is the authority for path and title: a document that was
	// renamed or rewritten after this version was published must not leak its
	// current metadata into a historical read.
	if v.Path != "" {
		doc.Path = v.Path
	} else {
		doc.Path = ""
	}
	doc.Title = v.Title
	doc.CurrentVersion = v.Version
	detail := &KnowledgeDocumentDetail{Document: doc, Version: v, Assertions: assertions, Relations: relations, Versions: versions}
	detail.Pinned = true
	if pinnedReleaseID != "" {
		detail.ReleaseID = pinnedReleaseID
	} else if v.ReleaseID != "" {
		detail.ReleaseID = v.ReleaseID
	}
	// Resolve the version's own evidence aliases so a release published before
	// canonical rewriting still opens its citations instead of failing.
	detail.EvidenceAliases = decodeAliasMap(v.EvidenceAliasJSON)
	return detail, nil
}

// GetKnowledgeEvidence returns collector-verified evidence with its binding
// and representation, which is what makes "open the original" possible.
func (s *Service) GetKnowledgeEvidence(ctx context.Context, workspaceID, evidenceID string) (*domain.KnowledgeEvidence, *domain.KnowledgeSnapshotBinding, *domain.KnowledgeRepresentation, error) {
	lib, err := s.EnsureKnowledgeLibrary(ctx, workspaceID)
	if err != nil {
		return nil, nil, nil, err
	}
	ev, err := s.store.Library().GetEvidence(ctx, lib.ID, evidenceID)
	if err != nil {
		return nil, nil, nil, err
	}
	binding, err := s.store.Library().GetBinding(ctx, ev.BindingID)
	if err != nil {
		return nil, nil, nil, err
	}
	rep, err := s.store.Library().GetRepresentation(ctx, ev.RepresentationID)
	if err != nil {
		return nil, nil, nil, err
	}
	return ev, binding, rep, nil
}

// KnowledgeGraph is the entity/relation view of one release.
type KnowledgeGraph struct {
	ReleaseID string                              `json:"release_id"`
	Entities  []*domain.KnowledgeEntity           `json:"entities"`
	Relations []domain.KnowledgeAssertionRelation `json:"relations"`
	Documents []*domain.KnowledgeDocument         `json:"documents"`
}

// GetKnowledgeGraph returns the release-pinned graph for browsing.
func (s *Service) GetKnowledgeGraph(ctx context.Context, workspaceID, releaseID string) (*KnowledgeGraph, error) {
	lib, err := s.EnsureKnowledgeLibrary(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	rel, err := s.resolveRelease(ctx, lib, releaseID)
	if err != nil {
		return nil, err
	}
	if rel == nil {
		return &KnowledgeGraph{}, nil
	}
	entities, relations, docs, err := s.store.Library().ReleaseGraph(ctx, lib.ID, rel.ID)
	if err != nil {
		return nil, err
	}
	return &KnowledgeGraph{ReleaseID: rel.ID, Entities: entities, Relations: relations, Documents: docs}, nil
}

// ListKnowledgeBridges returns derived cross-service views.
func (s *Service) ListKnowledgeBridges(ctx context.Context, workspaceID, releaseID string) ([]*domain.KnowledgeBridgeView, error) {
	lib, err := s.EnsureKnowledgeLibrary(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return s.store.Library().ListBridges(ctx, lib.ID, releaseID)
}

// ReindexKnowledgeLibrary rebuilds the search projection from the immutable
// document versions and the ledger. It is the recovery path that proves the
// index is derived, not authoritative.
//
// It rewrites index rows and content files, so it is a knowledge write and
// must run as a queued task head — never inline from a request handler.
// ReindexKnowledgeLibrary enqueues a reindex as a normal FIFO write task and
// returns its receipt. Rebuilding the index rewrites derived data, but it must
// not race a publish: the index would be built from a version that is about to
// be superseded. Queueing it behind the head is what makes the ordering
// guarantee real, and the receipt lets the caller watch it like any other task.
//
// The queue position is allocated by the inserted statement, so a reindex
// accepted at the same moment as an event cannot claim the same position. A
// caller that retries with the same client key gets the original receipt back
// instead of a second rebuild.
func (s *Service) ReindexKnowledgeLibrary(ctx context.Context, workspaceID, clientKey string) (*KnowledgeTaskReceipt, error) {
	lib, err := s.EnsureKnowledgeLibrary(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	clientKey = strings.TrimSpace(clientKey)
	if clientKey != "" {
		if existing, err := s.store.Library().TaskByClientKey(ctx, lib.ID, clientKey); err == nil {
			return &KnowledgeTaskReceipt{
				TaskID: existing.ID, Accepted: true, Duplicate: true, QueueSeq: existing.Seq,
				Status: existing.Status, EnqueuedAt: existing.CreatedAt,
			}, nil
		}
	}
	now := time.Now().UTC()
	task := &domain.KnowledgeWriteTask{
		ID: domain.NewID("ktask_"), LibraryID: lib.ID, Kind: knowledgelib.TaskReindex,
		Status: domain.KnowledgeTaskQueued, BaseReleaseID: lib.CurrentReleaseID, ClientKey: clientKey,
		ViewID: knowledgelib.DefaultViewID, FocusJSON: `{"event_type":"library.reindex","source":"admin"}`,
		MaxAttempts: 3, MaxRepairAttempts: knowledgeLibraryMaxRepairAttempts, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.Library().CreateTask(ctx, task); err != nil {
		return nil, err
	}
	return &KnowledgeTaskReceipt{
		TaskID: task.ID, Accepted: true, QueueSeq: task.Seq,
		Status: domain.KnowledgeTaskQueued, EnqueuedAt: now,
	}, nil
}

// runReindexTask executes one queued reindex at the head of the FIFO queue.
// It is idempotent by construction: every step rebuilds a derived projection
// from the immutable document versions and the evidence ledger, so a re-run
// after a crash converges on the same result.
func (s *Service) runReindexTask(ctx context.Context, lib *domain.KnowledgeLibrary, task *domain.KnowledgeWriteTask) error {
	owner := domain.NewID("knowner_")
	claimed, err := s.store.Library().ClaimTask(ctx, lib.ID, task.ID, owner)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	task, err = s.store.Library().GetTask(ctx, lib.ID, task.ID)
	if err != nil {
		return err
	}
	count, err := s.reindexProjection(ctx, lib)
	if err != nil {
		// A failing rebuild spends the same bounded retry budget as any other
		// knowledge write and ends up blocked with a reason, instead of
		// resetting to queued forever and hiding a permanent failure.
		return s.deferTask(ctx, lib, task, err)
	}
	lines := []string{fmt.Sprintf("索引已重建：%d 篇文档的检索行由已发布版本重新生成", count)}
	diag, _ := json.Marshal(lines)
	task.DiagnosticsJSON = string(diag)
	task.Status = domain.KnowledgeTaskCompleted
	task.LastError = ""
	task.BlockedReason = ""
	finished := time.Now().UTC()
	task.FinishedAt = &finished
	task.UpdatedAt = finished
	if err := s.store.Library().UpdateTask(ctx, task); err != nil {
		return err
	}
	if s.notifier != nil {
		s.notifier.Notify(lib.WorkspaceID)
	}
	return nil
}

// reindexProjection rebuilds every derived projection from the published
// Markdown. It never publishes a new version: the documents are the input.
func (s *Service) reindexProjection(ctx context.Context, lib *domain.KnowledgeLibrary) (int, error) {
	// Re-project the record columns from the Markdown first: they are derived
	// from the record grammar, so a grammar change must be repairable without
	// republishing.
	if _, err := s.store.Library().ReprojectRecordJSON(ctx, lib.ID, reprojectRecords); err != nil {
		return 0, err
	}
	count, err := s.store.Library().RebuildSearchIndex(ctx, lib.ID)
	if err != nil {
		return 0, err
	}
	// Release counts are derived from the versions each release pins, so the
	// same rebuild repairs them.
	if _, err := s.store.Library().RefreshReleaseTotals(ctx, lib.ID); err != nil {
		return count, err
	}
	// A rebuild republishes nothing, so it re-renders the release the library
	// currently points at — read here, inside the task, after any earlier
	// publish already moved the pointer.
	fresh, err := s.store.Library().GetLibraryByID(ctx, lib.ID)
	if err != nil {
		return count, err
	}
	current, err := s.resolveRelease(ctx, fresh, fresh.CurrentReleaseID)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return count, err
	}
	if err := s.materializeLibraryFiles(ctx, fresh, current); err != nil {
		return count, err
	}
	return count, nil
}

// ── Query ──────────────────────────────────────────────────────────────

// KnowledgeLibraryQuery is one query request against a pinned release.
type KnowledgeLibraryQuery struct {
	WorkspaceID string
	ReleaseID   string
	Question    string
	Terms       []string
	Limit       int
}

// KnowledgeLibraryAnswer is a bounded, version-pinned answer.
type KnowledgeLibraryAnswer struct {
	Release     *domain.KnowledgeRelease       `json:"release"`
	Hits        []domain.KnowledgeQueryHit     `json:"results"`
	Coverage    domain.KnowledgeCoverage       `json:"coverage"`
	Freshness   domain.KnowledgeFreshness      `json:"freshness"`
	Unknowns    []string                       `json:"unknowns"`
	Expandables []domain.KnowledgeExpandHandle `json:"expand_handles"`
}

// QueryKnowledgeLibrary answers from exactly one published release. A library
// with no release reports not_ready instead of returning an empty answer that
// could be read as "no such knowledge".
func (s *Service) QueryKnowledgeLibrary(ctx context.Context, in KnowledgeLibraryQuery) (*KnowledgeLibraryAnswer, error) {
	lib, err := s.EnsureKnowledgeLibrary(ctx, in.WorkspaceID)
	if err != nil {
		return nil, err
	}
	// Every list in a query answer is a real list, never null: an empty answer
	// must still be renderable by any client without a null check.
	answer := &KnowledgeLibraryAnswer{
		Hits:        []domain.KnowledgeQueryHit{},
		Coverage:    domain.KnowledgeCoverage{Notes: []string{}},
		Freshness:   domain.KnowledgeFreshness{StaleSources: []string{}},
		Unknowns:    []string{},
		Expandables: []domain.KnowledgeExpandHandle{},
	}
	rel, err := s.resolveRelease(ctx, lib, in.ReleaseID)
	if err != nil {
		return nil, err
	}
	pending, blocked, err := s.store.Library().CountTasks(ctx, lib.ID)
	if err != nil {
		return nil, err
	}
	if rel == nil {
		answer.Coverage.Status = "not_ready"
		answer.Coverage.Notes = append(answer.Coverage.Notes, "资料库尚未发布任何版本，初始化任务完成后才能查询。")
		answer.Freshness = domain.KnowledgeFreshness{PendingEvents: pending}
		return answer, nil
	}
	answer.Release = rel
	terms := append([]string{}, in.Terms...)
	terms = append(terms, questionTerms(in.Question)...)
	hits, truncated, scanned, err := s.store.Library().SearchRelease(ctx, lib.ID, rel.ID, terms, in.Limit)
	if err != nil {
		return nil, err
	}
	answer.Hits = hits
	// "Truncated" is about this search; the coverage status is about the
	// knowledge behind the answer. A complete search over a release that
	// reports gaps, or whose matched statements have no evidence, is not a
	// complete answer — saying so is the whole point of the coverage field.
	answer.Coverage.Truncated = truncated
	answer.Coverage.ScannedVersions = scanned
	answer.Coverage.Notes = append(answer.Coverage.Notes, coverageNotes(rel)...)
	latest, err := s.store.Library().CurrentRelease(ctx, lib.ID)
	if err == nil && latest.ID != rel.ID {
		answer.Freshness.NewerReleaseAvailable = true
	}
	published := rel.PublishedAt
	answer.Freshness.ReleaseID = rel.ID
	answer.Freshness.PublishedAt = &published
	answer.Freshness.PendingEvents = pending + blocked
	answer.Freshness.StaleSources = []string{}
	if pending+blocked > 0 {
		answer.Coverage.Notes = append(answer.Coverage.Notes,
			fmt.Sprintf("仍有 %d 个写入任务未完成，本答复固定读取已发布版本 %s，未包含尚未发布的变化。", pending+blocked, rel.ID))
	}
	for _, hit := range hits {
		answer.Expandables = append(answer.Expandables, domain.KnowledgeExpandHandle{
			Kind: "assertion", ID: hit.Assertion.ID, Label: firstLine(hit.Assertion.Statement),
		})
		for _, ev := range hit.Evidence {
			answer.Expandables = append(answer.Expandables, domain.KnowledgeExpandHandle{
				Kind: "evidence", ID: ev.ID, Label: ev.LocatorKind + " " + ev.ID,
			})
		}
		if len(answer.Unknowns) < 20 && strings.TrimSpace(hit.Assertion.UnknownNotes) != "" {
			answer.Unknowns = append(answer.Unknowns, hit.Assertion.ID+": "+firstLine(hit.Assertion.UnknownNotes))
		}
	}
	gaps := coverageGaps(rel)
	answer.Unknowns = append(answer.Unknowns, gaps...)
	answer.Coverage.Gaps = len(gaps)
	answer.Coverage.Unknowns = len(answer.Unknowns)
	missingEvidence := 0
	for _, hit := range hits {
		if len(hit.Evidence) == 0 && len(jsonArrayOf(hit.Assertion.EvidenceJSON)) == 0 {
			missingEvidence++
		}
	}
	answer.Coverage.EvidenceMissing = missingEvidence
	answer.Coverage.Status = "complete"
	if truncated {
		answer.Coverage.Status = "partial"
		answer.Coverage.Notes = append(answer.Coverage.Notes, "结果按预算截断，可缩小问题或继续展开条目。")
	}
	if answer.Coverage.Gaps > 0 {
		answer.Coverage.Status = "partial"
		answer.Coverage.Notes = append(answer.Coverage.Notes,
			fmt.Sprintf("本次发布登记了 %d 条覆盖缺口，命中条目之外的结论并未被覆盖。", answer.Coverage.Gaps))
	}
	if missingEvidence > 0 {
		answer.Coverage.Status = "partial"
		answer.Coverage.Notes = append(answer.Coverage.Notes,
			fmt.Sprintf("命中的 %d 条知识没有登记证据，只能作为线索而不是已核实结论。", missingEvidence))
	}
	if answer.Coverage.Unknowns > 0 {
		answer.Coverage.Status = "partial"
	}
	if pending+blocked > 0 {
		answer.Coverage.Status = "partial"
	}
	return answer, nil
}

// jsonArrayOf counts the entries of a JSON array column, tolerating the empty
// and malformed values older rows carry.
func jsonArrayOf(raw string) []any {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []any
	if json.Unmarshal([]byte(raw), &out) != nil {
		return nil
	}
	return out
}

func coverageNotes(rel *domain.KnowledgeRelease) []string {
	if rel == nil || strings.TrimSpace(rel.CoverageJSON) == "" {
		return nil
	}
	var cov struct {
		SourcesMissed []string `json:"sources_missed"`
		Notes         string   `json:"notes"`
	}
	if err := json.Unmarshal([]byte(rel.CoverageJSON), &cov); err != nil {
		return nil
	}
	var out []string
	if cov.Notes != "" {
		out = append(out, cov.Notes)
	}
	if len(cov.SourcesMissed) > 0 {
		out = append(out, "本次发布未覆盖来源："+strings.Join(cov.SourcesMissed, "、"))
	}
	return out
}

func coverageGaps(rel *domain.KnowledgeRelease) []string {
	if rel == nil || strings.TrimSpace(rel.CoverageJSON) == "" {
		return nil
	}
	var cov struct {
		Gaps []string `json:"gaps"`
	}
	if err := json.Unmarshal([]byte(rel.CoverageJSON), &cov); err != nil {
		return nil
	}
	out := make([]string, 0, len(cov.Gaps))
	for _, g := range cov.Gaps {
		if strings.TrimSpace(g) != "" {
			out = append(out, g)
		}
	}
	return out
}

// reprojectRecords re-derives the projected JSON columns of one document from
// its published Markdown. It is the repair half of "the index is a projection".
func reprojectRecords(markdown string, aliases map[string]string) (map[string]struct {
	About, Scope, Evidence string
}, map[string]string, error) {
	doc, err := knowledgelib.ParseDocument("content/reindex.md", []byte(markdown))
	if err != nil {
		return nil, nil, err
	}
	assertions := map[string]struct {
		About, Scope, Evidence string
	}{}
	resolve := func(refs []knowledgelib.EvidenceRef) []knowledgelib.EvidenceRef {
		out := make([]knowledgelib.EvidenceRef, 0, len(refs))
		for _, ref := range refs {
			if canonical, ok := aliases[ref.EvidenceID]; ok {
				ref.EvidenceID = canonical
			}
			out = append(out, ref)
		}
		return out
	}
	for _, a := range doc.Assertions {
		about, _ := json.Marshal(a.About)
		scope, _ := json.Marshal(a.Scope)
		evidence, _ := json.Marshal(resolve(a.Evidence))
		assertions[a.ID] = struct {
			About, Scope, Evidence string
		}{About: string(about), Scope: string(scope), Evidence: string(evidence)}
	}
	relations := map[string]string{}
	for _, rel := range doc.Relations {
		evidence, _ := json.Marshal(resolve(rel.Evidence))
		relations[rel.ID] = string(evidence)
	}
	return assertions, relations, nil
}

// questionTerms extracts searchable terms from a natural-language question.
// Whitespace splitting alone cannot serve Chinese text, so each CJK run also
// contributes its two- and three-character n-grams; the scorer is a substring
// match, which makes those n-grams meaningful without a segmenter.
func questionTerms(question string) []string {
	question = strings.TrimSpace(question)
	if question == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(term string) {
		term = strings.TrimSpace(term)
		if term == "" || seen[term] {
			return
		}
		seen[term] = true
		out = append(out, term)
	}
	for _, field := range strings.Fields(question) {
		add(field)
		for _, run := range cjkRuns(field) {
			runes := []rune(run)
			for size := 2; size <= 3; size++ {
				for i := 0; i+size <= len(runes); i++ {
					add(string(runes[i : i+size]))
				}
			}
		}
	}
	return out
}

// cjkRuns returns the maximal runs of CJK characters in a field.
func cjkRuns(field string) []string {
	var out []string
	var current []rune
	flush := func() {
		if len(current) >= 2 {
			out = append(out, string(current))
		}
		current = current[:0]
	}
	for _, r := range field {
		if isCJK(r) {
			current = append(current, r)
			continue
		}
		flush()
	}
	flush()
	return out
}

func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF, // CJK Unified Ideographs
		r >= 0x3400 && r <= 0x4DBF, // Extension A
		r >= 0xF900 && r <= 0xFAFF, // Compatibility Ideographs
		r >= 0x3040 && r <= 0x30FF: // Hiragana / Katakana
		return true
	}
	return false
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	r := []rune(s)
	if len(r) > 160 {
		return string(r[:160]) + "…"
	}
	return s
}

// knowledgeExpandAssertion resolves an assertion ID for the expand endpoint.
// When a release is given the lookup is confined to that release's pinned
// versions, so a handle from a historical answer cannot resolve to a newer
// assertion with the same ID.
func (s *Service) knowledgeExpandAssertion(ctx context.Context, workspaceID, releaseID, assertionID string) (*domain.KnowledgeAssertion, *domain.KnowledgeDocument, error) {
	lib, err := s.EnsureKnowledgeLibrary(ctx, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	var list []domain.KnowledgeAssertion
	if strings.TrimSpace(releaseID) != "" {
		list, err = s.store.Library().AssertionsInRelease(ctx, releaseID, []string{assertionID})
	} else {
		list, err = s.store.Library().ReleaseAssertionsByIDs(ctx, []string{assertionID})
	}
	if err != nil {
		return nil, nil, err
	}
	if len(list) == 0 {
		return nil, nil, domain.ErrNotFound
	}
	doc, err := s.store.Library().GetDocument(ctx, lib.ID, list[0].DocumentID)
	if err != nil {
		return nil, nil, err
	}
	// The pinned version row, not the document's current row, is the authority
	// for path, title and version number: an expanded historical assertion must
	// not be dressed in metadata written after it was published.
	if v, verr := s.store.Library().DocumentVersionByID(ctx, list[0].DocumentVersionID); verr == nil {
		if v.Path != "" {
			doc.Path = v.Path
		} else {
			doc.Path = ""
		}
		doc.Title = v.Title
		doc.CurrentVersion = v.Version
	} else if v, _, _, verr := s.store.Library().DocumentVersionDetail(ctx, list[0].DocumentID, 0); verr == nil {
		doc.CurrentVersion = v.Version
	}
	return &list[0], doc, nil
}

// ── Helpers ────────────────────────────────────────────────────────────

func jsonTextOrEmpty(v any) string {
	if v == nil {
		return "{}"
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func sha256Hex(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// KnowledgeLibraryQueueDepth reports how much work is waiting and blocked.
func (s *Service) KnowledgeLibraryQueueDepth(ctx context.Context, workspaceID string) (int, int, error) {
	lib, err := s.EnsureKnowledgeLibrary(ctx, workspaceID)
	if err != nil {
		return 0, 0, err
	}
	return s.store.Library().CountTasks(ctx, lib.ID)
}

// KnowledgeLibraryHeadTask returns the task currently holding the write slot.
func (s *Service) KnowledgeLibraryHeadTask(ctx context.Context, workspaceID string) (*domain.KnowledgeWriteTask, error) {
	lib, err := s.EnsureKnowledgeLibrary(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	task, err := s.store.Library().HeadTask(ctx, lib.ID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	return task, err
}

// CountKnowledgeReleases counts every published release of the library.
func (s *Service) CountKnowledgeReleases(ctx context.Context, workspaceID string) (int, error) {
	lib, err := s.EnsureKnowledgeLibrary(ctx, workspaceID)
	if err != nil {
		return 0, err
	}
	releases, err := s.store.Library().ListReleases(ctx, lib.ID, 200)
	if err != nil {
		return 0, err
	}
	return len(releases), nil
}

// ExpandKnowledgeAssertion resolves one assertion handle returned by a query.
func (s *Service) ExpandKnowledgeAssertion(ctx context.Context, workspaceID, releaseID, assertionID string) (*domain.KnowledgeAssertion, *domain.KnowledgeDocument, error) {
	return s.knowledgeExpandAssertion(ctx, workspaceID, releaseID, assertionID)
}

// ProcessKnowledgeLibraryOnce runs a single worker pass. It exists so tests
// and operators can advance the FIFO deterministically without a background
// ticker; production always uses RunKnowledgeLibraryWorker.
func (s *Service) ProcessKnowledgeLibraryOnce(ctx context.Context) error {
	s.knowledgeLibraryTick(ctx)
	return nil
}

// KnowledgeLibraryStatusView is the observability surface of the library.
type KnowledgeLibraryStatusView struct {
	Library       *domain.KnowledgeLibrary     `json:"library"`
	HeadTask      *domain.KnowledgeWriteTask   `json:"head_task"`
	BlockedTasks  []*domain.KnowledgeWriteTask `json:"blocked_tasks"`
	RecentErrors  []KnowledgeLibraryError      `json:"recent_errors"`
	LastRelease   *domain.KnowledgeRelease     `json:"last_release"`
	PendingEvents int                          `json:"pending_events"`
	IndexRevision int                          `json:"index_revision"`
}

// KnowledgeLibraryError is one recorded task failure worth surfacing.
type KnowledgeLibraryError struct {
	TaskID        string    `json:"task_id"`
	Seq           int       `json:"seq"`
	Kind          string    `json:"kind"`
	LastError     string    `json:"last_error"`
	BlockedReason string    `json:"blocked_reason"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// KnowledgeLibraryStatus reports the queue head, blocked work, recent errors
// and the published baseline in one place.
func (s *Service) KnowledgeLibraryStatus(ctx context.Context, workspaceID string) (*KnowledgeLibraryStatusView, error) {
	lib, err := s.EnsureKnowledgeLibrary(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	tasks, err := s.store.Library().ListTasks(ctx, lib.ID, "", 50)
	if err != nil {
		return nil, err
	}
	view := &KnowledgeLibraryStatusView{Library: lib, IndexRevision: lib.IndexRevision}
	view.BlockedTasks = []*domain.KnowledgeWriteTask{}
	view.RecentErrors = []KnowledgeLibraryError{}
	for _, task := range tasks {
		if task.Status == domain.KnowledgeTaskBlocked {
			view.BlockedTasks = append(view.BlockedTasks, task)
			view.PendingEvents++
		}
		if task.LastError != "" || task.BlockedReason != "" {
			view.RecentErrors = append(view.RecentErrors, KnowledgeLibraryError{
				TaskID: task.ID, Seq: task.Seq, Kind: task.Kind,
				LastError: task.LastError, BlockedReason: task.BlockedReason, UpdatedAt: task.UpdatedAt,
			})
		}
	}
	head, err := s.store.Library().HeadTask(ctx, lib.ID)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}
	if head != nil {
		view.HeadTask = head
	}
	releases, err := s.store.Library().ListReleases(ctx, lib.ID, 1)
	if err != nil {
		return nil, err
	}
	if len(releases) > 0 {
		view.LastRelease = releases[0]
	}
	return view, nil
}

// ReadStagingForTest exposes the staging reader to the acceptance tests so a
// test can reproduce exactly what a crashed publish would have validated.
func (s *Service) ReadStagingForTest(ctx context.Context, task *domain.KnowledgeWriteTask) (*knowledgelib.Staged, error) {
	lib, err := s.EnsureKnowledgeLibrary(ctx, task.LibraryID)
	if err != nil {
		lib, err = s.store.Library().GetLibraryByID(ctx, task.LibraryID)
		if err != nil {
			return nil, err
		}
	}
	kl, err := s.libraryRoot(lib)
	if err != nil {
		return nil, err
	}
	return kl.ReadStaging(task.ID)
}

// BuildProjectionForTest exposes the validation step to the acceptance tests.
func (s *Service) BuildProjectionForTest(ctx context.Context, task *domain.KnowledgeWriteTask, staged *knowledgelib.Staged) (*knowledgelib.Projection, *PublishInput, error) {
	lib, err := s.store.Library().GetLibraryByID(ctx, task.LibraryID)
	if err != nil {
		return nil, nil, err
	}
	return s.buildProjection(ctx, lib, task, staged)
}

// NotifyKnowledgeLibraryTerminalForTest replays one run's terminal wake-up so
// a test can prove a superseded run cannot overwrite published content.
func (s *Service) NotifyKnowledgeLibraryTerminalForTest(ctx context.Context, workspaceID, runID string) error {
	run, err := s.store.Runs().Get(ctx, runID)
	if err != nil {
		return err
	}
	if !isKnowledgeLibraryRun(run) {
		return nil
	}
	s.notifyKnowledgeLibraryTerminal(ctx, workspaceID)
	return nil
}

package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/knowledgelib"
)

const (
	knowledgeLibraryWorkerInterval = 2 * time.Second
	knowledgeLibraryTaskTitle      = "资料整理任务"
)

// RunKnowledgeLibraryWorker drives the single FIFO write queue of every
// enabled library. It is the only place that starts or finishes a knowledge
// write, which is what keeps knowledge writes serial across restarts.
func (s *Service) RunKnowledgeLibraryWorker(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = knowledgeLibraryWorkerInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.knowledgeLibraryTick(ctx)
		}
	}
}

func (s *Service) knowledgeLibraryTick(ctx context.Context) {
	libraries, err := s.store.Library().ListLibraries(ctx)
	if err != nil {
		log.Printf("knowledge library: list libraries: %v", err)
		return
	}
	for _, lib := range libraries {
		if !lib.Enabled {
			continue
		}
		if err := s.processLibrary(ctx, lib); err != nil {
			log.Printf("knowledge library %s: %v", lib.ID, err)
		}
	}
}

func (s *Service) processLibrary(ctx context.Context, lib *domain.KnowledgeLibrary) error {
	if err := s.recoverLibraryPublications(ctx, lib); err != nil {
		return err
	}
	head, err := s.store.Library().HeadTask(ctx, lib.ID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	// A reindex is a knowledge write with no model turn: it is executed here,
	// at the head of the queue, and re-executed if a crash left it running.
	// Blocking it as "running without a Run" would strand the queue forever.
	if head.Kind == knowledgelib.TaskReindex {
		switch head.Status {
		case domain.KnowledgeTaskQueued, domain.KnowledgeTaskRunning, domain.KnowledgeTaskAwaitingAgent:
			return s.runReindexTask(ctx, lib, head)
		case domain.KnowledgeTaskRetryWait:
			if head.NextAttemptAt != nil && head.NextAttemptAt.After(time.Now().UTC()) {
				return nil
			}
			return s.runReindexTask(ctx, lib, head)
		}
		return nil
	}
	switch head.Status {
	case domain.KnowledgeTaskQueued:
		// A task that already has a successful agent turn must re-run the
		// harness step (ingest and publish), not the model turn: reviving a
		// blocked task or retrying after a harness failure must not spend
		// another model turn on work the model already did.
		if head.StagingPath != "" && head.CurrentRunID != "" {
			if run, runErr := s.store.Runs().Get(ctx, head.CurrentRunID); runErr == nil && run.Status == domain.RunSucceeded {
				if ok, claimErr := s.store.Library().ClaimTask(ctx, lib.ID, head.ID, domain.NewID("knowner_")); claimErr != nil {
					return claimErr
				} else if ok {
					claimed, getErr := s.store.Library().GetTask(ctx, lib.ID, head.ID)
					if getErr != nil {
						return getErr
					}
					return s.ingestRunningTask(ctx, lib, claimed)
				}
				return nil
			}
		}
		return s.prepareAndDispatch(ctx, lib, head)
	case domain.KnowledgeTaskRunning, domain.KnowledgeTaskAwaitingAgent:
		if head.CurrentRunID == "" {
			return s.blockTask(ctx, lib, head, "任务处于运行态但没有关联的模型运行", "缺少 Run 关联")
		}
		return s.ingestRunningTask(ctx, lib, head)
	case domain.KnowledgeTaskRetryWait:
		if head.NextAttemptAt != nil && head.NextAttemptAt.After(time.Now().UTC()) {
			return nil
		}
		// A task that already has a finished agent turn must retry the
		// harness step (ingest/publish), not the model turn: re-running the
		// model would duplicate work and could produce different content.
		if head.StagingPath != "" && head.CurrentRunID != "" {
			if run, err := s.store.Runs().Get(ctx, head.CurrentRunID); err == nil && run.Status.IsTerminal() {
				if ok, err := s.store.Library().ClaimTask(ctx, lib.ID, head.ID, domain.NewID("knowner_")); err != nil {
					return err
				} else if ok {
					claimed, err := s.store.Library().GetTask(ctx, lib.ID, head.ID)
					if err != nil {
						return err
					}
					return s.ingestRunningTask(ctx, lib, claimed)
				}
				return nil
			}
		}
		head.Status = domain.KnowledgeTaskQueued
		head.UpdatedAt = time.Now().UTC()
		return s.store.Library().UpdateTask(ctx, head)
	case domain.KnowledgeTaskBlocked:
		return nil
	default:
		return nil
	}
}

// ── Prepare: freeze the input and hand the task to the library agent ────

func (s *Service) prepareAndDispatch(ctx context.Context, lib *domain.KnowledgeLibrary, task *domain.KnowledgeWriteTask) error {
	owner := domain.NewID("knowner_")
	claimed, err := s.store.Library().ClaimTask(ctx, lib.ID, task.ID, owner)
	if err != nil {
		return err
	}
	if !claimed {
		// Another worker owns the head, or it is no longer claimable.
		return nil
	}
	task, err = s.store.Library().GetTask(ctx, lib.ID, task.ID)
	if err != nil {
		return err
	}
	// The execution baseline is the library's real published state at the
	// moment this task reaches the head, not the state when it was enqueued:
	// a task that waited behind another publish must build on that result.
	fresh, err := s.store.Library().GetLibraryByID(ctx, lib.ID)
	if err != nil {
		return err
	}
	*lib = *fresh
	if err := s.validateTaskBaseline(ctx, lib, task); err != nil {
		return s.blockTask(ctx, lib, task, "缺少可用的发布基线，已停止写入", err.Error())
	}
	task.BaseReleaseID = lib.CurrentReleaseID
	task.UpdatedAt = time.Now().UTC()
	if err := s.store.Library().UpdateTask(ctx, task); err != nil {
		return err
	}
	kl, err := s.libraryRoot(lib)
	if err != nil {
		return s.blockTask(ctx, lib, task, "资料库根目录不可用", err.Error())
	}
	if err := kl.Ensure(); err != nil {
		return s.blockTask(ctx, lib, task, "资料库目录初始化失败", err.Error())
	}
	sources, err := s.store.Library().ListSources(ctx, lib.ID)
	if err != nil {
		return err
	}
	enabled := make([]*domain.KnowledgeSource, 0, len(sources))
	for _, src := range sources {
		if src.Enabled {
			enabled = append(enabled, src)
		}
	}
	if len(enabled) == 0 {
		return s.blockTask(ctx, lib, task, "资料库还没有登记任何来源，无法固定输入", "需要先在管理员页面登记至少一个来源仓库")
	}
	snapshot, briefSources, err := s.captureSnapshot(ctx, lib, kl, task, enabled)
	if err != nil {
		return s.deferTask(ctx, lib, task, err)
	}
	staging, err := kl.ResetStaging(task.ID)
	if err != nil {
		return s.deferTask(ctx, lib, task, err)
	}
	existing, entityCatalog, err := s.libraryCatalog(ctx, lib, kl, briefSources)
	if err != nil {
		return s.deferTask(ctx, lib, task, err)
	}
	baseRelease, _ := s.resolveRelease(ctx, lib, task.BaseReleaseID)
	var baseRef *knowledgelib.ReleaseRef
	if baseRelease != nil {
		baseRef = &knowledgelib.ReleaseRef{ID: baseRelease.ID, Seq: baseRelease.Seq, PublishedAt: baseRelease.PublishedAt.Format(time.RFC3339)}
	}
	focus := focusFromTask(task)
	requirement := s.requirementForTask(ctx, lib, task, staging)
	if requirement == nil && focus.RequirementUnresolved != "" {
		// Nothing was accepted, but the caller did ask for a requirement: say
		// so in the brief rather than quietly omitting it.
		requirement = &knowledgelib.Requirement{
			RequirementID: focus.RequirementID, Title: focus.RequirementTitle,
			Version: focus.RequirementVersion, Unresolved: focus.RequirementUnresolved,
		}
	}
	if _, err := knowledgelib.RenderBrief(knowledgelib.BriefInput{
		TaskID: task.ID, TaskKind: task.Kind, LibraryRoot: kl.Root, StagingDir: staging,
		ViewID: task.ViewID, BaseRelease: baseRef,
		Sources: briefSources, Focus: focus,
		Requirement:       requirement,
		ExistingDocuments: existing, EntityCatalog: entityCatalog,
	}); err != nil {
		return s.deferTask(ctx, lib, task, err)
	}
	task.SnapshotID = snapshot.ID
	task.StagingPath = staging
	task.OwnerToken = owner
	task.UpdatedAt = time.Now().UTC()
	task.LastError = ""
	if err := s.store.Library().UpdateTask(ctx, task); err != nil {
		return err
	}
	if task.EventID != "" {
		if err := s.store.Library().UpdateEventStatus(ctx, task.EventID, domain.KnowledgeEventProcessing, task.ID); err != nil {
			return err
		}
	}
	// Every attempt gets its own bounded model turn, except when the previous
	// turn never finished: a crash mid-turn must replay that same turn rather
	// than start a new one, or a restart would silently burn the turn budget.
	turn := task.TurnSeq + 1
	if replay, ok := s.unfinishedTurn(ctx, task); ok {
		turn = replay
	}
	run, err := s.dispatchLibraryRun(ctx, lib, task, staging, turn)
	if err != nil {
		return s.deferTask(ctx, lib, task, err)
	}
	task.CurrentRunID = run.ID
	task.WorkItemID = run.WorkItemID
	task.TurnSeq = turn
	task.Status = domain.KnowledgeTaskRunning
	task.UpdatedAt = time.Now().UTC()
	if err := s.store.Library().UpdateTask(ctx, task); err != nil {
		return err
	}
	return nil
}

// validateTaskBaseline refuses to start a non-initial write when the library
// has no resolvable published baseline. Continuing would publish a release
// with an empty parent and could drop existing knowledge.
func (s *Service) validateTaskBaseline(ctx context.Context, lib *domain.KnowledgeLibrary, task *domain.KnowledgeWriteTask) error {
	isFirstRelease := lib.CurrentReleaseID == ""
	if task.Kind == knowledgelib.TaskInitialize || task.Kind == knowledgelib.TaskLegacyImport {
		return nil
	}
	if isFirstRelease {
		return fmt.Errorf("%w: 资料库还没有任何已发布版本，%s 任务不能以空基线发布", domain.ErrStateConflict, task.Kind)
	}
	if _, err := s.store.Library().GetRelease(ctx, lib.ID, lib.CurrentReleaseID); err != nil {
		return fmt.Errorf("%w: 当前发布版本 %s 无法解析：%v", domain.ErrStateConflict, lib.CurrentReleaseID, err)
	}
	return nil
}

// captureSnapshot freezes the exact input set of this task.
func (s *Service) captureSnapshot(ctx context.Context, lib *domain.KnowledgeLibrary, kl knowledgelib.Library, task *domain.KnowledgeWriteTask, sources []*domain.KnowledgeSource) (*domain.KnowledgeSnapshot, []knowledgelib.BriefSource, error) {
	now := time.Now().UTC()
	snapshot := &domain.KnowledgeSnapshot{
		ID: domain.NewID("ksnap_"), LibraryID: lib.ID, ViewID: task.ViewID,
		ParentSnapshotID: lib.CurrentSnapshotID, Reason: task.Kind, CapturedAt: now,
	}
	runner := knowledgelib.ExecGitRunner{}
	var briefSources []knowledgelib.BriefSource
	for _, src := range sources {
		// A requirement input is not a repository: its bytes were frozen when
		// the event was accepted, and it joins the snapshot below as the
		// binding for this task's own requirement.
		if src.Kind == domain.SourceKindRequirement {
			continue
		}
		for _, usage := range src.EffectiveUsages() {
			spec := knowledgelib.SourceSpec{
				ID: src.ID, Name: src.Name, Kind: string(src.Kind), RepoPath: src.RepoPath,
				DefaultRef: src.DefaultRef, IncludeGlobs: src.IncludeGlobs, ExcludeGlobs: src.ExcludeGlobs,
				Usage: knowledgelib.Usage{
					Consumer: usage.Consumer, Artifact: usage.Artifact, Environment: usage.Environment,
				},
			}
			binding, err := knowledgelib.CaptureBinding(ctx, runner, snapshot.ID, spec)
			if err != nil {
				return nil, nil, fmt.Errorf("%w: 来源 %s 无法固定版本：%v", domain.ErrValidation, src.Name, err)
			}
			if err := kl.FreezeWorktreeContent(binding); err != nil {
				return nil, nil, fmt.Errorf("%w: 来源 %s 的未提交内容固化失败：%v", domain.ErrValidation, src.Name, err)
			}
			// The agent reads the exported pinned tree, never the live
			// repository, so a later worktree edit cannot change its input.
			if err := kl.FreezeBindingTree(ctx, runner, binding, spec); err != nil {
				return nil, nil, fmt.Errorf("%w: 来源 %s 的固定版本副本导出失败：%v", domain.ErrValidation, src.Name, err)
			}
			snapshot.Bindings = append(snapshot.Bindings, domain.KnowledgeSnapshotBinding{
				ID: binding.ID, SnapshotID: snapshot.ID, SourceID: src.ID, SourceName: src.Name,
				SourceKind: string(src.Kind), RepoPath: binding.RepoPath, GitRef: binding.GitRef,
				CommitSHA: binding.CommitSHA, ObjectFormat: binding.ObjectFormat,
				Dirty: binding.Dirty, DirtyDigest: binding.DirtyDigest, Untracked: binding.Untracked,
				Artifact: usage.Artifact, ArtifactResolution: usage.ArtifactState(),
				ArtifactResolutionRef: usage.ResolutionRef,
				Consumer:              usage.Consumer, Environment: usage.Environment,
				CapturedAt: binding.CapturedAt,
			})
			briefSources = append(briefSources, knowledgelib.BriefSource{
				Binding: binding.QualifiedName(), Name: src.Name, Kind: string(src.Kind),
				RepoPath: binding.RepoPath, ReadPath: binding.TreeDir,
				GitRef: binding.GitRef, CommitSHA: binding.CommitSHA, ObjectFmt: binding.ObjectFormat,
				Dirty: binding.Dirty, Untracked: binding.Untracked,
				WorktreeNote: binding.WorktreeNote,
				Artifact:     usage.Artifact, ArtifactResolution: usage.ArtifactState(),
				Consumer: usage.Consumer, Environment: usage.Environment,
			})
		}
	}
	// A frozen requirement input joins the snapshot as a real binding: its
	// text is a source the librarian cites, the collector resolves and the
	// ledger references, not a note that only exists in the brief.
	if strings.TrimSpace(task.RequirementInputID) != "" {
		input, err := s.store.Library().GetRequirementInput(ctx, lib.ID, task.RequirementInputID)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: 本次任务冻结的需求原文不可读：%v", domain.ErrValidation, err)
		}
		// The binding carries the source's own name, so an inline payload copy
		// is visibly a different source from the referenced document: the
		// brief, the evidence and the ledger all name the same thing.
		sourceName := "requirement:" + input.RequirementID
		if source, err := s.store.Library().GetSource(ctx, lib.ID, input.SourceID); err == nil {
			sourceName = source.Name
		}
		binding := requirementBinding(snapshot.ID, input, sourceName)
		snapshot.Bindings = append(snapshot.Bindings, domain.KnowledgeSnapshotBinding{
			ID: binding.ID, SnapshotID: snapshot.ID, SourceID: input.SourceID, SourceName: sourceName,
			SourceKind: string(domain.SourceKindRequirement), RepoPath: binding.RepoPath,
			GitRef: binding.GitRef, CommitSHA: binding.CommitSHA, ObjectFormat: binding.ObjectFormat,
			Untracked: []string{}, CapturedAt: binding.CapturedAt,
		})
		briefSources = append(briefSources, knowledgelib.BriefSource{
			Binding: binding.QualifiedName(), Name: sourceName, Kind: string(domain.SourceKindRequirement),
			RepoPath: binding.RepoPath, ReadPath: binding.TreeDir, GitRef: binding.GitRef,
			CommitSHA: binding.CommitSHA, ObjectFmt: binding.ObjectFormat,
			Artifact: "", ArtifactResolution: "not_applicable",
		})
	}
	if err := s.store.Library().CreateSnapshot(ctx, snapshot); err != nil {
		return nil, nil, err
	}
	return snapshot, briefSources, nil
}

// libraryCatalog builds the brief's existing-document digest and the stable
// entity catalog the agent must reuse instead of inventing IDs.
func (s *Service) libraryCatalog(ctx context.Context, lib *domain.KnowledgeLibrary, kl knowledgelib.Library, sources []knowledgelib.BriefSource) ([]knowledgelib.ExistingDocument, []knowledgelib.EntitySpec, error) {
	// A configured base release that cannot be read is a real failure, not an
	// empty catalog: the agent would otherwise be told there is no existing
	// knowledge and could re-create every document from scratch.
	current, err := s.resolveRelease(ctx, lib, lib.CurrentReleaseID)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: 读取当前发布基线失败：%v", domain.ErrStateConflict, err)
	}
	var out []knowledgelib.ExistingDocument
	if current != nil {
		docs, err := s.store.Library().ReleaseDocuments(ctx, current.ID, "", "", 500)
		if err != nil {
			return nil, nil, err
		}
		for _, d := range docs {
			entry := knowledgelib.ExistingDocument{ID: d.ID, Path: d.Path, Title: d.Title, Kind: d.Kind}
			_, assertions, _, err := s.store.Library().DocumentVersionDetail(ctx, d.ID, 0)
			if err == nil {
				for _, a := range assertions {
					entry.AssertionIDs = append(entry.AssertionIDs, a.ID)
				}
			}
			out = append(out, entry)
		}
	}
	catalog := []knowledgelib.EntitySpec{}
	for _, src := range sources {
		kind := "service"
		switch src.Kind {
		case "common":
			kind = "common"
		case "documents":
			kind = "component"
		}
		catalog = append(catalog, knowledgelib.EntitySpec{
			ID: knowledgelib.EntityIDFromSource(src.Kind, src.Name), Kind: kind, Name: src.Name,
		})
	}
	entities, err := s.store.Library().ListEntities(ctx, lib.ID)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entities {
		catalog = append(catalog, knowledgelib.EntitySpec{
			ID: e.ID, Kind: e.Kind, Namespace: e.Namespace, Name: e.CanonicalName, Aliases: e.Aliases,
		})
	}
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].ID < catalog[j].ID })
	return out, catalog, nil
}

// requirementForTask hands the agent the text that opened this task. It reads
// the event row rather than the task row because the requirement body is
// external declared input, and a repair turn must see exactly the same text as
// the first turn.
func (s *Service) requirementForTask(ctx context.Context, lib *domain.KnowledgeLibrary, task *domain.KnowledgeWriteTask, staging string) *knowledgelib.Requirement {
	if strings.TrimSpace(task.RequirementInputID) == "" {
		return nil
	}
	input, err := s.store.Library().GetRequirementInput(ctx, lib.ID, task.RequirementInputID)
	if err != nil {
		// A task whose frozen requirement is gone must say so: the alternative
		// is a silent run against an unknown requirement.
		return &knowledgelib.Requirement{
			Unresolved: "本次任务冻结的需求原文 " + task.RequirementInputID + " 已不可读：" + err.Error(),
		}
	}
	req := requirementFromInput(input, staging)
	if source, err := s.store.Library().GetSource(ctx, lib.ID, input.SourceID); err == nil {
		req.Binding = source.Name
	}
	return req
}

func focusFromTask(task *domain.KnowledgeWriteTask) knowledgelib.Focus {
	var raw map[string]any
	_ = json.Unmarshal([]byte(task.FocusJSON), &raw)
	focus := knowledgelib.Focus{}
	if raw == nil {
		return focus
	}
	if v, ok := raw["event_type"].(string); ok {
		focus.EventType = v
	}
	focus.ChangedPaths = stringSlice(raw["changed_paths"])
	focus.RemovedPaths = stringSlice(raw["removed_paths"])
	focus.RenamedPaths = stringSlice(raw["renamed_paths"])
	focus.SourceNames = stringSlice(raw["source_names"])
	if v, ok := raw["branch"].(string); ok {
		focus.Branch = v
	} else if v, ok := raw["ref"].(string); ok {
		focus.Branch = v
	}
	if v, ok := raw["head_sha"].(string); ok {
		focus.HeadSHA = v
	}
	if v, ok := raw["previous_sha"].(string); ok {
		focus.PreviousSHA = v
	}
	if v, ok := raw["requirement_id"].(string); ok {
		focus.RequirementID = v
	}
	if v, ok := raw["requirement_version"].(string); ok {
		focus.RequirementVersion = v
	}
	if v, ok := raw["title"].(string); ok {
		focus.RequirementTitle = v
	}
	if v, ok := raw["requirement_unresolved"].(string); ok {
		focus.RequirementUnresolved = v
	}
	if v, ok := raw["content_ref"].(string); ok {
		focus.Reference = v
	}
	if v, ok := raw["summary"].(string); ok {
		focus.Summary = v
	}
	if prev, ok := raw["previous_version"].(string); ok {
		focus.Notes = "观测到的前一版本：" + prev
	}
	if cur, ok := raw["observed_version"].(string); ok {
		focus.Notes = strings.TrimSpace(focus.Notes + " 当前版本：" + cur)
	}
	return focus
}

func stringSlice(v any) []string {
	list, ok := v.([]any)
	if !ok {
		if s, ok := v.([]string); ok {
			return s
		}
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// dispatchLibraryRun starts one bounded librarian Run. Its working directory
// is the workspace root, so the brief carries absolute paths for the staging
// directory and every source repository.
func (s *Service) dispatchLibraryRun(ctx context.Context, lib *domain.KnowledgeLibrary, task *domain.KnowledgeWriteTask, staging string, turn int) (*domain.ExecutionRun, error) {
	agent, err := s.store.Agents().Get(ctx, lib.LibrarianAgentID)
	if err != nil {
		return nil, fmt.Errorf("%w: library agent %s is missing: %v", domain.ErrCapabilityMissing, lib.LibrarianAgentID, err)
	}
	now := time.Now().UTC()
	// Reuse the task's WorkItem when it already exists: a restarted worker must
	// not mint a second WorkItem under the same run client key, which would
	// turn a crash recovery into an idempotency conflict instead of a replay.
	var workItemID string
	if strings.TrimSpace(task.WorkItemID) != "" {
		if existing, err := s.store.WorkItems().Get(ctx, task.WorkItemID); err == nil && existing.WorkspaceID == lib.WorkspaceID {
			workItemID = existing.ID
		}
	}
	if workItemID == "" {
		wi := &domain.WorkItem{
			ID: domain.NewID(domain.PrefixWorkItem), WorkspaceID: lib.WorkspaceID,
			RecordKind: domain.RecordKindTask,
			Title:      fmt.Sprintf("%s #%d（%s）", knowledgeLibraryTaskTitle, task.Seq, task.Kind),
			Status:     domain.WorkItemInProgress, Priority: domain.PriorityMedium,
			AgentProfileID: agent.ID, Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		if err := s.store.WorkItems().Create(ctx, wi); err != nil {
			return nil, err
		}
		workItemID = wi.ID
	}
	briefPath := filepath.Join(staging, "brief.md")
	instruction := strings.Join([]string{
		"本轮资料整理任务说明书：" + briefPath,
		"先完整读取 brief.md 与同目录的 task.json，再按说明书读取被点名的来源仓库，最后把知识文档与 plan.json/evidence.yaml/entities.yaml 写进同一目录。",
		"只写暂存目录，不要修改来源仓库或资料库根目录。",
	}, "\n")
	run, replayed, err := s.CreateRunIdempotent(ctx, workItemID, CreateRunParams{
		AgentProfileID:  agent.ID,
		Instruction:     instruction,
		ClientKey:       fmt.Sprintf("knowledge-task:%s:turn:%d", task.ID, turn),
		knowledgeTaskID: task.ID, knowledgeTaskTurn: int64(turn),
	})
	if err != nil {
		return nil, err
	}
	if replayed {
		return run, nil
	}
	if err := s.dispatchCommittedRun(ctx, run); err != nil {
		return nil, err
	}
	turnRecord := &domain.KnowledgeTaskTurn{
		ID: domain.NewID("kturn_"), TaskID: task.ID, TurnSeq: turn, RunID: run.ID,
		Purpose: task.Kind, Status: "pending", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.Library().CreateTurn(ctx, turnRecord); err != nil {
		// A replay may already have recorded this turn; that is not a failure.
		if existing, listErr := s.store.Library().ListTurns(ctx, task.ID); listErr != nil || !turnExists(existing, turn) {
			return nil, err
		}
	}
	return run, nil
}

// ── Ingest: read staging, verify, publish ──────────────────────────────

func (s *Service) ingestRunningTask(ctx context.Context, lib *domain.KnowledgeLibrary, task *domain.KnowledgeWriteTask) error {
	run, err := s.store.Runs().Get(ctx, task.CurrentRunID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return s.deferTask(ctx, lib, task, fmt.Errorf("%w: 任务绑定的运行不存在", domain.ErrStateConflict))
		}
		return err
	}
	if !run.Status.IsTerminal() {
		return nil
	}
	turns, err := s.store.Library().ListTurns(ctx, task.ID)
	if err != nil {
		return err
	}
	for _, t := range turns {
		if t.RunID == run.ID && t.Status == "pending" {
			status := "completed"
			if run.Status != domain.RunSucceeded {
				status = "failed"
			}
			_ = s.store.Library().UpdateTurn(ctx, t.ID, status, "{}", string(run.Status))
		}
	}
	if run.Status != domain.RunSucceeded {
		return s.deferTask(ctx, lib, task, fmt.Errorf("%w: 资料 agent 运行以 %s 结束", domain.ErrStateConflict, run.Status))
	}
	kl, err := s.libraryRoot(lib)
	if err != nil {
		return err
	}
	staged, err := kl.ReadStaging(task.ID)
	if err != nil {
		return s.repairOrBlock(ctx, lib, task, err)
	}
	projection, publishInput, err := s.buildProjection(ctx, lib, task, staged)
	if err != nil {
		return s.repairOrBlock(ctx, lib, task, err)
	}
	// If an earlier attempt already committed this exact projection, finish the
	// task against that release instead of publishing a second copy.
	release, err := s.store.Library().ReleaseByProjectionDigest(ctx, lib.ID, publishInput.ProjectionDigest)
	if err == nil && release != nil && release.TaskID == task.ID {
		return s.completeTask(ctx, lib, task, release, projection, true)
	}
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return s.deferTask(ctx, lib, task, err)
	}
	// Record the attempt before publishing so a crash mid-publish is
	// recoverable from durable state rather than from a guess.
	publishInput.PublicationID = domain.NewID("kpub_")
	if createErr := s.store.Library().CreatePublication(ctx, &domain.KnowledgePublication{
		ID: publishInput.PublicationID, LibraryID: lib.ID, TaskID: task.ID,
		Status: "prepared", ProjectionDigest: publishInput.ProjectionDigest, PreparedAt: time.Now().UTC(),
	}); createErr != nil {
		return s.deferTask(ctx, lib, task, createErr)
	}
	release, err = s.store.Library().PublishProjection(ctx, *publishInput)
	if err != nil {
		return s.deferTask(ctx, lib, task, err)
	}
	return s.completeTask(ctx, lib, task, release, projection, false)
}

// unfinishedTurn reports the turn number of a model turn that was started but
// never reached a terminal Run, which is exactly the state a crash leaves.
func (s *Service) unfinishedTurn(ctx context.Context, task *domain.KnowledgeWriteTask) (int, bool) {
	if task.TurnSeq < 1 {
		return 0, false
	}
	turns, err := s.store.Library().ListTurns(ctx, task.ID)
	if err != nil {
		return 0, false
	}
	for _, t := range turns {
		if t.TurnSeq != task.TurnSeq {
			continue
		}
		run, runErr := s.store.Runs().Get(ctx, t.RunID)
		if runErr != nil || !run.Status.IsTerminal() {
			return t.TurnSeq, true
		}
		return 0, false
	}
	return 0, false
}

// completeTask finishes one write task against the release it published. It is
// idempotent: a recovery pass that finds the release already committed only
// finishes the bookkeeping.
func (s *Service) completeTask(ctx context.Context, lib *domain.KnowledgeLibrary, task *domain.KnowledgeWriteTask,
	release *domain.KnowledgeRelease, projection *knowledgelib.Projection, recovered bool) error {
	kl, err := s.libraryRoot(lib)
	if err != nil {
		return err
	}
	task.TargetReleaseID = release.ID
	// Materialize by the release that was just published, never through the
	// library row: the caller's lib was loaded before the publish, so its
	// current_release_id still points at the previous release and the official
	// Markdown would lag one version behind the database.
	if err := s.materializeLibraryFiles(ctx, lib, release); err != nil {
		// A task is not finished while the official files disagree with the
		// release it published. Surfacing this as a task failure keeps it in
		// the retry/block policy; the publication stays prepared, and the
		// recovery path below picks the same release up again without
		// publishing a second copy.
		return s.deferTask(ctx, lib, task, fmt.Errorf("%w: 正式知识文件与发布 %s 不一致：%v", domain.ErrStateConflict, release.ID, err))
	}
	// diagnostics is always a JSON array of human-readable lines; a structured
	// summary would change the field's shape between task states and break any
	// client that renders it as a list.
	lines := append([]string{}, diagnosticsOf(task)...)
	lines = append(lines, fmt.Sprintf("已发布 %s（seq %d）：文档 %d 篇，证据 %d 条%s",
		release.ID, release.Seq, len(projection.Documents), len(projection.Evidence),
		map[bool]string{true: "（从中断中恢复）", false: ""}[recovered]))
	diag, _ := json.Marshal(lines)
	// One transaction seals the publish: publication and current pointer, the
	// task's terminal state and its event's terminal state. Written separately,
	// a failure between them leaves a completed task with a processing event,
	// or a release the library points at whose publication never committed.
	if err := s.store.Library().SettlePublication(ctx, PublicationSettlement{
		TaskID: task.ID, DiagnosticsJSON: string(diag), EventID: task.EventID,
	}); err != nil {
		return s.deferTask(ctx, lib, task, fmt.Errorf("%w: 发布封口失败：%v", domain.ErrStateConflict, err))
	}
	task.DiagnosticsJSON = string(diag)
	task.Status = domain.KnowledgeTaskCompleted
	task.LastError = ""
	task.BlockedReason = ""
	finished := time.Now().UTC()
	task.FinishedAt = &finished
	task.UpdatedAt = finished
	// The frozen working copies are released only after the seal succeeded:
	// while recovery may still have to redo this step, the frozen input it
	// re-collects evidence from must still exist.
	if err := kl.ReleaseSnapshotTrees(task.SnapshotID); err != nil {
		log.Printf("knowledge library %s: release snapshot trees: %v", lib.ID, err)
	}
	if s.notifier != nil {
		s.notifier.Notify(lib.WorkspaceID)
	}
	return nil
}

// buildProjection validates the staged work and prepares the publish input.
// Every rejection names the exact object so a repair turn can quote it.
func (s *Service) buildProjection(ctx context.Context, lib *domain.KnowledgeLibrary, task *domain.KnowledgeWriteTask, staged *knowledgelib.Staged) (*knowledgelib.Projection, *PublishInput, error) {
	kl, err := s.libraryRoot(lib)
	if err != nil {
		return nil, nil, err
	}
	snapshot, err := s.store.Library().GetSnapshot(ctx, task.SnapshotID)
	if err != nil {
		return nil, nil, err
	}
	bindings := make([]*knowledgelib.Binding, 0, len(snapshot.Bindings))
	for _, b := range snapshot.Bindings {
		treeDir := kl.SnapshotTreeDirFor(b.SnapshotID, b.ID)
		frozenDir := kl.SnapshotWorktreeDirFor(b.SnapshotID, b.ID)
		if b.SourceKind == string(domain.SourceKindRequirement) {
			// A frozen requirement input is not exported from a repository
			// checkout: it lives where it was frozen, and that directory is
			// exactly the pinned version this binding names.
			treeDir = b.RepoPath
			frozenDir = ""
		}
		binding := &knowledgelib.Binding{
			ID: b.ID, SnapshotID: b.SnapshotID, SourceID: b.SourceID, SourceName: b.SourceName,
			SourceKind: b.SourceKind, RepoPath: b.RepoPath, GitRef: b.GitRef, CommitSHA: b.CommitSHA,
			ObjectFormat: b.ObjectFormat, Dirty: b.Dirty, DirtyDigest: b.DirtyDigest,
			Untracked: b.Untracked, Artifact: b.Artifact, Consumer: b.Consumer, Environment: b.Environment,
			DirtyPaths: map[string]bool{},
			FrozenDir:  frozenDir,
			TreeDir:    treeDir,
		}
		for _, u := range b.Untracked {
			binding.DirtyPaths[u] = true
		}
		markDirtyFromFrozen(kl, binding, b)
		bindings = append(bindings, binding)
	}
	collector := &knowledgelib.EvidenceCollector{Library: kl}
	collected, err := collector.Collect(ctx, task.SnapshotID, bindings, staged.Evidence)
	if err != nil {
		return nil, nil, err
	}
	knownEntities, knownDocs, knownPaths, knownEvidence, err := s.knownLibraryState(ctx, lib)
	if err != nil {
		return nil, nil, err
	}
	sourceEntities := map[string]bool{}
	sources, err := s.store.Library().ListSources(ctx, lib.ID)
	if err != nil {
		return nil, nil, err
	}
	for _, src := range sources {
		sourceEntities[knowledgelib.EntityIDFromSource(string(src.Kind), src.Name)] = true
	}
	proj, err := knowledgelib.BuildProjection(knowledgelib.ProjectionInput{
		Staged: staged, KnownEntities: knownEntities, SourceEntities: sourceEntities,
		KnownEvidence: knownEvidence, KnownDocumentIDs: knownDocs, KnownPaths: knownPaths,
		Collected: collected,
	})
	if err != nil {
		return nil, nil, err
	}
	coverage, _ := json.Marshal(staged.Plan.Coverage)
	input := &PublishInput{
		LibraryID: lib.ID, TaskID: task.ID, SnapshotID: task.SnapshotID,
		ParentReleaseID: task.BaseReleaseID, ProjectionDigest: proj.Digest,
		CoverageJSON: string(coverage), Notes: staged.Plan.Summary,
		RemovedNotes:     map[string]string{},
		DocumentIDByPath: knownPaths, DocumentIDByID: knownDocs,
	}
	for i := range proj.Evidence {
		ce := proj.Evidence[i]
		input.Evidence = append(input.Evidence, &domain.KnowledgeEvidence{
			ID: ce.ID, LibraryID: lib.ID, SnapshotID: ce.SnapshotID, BindingID: ce.BindingID,
			RepresentationID: ce.RepresentationID, LocatorJSON: ce.LocatorJSON, LocatorKind: ce.LocatorKind,
			Excerpt: ce.Excerpt, ExcerptDigest: ce.ExcerptDigest, MatchCount: ce.MatchCount,
			Availability: ce.Availability, CollectedAt: ce.CollectedAt,
		})
		rep := ce.Representation
		if _, err := s.store.Library().EnsureRepresentation(ctx, &domain.KnowledgeRepresentation{
			ID: rep.ID, BindingID: rep.BindingID, Path: rep.Path, MediaType: rep.MediaType,
			Encoding: rep.Encoding, DigestAlgo: rep.DigestAlgo, ContentDigest: rep.ContentDigest,
			ByteSize: rep.ByteSize, Coverage: rep.Coverage, Transform: rep.Transform,
			StoredPath: rep.StoredPath, Origin: rep.Origin, CreatedAt: ce.CollectedAt,
		}); err != nil {
			return nil, nil, err
		}
	}
	for _, e := range proj.Entities {
		input.Entities = append(input.Entities, PublishEntity{
			ID: e.ID, Kind: e.Kind, Namespace: e.Namespace, CanonicalName: e.Name, Aliases: e.Aliases,
		})
	}
	for _, d := range proj.Documents {
		pd := PublishDocument{
			DocumentID: d.DocumentID, Path: d.Path, Kind: d.Kind, Title: d.Title, Summary: d.Summary,
			Domains: d.Domains, ContentDigest: d.ContentDigest, FrontmatterJSON: d.FrontmatterJSON,
		}
		// The projection holds the canonical form (evidence keys rewritten to
		// the collector's IDs); publishing the raw staging bytes would leave
		// the Markdown citing keys nothing can resolve.
		pd.ContentMarkdown = d.ContentMarkdown
		aliasRaw, _ := json.Marshal(proj.EvidenceAliases)
		pd.EvidenceAliasJSON = string(aliasRaw)
		for _, a := range d.Assertions {
			scopeJSON, _ := json.Marshal(a.Scope)
			evJSON, _ := json.Marshal(a.Evidence)
			aboutJSON, _ := json.Marshal(a.About)
			pd.Assertions = append(pd.Assertions, domain.KnowledgeAssertion{
				ID: a.AssertionID, Heading: a.Heading, About: a.About, Perspective: a.Perspective,
				Basis: a.Basis, Statement: a.Statement, ScopeJSON: string(scopeJSON),
				EvidenceJSON: string(evJSON), UnknownNotes: a.UnknownNotes, Ordinal: a.Ordinal,
				ContentDigest: a.ContentDigest,
			})
			_ = aboutJSON
		}
		for _, r := range d.Relations {
			evJSON, _ := json.Marshal(r.Evidence)
			pd.Relations = append(pd.Relations, domain.KnowledgeAssertionRelation{
				ID: r.RelationID, FromKind: r.FromKind, FromID: r.FromID, Predicate: r.Predicate,
				ToKind: r.ToKind, ToID: r.ToID, ToResolved: r.ToResolved, ToRaw: r.ToRaw,
				Perspective: r.Perspective, Basis: r.Basis, Condition: r.Condition,
				EvidenceJSON: string(evJSON), Ordinal: r.Ordinal, ContentDigest: r.ContentDigest,
			})
		}
		input.Documents = append(input.Documents, pd)
	}
	input.Removals = proj.Removals
	input.Renames = make([]PublishRename, 0, len(proj.Renames))
	for _, rn := range proj.Renames {
		input.Renames = append(input.Renames, PublishRename{
			DocumentID: rn.DocumentID, FromPath: rn.FromPath, ToPath: rn.ToPath, Note: rn.Note,
		})
	}
	return proj, input, nil
}

func markDirtyFromFrozen(kl knowledgelib.Library, b *knowledgelib.Binding, rec domain.KnowledgeSnapshotBinding) {
	dir := kl.SnapshotWorktreeDirFor(rec.SnapshotID, rec.ID)
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return nil
		}
		b.DirtyPaths[filepath.ToSlash(rel)] = true
		return nil
	})
}

func readStagedDocument(staged *knowledgelib.Staged, path string) (string, error) {
	rel := strings.TrimPrefix(path, "content/")
	for _, d := range staged.Documents {
		if d.RelPath == rel {
			return string(knowledgelib.Canonicalize(d.Raw)), nil
		}
	}
	return "", fmt.Errorf("%w: staged document %s is missing", domain.ErrValidation, path)
}

func (s *Service) knownLibraryState(ctx context.Context, lib *domain.KnowledgeLibrary) (map[string]bool, map[string]string, map[string]string, map[string]bool, error) {
	entities, err := s.store.Library().ListEntities(ctx, lib.ID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	knownEntities := map[string]bool{}
	for _, e := range entities {
		knownEntities[e.ID] = true
	}
	docs, err := s.store.Library().ListDocuments(ctx, lib.ID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	knownDocs := map[string]string{}
	knownPaths := map[string]string{}
	evidenceIDs := []string{}
	for _, d := range docs {
		knownDocs[d.ID] = d.Path
		knownPaths[d.Path] = d.ID
		_, assertions, _, err := s.store.Library().DocumentVersionDetail(ctx, d.ID, 0)
		if err != nil {
			continue
		}
		for _, a := range assertions {
			var refs []struct {
				EvidenceID string `json:"evidence_id"`
			}
			if json.Unmarshal([]byte(a.EvidenceJSON), &refs) == nil {
				for _, r := range refs {
					evidenceIDs = append(evidenceIDs, r.EvidenceID)
				}
			}
		}
	}
	known, err := s.store.Library().EvidenceExists(ctx, lib.ID, evidenceIDs)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return knownEntities, knownDocs, knownPaths, known, nil
}

// ── Task state transitions ─────────────────────────────────────────────

func (s *Service) deferTask(ctx context.Context, lib *domain.KnowledgeLibrary, task *domain.KnowledgeWriteTask, cause error) error {
	task.Attempt++
	task.LastError = cause.Error()
	task.UpdatedAt = time.Now().UTC()
	if task.Attempt >= task.MaxAttempts {
		return s.blockTask(ctx, lib, task, "任务重试次数已达上限", cause.Error())
	}
	wait := time.Duration(task.Attempt) * 5 * time.Second
	next := time.Now().UTC().Add(wait)
	task.NextAttemptAt = &next
	task.Status = domain.KnowledgeTaskRetryWait
	task.OwnerToken = ""
	if err := s.store.Library().UpdateTask(ctx, task); err != nil {
		return err
	}
	if task.EventID != "" {
		return s.store.Library().UpdateEventStatus(ctx, task.EventID, domain.KnowledgeEventQueued, task.ID)
	}
	return nil
}

// repairOrBlock asks the model to fix a specific validation failure, bounded
// by the task's repair budget. The queue head is retained throughout.
func (s *Service) repairOrBlock(ctx context.Context, lib *domain.KnowledgeLibrary, task *domain.KnowledgeWriteTask, cause error) error {
	if task.RepairAttempt >= task.MaxRepairAttempts {
		return s.blockTask(ctx, lib, task, "自动修正次数已达上限，需要人工处理", cause.Error())
	}
	if task.StagingPath == "" {
		return s.blockTask(ctx, lib, task, "暂存目录缺失，无法修正", cause.Error())
	}
	run, err := s.store.Runs().Get(ctx, task.CurrentRunID)
	if err != nil {
		return s.blockTask(ctx, lib, task, "找不到上一轮运行，无法修正", cause.Error())
	}
	diagnostics := append([]string{}, diagnosticsOf(task)...)
	diagnostics = append(diagnostics, cause.Error())
	raw, _ := json.Marshal(diagnostics)
	task.DiagnosticsJSON = string(raw)
	task.RepairAttempt++
	task.TurnSeq++
	task.LastError = cause.Error()
	task.UpdatedAt = time.Now().UTC()
	instruction := strings.Join([]string{
		"上一次资料整理的产物未通过资料程序校验。下面是本轮发现的**全部**问题，请一次把它们都改掉。",
		"",
		cause.Error(),
		"",
		"修正要求：",
		"1. 重新读取暂存目录下的 brief.md 中的记录格式样例与硬性规则。",
		"2. 逐个修正上面列出的文件与位置；只改被点名的地方，不要重写没有报错的文档。",
		"3. 每条 assertion / relation 的元数据必须同时包含 perspective（normative 或 descriptive）、basis（source_statement / code_static / runtime_observed / inferred）、scope（conditions 与 environments 两个列表）。",
		"4. 修完后直接结束本轮；资料程序会重新校验并发布。",
	}, "\n")
	next, err := s.createRunLocked(ctx, task.WorkItemID, CreateRunParams{
		AgentProfileID: lib.LibrarianAgentID, Instruction: instruction,
		ClientKey:       fmt.Sprintf("knowledge-task:%s:repair:%d", task.ID, task.TurnSeq),
		knowledgeTaskID: task.ID, knowledgeTaskTurn: int64(task.TurnSeq),
		ContextSource:           domain.SnapshotSourceInherited,
		ContextSourceSnapshotID: run.ContextSnapshotID,
	})
	if err != nil {
		return s.blockTask(ctx, lib, task, "无法发起修正轮次", err.Error())
	}
	if err := s.dispatchCommittedRun(ctx, next); err != nil {
		return s.blockTask(ctx, lib, task, "无法派发修正轮次", err.Error())
	}
	turn := &domain.KnowledgeTaskTurn{
		ID: domain.NewID("kturn_"), TaskID: task.ID, TurnSeq: task.TurnSeq, RunID: next.ID,
		Purpose: "repair", Status: "pending", ErrorMessage: cause.Error(),
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.store.Library().CreateTurn(ctx, turn); err != nil {
		return err
	}
	task.CurrentRunID = next.ID
	task.Status = domain.KnowledgeTaskRunning
	if err := s.store.Library().UpdateTask(ctx, task); err != nil {
		return err
	}
	return nil
}

func diagnosticsOf(task *domain.KnowledgeWriteTask) []string {
	var out []string
	_ = json.Unmarshal([]byte(task.DiagnosticsJSON), &out)
	return out
}

func (s *Service) blockTask(ctx context.Context, lib *domain.KnowledgeLibrary, task *domain.KnowledgeWriteTask, reason, detail string) error {
	task.Status = domain.KnowledgeTaskBlocked
	task.BlockedReason = strings.TrimSpace(reason)
	if strings.TrimSpace(detail) != "" {
		task.LastError = detail
	}
	task.OwnerToken = ""
	task.UpdatedAt = time.Now().UTC()
	if err := s.store.Library().UpdateTask(ctx, task); err != nil {
		return err
	}
	if task.EventID != "" {
		return s.store.Library().UpdateEventStatus(ctx, task.EventID, domain.KnowledgeEventBlocked, task.ID)
	}
	return nil
}

// ── Publish recovery ───────────────────────────────────────────────────

// recoverLibraryPublications resolves an interrupted publish from durable
// records instead of guessing: a prepared journal entry with a matching
// release row is completed, otherwise it is abandoned and the task retried.
func (s *Service) recoverLibraryPublications(ctx context.Context, lib *domain.KnowledgeLibrary) error {
	prepared, err := s.store.Library().ListPreparedPublications(ctx, lib.ID)
	if err != nil {
		return err
	}
	for _, pub := range prepared {
		// The identity of a prepared publish is its projection digest, not a
		// release row: the journal entry is written before the release exists,
		// so release_id is empty until the publish commits. Looking the release
		// up by digest is what tells the two crash windows apart.
		rel, err := s.store.Library().ReleaseByProjectionDigest(ctx, lib.ID, pub.ProjectionDigest)
		switch {
		case err == nil:
			// The release exists, so the only remaining work is the official
			// files. While the owning task is still active it owns that work —
			// and its bounded retry/block policy must not be bypassed by a
			// recovery pass that would retry forever without ever blocking.
			if task, taskErr := s.store.Library().GetTask(ctx, lib.ID, pub.TaskID); taskErr == nil && taskOccupiesQueue(task.Status) {
				continue
			}
			// A failure here must stay visible and retryable: leaving the
			// publication prepared is what makes the next pass redo it.
			if err := s.materializeLibraryFiles(ctx, lib, rel); err != nil {
				return fmt.Errorf("knowledge library %s: 恢复发布 %s 的正式文件失败：%w", lib.ID, rel.ID, err)
			}
			// Sealed the same way the normal path seals it, so a recovery can
			// never leave a completed task beside a processing event either.
			settle := PublicationSettlement{TaskID: pub.TaskID, DiagnosticsJSON: "[]"}
			if task, taskErr := s.store.Library().GetTask(ctx, lib.ID, pub.TaskID); taskErr == nil {
				settle.EventID = task.EventID
				if strings.TrimSpace(task.DiagnosticsJSON) != "" {
					settle.DiagnosticsJSON = task.DiagnosticsJSON
				}
			}
			if err := s.store.Library().SettlePublication(ctx, settle); err != nil {
				return fmt.Errorf("knowledge library %s: 恢复发布 %s 的封口失败：%w", lib.ID, rel.ID, err)
			}
		case errors.Is(err, domain.ErrNotFound):
			// No release for this digest: either the publish never happened or
			// the task is still going to do it. Abandoning under an active task
			// would erase the journal entry that its own recovery relies on.
			if task, taskErr := s.store.Library().GetTask(ctx, lib.ID, pub.TaskID); taskErr == nil && taskOccupiesQueue(task.Status) {
				continue
			}
			if err := s.store.Library().AbandonPublication(ctx, pub.TaskID); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return nil
}

// taskOccupiesQueue reports whether a task still owns the queue head, i.e.
// whether it is still managed by the worker state machine rather than by
// recovery. A blocked task counts: it is waiting for an operator's retry, and
// letting recovery act on its publication would spend the materialization
// attempts its own budget already exhausted. Only a terminal task — or one
// that no longer exists — leaves its publication to recovery.
func taskOccupiesQueue(status domain.KnowledgeTaskStatus) bool {
	return status.OccupiesHead()
}

// ── Materialize the published Markdown view ────────────────────────────

// materializeLibraryFiles writes one release back out as ordinary
// Markdown: content/, catalog/views, _sources/manifest.yaml and INDEX.md.
// The database rows are the durable record; the files are the same projection
// rendered for humans, Obsidian and the library agent's own reading.
func (s *Service) materializeLibraryFiles(ctx context.Context, lib *domain.KnowledgeLibrary, current *domain.KnowledgeRelease) error {
	if current == nil {
		return nil
	}
	kl, err := s.libraryRoot(lib)
	if err != nil {
		return err
	}
	if err := kl.Ensure(); err != nil {
		return err
	}
	docs, err := s.store.Library().ReleaseDocuments(ctx, current.ID, "", "", 1000)
	if err != nil {
		return err
	}
	// Remove only files this library owns and the new release no longer
	// contains. A blanket sweep would destroy unrelated files, and on a
	// release that legitimately carries no documents it would empty the whole
	// content zone.
	live := map[string]bool{}
	for _, d := range docs {
		live[filepath.Join(kl.Root, filepath.FromSlash(d.Path))] = true
	}
	owned, err := s.store.Library().OwnedContentPaths(ctx, lib.ID)
	if err != nil {
		return err
	}
	for _, rel := range owned {
		target := filepath.Join(kl.Root, filepath.FromSlash(rel))
		if live[target] {
			continue
		}
		if _, statErr := os.Stat(target); statErr != nil {
			continue
		}
		if err := os.Remove(target); err != nil {
			return err
		}
	}
	for _, d := range docs {
		version, _, _, err := s.store.Library().DocumentVersionDetail(ctx, d.ID, 0)
		if err != nil {
			return err
		}
		target := filepath.Join(kl.Root, filepath.FromSlash(d.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(version.ContentMarkdown), 0o644); err != nil {
			return err
		}
	}
	if err := s.writeLibraryIndex(ctx, kl, lib, current, docs); err != nil {
		return err
	}
	return s.writeSourceManifest(ctx, kl, lib)
}

func (s *Service) writeLibraryIndex(ctx context.Context, kl knowledgelib.Library, lib *domain.KnowledgeLibrary, rel *domain.KnowledgeRelease, docs []*domain.KnowledgeDocument) error {
	var b strings.Builder
	b.WriteString("# 项目资料库\n\n")
	b.WriteString(fmt.Sprintf("当前发布版本：`%s`（seq %d，%s）\n\n", rel.ID, rel.Seq, rel.PublishedAt.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("文档 %d 篇，知识条目 %d 条，关系 %d 条，证据 %d 条。\n\n",
		rel.DocumentCount, rel.AssertionCount, rel.RelationCount, rel.EvidenceCount))
	b.WriteString("## 按主类浏览\n\n")
	byKind := map[string][]*domain.KnowledgeDocument{}
	for _, d := range docs {
		byKind[d.Kind] = append(byKind[d.Kind], d)
	}
	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		b.WriteString(fmt.Sprintf("### %s\n\n", k))
		for _, d := range byKind[k] {
			b.WriteString(fmt.Sprintf("- [%s](%s) — %s\n", d.Title, d.Path, firstLine(d.Summary)))
		}
		b.WriteString("\n")
	}
	b.WriteString("## 按业务域浏览\n\n")
	byDomain := map[string][]*domain.KnowledgeDocument{}
	for _, d := range docs {
		for _, dom := range d.Domains {
			byDomain[dom] = append(byDomain[dom], d)
		}
	}
	domains := make([]string, 0, len(byDomain))
	for k := range byDomain {
		domains = append(domains, k)
	}
	sort.Strings(domains)
	for _, dom := range domains {
		b.WriteString(fmt.Sprintf("### %s\n\n", dom))
		for _, d := range byDomain[dom] {
			b.WriteString(fmt.Sprintf("- [%s](%s)\n", d.Title, d.Path))
		}
		b.WriteString("\n")
	}
	if err := os.WriteFile(kl.IndexDocPath(), []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(kl.CatalogDir(), "by-component.md"),
		[]byte(renderCatalogByPath(docs)), 0o644)
}

func renderCatalogByPath(docs []*domain.KnowledgeDocument) string {
	var b strings.Builder
	b.WriteString("# 按组成部分浏览\n\n")
	for _, d := range docs {
		b.WriteString(fmt.Sprintf("- `%s` [%s](%s)\n", d.Kind, d.Title, "../"+d.Path))
	}
	return b.String()
}

func (s *Service) writeSourceManifest(ctx context.Context, kl knowledgelib.Library, lib *domain.KnowledgeLibrary) error {
	sources, err := s.store.Library().ListSources(ctx, lib.ID)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# 来源登记（由资料 harness 生成，人工修改不会生效）\n")
	b.WriteString("version: 1\nsources:\n")
	for _, src := range sources {
		enabled := "false"
		if src.Enabled {
			enabled = "true"
		}
		b.WriteString(fmt.Sprintf("  - name: %s\n    kind: %s\n    repo_path: %s\n    default_ref: %s\n    enabled: %s\n    usages:\n",
			src.Name, src.Kind, src.RepoPath, src.DefaultRef, enabled))
		for _, usage := range src.EffectiveUsages() {
			b.WriteString(fmt.Sprintf("    - consumer: %s\n      artifact: %s\n", usage.Consumer, usage.Artifact))
		}
	}
	return os.WriteFile(filepath.Join(kl.SourcesDir(), knowledgelib.SourcesFileName), []byte(b.String()), 0o644)
}

// ── Run markers ────────────────────────────────────────────────────────

// knowledgeLibraryMarker identifies a Run started by the library harness.
// It is read from the immutable Run input, never from a caller field.
type knowledgeLibraryMarkerValue struct {
	TaskID    string
	TurnSeq   int64
	PromptVer string
}

func knowledgeLibraryMarker(run *domain.ExecutionRun) (knowledgeLibraryMarkerValue, bool) {
	if run == nil || run.Input == nil {
		return knowledgeLibraryMarkerValue{}, false
	}
	raw, ok := run.Input["knowledge_library"].(map[string]any)
	if !ok {
		return knowledgeLibraryMarkerValue{}, false
	}
	taskID, _ := raw["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		return knowledgeLibraryMarkerValue{}, false
	}
	marker := knowledgeLibraryMarkerValue{TaskID: taskID}
	switch v := raw["turn_seq"].(type) {
	case float64:
		marker.TurnSeq = int64(v)
	case int64:
		marker.TurnSeq = v
	case int:
		marker.TurnSeq = int64(v)
	}
	marker.PromptVer, _ = raw["prompt_version"].(string)
	return marker, true
}

// turnExists reports whether a task already recorded the given turn.
func turnExists(turns []*domain.KnowledgeTaskTurn, seq int) bool {
	for _, t := range turns {
		if t.TurnSeq == seq {
			return true
		}
	}
	return false
}

func isKnowledgeLibraryRun(run *domain.ExecutionRun) bool {
	_, ok := knowledgeLibraryMarker(run)
	return ok
}

// notifyKnowledgeLibraryTerminal wakes the FIFO worker so a terminal agent Run
// is ingested immediately instead of on the next poll.
func (s *Service) notifyKnowledgeLibraryTerminal(ctx context.Context, workspaceID string) {
	if s.notifier != nil {
		s.notifier.Notify(workspaceID)
	}
}

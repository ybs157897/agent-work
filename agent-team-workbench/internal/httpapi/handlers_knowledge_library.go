package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/security"
)

const (
	knowledgeLibraryListLimit = 200
	knowledgeLibraryBodyBytes = 1 << 20
)

func (s *Server) registerKnowledgeLibraryRoutes(mux *http.ServeMux) {
	// Route patterns are written as single string literals because the
	// contract guard diffs registered routes against contracts/web/openapi.yaml
	// by parsing this file's AST.
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/library", s.guard(security.PermRead, s.handleLibrarySummary))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/library/status", s.guard(security.PermRead, s.handleLibraryStatus))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/library/sources", s.guard(security.PermRead, s.handleLibrarySources))
	mux.HandleFunc("POST /api/v1/workspaces/{workspace_id}/library/sources", s.guard(security.PermAgentWrite, s.handleLibraryCreateSource))
	mux.HandleFunc("PATCH /api/v1/workspaces/{workspace_id}/library/sources/{source_id}", s.guard(security.PermAgentWrite, s.handleLibraryUpdateSource))
	mux.HandleFunc("DELETE /api/v1/workspaces/{workspace_id}/library/sources/{source_id}", s.guard(security.PermAgentWrite, s.handleLibraryDeleteSource))
	mux.HandleFunc("POST /api/v1/workspaces/{workspace_id}/library/events", s.guard(security.PermAgentWrite, s.handleLibrarySubmitEvent))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/library/events", s.guard(security.PermRead, s.handleLibraryEvents))
	mux.HandleFunc("POST /api/v1/workspaces/{workspace_id}/library/initialize", s.guard(security.PermAgentWrite, s.handleLibraryInitialize))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/library/tasks", s.guard(security.PermRead, s.handleLibraryTasks))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/library/tasks/{task_id}", s.guard(security.PermRead, s.handleLibraryTask))
	mux.HandleFunc("POST /api/v1/workspaces/{workspace_id}/library/tasks/{task_id}/retry", s.guard(security.PermRunControl, s.handleLibraryRetryTask))
	mux.HandleFunc("POST /api/v1/workspaces/{workspace_id}/library/tasks/{task_id}/cancel", s.guard(security.PermRunControl, s.handleLibraryCancelTask))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/library/releases", s.guard(security.PermRead, s.handleLibraryReleases))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/library/releases/{release_id}", s.guard(security.PermRead, s.handleLibraryRelease))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/library/documents", s.guard(security.PermRead, s.handleLibraryDocuments))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/library/documents/{document_id}", s.guard(security.PermRead, s.handleLibraryDocument))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/library/evidence/{evidence_id}", s.guard(security.PermRead, s.handleLibraryEvidence))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/library/graph", s.guard(security.PermRead, s.handleLibraryGraph))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/library/bridges", s.guard(security.PermRead, s.handleLibraryBridges))
	mux.HandleFunc("POST /api/v1/workspaces/{workspace_id}/library/query", s.guard(security.PermRead, s.handleLibraryQuery))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/library/expand", s.guard(security.PermRead, s.handleLibraryExpand))
	mux.HandleFunc("POST /api/v1/workspaces/{workspace_id}/library/reindex", s.guard(security.PermAgentWrite, s.handleLibraryReindex))
}

func (s *Server) libraryWorkspace(r *http.Request) string {
	return r.PathValue("workspace_id")
}

func (s *Server) decodeLibraryBody(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, knowledgeLibraryBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		writeProblem(w, r, Problem{Type: "about:blank", Title: "Invalid request body", Status: http.StatusBadRequest,
			Code: "invalid_body", Detail: err.Error()})
		return false
	}
	return true
}

func (s *Server) libraryLimit(r *http.Request) int {
	limit, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
	if err != nil || limit <= 0 || limit > knowledgeLibraryListLimit {
		return 0
	}
	return limit
}

// ── Library summary and status ─────────────────────────────────────────

func (s *Server) handleLibrarySummary(w http.ResponseWriter, r *http.Request) {
	ws := s.libraryWorkspace(r)
	lib, err := s.svc.EnsureKnowledgeLibrary(r.Context(), ws)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.librarySummaryPayload(r, lib))
}

func (s *Server) librarySummaryPayload(r *http.Request, lib *domain.KnowledgeLibrary) map[string]any {
	sources, _ := s.svc.ListKnowledgeSources(r.Context(), lib.WorkspaceID)
	releases, _ := s.svc.ListKnowledgeReleases(r.Context(), lib.WorkspaceID, 1)
	var current *domain.KnowledgeRelease
	if len(releases) > 0 {
		current = releases[0]
	}
	pending, blocked, _ := s.svc.KnowledgeLibraryQueueDepth(r.Context(), lib.WorkspaceID)
	head, _ := s.svc.KnowledgeLibraryHeadTask(r.Context(), lib.WorkspaceID)
	documentCount := 0
	releaseCount := len(releases)
	if current != nil {
		documentCount = current.DocumentCount
	}
	releaseCountAll, _ := s.svc.CountKnowledgeReleases(r.Context(), lib.WorkspaceID)
	if releaseCountAll > 0 {
		releaseCount = releaseCountAll
	}
	queue := map[string]any{"pending": pending, "blocked": blocked, "head_seq": nil, "running": nil}
	if head != nil {
		queue["head_seq"] = head.Seq
		if head.Status == domain.KnowledgeTaskRunning {
			queue["running"] = taskSummaryJSON(head)
		}
	}
	return map[string]any{
		"workspace_id": lib.WorkspaceID, "root_path": lib.RootPath, "enabled": lib.Enabled,
		"current_release": releaseJSON(current), "release_count": releaseCount,
		"document_count": documentCount, "source_count": len(sources),
		"index_revision": lib.IndexRevision, "updated_at": lib.UpdatedAt,
		"queue": queue,
	}
}

func (s *Server) handleLibraryStatus(w http.ResponseWriter, r *http.Request) {
	ws := s.libraryWorkspace(r)
	lib, err := s.svc.EnsureKnowledgeLibrary(r.Context(), ws)
	if err != nil {
		fail(w, r, err)
		return
	}
	tasks, err := s.svc.ListKnowledgeWriteTasks(r.Context(), ws, "", 50)
	if err != nil {
		fail(w, r, err)
		return
	}
	blocked := []map[string]any{}
	errorsOut := []map[string]any{}
	for _, t := range tasks {
		if t.Status == domain.KnowledgeTaskBlocked {
			blocked = append(blocked, taskSummaryJSON(t))
		}
		if t.LastError != "" || t.BlockedReason != "" {
			errorsOut = append(errorsOut, map[string]any{
				"task_id": t.ID, "seq": t.Seq, "kind": t.Kind, "last_error": t.LastError,
				"blocked_reason": t.BlockedReason, "updated_at": t.UpdatedAt,
			})
		}
	}
	head, _ := s.svc.KnowledgeLibraryHeadTask(r.Context(), ws)
	pending, _, _ := s.svc.KnowledgeLibraryQueueDepth(r.Context(), ws)
	releases, _ := s.svc.ListKnowledgeReleases(r.Context(), ws, 1)
	var last *domain.KnowledgeRelease
	if len(releases) > 0 {
		last = releases[0]
	}
	freshness := map[string]any{"pending_events": pending, "newer_release_available": false, "stale_sources": []string{}}
	if last != nil {
		freshness["release_id"] = last.ID
		freshness["published_at"] = last.PublishedAt
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"library": s.librarySummaryPayload(r, lib), "head_task": taskSummaryJSON(head),
		"blocked_tasks": blocked, "recent_errors": errorsOut, "last_release": releaseJSON(last),
		"pending_events": pending, "index_revision": lib.IndexRevision, "freshness": freshness,
	})
}

// ── Sources ────────────────────────────────────────────────────────────

func (s *Server) handleLibrarySources(w http.ResponseWriter, r *http.Request) {
	sources, err := s.svc.ListKnowledgeSources(r.Context(), s.libraryWorkspace(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(sources))
	for _, src := range sources {
		items = append(items, sourceJSON(src))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type librarySourceUsage struct {
	Consumer    string `json:"consumer"`
	Artifact    string `json:"artifact"`
	Environment string `json:"environment"`
	// ArtifactResolution carries how the version was obtained ('declared',
	// 'resolved', 'unknown'). It is part of the write contract: rejecting it
	// would silently drop the operator's declaration.
	ArtifactResolution string `json:"artifact_resolution"`
	// ResolutionRef is the verifiable basis required for 'resolved'. Without it
	// the server stores 'declared' instead of trusting the label.
	ResolutionRef string `json:"resolution_ref"`
}

type librarySourceRequest struct {
	Name         string               `json:"name"`
	Kind         string               `json:"kind"`
	RepoPath     string               `json:"repo_path"`
	DefaultRef   string               `json:"default_ref"`
	Usages       []librarySourceUsage `json:"usages"`
	IncludeGlobs []string             `json:"include_globs"`
	ExcludeGlobs []string             `json:"exclude_globs"`
	Enabled      *bool                `json:"enabled"`
}

// usagesOf converts the wire shape to the application input.
func (r librarySourceRequest) usagesOf() []domain.KnowledgeSourceUsage {
	out := make([]domain.KnowledgeSourceUsage, 0, len(r.Usages))
	for _, u := range r.Usages {
		out = append(out, domain.KnowledgeSourceUsage{
			Consumer: u.Consumer, Artifact: u.Artifact, Environment: u.Environment,
			ArtifactResolution: u.ArtifactResolution, ResolutionRef: u.ResolutionRef,
		})
	}
	return out
}

type librarySourceUpdateRequest struct {
	librarySourceRequest
	ExpectedVersion int `json:"expected_version"`
}

func (s *Server) handleLibraryCreateSource(w http.ResponseWriter, r *http.Request) {
	var req librarySourceRequest
	if !s.decodeLibraryBody(w, r, &req) {
		return
	}
	src, err := s.svc.CreateKnowledgeSource(r.Context(), s.libraryWorkspace(r), application.KnowledgeLibrarySourceInput{
		Name: req.Name, Kind: domain.KnowledgeSourceKind(req.Kind), RepoPath: req.RepoPath,
		DefaultRef: req.DefaultRef, IncludeGlobs: req.IncludeGlobs, ExcludeGlobs: req.ExcludeGlobs,
		Usages: req.usagesOf(), Enabled: req.Enabled,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, sourceJSON(src))
}

func (s *Server) handleLibraryUpdateSource(w http.ResponseWriter, r *http.Request) {
	var req librarySourceUpdateRequest
	if !s.decodeLibraryBody(w, r, &req) {
		return
	}
	src, err := s.svc.UpdateKnowledgeSource(r.Context(), s.libraryWorkspace(r),
		r.PathValue("source_id"), req.ExpectedVersion, application.KnowledgeLibrarySourceInput{
			Name: req.Name, Kind: domain.KnowledgeSourceKind(req.Kind), RepoPath: req.RepoPath,
			DefaultRef: req.DefaultRef, IncludeGlobs: req.IncludeGlobs, ExcludeGlobs: req.ExcludeGlobs,
			Usages: req.usagesOf(), Enabled: req.Enabled,
		})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sourceJSON(src))
}

func (s *Server) handleLibraryDeleteSource(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.DeleteKnowledgeSource(r.Context(), s.libraryWorkspace(r), r.PathValue("source_id")); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Events ─────────────────────────────────────────────────────────────

type libraryEventRequest struct {
	ProtocolVersion string         `json:"protocol_version"`
	EventType       string         `json:"event_type"`
	Source          string         `json:"source"`
	Subject         map[string]any `json:"subject"`
	ContentRef      string         `json:"content_ref"`
	Payload         map[string]any `json:"payload"`
	ClientKey       string         `json:"client_key"`
}

func (s *Server) handleLibrarySubmitEvent(w http.ResponseWriter, r *http.Request) {
	var req libraryEventRequest
	if !s.decodeLibraryBody(w, r, &req) {
		return
	}
	receipt, err := s.svc.SubmitKnowledgeLibraryEvent(r.Context(), application.KnowledgeLibraryEventInput{
		WorkspaceID: s.libraryWorkspace(r), ProtocolVersion: req.ProtocolVersion,
		EventType: req.EventType, Source: req.Source, Subject: req.Subject,
		ContentRef: req.ContentRef, Payload: req.Payload, ClientKey: req.ClientKey,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, receipt)
}

type libraryInitializeRequest struct {
	ClientKey string `json:"client_key"`
	ViewID    string `json:"view_id"`
	Reason    string `json:"reason"`
}

func (s *Server) handleLibraryInitialize(w http.ResponseWriter, r *http.Request) {
	var req libraryInitializeRequest
	if !s.decodeLibraryBody(w, r, &req) {
		return
	}
	subject := map[string]any{}
	if strings.TrimSpace(req.ViewID) != "" {
		subject["view_id"] = req.ViewID
	}
	receipt, err := s.svc.SubmitKnowledgeLibraryEvent(r.Context(), application.KnowledgeLibraryEventInput{
		WorkspaceID: s.libraryWorkspace(r), EventType: "workspace.connected", Source: "admin_console",
		Subject: subject, Payload: map[string]any{"reason": req.Reason}, ClientKey: req.ClientKey,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, receipt)
}

func (s *Server) handleLibraryEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.svc.ListKnowledgeLibraryEvents(r.Context(), s.libraryWorkspace(r),
		strings.TrimSpace(r.URL.Query().Get("status")), s.libraryLimit(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(events))
	for _, e := range events {
		items = append(items, map[string]any{
			"id": e.ID, "event_type": e.EventType, "source": e.Source,
			"subject": jsonObject(e.SubjectJSON), "content_ref": e.ContentRef,
			"status": e.Status, "task_id": e.TaskID, "client_key": e.ClientKey,
			"received_at": e.ReceivedAt, "updated_at": e.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// ── Tasks ──────────────────────────────────────────────────────────────

func (s *Server) handleLibraryTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.svc.ListKnowledgeWriteTasks(r.Context(), s.libraryWorkspace(r),
		strings.TrimSpace(r.URL.Query().Get("status")), s.libraryLimit(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(tasks))
	for _, t := range tasks {
		items = append(items, taskSummaryJSON(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleLibraryTask(w http.ResponseWriter, r *http.Request) {
	task, turns, err := s.svc.GetKnowledgeWriteTask(r.Context(), s.libraryWorkspace(r), r.PathValue("task_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	payload := taskSummaryJSON(task)
	payload["staging_path"] = task.StagingPath
	payload["plan"] = jsonObject(task.PlanJSON)
	payload["coverage"] = jsonObject(task.CoverageJSON)
	payload["diagnostics"] = jsonStrings(task.DiagnosticsJSON)
	turnItems := make([]map[string]any, 0, len(turns))
	for _, t := range turns {
		turnItems = append(turnItems, map[string]any{
			"id": t.ID, "turn_seq": t.TurnSeq, "run_id": t.RunID, "purpose": t.Purpose,
			"status": t.Status, "error_message": t.ErrorMessage,
			"created_at": t.CreatedAt, "updated_at": t.UpdatedAt,
		})
	}
	payload["turns"] = turnItems
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleLibraryRetryTask(w http.ResponseWriter, r *http.Request) {
	task, err := s.svc.RetryKnowledgeWriteTask(r.Context(), s.libraryWorkspace(r), r.PathValue("task_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, taskSummaryJSON(task))
}

type libraryCancelRequest struct {
	Reason string `json:"reason"`
}

func (s *Server) handleLibraryCancelTask(w http.ResponseWriter, r *http.Request) {
	var req libraryCancelRequest
	if !s.decodeLibraryBody(w, r, &req) {
		return
	}
	task, err := s.svc.CancelKnowledgeWriteTask(r.Context(), s.libraryWorkspace(r), r.PathValue("task_id"), req.Reason)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, taskSummaryJSON(task))
}

// ── Releases and browse ────────────────────────────────────────────────

func (s *Server) handleLibraryReleases(w http.ResponseWriter, r *http.Request) {
	releases, err := s.svc.ListKnowledgeReleases(r.Context(), s.libraryWorkspace(r), s.libraryLimit(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(releases))
	for _, rel := range releases {
		items = append(items, releaseJSON(rel))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleLibraryRelease(w http.ResponseWriter, r *http.Request) {
	rel, err := s.svc.GetKnowledgeRelease(r.Context(), s.libraryWorkspace(r), r.PathValue("release_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	payload := releaseJSON(rel)
	items := []map[string]any{}
	if docs, _, err := s.svc.ListKnowledgeDocuments(r.Context(), s.libraryWorkspace(r), rel.ID, "", "", 0); err == nil {
		for _, d := range docs {
			items = append(items, documentJSON(d))
		}
	}
	payload["documents"] = items
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleLibraryDocuments(w http.ResponseWriter, r *http.Request) {
	docs, rel, err := s.svc.ListKnowledgeDocuments(r.Context(), s.libraryWorkspace(r),
		strings.TrimSpace(r.URL.Query().Get("release_id")),
		strings.TrimSpace(r.URL.Query().Get("q")),
		strings.TrimSpace(r.URL.Query().Get("kind")), s.libraryLimit(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(docs))
	for _, d := range docs {
		item := documentJSON(d)
		if rel != nil {
			item["release_id"] = rel.ID
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "release": releaseJSON(rel)})
}

func (s *Server) handleLibraryDocument(w http.ResponseWriter, r *http.Request) {
	version, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("version")))
	detail, err := s.svc.GetKnowledgeDocument(r.Context(), s.libraryWorkspace(r), r.PathValue("document_id"),
		strings.TrimSpace(r.URL.Query().Get("release_id")), version)
	if err != nil {
		fail(w, r, err)
		return
	}
	payload := map[string]any{"document": documentJSON(detail.Document)}
	if detail.Version != nil {
		payload["version"] = map[string]any{
			"id": detail.Version.ID, "version": detail.Version.Version,
			"content_digest": detail.Version.ContentDigest, "created_at": detail.Version.CreatedAt,
			"content_markdown": detail.Version.ContentMarkdown,
		}
	}
	payload["assertions"] = assertionListJSON(detail.Assertions)
	payload["relations"] = relationListJSON(detail.Relations)
	versions := make([]map[string]any, 0, len(detail.Versions))
	for _, v := range detail.Versions {
		versions = append(versions, map[string]any{
			"id": v.ID, "version": v.Version, "content_digest": v.ContentDigest, "created_at": v.CreatedAt,
		})
	}
	payload["versions"] = versions
	payload["pinned"] = detail.Pinned
	if detail.ReleaseID != "" {
		payload["release_id"] = detail.ReleaseID
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleLibraryEvidence(w http.ResponseWriter, r *http.Request) {
	ev, binding, rep, err := s.svc.GetKnowledgeEvidence(r.Context(), s.libraryWorkspace(r), r.PathValue("evidence_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": ev.ID, "snapshot_id": ev.SnapshotID, "binding_id": ev.BindingID,
		"representation_id": ev.RepresentationID, "locator": jsonObject(ev.LocatorJSON),
		"locator_kind": ev.LocatorKind, "excerpt": ev.Excerpt, "excerpt_digest": ev.ExcerptDigest,
		"match_count": ev.MatchCount, "availability": ev.Availability, "collected_at": ev.CollectedAt,
		"binding": map[string]any{
			"source_name": binding.SourceName, "source_kind": binding.SourceKind,
			"repo_path": binding.RepoPath, "git_ref": binding.GitRef, "commit_sha": binding.CommitSHA,
			"dirty": binding.Dirty, "artifact": binding.Artifact,
			"artifact_resolution": binding.ArtifactResolution,
			"resolution_ref":      binding.ArtifactResolutionRef,
			"consumer":            binding.Consumer, "environment": binding.Environment,
			"snapshot_id": binding.SnapshotID,
		},
		// The triple the client must be able to check: this evidence belongs to
		// exactly this snapshot, binding and frozen representation.
		"consistency": map[string]any{
			"snapshot_id":       ev.SnapshotID,
			"binding_id":        ev.BindingID,
			"representation_id": ev.RepresentationID,
		},
		"representation": map[string]any{
			"path": rep.Path, "media_type": rep.MediaType, "encoding": rep.Encoding,
			"digest_algo": rep.DigestAlgo, "content_digest": rep.ContentDigest, "byte_size": rep.ByteSize,
			"coverage": rep.Coverage, "origin": rep.Origin, "stored_path": rep.StoredPath,
		},
	})
}

func (s *Server) handleLibraryGraph(w http.ResponseWriter, r *http.Request) {
	graph, err := s.svc.GetKnowledgeGraph(r.Context(), s.libraryWorkspace(r),
		strings.TrimSpace(r.URL.Query().Get("release_id")))
	if err != nil {
		fail(w, r, err)
		return
	}
	entities := make([]map[string]any, 0, len(graph.Entities))
	for _, e := range graph.Entities {
		entities = append(entities, map[string]any{
			"id": e.ID, "kind": e.Kind, "namespace": e.Namespace, "canonical_name": e.CanonicalName,
			"aliases": e.Aliases, "document_id": e.PrimaryDocumentID,
		})
	}
	docs := make([]map[string]any, 0, len(graph.Documents))
	for _, d := range graph.Documents {
		docs = append(docs, documentJSON(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"release_id": graph.ReleaseID, "entities": entities,
		"relations": relationListJSON(graph.Relations), "documents": docs,
	})
}

func (s *Server) handleLibraryBridges(w http.ResponseWriter, r *http.Request) {
	bridges, err := s.svc.ListKnowledgeBridges(r.Context(), s.libraryWorkspace(r),
		strings.TrimSpace(r.URL.Query().Get("release_id")))
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(bridges))
	for _, b := range bridges {
		items = append(items, map[string]any{
			"id": b.ID, "release_id": b.ReleaseID, "base_release_id": b.BaseReleaseID,
			"anchor_entity_id": b.AnchorEntityID, "title": b.Title, "summary": b.Summary,
			"steps": jsonArray(b.StepsJSON), "participants": jsonArray(b.ParticipantsJSON),
			"coverage": jsonObject(b.CoverageJSON), "gaps": jsonArray(b.GapsJSON),
			"builder_version": b.BuilderVersion, "content_digest": b.ContentDigest,
			"created_at": b.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// ── Query and expand ───────────────────────────────────────────────────

type libraryQueryRequest struct {
	Question  string   `json:"question"`
	Terms     []string `json:"terms"`
	ReleaseID string   `json:"release_id"`
	Limit     int      `json:"limit"`
}

func (s *Server) handleLibraryQuery(w http.ResponseWriter, r *http.Request) {
	var req libraryQueryRequest
	if !s.decodeLibraryBody(w, r, &req) {
		return
	}
	answer, err := s.svc.QueryKnowledgeLibrary(r.Context(), application.KnowledgeLibraryQuery{
		WorkspaceID: s.libraryWorkspace(r), ReleaseID: req.ReleaseID,
		Question: req.Question, Terms: req.Terms, Limit: req.Limit,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	results := make([]map[string]any, 0, len(answer.Hits))
	for _, hit := range answer.Hits {
		evItems := make([]map[string]any, 0, len(hit.Evidence))
		for _, ev := range hit.Evidence {
			evItems = append(evItems, map[string]any{
				"id": ev.ID, "locator_kind": ev.LocatorKind, "excerpt": ev.Excerpt,
				"availability": ev.Availability, "match_count": ev.MatchCount,
			})
		}
		results = append(results, map[string]any{
			"assertion": assertionJSON(hit.Assertion),
			"document":  map[string]any{"id": hit.Document.ID, "path": hit.Document.Path, "title": hit.Document.Title},
			"score":     hit.Score, "snippet": hit.Snippet, "evidence": evItems,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"release": releaseJSON(answer.Release), "results": results,
		"coverage": map[string]any{
			"status": answer.Coverage.Status, "truncated": answer.Coverage.Truncated,
			"scanned_versions": answer.Coverage.ScannedVersions, "notes": nonNilStrings(answer.Coverage.Notes),
		},
		"freshness": map[string]any{
			"release_id": answer.Freshness.ReleaseID, "published_at": answer.Freshness.PublishedAt,
			"pending_events":          answer.Freshness.PendingEvents,
			"newer_release_available": answer.Freshness.NewerReleaseAvailable,
			"stale_sources":           answer.Freshness.StaleSources,
		},
		"unknowns": answer.Unknowns, "expand_handles": answer.Expandables,
	})
}

func (s *Server) handleLibraryExpand(w http.ResponseWriter, r *http.Request) {
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		fail(w, r, domain.ErrValidation)
		return
	}
	switch kind {
	case "evidence":
		s.handleLibraryEvidence(w, r)
		return
	case "assertion", "":
		assertion, doc, err := s.svc.ExpandKnowledgeAssertion(r.Context(), s.libraryWorkspace(r),
			strings.TrimSpace(r.URL.Query().Get("release_id")), id)
		if err != nil {
			fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"kind": "assertion", "assertion": assertionJSON(*assertion), "document": documentJSON(doc),
		})
	default:
		fail(w, r, domain.ErrValidation)
	}
}

// ── Reindex ────────────────────────────────────────────────────────────

func (s *Server) handleLibraryReindex(w http.ResponseWriter, r *http.Request) {
	count, err := s.svc.ReindexKnowledgeLibrary(r.Context(), s.libraryWorkspace(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"indexed_versions": count})
}

// ── JSON projections ───────────────────────────────────────────────────

func sourceJSON(src *domain.KnowledgeSource) map[string]any {
	usages := make([]map[string]any, 0, len(src.Usages))
	for _, u := range src.Usages {
		usages = append(usages, map[string]any{
			"consumer": u.Consumer, "artifact": u.Artifact, "environment": u.Environment,
			// artifact_resolution is the effective state this build can
			// establish; claimed_resolution is what the caller asserted, and
			// resolution_ref is kept only as an unverified note. Reporting the
			// claim as a state would let a caller mint verification.
			"artifact_resolution": u.ArtifactState(),
			"claimed_resolution":  u.ResolutionClaimed(),
			"resolution_ref":      u.ResolutionRef,
			"resolution_note":     "引用未经解析核实，仅作登记备注",
			"downgraded":          u.ResolutionDowngraded(),
		})
	}
	return map[string]any{
		"id": src.ID, "name": src.Name, "kind": src.Kind, "repo_path": src.RepoPath,
		"default_ref": src.DefaultRef, "usages": usages,
		"enabled": src.Enabled, "version": src.Version,
		"created_at": src.CreatedAt, "updated_at": src.UpdatedAt,
	}
}

// coverageStateOf distinguishes "the library has not computed coverage yet"
// from "the library reported coverage and found no gaps". An empty coverage
// object is never presented as a verified clean result.
func coverageStateOf(coverageJSON string, terminal bool) string {
	trimmed := strings.TrimSpace(coverageJSON)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		if terminal {
			return "not_computed"
		}
		return "pending"
	}
	if !terminal {
		return "in_progress"
	}
	return "reported"
}

func taskSummaryJSON(t *domain.KnowledgeWriteTask) map[string]any {
	if t == nil {
		return nil
	}
	return map[string]any{
		"coverage_state": coverageStateOf(t.CoverageJSON, t.Status.IsTerminal()),
		"id":             t.ID, "seq": t.Seq, "kind": t.Kind, "status": t.Status, "view_id": t.ViewID,
		"attempt": t.Attempt, "max_attempts": t.MaxAttempts,
		"repair_attempt": t.RepairAttempt, "max_repair_attempts": t.MaxRepairAttempts,
		"base_release_id": t.BaseReleaseID, "target_release_id": t.TargetReleaseID,
		"snapshot_id": t.SnapshotID, "turn_seq": t.TurnSeq,
		"last_error": t.LastError, "blocked_reason": t.BlockedReason,
		"created_at": t.CreatedAt, "updated_at": t.UpdatedAt, "finished_at": t.FinishedAt,
	}
}

func releaseJSON(rel *domain.KnowledgeRelease) map[string]any {
	if rel == nil {
		return nil
	}
	return map[string]any{
		"coverage_state": coverageStateOf(rel.CoverageJSON, true),
		"id":             rel.ID, "seq": rel.Seq, "snapshot_id": rel.SnapshotID, "task_id": rel.TaskID,
		"parent_release_id": rel.ParentReleaseID, "projection_digest": rel.ProjectionDigest,
		"document_count": rel.DocumentCount, "assertion_count": rel.AssertionCount,
		"relation_count": rel.RelationCount, "evidence_count": rel.EvidenceCount,
		"coverage": jsonObject(rel.CoverageJSON), "notes": rel.Notes,
		"status": rel.Status, "published_at": rel.PublishedAt,
	}
}

func documentJSON(d *domain.KnowledgeDocument) map[string]any {
	if d == nil {
		return nil
	}
	return map[string]any{
		"id": d.ID, "path": d.Path, "kind": d.Kind, "title": d.Title, "summary": d.Summary,
		"domains": d.Domains, "status": d.Status, "renamed_from": d.RenamedFrom,
		"version": d.CurrentVersion, "updated_at": d.UpdatedAt,
	}
}

func assertionJSON(a domain.KnowledgeAssertion) map[string]any {
	refs := []map[string]any{}
	for _, item := range jsonArray(a.EvidenceJSON) {
		if m, ok := item.(map[string]any); ok {
			refs = append(refs, m)
		}
	}
	return map[string]any{
		"id": a.ID, "document_id": a.DocumentID, "document_version_id": a.DocumentVersionID,
		"heading": a.Heading, "about": a.About, "perspective": a.Perspective, "basis": a.Basis,
		"statement": a.Statement, "scope": jsonObject(a.ScopeJSON), "evidence": refs,
		"unknown_notes": a.UnknownNotes,
	}
}

func assertionListJSON(list []domain.KnowledgeAssertion) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	for _, a := range list {
		out = append(out, assertionJSON(a))
	}
	return out
}

func relationListJSON(list []domain.KnowledgeAssertionRelation) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	for _, rel := range list {
		out = append(out, map[string]any{
			"id": rel.ID, "document_id": rel.DocumentID,
			"from":        map[string]any{"kind": rel.FromKind, "id": rel.FromID},
			"predicate":   rel.Predicate,
			"to":          map[string]any{"kind": rel.ToKind, "id": rel.ToID},
			"to_resolved": rel.ToResolved, "to_raw": rel.ToRaw,
			"perspective": rel.Perspective, "basis": rel.Basis, "condition": rel.Condition,
			"evidence": jsonArray(rel.EvidenceJSON),
		})
	}
	return out
}

// nonNilStrings guarantees a JSON array, never null, for an empty list.
func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// nonNilHandles guarantees a JSON array of expand handles.
func nonNilHandles(in []domain.KnowledgeExpandHandle) []domain.KnowledgeExpandHandle {
	if in == nil {
		return []domain.KnowledgeExpandHandle{}
	}
	return in
}

// jsonObject decodes stored JSON text that the contract declares as an object,
// so a client never receives null where it expects a map.
func jsonObject(raw string) map[string]any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

// jsonArray decodes stored JSON text that the contract declares as an array.
func jsonArray(raw string) []any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return []any{}
	}
	var out []any
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil || out == nil {
		return []any{}
	}
	return out
}

// jsonStrings decodes stored JSON text that the contract declares as a list of
// strings. A legacy object or scalar is flattened into one readable line
// instead of being handed to the client in a shape it cannot render.
func jsonStrings(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return []string{}
	}
	var list []string
	if err := json.Unmarshal([]byte(trimmed), &list); err == nil && list != nil {
		return list
	}
	var single any
	if err := json.Unmarshal([]byte(trimmed), &single); err != nil {
		return []string{trimmed}
	}
	encoded, err := json.Marshal(single)
	if err != nil {
		return []string{trimmed}
	}
	return []string{string(encoded)}
}

// CoverageStateForTest exposes the coverage-state classifier to the
// acceptance tests, which assert the four observable states directly.
func CoverageStateForTest(coverageJSON string, terminal bool) string {
	return coverageStateOf(coverageJSON, terminal)
}

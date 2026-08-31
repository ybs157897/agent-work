// handlers_review.go 评审面只读端点（任务控制面 RFC §9.5/§9.6）：
//
//   - GET /api/v1/workspaces/{workspace_id}/review-queue：服务端权威 Review
//     Queue 投影（total_count 是 badge 权威值；cursor 为含排序键全列的不透明
//     token，非法值 422 validation_failed）。
//   - GET /api/v1/work-items/{work_item_id}/delivery-brief：确定性交付简报
//     （服务端聚合，无 LLM 摘要）；restricted/secret classification 产物要求
//     runtime 管理权限，其余随 PermRead 可见。
//
// DTO 与 contracts/web/openapi.yaml 逐字段对齐（snake_case、水印/截断结构）。
package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/security"
)

// rfc3339NanoUTC 队列/简报时间戳格式：定宽毫秒内精度、字典序稳定（cursor 依赖）。
const rfc3339NanoUTC = time.RFC3339Nano

// ── Review Queue ─────────────────────────────────────────────────────

type reviewQueueCoordinatorDTO struct {
	Status    string `json:"status"`
	Stage     string `json:"stage,omitempty"`
	UpdatedAt string `json:"updated_at"`
	Version   int    `json:"version"`
}

type sourceWatermarkDTO struct {
	AsOfEventSeq       int64 `json:"as_of_event_seq"`
	WorkItemVersion    int   `json:"work_item_version,omitempty"`
	CoordinatorVersion int   `json:"coordinator_version,omitempty"`
	LatestRunVersion   int   `json:"latest_run_version,omitempty"`
	CommentRevision    int64 `json:"comment_revision,omitempty"`
}

type reviewQueueItemDTO struct {
	WorkItem        workItemDTO                `json:"work_item"`
	PendingSince    string                     `json:"pending_since"`
	Coordinator     *reviewQueueCoordinatorDTO `json:"coordinator"`
	LatestRunID     *string                    `json:"latest_run_id"`
	SourceWatermark sourceWatermarkDTO         `json:"source_watermark"`
}

type reviewQueueDTO struct {
	Items       []reviewQueueItemDTO `json:"items"`
	TotalCount  int                  `json:"total_count"`
	NextCursor  *string              `json:"next_cursor"`
	GeneratedAt string               `json:"generated_at"`
}

func (s *Server) handleReviewQueue(w http.ResponseWriter, r *http.Request) {
	q := application.ReviewQueueQuery{WorkspaceID: r.PathValue("workspace_id")}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeProblem(w, r, validationProblem("limit 必须是正整数"))
			return
		}
		q.Limit = n
	}
	if raw := r.URL.Query().Get("priority"); raw != "" {
		q.Priority = domain.Priority(raw)
	}
	if raw := r.URL.Query().Get("phase"); raw != "" {
		q.Phase = domain.WorkItemPhase(raw)
	}
	q.Cursor = r.URL.Query().Get("cursor")
	page, err := s.svc.ReviewQueue(r.Context(), q)
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]reviewQueueItemDTO, 0, len(page.Items))
	for i := range page.Items {
		items = append(items, toReviewQueueItemDTO(&page.Items[i]))
	}
	out := reviewQueueDTO{
		Items: items, TotalCount: page.TotalCount, GeneratedAt: page.GeneratedAt.UTC().Format(rfc3339NanoUTC),
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		out.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, out)
}

func toReviewQueueItemDTO(item *application.ReviewQueueItem) reviewQueueItemDTO {
	wiDTO := toWorkItemDTO(item.WorkItem)
	wiDTO.RunsCount = item.RunCount
	wiDTO.LatestRunID = item.LatestRunID
	out := reviewQueueItemDTO{
		WorkItem:     wiDTO,
		PendingSince: item.PendingSince.UTC().Format(rfc3339NanoUTC),
		SourceWatermark: sourceWatermarkDTO{
			AsOfEventSeq:       item.Watermark.AsOfEventSeq,
			WorkItemVersion:    item.Watermark.WorkItemVersion,
			CoordinatorVersion: item.Watermark.CoordinatorVersion,
			LatestRunVersion:   item.Watermark.LatestRunVersion,
			CommentRevision:    item.Watermark.CommentRevision,
		},
	}
	if item.Coordinator != nil {
		out.Coordinator = &reviewQueueCoordinatorDTO{
			Status: item.Coordinator.Status, Stage: item.Coordinator.Stage,
			UpdatedAt: item.Coordinator.UpdatedAt.UTC().Format(rfc3339NanoUTC),
			Version:   item.Coordinator.Version,
		}
	}
	if item.LatestRunID != "" {
		runID := item.LatestRunID
		out.LatestRunID = &runID
	}
	return out
}

// ── Delivery Brief ───────────────────────────────────────────────────

type briefConclusionDTO struct {
	CoordinatorStatus string `json:"coordinator_status"`
	Stage             string `json:"stage,omitempty"`
	Summary           string `json:"summary,omitempty"`
	NextAction        string `json:"next_action,omitempty"`
	Version           int    `json:"version"`
}

type briefFailureDTO struct {
	Code      string `json:"code,omitempty"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

type briefAttemptDTO struct {
	Attempt    int              `json:"attempt"`
	Role       string           `json:"role"`
	RunID      string           `json:"run_id"`
	AgentID    string           `json:"agent_id,omitempty"`
	AgentName  string           `json:"agent_name,omitempty"`
	Status     string           `json:"status"`
	StartedAt  *string          `json:"started_at"`
	FinishedAt *string          `json:"finished_at"`
	RetryOf    string           `json:"retry_of,omitempty"`
	Failure    *briefFailureDTO `json:"failure"`
}

type briefEvidenceDTO struct {
	ID         string `json:"id"`
	SourceKind string `json:"source_kind"`
	SourceID   string `json:"source_id"`
	Label      string `json:"label,omitempty"`
	Status     string `json:"status"`
	Trust      string `json:"trust"`
	OccurredAt string `json:"occurred_at"`
}

type briefRunEvidenceDTO struct {
	Run       runDTO             `json:"run"`
	Summary   string             `json:"summary,omitempty"`
	Evidence  []briefEvidenceDTO `json:"evidence"`
	Truncated bool               `json:"truncated"`
}

type briefFileChangeDTO struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	Status  string `json:"status"`
}

type briefChangeSetDTO struct {
	RunID        string               `json:"run_id"`
	Files        []briefFileChangeDTO `json:"files"`
	TotalFiles   int                  `json:"total_files"`
	TotalAdded   int                  `json:"total_added"`
	TotalDeleted int                  `json:"total_deleted"`
	Truncated    bool                 `json:"truncated"`
}

type briefBlockerDTO struct {
	Code      string  `json:"code"`
	Message   string  `json:"message"`
	Source    string  `json:"source"`
	RunID     *string `json:"run_id"`
	CreatedAt string  `json:"created_at"`
}

type briefRiskDTO struct {
	SourceKind string `json:"source_kind"`
	SourceID   string `json:"source_id"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Severity   string `json:"severity"`
}

type briefFreshnessDTO struct {
	GeneratedAt    string           `json:"generated_at"`
	AsOfEventSeq   int64            `json:"as_of_event_seq"`
	SourceVersions map[string]int64 `json:"source_versions,omitempty"`
	State          string           `json:"state"`
	MissingSources []string         `json:"missing_sources"`
}

type briefTruncationDTO struct {
	Attempts  bool `json:"attempts"`
	Runs      bool `json:"runs"`
	Files     bool `json:"files"`
	Artifacts bool `json:"artifacts"`
	Comments  bool `json:"comments"`
}

type deliveryBriefDTO struct {
	WorkItem           workItemDTO           `json:"work_item"`
	AcceptanceCriteria []string              `json:"acceptance_criteria"`
	Conclusion         briefConclusionDTO    `json:"conclusion"`
	Attempts           []briefAttemptDTO     `json:"attempts"`
	Runs               []briefRunEvidenceDTO `json:"runs"`
	Changes            *briefChangeSetDTO    `json:"changes"`
	Artifacts          []artifactDTO         `json:"artifacts"`
	Blocker            *briefBlockerDTO      `json:"blocker"`
	Risks              []briefRiskDTO        `json:"risks"`
	Comments           []taskCommentDTO      `json:"comments"`
	Freshness          briefFreshnessDTO     `json:"freshness"`
	Truncation         briefTruncationDTO    `json:"truncation"`
}

func (s *Server) handleDeliveryBrief(w http.ResponseWriter, r *http.Request) {
	workItemID := r.PathValue("work_item_id")
	// classification 权限过滤：restricted/secret 产物要求 runtime 管理权限。
	brief, err := s.svc.DeliveryBrief(r.Context(), workItemID, application.BriefOptions{
		IncludeRestrictedArtifacts: security.Allow(s.demoRole, security.PermRuntimeManage),
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toDeliveryBriefDTO(brief))
}

func toDeliveryBriefDTO(b *application.DeliveryBrief) deliveryBriefDTO {
	wiDTO := toWorkItemDTO(b.WorkItem)
	wiDTO.RunsCount = b.RunsTotal
	wiDTO.LatestRunID = b.LatestRunID

	attempts := make([]briefAttemptDTO, 0, len(b.Attempts))
	for _, a := range b.Attempts {
		item := briefAttemptDTO{
			Attempt: a.Attempt, Role: a.Role, RunID: a.RunID,
			AgentID: a.AgentID, AgentName: a.AgentName, Status: a.Status,
			RetryOf: a.RetryOf,
		}
		if a.StartedAt != nil {
			t := a.StartedAt.UTC().Format(rfc3339NanoUTC)
			item.StartedAt = &t
		}
		if a.FinishedAt != nil {
			t := a.FinishedAt.UTC().Format(rfc3339NanoUTC)
			item.FinishedAt = &t
		}
		if a.Failure != nil {
			item.Failure = &briefFailureDTO{Code: a.Failure.Code, Message: a.Failure.Message, Retryable: a.Failure.Retryable}
		}
		attempts = append(attempts, item)
	}

	runs := make([]briefRunEvidenceDTO, 0, len(b.Runs))
	for _, re := range b.Runs {
		item := briefRunEvidenceDTO{Run: toRunDTO(re.Run), Summary: re.Summary, Truncated: re.Truncated}
		item.Evidence = make([]briefEvidenceDTO, 0, len(re.Evidence))
		for _, ev := range re.Evidence {
			item.Evidence = append(item.Evidence, briefEvidenceDTO{
				ID: ev.ID, SourceKind: ev.SourceKind, SourceID: ev.SourceID,
				Label: ev.Label, Status: ev.Status, Trust: ev.Trust,
				OccurredAt: ev.OccurredAt.UTC().Format(rfc3339NanoUTC),
			})
		}
		runs = append(runs, item)
	}

	artifacts := make([]artifactDTO, 0, len(b.Artifacts))
	for _, a := range b.Artifacts {
		artifacts = append(artifacts, toArtifactDTO(a))
	}

	risks := make([]briefRiskDTO, 0, len(b.Risks))
	for _, risk := range b.Risks {
		risks = append(risks, briefRiskDTO{
			SourceKind: risk.SourceKind, SourceID: risk.SourceID,
			Code: risk.Code, Message: risk.Message, Severity: risk.Severity,
		})
	}

	comments := make([]taskCommentDTO, 0, len(b.Comments))
	for _, c := range b.Comments {
		comments = append(comments, toTaskCommentDTO(c))
	}

	out := deliveryBriefDTO{
		WorkItem:           wiDTO,
		AcceptanceCriteria: b.AcceptanceCriteria,
		Conclusion: briefConclusionDTO{
			CoordinatorStatus: b.Conclusion.CoordinatorStatus, Stage: b.Conclusion.Stage,
			Summary: b.Conclusion.Summary, NextAction: b.Conclusion.NextAction,
			Version: b.Conclusion.Version,
		},
		Attempts:  attempts,
		Runs:      runs,
		Artifacts: artifacts,
		Risks:     risks,
		Comments:  comments,
		Freshness: briefFreshnessDTO{
			GeneratedAt:    b.Freshness.GeneratedAt.UTC().Format(rfc3339NanoUTC),
			AsOfEventSeq:   b.Freshness.AsOfEventSeq,
			SourceVersions: b.Freshness.SourceVersions,
			State:          b.Freshness.State,
			MissingSources: b.Freshness.MissingSources,
		},
		Truncation: briefTruncationDTO{
			Attempts: b.Truncation.Attempts, Runs: b.Truncation.Runs, Files: b.Truncation.Files,
			Artifacts: b.Truncation.Artifacts, Comments: b.Truncation.Comments,
		},
	}
	if len(b.Changes) > 0 {
		latest := b.Changes[len(b.Changes)-1]
		files := make([]briefFileChangeDTO, 0, len(latest.Files))
		for _, f := range latest.Files {
			files = append(files, briefFileChangeDTO{
				Path: f.Path, Added: f.Added, Deleted: f.Deleted, Status: f.Status,
			})
		}
		out.Changes = &briefChangeSetDTO{
			RunID: latest.RunID, Files: files, TotalFiles: latest.TotalFiles,
			TotalAdded: latest.TotalAdded, TotalDeleted: latest.TotalDeleted,
			Truncated: latest.Truncated,
		}
	}
	return out
}

func validationProblem(detail string) Problem {
	return Problem{
		Type:  "https://workbench.example/problems/validation",
		Title: "Validation failed", Status: http.StatusUnprocessableEntity,
		Code: "validation_failed", Detail: detail,
	}
}

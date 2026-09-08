package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// enqueueKnowledgeRunCaptureLocked is only a durable outbox insert; model
// work and evidence parsing never run inside the Run terminal transaction.
func (s *Service) enqueueKnowledgeRunCaptureLocked(ctx context.Context, run *domain.ExecutionRun, wi *domain.WorkItem) error {
	if run == nil || !run.Status.IsTerminal() || wi == nil || !isTaskWorkItem(wi) || isKnowledgeLibrarianRun(run) || isGovernedCoordinatorRun(run) {
		return nil
	}
	if run.AgentProfileID == "" {
		return nil
	}
	agent, err := s.store.Agents().Get(ctx, run.AgentProfileID)
	if err != nil {
		return err
	}
	if agent.Kind.IsSystem() {
		return nil
	}
	cfg, err := s.store.KnowledgeJobs().GetConfig(ctx, run.WorkspaceID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !cfg.Enabled || !cfg.AutoCollect {
		return nil
	}
	return s.store.Knowledge().EnqueueRunCapture(ctx, run.WorkspaceID, run.ID)
}

// DrainKnowledgeCaptures consumes durable capture and submission inboxes.
// Restarting or replaying terminal events cannot lose or duplicate a capture.
func (s *Service) DrainKnowledgeCaptures(ctx context.Context) (int, error) {
	rows, err := s.store.Knowledge().ListPendingRunCaptures(ctx, 50)
	if err != nil {
		return 0, err
	}
	acted := 0
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return acted, err
		}
		if err := s.captureKnowledgeRun(ctx, row.WorkspaceID, row.RunID); err != nil {
			_ = s.store.Knowledge().RecordRunCaptureError(ctx, row.RunID, truncateKnowledgeRunes(err.Error(), 2000))
			continue
		}
		acted++
	}
	workspaces, err := s.store.Workspaces().ListIDs(ctx)
	if err != nil {
		return acted, err
	}
	for _, workspaceID := range workspaces {
		if err := ctx.Err(); err != nil {
			return acted, err
		}
		cfg, err := s.GetKnowledgeLibrarianConfig(ctx, workspaceID)
		if err != nil {
			return acted, err
		}
		if !cfg.Enabled || !cfg.AutoCollect {
			continue
		}
		pending, err := s.store.Knowledge().ListSubmissionsForWorkspace(ctx, workspaceID, domain.KnowledgeSubmissionReceived, 50)
		if err != nil {
			return acted, err
		}
		for _, sub := range pending {
			if sub.Request.NoChange {
				if err := s.store.Knowledge().UpdateSubmissionStatus(ctx, sub.ID, domain.KnowledgeSubmissionMerged, nil, nil, "no durable knowledge change", sub.Version); err != nil && !errors.Is(err, domain.ErrVersionConflict) {
					return acted, err
				}
				continue
			}
			if sub.RunID != "" {
				run, err := s.store.Runs().Get(ctx, sub.RunID)
				if err != nil {
					continue
				}
				if !run.Status.IsTerminal() || isKnowledgeLibrarianRun(run) {
					continue
				}
			}
			_, err = s.StartKnowledgeCuration(ctx, StartKnowledgeCurationParams{WorkspaceID: workspaceID, RequestingAgentID: cfg.LibrarianAgentID, SubmissionID: sub.ID, ClientKey: knowledgeCurationClientKey(sub.ID)})
			if err != nil {
				// A malformed candidate or unavailable runtime is visible in the
				// inbox. It must not consume model attempts on every scheduler tick.
				_ = s.store.Knowledge().UpdateSubmissionStatus(ctx, sub.ID, domain.KnowledgeSubmissionNeedsReview, sub.ResultItemIDs, sub.ResultVersionIDs, truncateKnowledgeRunes(err.Error(), 2000), sub.Version)
				continue
			}
			acted++
		}
	}
	return acted, nil
}

func (s *Service) captureKnowledgeRun(ctx context.Context, workspaceID, runID string) error {
	return s.store.InTx(ctx, func(ctx context.Context) error {
		run, err := s.store.Runs().Get(ctx, runID)
		if err != nil {
			return err
		}
		if run.WorkspaceID != workspaceID || !run.Status.IsTerminal() {
			return fmt.Errorf("%w: invalid knowledge capture source", domain.ErrStateConflict)
		}
		if isKnowledgeLibrarianRun(run) {
			return s.store.Knowledge().CompleteRunCapture(ctx, runID, "")
		}
		has, err := s.store.Knowledge().HasSubmissionForRun(ctx, workspaceID, run.AgentProfileID, runID)
		if err != nil {
			return err
		}
		if has {
			return s.store.Knowledge().CompleteRunCapture(ctx, runID, "")
		}
		text, err := s.runFinalText(ctx, runID)
		if err != nil {
			return err
		}
		req := domain.KnowledgeSubmitCandidate{WorkspaceID: workspaceID, AgentID: run.AgentProfileID, RunID: runID, WorkItemID: run.WorkItemID, ClientKey: "terminal-capture:" + runID}
		if strings.TrimSpace(text) == "" {
			artifacts, err := s.Artifacts(ctx, runID)
			if err != nil {
				return err
			}
			if len(artifacts) == 0 {
				req.NoChange = true
			} else {
				var body strings.Builder
				body.WriteString("任务产生了以下产物；此收件只保存已登记元信息，尚未读取产物正文。\n")
				var sources []domain.KnowledgeSourceInput
				for _, artifact := range artifacts {
					fmt.Fprintf(&body, "- %s (%s, %s)\n", artifact.LogicalPath, artifact.ID, artifact.Sha256)
					sources = append(sources, domain.KnowledgeSourceInput{Kind: domain.KnowledgeSourceArtifact, Ref: artifact.ID, Locator: artifact.LogicalPath, Excerpt: artifact.LogicalPath, Digest: artifact.Sha256, Metadata: map[string]any{"content_read": false, "artifact_status": string(artifact.Status)}})
				}
				req.Changes = []domain.KnowledgeChange{{Title: "待查任务产物 " + run.ID, Kind: "observation", Body: body.String(), Sources: sources}}
			}
		} else {
			changes, noChange, found, parseErr := decodeKnowledgeCapture(text)
			if found && parseErr == nil {
				req.Changes = changes
				req.NoChange = noChange
			} else {
				body := truncateKnowledgeRunes(text, 48000)
				if parseErr != nil {
					body = "知识变化声明未通过校验，以下仅为待查原始产出：\n" + body
				}
				req.Changes = []domain.KnowledgeChange{{Title: "待整理任务产出 " + run.ID, Kind: "observation", Body: body, Visibility: domain.KnowledgeVisibilityWorkspace, OwnerAgentID: run.AgentProfileID}}
			}
			for n := range req.Changes {
				if len(req.Changes[n].Sources) == 0 {
					req.Changes[n].Sources = []domain.KnowledgeSourceInput{{Kind: domain.KnowledgeSourceRun, Ref: runID, Locator: "main assistant final output", Excerpt: truncateKnowledgeRunes(text, 12000), Metadata: map[string]any{"run_status": string(run.Status), "truncated": len([]rune(text)) > 12000, "task_acceptance_not_inferred": true}}}
				}
			}
		}
		if validationErr := s.normalizeKnowledgeCandidate(ctx, &req); validationErr != nil {
			// Invalid model-supplied identities/changes become reviewable source
			// material instead of poisoning the durable capture queue forever.
			req.NoChange = false
			req.Changes = []domain.KnowledgeChange{{Title: "待核实运行产出 " + run.ID, Kind: "observation", Body: "结构化知识声明未通过验证：" + validationErr.Error() + "\n\n" + truncateKnowledgeRunes(text, 48000), OwnerAgentID: run.AgentProfileID, Visibility: domain.KnowledgeVisibilityWorkspace, Sources: []domain.KnowledgeSourceInput{{Kind: domain.KnowledgeSourceRun, Ref: run.ID, Excerpt: truncateKnowledgeRunes(text, 12000), Metadata: map[string]any{"unverified": true, "run_status": string(run.Status)}}}}}
		}
		sub := &domain.KnowledgeSubmission{ID: domain.NewID(domain.PrefixKnowledgeSubmission), WorkspaceID: workspaceID, AgentID: run.AgentProfileID, RunID: runID, WorkItemID: run.WorkItemID, ClientKey: req.ClientKey, Request: req, Status: domain.KnowledgeSubmissionReceived, Version: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		_, saved, err := s.store.Knowledge().SubmitCandidate(ctx, sub)
		if err != nil {
			return err
		}
		if req.NoChange {
			if err := s.store.Knowledge().UpdateSubmissionStatus(ctx, saved.ID, domain.KnowledgeSubmissionMerged, nil, nil, "no final output to retain", saved.Version); err != nil {
				return err
			}
		}
		return s.store.Knowledge().CompleteRunCapture(ctx, runID, saved.ID)
	})
}

func decodeKnowledgeCapture(text string) ([]domain.KnowledgeChange, bool, bool, error) {
	const fence = "```knowledge-submission/v1"
	_, rest, found := strings.Cut(text, fence)
	if !found {
		return nil, false, false, nil
	}
	raw, _, closed := strings.Cut(rest, "```")
	if !closed || len(raw) > 1<<20 {
		return nil, false, true, fmt.Errorf("invalid knowledge submission fence")
	}
	if err := rejectDuplicateJSONKeys([]byte(raw)); err != nil {
		return nil, false, true, err
	}
	var payload struct {
		Changes  []domain.KnowledgeChange `json:"changes"`
		NoChange bool                     `json:"no_change"`
	}
	d := json.NewDecoder(bytes.NewBufferString(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&payload); err != nil {
		return nil, false, true, err
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return nil, false, true, fmt.Errorf("trailing knowledge submission data")
	}
	if payload.NoChange == (len(payload.Changes) > 0) {
		return nil, false, true, fmt.Errorf("choose changes or no_change")
	}
	return payload.Changes, payload.NoChange, true, nil
}

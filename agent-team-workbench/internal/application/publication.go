package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type PublicationBaselineResolver func(context.Context, *domain.ExecutionContextSnapshot) (domain.ProjectBaseline, error)

type CreatePublicationDraftParams struct {
	ChatWorkItemID  string
	ExpectedVersion int
	Revision        int64
	ItemIDs         []string
	Title           string
	ClientKey       string
}

type PublicationDraftView struct {
	Draft *domain.TaskPublicationDraft
}

var publicationDeferCoordinatorStartKey struct{}

func (s *Service) SetPublicationBaselineResolver(resolver PublicationBaselineResolver) {
	s.publicationBaselineResolver = resolver
}

func (s *Service) CreatePublicationDraft(ctx context.Context, p CreatePublicationDraftParams) (*PublicationDraftView, bool, error) {
	wi, err := s.store.WorkItems().Get(ctx, p.ChatWorkItemID)
	if err != nil {
		return nil, false, err
	}
	if wi.RecordKind != domain.RecordKindChat {
		return nil, false, fmt.Errorf("%w: publication drafts require a Chat record", domain.ErrValidation)
	}
	if strings.TrimSpace(p.ClientKey) == "" || strings.TrimSpace(p.Title) == "" || len(p.ItemIDs) == 0 {
		return nil, false, fmt.Errorf("%w: client_key, title and item_ids are required", domain.ErrValidation)
	}
	if existing, lookupErr := s.store.TaskPublicationDrafts().GetByClientKey(ctx, wi.WorkspaceID, wi.ID, p.ClientKey); lookupErr == nil {
		fingerprint, fpErr := s.publicationRequestFingerprint(ctx, wi, p)
		if fpErr != nil || existing.Fingerprint != fingerprint {
			return nil, false, domain.ErrIdempotencyConflict
		}
		return &PublicationDraftView{Draft: existing}, true, nil
	} else if !errors.Is(lookupErr, domain.ErrNotFound) {
		return nil, false, lookupErr
	}
	view, err := s.RecheckChatAnalysis(ctx, wi.ID, p.ExpectedVersion)
	if err != nil {
		return nil, false, err
	}
	if p.ExpectedVersion < 1 || view.Projection.Version != p.ExpectedVersion {
		return nil, false, domain.ErrVersionConflict
	}
	draft, snapshot, err := s.buildPublicationDraft(ctx, wi, view, p)
	if err != nil {
		return nil, false, err
	}
	draft.ContextSnapshotID = snapshot.ID
	err = s.store.InTx(ctx, func(ctx context.Context) error {
		return s.store.TaskPublicationDrafts().Create(ctx, draft)
	})
	if err != nil {
		if errors.Is(err, domain.ErrIdempotencyConflict) {
			existing, getErr := s.store.TaskPublicationDrafts().GetByClientKey(ctx, wi.WorkspaceID, wi.ID, p.ClientKey)
			if getErr == nil && existing.Fingerprint == draft.Fingerprint {
				return &PublicationDraftView{Draft: existing}, true, nil
			}
		}
		return nil, false, err
	}
	return &PublicationDraftView{Draft: draft}, false, nil
}

func (s *Service) buildPublicationDraft(ctx context.Context, wi *domain.WorkItem, view *ChatAnalysisView,
	p CreatePublicationDraftParams) (*domain.TaskPublicationDraft, *domain.ExecutionContextSnapshot, error) {
	if view == nil || view.Projection == nil || view.Document == nil || view.Projection.Revision != p.Revision ||
		(view.Projection.Status != domain.ChatAnalysisReady && view.Projection.Status != domain.ChatAnalysisNeedsAnswer) {
		return nil, nil, fmt.Errorf("%w: analysis is not publishable", domain.ErrStateConflict)
	}
	selected := map[string]bool{}
	for _, itemID := range p.ItemIDs {
		if strings.TrimSpace(itemID) == "" || selected[itemID] {
			return nil, nil, fmt.Errorf("%w: duplicate or empty item_id", domain.ErrValidation)
		}
		selected[itemID] = true
	}
	stateByItem := map[string]*domain.ChatAnalysisDecisionState{}
	for _, state := range view.Decisions {
		stateByItem[state.ItemID] = state
	}
	itemByID := map[string]string{}
	for _, item := range view.Document.Items {
		itemByID[item.ID] = item.Detail
	}
	var descriptions []string
	var criteria []string
	var confirmationIDs []string
	var deps []any
	for _, itemID := range p.ItemIDs {
		state := stateByItem[itemID]
		if state == nil || state.Revision != p.Revision || state.Status != domain.ChatAnalysisDecisionValid || state.Outcome != domain.ChatAnalysisDecisionConfirmed {
			return nil, nil, fmt.Errorf("%w: item %s lacks a valid confirmed product decision", domain.ErrStateConflict, itemID)
		}
		if state.DecisionID != "" {
			confirmationIDs = append(confirmationIDs, state.DecisionID)
		}
		background := itemByID[itemID]
		descriptions = append(descriptions, fmt.Sprintf("结论：%s\n依据：%s\n产品版本：%s\n背景（仅供核对，不构成批准）：%s", state.Conclusion, state.Basis, state.ProductVersion, background))
		criteria = append(criteria, state.Conclusion)
		var sourceDeps []map[string]any
		_ = json.Unmarshal([]byte(state.SourceDependenciesJSON), &sourceDeps)
		for _, dep := range sourceDeps {
			deps = append(deps, dep)
		}
	}
	revision, err := s.store.ChatAnalyses().GetRevision(ctx, wi.WorkspaceID, wi.ID, p.Revision)
	if err != nil {
		return nil, nil, err
	}
	snapshot, err := s.store.ContextSnapshots().GetByRun(ctx, revision.RunID)
	if err != nil {
		return nil, nil, err
	}
	baseline, err := s.resolvePublicationBaseline(ctx, snapshot)
	if err != nil {
		return nil, nil, err
	}
	baselineJSON, _ := json.Marshal(baseline)
	itemIDsJSON, _ := json.Marshal(p.ItemIDs)
	confirmationJSON, _ := json.Marshal(confirmationIDs)
	depsJSON, _ := json.Marshal(deps)
	criteriaJSON, _ := json.Marshal(criteria)
	// Description is always regenerated from the selected human conclusions;
	// a prior draft's AI-derived body cannot survive a recheck or publish.
	description := strings.Join(descriptions, "\n\n")
	request := map[string]any{"workspace_id": wi.WorkspaceID, "chat_id": wi.ID, "revision": p.Revision,
		"item_ids": p.ItemIDs, "title": p.Title, "description": description, "criteria": criteria,
		"confirmation_ids": confirmationIDs, "source_dependencies": deps, "baseline": baseline}
	raw, _ := json.Marshal(request)
	sum := sha256.Sum256(raw)
	draft := &domain.TaskPublicationDraft{ID: domain.NewID("pubdraft_"), WorkspaceID: wi.WorkspaceID,
		ChatWorkItemID: wi.ID, AgentProfileID: wi.AgentProfileID, AnalysisRevision: p.Revision,
		ItemIDsJSON: string(itemIDsJSON), ConfirmationIDsJSON: string(confirmationJSON), SourceDependenciesJSON: string(depsJSON),
		Title: strings.TrimSpace(p.Title), Description: description, AcceptanceCriteriaJSON: string(criteriaJSON),
		BaselineJSON: string(baselineJSON), Fingerprint: hex.EncodeToString(sum[:]), Status: domain.PublicationDraftReady,
		ClientKey: p.ClientKey, Version: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	return draft, snapshot, nil
}

func (s *Service) resolvePublicationBaseline(ctx context.Context, snapshot *domain.ExecutionContextSnapshot) (domain.ProjectBaseline, error) {
	if s.publicationBaselineResolver == nil {
		return domain.ProjectBaseline{}, fmt.Errorf("%w: publication baseline resolver is not configured", domain.ErrCapabilityMissing)
	}
	baseline, err := s.publicationBaselineResolver(ctx, snapshot)
	if err != nil {
		return domain.ProjectBaseline{}, err
	}
	if err := baseline.Validate(); err != nil {
		return domain.ProjectBaseline{}, err
	}
	return baseline, nil
}

func (s *Service) ensurePublicationBaselineBeforeCoordinator(ctx context.Context, workItemID string) error {
	draft, err := s.store.TaskPublicationDrafts().GetByTaskID(ctx, workItemID)
	if errors.Is(err, domain.ErrNotFound) || draft == nil || draft.Status != domain.PublicationDraftPublished {
		return nil
	}
	if err != nil {
		return err
	}
	if s.publicationBaselineResolver == nil {
		return fmt.Errorf("%w: publication baseline resolver is not configured", domain.ErrCapabilityMissing)
	}
	snapshot, err := s.store.ContextSnapshots().Get(ctx, draft.ContextSnapshotID)
	if err != nil {
		return err
	}
	current, err := s.resolvePublicationBaseline(ctx, snapshot)
	if err != nil {
		return err
	}
	var expected domain.ProjectBaseline
	if err := json.Unmarshal([]byte(draft.BaselineJSON), &expected); err != nil {
		return fmt.Errorf("%w: publication baseline is invalid", domain.ErrStateConflict)
	}
	if domain.ComputeProjectBaselineDigest(current) != domain.ComputeProjectBaselineDigest(expected) {
		return fmt.Errorf("%w: project baseline drifted before first Coordinator Run", domain.ErrStateConflict)
	}
	return nil
}

func (s *Service) publicationRequestFingerprint(ctx context.Context, wi *domain.WorkItem, p CreatePublicationDraftParams) (string, error) {
	view, err := s.GetChatAnalysis(ctx, wi.ID)
	if err != nil {
		return "", err
	}
	draft, _, err := s.buildPublicationDraft(ctx, wi, view, p)
	if err != nil {
		return "", err
	}
	return draft.Fingerprint, nil
}

func (s *Service) ListPublicationDrafts(ctx context.Context, chatID string, limit int) ([]*domain.TaskPublicationDraft, error) {
	wi, err := s.store.WorkItems().Get(ctx, chatID)
	if err != nil {
		return nil, err
	}
	return s.store.TaskPublicationDrafts().List(ctx, wi.WorkspaceID, wi.ID, limit)
}

func (s *Service) GetPublicationDraft(ctx context.Context, chatID, draftID string) (*PublicationDraftView, error) {
	wi, err := s.store.WorkItems().Get(ctx, chatID)
	if err != nil {
		return nil, err
	}
	draft, err := s.store.TaskPublicationDrafts().Get(ctx, wi.WorkspaceID, wi.ID, draftID)
	if err != nil {
		return nil, err
	}
	return &PublicationDraftView{Draft: draft}, nil
}

func (s *Service) RecheckPublicationDraft(ctx context.Context, chatID, draftID string, expectedVersion int) (*PublicationDraftView, error) {
	view, err := s.GetPublicationDraft(ctx, chatID, draftID)
	if err != nil {
		return nil, err
	}
	if view.Draft.Status == domain.PublicationDraftPublished {
		return view, nil
	}
	// Reconstruct the server-owned request from the frozen draft; this never
	// trusts browser-supplied source or confirmation identities.
	var itemIDs []string
	if err := json.Unmarshal([]byte(view.Draft.ItemIDsJSON), &itemIDs); err != nil {
		return nil, fmt.Errorf("%w: draft item ids are invalid", domain.ErrStateConflict)
	}
	var currentItemIDs = append([]string(nil), itemIDs...)
	if expectedVersion == 0 {
		expectedVersion = view.Draft.Version
	}
	wi, err := s.store.WorkItems().Get(ctx, chatID)
	if err != nil {
		return nil, err
	}
	analysis, err := s.RecheckChatAnalysis(ctx, chatID, 0)
	if err != nil {
		return nil, err
	}
	draft, _, err := s.buildPublicationDraft(ctx, wi, analysis, CreatePublicationDraftParams{Revision: view.Draft.AnalysisRevision, ItemIDs: currentItemIDs, Title: view.Draft.Title, ClientKey: view.Draft.ClientKey})
	if err != nil {
		return nil, err
	}
	if draft.Fingerprint != view.Draft.Fingerprint {
		if updateErr := s.store.TaskPublicationDrafts().UpdateStatus(ctx, draftID, domain.PublicationDraftStale, "", expectedVersion); updateErr != nil {
			return nil, updateErr
		}
	}
	return s.GetPublicationDraft(ctx, chatID, draftID)
}

func (s *Service) PublishPublicationDraft(ctx context.Context, chatID, draftID string, expectedVersion int, clientKey string) (*domain.WorkItem, error) {
	view, err := s.GetPublicationDraft(ctx, chatID, draftID)
	if err != nil {
		return nil, err
	}
	draft := view.Draft
	if expectedVersion < 1 {
		return nil, fmt.Errorf("%w: expected_version is required", domain.ErrValidation)
	}
	if draft.Status == domain.PublicationDraftPublished && draft.TaskID != "" {
		publication, publicationErr := s.store.TaskPublicationDrafts().GetPublicationByDraft(ctx, draft.ID)
		if publicationErr == nil && (publication.ClientKey != clientKey || publication.ExpectedVersion != expectedVersion) {
			return nil, domain.ErrIdempotencyConflict
		}
		return s.store.WorkItems().Get(ctx, draft.TaskID)
	}
	if strings.TrimSpace(clientKey) == "" {
		return nil, fmt.Errorf("%w: publish client_key is required", domain.ErrValidation)
	}
	wi, err := s.store.WorkItems().Get(ctx, chatID)
	if err != nil {
		return nil, err
	}
	var itemIDs []string
	var criteria []string
	if json.Unmarshal([]byte(draft.ItemIDsJSON), &itemIDs) != nil || json.Unmarshal([]byte(draft.AcceptanceCriteriaJSON), &criteria) != nil {
		return nil, fmt.Errorf("%w: publication draft payload is invalid", domain.ErrStateConflict)
	}
	var capturedBaseline domain.ProjectBaseline
	if err := json.Unmarshal([]byte(draft.BaselineJSON), &capturedBaseline); err != nil {
		return nil, fmt.Errorf("%w: publication baseline is invalid", domain.ErrStateConflict)
	}
	if err := capturedBaseline.Validate(); err != nil {
		return nil, err
	}
	analysis, err := s.RecheckChatAnalysis(ctx, chatID, 0)
	if err != nil {
		return nil, err
	}
	current, _, err := s.buildPublicationDraft(ctx, wi, analysis, CreatePublicationDraftParams{Revision: draft.AnalysisRevision, ItemIDs: itemIDs, Title: draft.Title, ClientKey: draft.ClientKey})
	if err != nil {
		return nil, err
	}
	if current.Fingerprint != draft.Fingerprint {
		return nil, fmt.Errorf("%w: publication draft is stale", domain.ErrStateConflict)
	}
	revision, err := s.store.ChatAnalyses().GetRevision(ctx, wi.WorkspaceID, wi.ID, draft.AnalysisRevision)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.store.ContextSnapshots().GetByRun(ctx, revision.RunID)
	if err != nil {
		return nil, err
	}
	var task *domain.WorkItem
	pub := &domain.TaskPublication{ID: domain.NewID("publication_"), DraftID: draft.ID, WorkspaceID: wi.WorkspaceID, ChatWorkItemID: wi.ID, AnalysisRevision: draft.AnalysisRevision, Fingerprint: draft.Fingerprint, ClientKey: clientKey, ExpectedVersion: expectedVersion, ConfirmationIDsJSON: draft.ConfirmationIDsJSON, SourceDependenciesJSON: draft.SourceDependenciesJSON, BaselineJSON: draft.BaselineJSON, ContextSnapshotID: draft.ContextSnapshotID, CreatedAt: time.Now().UTC()}
	pubCtx := context.WithValue(ctx, publicationDeferCoordinatorStartKey, true)
	err = s.store.InTx(pubCtx, func(txctx context.Context) error {
		if draft.Version != expectedVersion {
			return domain.ErrVersionConflict
		}
		currentAnalysis, preflightErr := s.RecheckChatAnalysis(txctx, chatID, 0)
		if preflightErr != nil {
			return preflightErr
		}
		currentDraft, _, preflightErr := s.buildPublicationDraft(txctx, wi, currentAnalysis, CreatePublicationDraftParams{Revision: draft.AnalysisRevision, ItemIDs: itemIDs, Title: draft.Title, ClientKey: draft.ClientKey})
		if preflightErr != nil {
			return preflightErr
		}
		if currentDraft.Fingerprint != draft.Fingerprint {
			return fmt.Errorf("%w: publication draft changed during publish", domain.ErrStateConflict)
		}
		task, err = s.CreateWorkItem(txctx, wi.WorkspaceID, CreateWorkItemParams{Title: draft.Title, Description: draft.Description, AgentProfileID: wi.AgentProfileID, ClientKey: "publication:" + draft.ID, AutoCoordinate: true, AcceptanceCriteria: criteria})
		if err != nil {
			return err
		}
		if _, err := s.SetDevelopmentContext(txctx, task.ID, SetDevelopmentContextParams{WorkspaceLocationID: snapshot.WorkspaceLocationID, RefKind: snapshot.RefKind, BranchName: snapshot.BranchName, CheckoutRef: snapshot.CheckoutRef, WorktreeRef: snapshot.WorktreeRef, BaseRevision: capturedBaseline.Head, ExpectedVersion: task.Version}); err != nil {
			return err
		}
		pub.TaskID = task.ID
		if err := s.store.TaskPublicationDrafts().CreatePublication(txctx, pub); err != nil {
			return err
		}
		return s.store.TaskPublicationDrafts().UpdateStatus(txctx, draft.ID, domain.PublicationDraftPublished, task.ID, expectedVersion)
	})
	if err != nil {
		return nil, err
	}
	if err := s.StartCoordinator(context.WithoutCancel(ctx), task.ID); err != nil {
		// Durable queued state is the recovery authority; do not create a second Task.
		return task, nil
	}
	return task, nil
}

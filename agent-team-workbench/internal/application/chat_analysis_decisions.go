package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/chatanalysis"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

type SaveChatAnalysisDecisionParams struct {
	ChatWorkItemID  string
	ExpectedVersion int
	Revision        int64
	ItemID          string
	Outcome         domain.ChatAnalysisDecisionOutcome
	Conclusion      string
	Basis           string
	ProductVersion  string
	ClientKey       string
	// ActorID is minted by the authenticated HTTP/application boundary. It is
	// deliberately not part of the browser request DTO.
	ActorID string
}

func (s *Service) markChatAnalysisNewMaterial(ctx context.Context, wi *domain.WorkItem) error {
	return s.store.InTx(ctx, func(ctx context.Context) error {
		return s.markChatAnalysisNewMaterialLocked(ctx, wi)
	})
}

func (s *Service) markChatAnalysisNewMaterialLocked(ctx context.Context, wi *domain.WorkItem) error {
	if wi == nil {
		return nil
	}
	projection, err := s.store.ChatAnalyses().Get(ctx, wi.WorkspaceID, wi.ID)
	if errors.Is(err, domain.ErrNotFound) || projection == nil || projection.Revision < 1 {
		return nil
	}
	if err != nil {
		return err
	}
	return s.store.ChatAnalyses().MarkStale(ctx, wi.WorkspaceID, wi.ID, projection.Version, "new_materials: chat original added")
}

// SaveChatAnalysisDecision records an explicit product-facing conclusion.
// The actor is derived from the Chat's assigned Agent; no client actor,
// fingerprint, scope or timestamp is trusted.
func (s *Service) SaveChatAnalysisDecision(ctx context.Context, p SaveChatAnalysisDecisionParams) (*ChatAnalysisView, bool, error) {
	wi, err := s.store.WorkItems().Get(ctx, p.ChatWorkItemID)
	if err != nil {
		return nil, false, err
	}
	if wi.RecordKind != domain.RecordKindChat {
		return nil, false, fmt.Errorf("%w: product decisions require a Chat record", domain.ErrValidation)
	}
	if strings.TrimSpace(p.ClientKey) == "" {
		return nil, false, fmt.Errorf("%w: decision client_key is required", domain.ErrValidation)
	}
	if existing, lookupErr := s.store.ChatAnalysisDecisions().GetDecisionByClientKey(ctx, wi.WorkspaceID, wi.ID, p.ClientKey); lookupErr == nil {
		if existing.Revision != p.Revision || existing.ItemID != p.ItemID || existing.Outcome != p.Outcome ||
			existing.Conclusion != p.Conclusion || existing.Basis != p.Basis || existing.ProductVersion != p.ProductVersion {
			return nil, false, domain.ErrIdempotencyConflict
		}
		view, getErr := s.GetChatAnalysis(ctx, wi.ID)
		return view, true, getErr
	} else if !errors.Is(lookupErr, domain.ErrNotFound) {
		return nil, false, lookupErr
	}
	view, err := s.RecheckChatAnalysis(ctx, wi.ID, p.ExpectedVersion)
	if err != nil {
		return nil, false, err
	}
	if view.Document == nil || view.Projection.Revision != p.Revision ||
		(view.Projection.Status != domain.ChatAnalysisNeedsAnswer && view.Projection.Status != domain.ChatAnalysisReady) {
		return nil, false, domain.ErrVersionConflict
	}
	var item chatanalysis.Item
	found := false
	for _, candidate := range view.Document.Items {
		if candidate.ID == p.ItemID {
			item, found = candidate, true
			break
		}
	}
	if !found {
		return nil, false, fmt.Errorf("%w: analysis item %q not found", domain.ErrVersionConflict, p.ItemID)
	}
	if !p.Outcome.Valid() || strings.TrimSpace(p.Conclusion) == "" || strings.TrimSpace(p.Basis) == "" || strings.TrimSpace(p.ProductVersion) == "" {
		return nil, false, fmt.Errorf("%w: decision outcome, conclusion, basis and product_version are required", domain.ErrValidation)
	}
	fingerprint := chatanalysis.ItemFingerprint(view.Document, item)
	deps := analysisItemSourceDependencies(view.Document, item)
	actorID := strings.TrimSpace(p.ActorID)
	if actorID == "" {
		return nil, false, fmt.Errorf("%w: authenticated human actor is required", domain.ErrValidation)
	}
	now := time.Now().UTC()
	decision := &domain.ChatAnalysisDecision{
		ID: domain.NewID("cad_"), WorkspaceID: wi.WorkspaceID, ChatWorkItemID: wi.ID,
		AgentProfileID: wi.AgentProfileID, Revision: p.Revision, ItemID: p.ItemID,
		Outcome: p.Outcome, Conclusion: strings.TrimSpace(p.Conclusion), Basis: strings.TrimSpace(p.Basis),
		ProductVersion: strings.TrimSpace(p.ProductVersion), ItemFingerprint: fingerprint,
		SourceDependenciesJSON: jsonString(deps), ActorID: actorID, ClientKey: p.ClientKey, CreatedAt: now,
	}
	state := &domain.ChatAnalysisDecisionState{
		WorkspaceID: wi.WorkspaceID, ChatWorkItemID: wi.ID, ItemID: p.ItemID, Revision: p.Revision,
		DecisionID: decision.ID, Outcome: p.Outcome, Conclusion: decision.Conclusion, Basis: decision.Basis,
		ProductVersion: decision.ProductVersion, ItemFingerprint: fingerprint, SourceDependenciesJSON: jsonString(deps),
		Status: domain.ChatAnalysisDecisionValid, UpdatedAt: now,
	}
	var replayed bool
	err = s.store.InTx(ctx, func(ctx context.Context) error {
		_, _, replayed, err = s.store.ChatAnalysisDecisions().AppendDecision(ctx, decision, state, p.ExpectedVersion, p.Revision)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	view, err = s.GetChatAnalysis(ctx, wi.ID)
	return view, replayed, err
}

// RecheckChatAnalysis revalidates the current revision against its frozen
// source catalog. A source change marks the projection stale and marks every
// current product conclusion for reconfirmation; repeated checks are idempotent.
func (s *Service) RecheckChatAnalysis(ctx context.Context, chatID string, expectedVersion int) (*ChatAnalysisView, error) {
	wi, err := s.store.WorkItems().Get(ctx, chatID)
	if err != nil {
		return nil, err
	}
	view, err := s.GetChatAnalysis(ctx, chatID)
	if err != nil {
		return nil, err
	}
	if view.Document == nil || view.Projection.Revision < 1 {
		return view, nil
	}
	if view.Projection.Status == domain.ChatAnalysisAnalyzing {
		return nil, fmt.Errorf("%w: analysis is still running", domain.ErrStateConflict)
	}
	if view.Projection.Status == domain.ChatAnalysisStale && strings.HasPrefix(view.Projection.Error, "new_materials:") {
		// New material has not been included in a completed analysis revision;
		// recheck must remain a read/ack path and cannot clear this gate.
		return view, nil
	}
	if expectedVersion == 0 {
		expectedVersion = view.Projection.Version
	}
	revision, err := s.store.ChatAnalyses().GetRevision(ctx, wi.WorkspaceID, wi.ID, view.Projection.Revision)
	if err != nil {
		return nil, err
	}
	run, err := s.store.Runs().Get(ctx, revision.RunID)
	if err != nil {
		return nil, err
	}
	attempt, err := s.store.ChatAnalyses().GetAttemptByRun(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	checkErr := s.validateChatAnalysisSources(ctx, run, attempt, view.Document)
	states, stateErr := s.store.ChatAnalysisDecisions().ListDecisionStates(ctx, wi.WorkspaceID, wi.ID)
	if stateErr != nil {
		return nil, stateErr
	}
	badStates := make([]*domain.ChatAnalysisDecisionState, 0)
	for _, state := range states {
		if dependencyErr := s.validateDecisionStateDependencies(ctx, run, attempt, state); dependencyErr != nil {
			state.ReviewReason = "source_changed"
			badStates = append(badStates, state)
		}
	}
	if checkErr != nil && len(badStates) == 0 {
		// The current document itself can be unavailable even when no product
		// conclusion depended on the changed source; stale still blocks publish,
		// while unrelated decision states remain valid.
		reason := "analysis source changed or is no longer available: " + checkErr.Error()
		err = s.store.InTx(ctx, func(ctx context.Context) error {
			return s.store.ChatAnalyses().MarkStale(ctx, wi.WorkspaceID, wi.ID, expectedVersion, reason)
		})
		if err != nil {
			return nil, err
		}
		return s.GetChatAnalysis(ctx, chatID)
	}
	if checkErr != nil || len(badStates) > 0 {
		reason := "analysis source changed or is no longer available"
		err = s.store.InTx(ctx, func(ctx context.Context) error {
			if err := s.store.ChatAnalyses().MarkStale(ctx, wi.WorkspaceID, wi.ID, expectedVersion, reason); err != nil {
				return err
			}
			for _, state := range badStates {
				if state.Status == domain.ChatAnalysisDecisionNeedsReconfirmation {
					continue
				}
				state.Status = domain.ChatAnalysisDecisionNeedsReconfirmation
				state.ReviewReason = "source_changed"
				state.UpdatedAt = time.Now().UTC()
				if err := s.store.ChatAnalysisDecisions().UpsertState(ctx, state); err != nil {
					return err
				}
				if _, err := s.store.ChatAnalysisDecisions().AppendReopen(ctx, &domain.ChatAnalysisReopen{
					ID: domain.NewID("caro_"), WorkspaceID: wi.WorkspaceID, ChatWorkItemID: wi.ID,
					ItemID: state.ItemID, FromRevision: state.Revision, ToRevision: state.Revision,
					Reason: "source_changed", SourceJSON: state.SourceDependenciesJSON, CreatedAt: time.Now().UTC(),
				}); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		return s.GetChatAnalysis(ctx, chatID)
	}
	if view.Projection.Status == domain.ChatAnalysisStale {
		status := domain.ChatAnalysisReady
		if view.PendingCount > 0 {
			status = domain.ChatAnalysisNeedsAnswer
		}
		if err := s.store.InTx(ctx, func(ctx context.Context) error {
			return s.store.ChatAnalyses().MarkRechecked(ctx, wi.WorkspaceID, wi.ID, expectedVersion, status)
		}); err != nil {
			return nil, err
		}
	}
	return s.GetChatAnalysis(ctx, chatID)
}

type ChatAnalysisHistory struct {
	Revisions []*domain.ChatAnalysisRevision
	Answers   []*domain.ChatAnalysisAnswer
	Decisions []*domain.ChatAnalysisDecision
	Reopens   []*domain.ChatAnalysisReopen
}

func (s *Service) validateDecisionStateDependencies(ctx context.Context, run *domain.ExecutionRun,
	attempt *domain.ChatAnalysisAttempt, state *domain.ChatAnalysisDecisionState) error {
	if state == nil || strings.TrimSpace(state.SourceDependenciesJSON) == "" {
		return nil
	}
	var raw []map[string]any
	if err := json.Unmarshal([]byte(state.SourceDependenciesJSON), &raw); err != nil {
		return fmt.Errorf("%w: decision source dependencies are invalid", domain.ErrStateConflict)
	}
	var catalog chatAnalysisCatalog
	if err := json.Unmarshal([]byte(attempt.SourceCatalogJSON), &catalog); err != nil {
		return err
	}
	byRef := make(map[string]chatAnalysisCatalogSource, len(catalog.Sources))
	for _, source := range catalog.Sources {
		byRef[source.Kind+"\x00"+source.Ref] = source
	}
	for _, value := range raw {
		kind, _ := value["kind"].(string)
		ref, _ := value["ref"].(string)
		sha, _ := value["sha256"].(string)
		version, _ := value["version"].(float64)
		if kind == "" || ref == "" || sha == "" {
			return fmt.Errorf("%w: decision source dependency is incomplete", domain.ErrStateConflict)
		}
		if err := s.validateOneChatAnalysisSource(ctx, run, byRef, chatanalysis.Source{ID: "dependency", Kind: kind, Ref: ref, SHA256: sha, Version: int(version), ReadStatus: "read"}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ChatAnalysisHistory(ctx context.Context, chatID string, limit int) (*ChatAnalysisHistory, error) {
	wi, err := s.store.WorkItems().Get(ctx, chatID)
	if err != nil {
		return nil, err
	}
	if wi.RecordKind != domain.RecordKindChat {
		return nil, fmt.Errorf("%w: analysis history requires a Chat record", domain.ErrValidation)
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	revisions, err := s.store.ChatAnalyses().ListRevisions(ctx, wi.WorkspaceID, wi.ID, limit)
	if err != nil {
		return nil, err
	}
	decisions, err := s.store.ChatAnalysisDecisions().ListDecisions(ctx, wi.WorkspaceID, wi.ID, limit)
	if err != nil {
		return nil, err
	}
	reopens, err := s.store.ChatAnalysisDecisions().ListReopens(ctx, wi.WorkspaceID, wi.ID, limit)
	if err != nil {
		return nil, err
	}
	answers := make([]*domain.ChatAnalysisAnswer, 0)
	for _, revision := range revisions {
		items, listErr := s.store.ChatAnalyses().ListAnswers(ctx, wi.WorkspaceID, wi.ID, revision.Revision)
		if listErr != nil {
			return nil, listErr
		}
		answers = append(answers, items...)
	}
	return &ChatAnalysisHistory{Revisions: revisions, Answers: answers, Decisions: decisions, Reopens: reopens}, nil
}

func (s *Service) reconcileChatAnalysisRevision(ctx context.Context, wi *domain.WorkItem,
	previous *chatanalysis.Document, oldRevision, newRevision int64, next *chatanalysis.Document) error {
	if wi == nil || previous == nil || next == nil || oldRevision < 1 || newRevision < 1 {
		return nil
	}
	states, err := s.store.ChatAnalysisDecisions().ListDecisionStates(ctx, wi.WorkspaceID, wi.ID)
	if err != nil {
		return err
	}
	newItems := make(map[string]chatanalysis.Item, len(next.Items))
	for _, item := range next.Items {
		newItems[item.ID] = item
	}
	for _, state := range states {
		item, exists := newItems[state.ItemID]
		reason := "analysis_changed"
		unchanged := false
		if exists {
			unchanged = chatanalysis.ItemFingerprint(next, item) == state.ItemFingerprint
		} else {
			reason = "item_removed"
		}
		state.Revision = newRevision
		state.UpdatedAt = time.Now().UTC()
		if unchanged && state.Status == domain.ChatAnalysisDecisionValid {
			state.Status = domain.ChatAnalysisDecisionValid
			state.ReviewReason = ""
		} else {
			state.Status = domain.ChatAnalysisDecisionNeedsReconfirmation
			state.ReviewReason = reason
			if _, err := s.store.ChatAnalysisDecisions().AppendReopen(ctx, &domain.ChatAnalysisReopen{
				ID: domain.NewID("caro_"), WorkspaceID: wi.WorkspaceID, ChatWorkItemID: wi.ID,
				ItemID: state.ItemID, FromRevision: oldRevision, ToRevision: newRevision,
				Reason: reason, SourceJSON: state.SourceDependenciesJSON, CreatedAt: time.Now().UTC(),
			}); err != nil {
				return err
			}
		}
		if err := s.store.ChatAnalysisDecisions().UpsertState(ctx, state); err != nil {
			return err
		}
	}
	oldQuestions := make(map[string]chatanalysis.Question, len(previous.Questions))
	for _, question := range previous.Questions {
		oldQuestions[question.ID] = question
	}
	newQuestions := make(map[string]chatanalysis.Question, len(next.Questions))
	for _, question := range next.Questions {
		newQuestions[question.ID] = question
	}
	oldAnswers, err := s.store.ChatAnalyses().ListAnswers(ctx, wi.WorkspaceID, wi.ID, oldRevision)
	if err != nil {
		return err
	}
	for _, answer := range oldAnswers {
		oldQuestion, oldOK := oldQuestions[answer.QuestionID]
		newQuestion, newOK := newQuestions[answer.QuestionID]
		if !oldOK || !newOK || chatanalysis.QuestionFingerprint(previous, oldQuestion) != chatanalysis.QuestionFingerprint(next, newQuestion) {
			continue
		}
		inherited := *answer
		inherited.ID = domain.NewID("caa_")
		inherited.Revision = newRevision
		inherited.ClientKey = "inherit:" + answer.ID
		inherited.InheritedFromAnswerID = answer.ID
		inherited.LineageReason = "question_unchanged"
		inherited.CreatedAt = time.Now().UTC()
		if err := s.store.ChatAnalyses().CreateInheritedAnswer(ctx, &inherited); err != nil {
			return err
		}
	}
	return nil
}

func analysisItemSourceDependencies(doc *chatanalysis.Document, item chatanalysis.Item) []map[string]any {
	byID := make(map[string]chatanalysis.Source, len(doc.Sources))
	for _, source := range doc.Sources {
		byID[source.ID] = source
	}
	deps := make([]map[string]any, 0, len(item.SourceIDs))
	for _, sourceID := range item.SourceIDs {
		source, ok := byID[sourceID]
		if !ok {
			continue
		}
		deps = append(deps, map[string]any{"id": source.ID, "kind": source.Kind, "ref": source.Ref, "sha256": source.SHA256, "version": source.Version})
	}
	return deps
}

func jsonString(value any) string {
	b, _ := json.Marshal(value)
	return string(b)
}

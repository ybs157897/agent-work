package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// ValidateEvidenceReference validates both the reference identity and the
// current authoritative source. Evidence summaries remain annotations; the
// source status, scope and freshness are what the finish gate trusts.
func (s *Service) ValidateEvidenceReference(ctx context.Context, goalID, todoID string,
	evidence domain.GovernanceEvidenceItem) error {
	return s.store.InTx(ctx, func(txctx context.Context) error {
		return s.validateGovernanceEvidenceReferenceTx(txctx, goalID, todoID, evidence)
	})
}

func (s *Service) validateGovernanceEvidenceReferenceTx(ctx context.Context, goalID, todoID string,
	evidence domain.GovernanceEvidenceItem) error {
	if err := evidence.Validate(); err != nil {
		return err
	}
	goal, err := s.store.Goals().Get(ctx, goalID)
	if err != nil {
		return err
	}
	todo, err := s.store.Todos().Get(ctx, todoID)
	if err != nil {
		return err
	}
	if todo.GoalID != goal.ID {
		return fmt.Errorf("%w: evidence Todo is outside Goal", domain.ErrValidation)
	}
	now := time.Now().UTC()
	if evidence.RecordedAt.After(now.Add(time.Minute)) {
		return fmt.Errorf("%w: evidence recorded_at is in the future", domain.ErrValidation)
	}
	passed := evidence.Verification == domain.EvidenceVerificationPassed ||
		evidence.Verification == domain.EvidenceVerificationAccepted
	if !passed {
		// Observed/failed evidence is valid audit material, but it cannot assert
		// a successful source state or satisfy the finish gate.
		return s.validateEvidenceScopeOnly(ctx, goal, todo, evidence)
	}

	sourceTime, err := s.validateEvidenceScopeOnlyWithTime(ctx, goal, todo, evidence)
	if err != nil {
		return err
	}
	if !sourceTime.IsZero() && sourceTime.After(evidence.RecordedAt) {
		return fmt.Errorf("%w: evidence is stale after its source changed", domain.ErrStateConflict)
	}
	switch evidence.SourceKind {
	case domain.EvidenceSourceWorkItem:
		item, err := s.store.WorkItems().Get(ctx, evidence.SourceID)
		if err != nil {
			return err
		}
		if item.Status != domain.WorkItemCompleted {
			return fmt.Errorf("%w: WorkItem evidence is not completed", domain.ErrStateConflict)
		}
	case domain.EvidenceSourcePlan:
		plan, err := s.store.Plans().Get(ctx, evidence.SourceID)
		if err != nil {
			return err
		}
		if plan.Status != domain.PlanFinished {
			return fmt.Errorf("%w: Plan evidence is not finished", domain.ErrStateConflict)
		}
	case domain.EvidenceSourceRun:
		run, err := s.store.Runs().Get(ctx, evidence.SourceID)
		if err != nil {
			return err
		}
		if !run.Status.IsTerminal() || run.Status != domain.RunSucceeded {
			return fmt.Errorf("%w: Run evidence is not a succeeded terminal Run", domain.ErrStateConflict)
		}
	case domain.EvidenceSourceArtifact:
		artifact, err := s.store.Runs().GetArtifact(ctx, evidence.SourceID)
		if err != nil {
			return err
		}
		if artifact.Status != domain.ArtifactAccepted {
			return fmt.Errorf("%w: Artifact evidence is not accepted", domain.ErrStateConflict)
		}
	case domain.EvidenceSourceApproval:
		approval, err := s.store.Runs().GetApproval(ctx, evidence.SourceID)
		if err != nil {
			return err
		}
		if approval.Status != domain.ApprovalApproved {
			return fmt.Errorf("%w: Approval evidence is not approved", domain.ErrStateConflict)
		}
	case domain.EvidenceSourceValidationResult:
		result, err := s.store.ValidationResults().Get(ctx, evidence.SourceID)
		if err != nil {
			return err
		}
		if result.Status != domain.ValidationResultPassed {
			return fmt.Errorf("%w: validation result is not passed", domain.ErrStateConflict)
		}
	case domain.EvidenceSourceDeliveryBrief:
		snapshot, err := s.store.DeliveryBriefSnapshots().Get(ctx, evidence.SourceID)
		if err != nil {
			return err
		}
		if snapshot.GoalID != goal.ID || snapshot.TodoID != todo.ID {
			return fmt.Errorf("%w: Delivery Brief snapshot governance lineage mismatch", domain.ErrValidation)
		}
		if snapshot.FreshnessState != "current" {
			return fmt.Errorf("%w: Delivery Brief snapshot is partial and cannot satisfy passed evidence", domain.ErrStateConflict)
		}
		current, err := s.DeliveryBrief(ctx, snapshot.WorkItemID, BriefOptions{IncludeRestrictedArtifacts: true})
		if err != nil {
			return fmt.Errorf("%w: Delivery Brief snapshot source is unreadable", domain.ErrStateConflict)
		}
		currentGoal, err := s.store.Goals().Get(ctx, snapshot.GoalID)
		if err != nil {
			return fmt.Errorf("%w: Delivery Brief snapshot Goal source is unreadable", domain.ErrStateConflict)
		}
		currentTodo, err := s.store.Todos().Get(ctx, snapshot.TodoID)
		if err != nil {
			return fmt.Errorf("%w: Delivery Brief snapshot Todo source is unreadable", domain.ErrStateConflict)
		}
		if current.Freshness.State != "current" || len(current.Freshness.MissingSources) != 0 {
			return fmt.Errorf("%w: current Delivery Brief sources are partial", domain.ErrStateConflict)
		}
		currentSources := cloneInt64Map(current.Freshness.SourceVersions)
		currentSources["goal"] = int64(currentGoal.Version)
		currentSources["todo"] = int64(currentTodo.Version)
		current.Freshness.SourceVersions = currentSources
		// Workspace MAX(stream_seq) is a monotonic observation watermark, not
		// the identity of this WorkItem's read model. Unrelated events may move
		// it forward without invalidating the capture; a backwards watermark is
		// always corrupt. Source versions and the closed payload below decide
		// whether this Brief's authoritative facts changed.
		if current.Freshness.AsOfEventSeq < snapshot.AsOfEventSeq ||
			!sameDeliveryBriefSourceVersions(snapshot.SourceVersions, current.Freshness.SourceVersions) {
			return fmt.Errorf("%w: Delivery Brief snapshot is stale", domain.ErrStateConflict)
		}
		currentPayload, err := canonicalDeliveryBriefSnapshotPayload(current)
		if err != nil {
			return fmt.Errorf("%w: canonical Delivery Brief snapshot source is unreadable", domain.ErrStateConflict)
		}
		currentProbe := &domain.DeliveryBriefSnapshot{
			ID:             snapshot.ID,
			SchemaVersion:  snapshot.SchemaVersion,
			GoalID:         snapshot.GoalID,
			TodoID:         snapshot.TodoID,
			WorkItemID:     snapshot.WorkItemID,
			SnapshotJSON:   currentPayload,
			AsOfEventSeq:   current.Freshness.AsOfEventSeq,
			SourceVersions: currentSources,
			FreshnessState: current.Freshness.State,
			CreatedAt:      snapshot.CreatedAt,
			ClientKey:      snapshot.ClientKey,
		}
		if err := currentProbe.Seal(); err != nil || currentProbe.SnapshotJSON != snapshot.SnapshotJSON {
			return fmt.Errorf("%w: Delivery Brief snapshot content is stale", domain.ErrStateConflict)
		}
	}
	return nil
}

func (s *Service) validateEvidenceScopeOnly(ctx context.Context, goal *domain.Goal, todo *domain.Todo,
	evidence domain.GovernanceEvidenceItem) error {
	_, err := s.validateEvidenceScopeOnlyWithTime(ctx, goal, todo, evidence)
	return err
}

func (s *Service) validateEvidenceScopeOnlyWithTime(ctx context.Context, goal *domain.Goal, todo *domain.Todo,
	evidence domain.GovernanceEvidenceItem) (time.Time, error) {
	if goal == nil || todo == nil {
		return time.Time{}, fmt.Errorf("%w: evidence Goal/Todo required", domain.ErrValidation)
	}
	if goal.ID != todo.GoalID {
		return time.Time{}, fmt.Errorf("%w: evidence Goal/Todo mismatch", domain.ErrValidation)
	}
	inScopeWorkItem := func(id string) bool { return s.workItemInGoalTree(ctx, goal, todo, id) }
	switch evidence.SourceKind {
	case domain.EvidenceSourceWorkItem:
		item, err := s.store.WorkItems().Get(ctx, evidence.SourceID)
		if err != nil {
			return time.Time{}, err
		}
		if item.WorkspaceID != goal.WorkspaceID || !inScopeWorkItem(item.ID) {
			return time.Time{}, fmt.Errorf("%w: WorkItem evidence is outside Goal/Todo scope", domain.ErrValidation)
		}
		return item.UpdatedAt, nil
	case domain.EvidenceSourcePlan:
		plan, err := s.store.Plans().Get(ctx, evidence.SourceID)
		if err != nil {
			return time.Time{}, err
		}
		if plan.WorkspaceID != goal.WorkspaceID || !inScopeWorkItem(plan.WorkItemID) {
			return time.Time{}, fmt.Errorf("%w: Plan evidence is outside Goal/Todo scope", domain.ErrValidation)
		}
		if plan.GovernanceTurnKey != nil && (plan.GovernanceTurnKey.GoalID != goal.ID || plan.GovernanceTurnKey.TodoID != todo.ID) {
			return time.Time{}, fmt.Errorf("%w: Plan evidence turn lineage mismatch", domain.ErrValidation)
		}
		return plan.UpdatedAt, nil
	case domain.EvidenceSourceRun:
		run, err := s.store.Runs().Get(ctx, evidence.SourceID)
		if err != nil {
			return time.Time{}, err
		}
		if run.WorkspaceID != goal.WorkspaceID || !inScopeWorkItem(run.WorkItemID) {
			return time.Time{}, fmt.Errorf("%w: Run evidence is outside Goal/Todo scope", domain.ErrValidation)
		}
		if key, ok := runGovernanceTurnKey(run); ok && (key.GoalID != goal.ID || key.TodoID != todo.ID) {
			return time.Time{}, fmt.Errorf("%w: Run evidence turn lineage mismatch", domain.ErrValidation)
		}
		return run.UpdatedAt, nil
	case domain.EvidenceSourceArtifact:
		artifact, err := s.store.Runs().GetArtifact(ctx, evidence.SourceID)
		if err != nil {
			return time.Time{}, err
		}
		run, err := s.store.Runs().Get(ctx, artifact.RunID)
		if err != nil {
			return time.Time{}, err
		}
		if run.WorkspaceID != goal.WorkspaceID || !inScopeWorkItem(run.WorkItemID) {
			return time.Time{}, fmt.Errorf("%w: Artifact evidence is outside Goal/Todo scope", domain.ErrValidation)
		}
		if key, ok := runGovernanceTurnKey(run); ok && (key.GoalID != goal.ID || key.TodoID != todo.ID) {
			return time.Time{}, fmt.Errorf("%w: Artifact evidence turn lineage mismatch", domain.ErrValidation)
		}
		return artifact.CreatedAt, nil
	case domain.EvidenceSourceApproval:
		approval, err := s.store.Runs().GetApproval(ctx, evidence.SourceID)
		if err != nil {
			return time.Time{}, err
		}
		item, err := s.store.WorkItems().Get(ctx, approval.WorkItemID)
		if err != nil {
			return time.Time{}, err
		}
		if item.WorkspaceID != goal.WorkspaceID || !inScopeWorkItem(item.ID) {
			return time.Time{}, fmt.Errorf("%w: Approval evidence is outside Goal/Todo scope", domain.ErrValidation)
		}
		if approval.RunID != "" {
			run, err := s.store.Runs().Get(ctx, approval.RunID)
			if err != nil {
				return time.Time{}, err
			}
			if run.WorkspaceID != goal.WorkspaceID || !inScopeWorkItem(run.WorkItemID) {
				return time.Time{}, fmt.Errorf("%w: Approval source Run is outside Goal/Todo scope", domain.ErrValidation)
			}
			if key, ok := runGovernanceTurnKey(run); ok && (key.GoalID != goal.ID || key.TodoID != todo.ID) {
				return time.Time{}, fmt.Errorf("%w: Approval evidence turn lineage mismatch", domain.ErrValidation)
			}
		}
		if approval.ResolvedAt != nil {
			return *approval.ResolvedAt, nil
		}
		return time.Time{}, nil
	case domain.EvidenceSourceValidationResult:
		result, err := s.store.ValidationResults().Get(ctx, evidence.SourceID)
		if err != nil {
			return time.Time{}, err
		}
		if result.GoalID != goal.ID || result.TodoID != todo.ID || !inScopeWorkItem(result.WorkItemID) {
			return time.Time{}, fmt.Errorf("%w: validation result lineage mismatch", domain.ErrValidation)
		}
		return result.RecordedAt, nil
	case domain.EvidenceSourceDeliveryBrief:
		snapshot, err := s.store.DeliveryBriefSnapshots().Get(ctx, evidence.SourceID)
		if err != nil {
			return time.Time{}, err
		}
		if snapshot.GoalID != goal.ID || snapshot.TodoID != todo.ID {
			return time.Time{}, fmt.Errorf("%w: Delivery Brief snapshot governance lineage mismatch", domain.ErrValidation)
		}
		if err := s.validateDeliveryBriefSnapshotScope(ctx, snapshot); err != nil {
			return time.Time{}, err
		}
		if !inScopeWorkItem(snapshot.WorkItemID) {
			return time.Time{}, fmt.Errorf("%w: Delivery Brief snapshot WorkItem is outside Goal/Todo scope", domain.ErrValidation)
		}
		return snapshot.CreatedAt, nil
	default:
		return time.Time{}, fmt.Errorf("%w: unsupported evidence kind %q", domain.ErrValidation, evidence.SourceKind)
	}
}

func sameDeliveryBriefSourceVersions(left, right map[string]int64) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		other, ok := right[key]
		if !ok || other != value {
			return false
		}
	}
	return true
}

// workItemInGoalTree admits the root and its task descendants only after the
// root/workspace boundary is proved. DecisionScope still controls writes; the
// tree check merely lets authoritative child Plan/Run/Artifact evidence be
// observed without granting an arbitrary same-workspace child.
func (s *Service) workItemInGoalTree(ctx context.Context, goal *domain.Goal, todo *domain.Todo, wanted string) bool {
	if goal == nil || todo == nil || wanted == "" {
		return false
	}
	if wanted == goal.RootWorkItemID {
		return true
	}
	seen := map[string]bool{goal.RootWorkItemID: true}
	frontier := []string{goal.RootWorkItemID}
	for len(frontier) > 0 {
		parentID := frontier[0]
		frontier = frontier[1:]
		children, err := s.store.WorkItems().ListByParent(ctx, parentID)
		if err != nil {
			return false
		}
		for _, child := range children {
			if child == nil || child.WorkspaceID != goal.WorkspaceID || !isTaskWorkItem(child) || seen[child.ID] {
				continue
			}
			seen[child.ID] = true
			if child.ID == wanted {
				return true
			}
			frontier = append(frontier, child.ID)
		}
	}
	return false
}

// ValidateEvidenceForFinish requires positive, current references. It does
// not evaluate free-form summaries and never treats a model verdict as proof.
func (s *Service) ValidateEvidenceForFinish(ctx context.Context, goalID, todoID string,
	evidence []domain.GovernanceEvidenceItem) error {
	positive := make([]domain.GovernanceEvidenceItem, 0, len(evidence))
	for _, item := range evidence {
		if item.Verification == domain.EvidenceVerificationPassed || item.Verification == domain.EvidenceVerificationAccepted {
			positive = append(positive, item)
		}
	}
	if len(positive) == 0 {
		return fmt.Errorf("%w: finish requires evidence", domain.ErrValidation)
	}
	for _, item := range positive {
		if err := s.ValidateEvidenceReference(ctx, goalID, todoID, item); err != nil {
			return err
		}
	}
	return nil
}

// RecordValidationResult creates the canonical source allowed by
// EvidenceSourceValidationResult. A model-reported verdict is deliberately
// not accepted as ProducedBy.
func (s *Service) RecordValidationResult(ctx context.Context, result *domain.ValidationResult) (*domain.ValidationResult, error) {
	if result == nil {
		return nil, fmt.Errorf("%w: validation result required", domain.ErrValidation)
	}
	working := *result
	if working.ID == "" {
		working.ID = domain.NewID(domain.PrefixValidationResult)
	}
	if working.ProducedBy != "control_plane" {
		return nil, fmt.Errorf("%w: validation result must be produced by the control plane evaluation hook", domain.ErrValidation)
	}
	var stored *domain.ValidationResult
	created := false
	err := s.store.InTx(ctx, func(txctx context.Context) error {
		goal, err := s.store.Goals().Get(txctx, working.GoalID)
		if err != nil {
			return err
		}
		todo, err := s.store.Todos().Get(txctx, working.TodoID)
		if err != nil {
			return err
		}
		if todo.GoalID != goal.ID {
			return fmt.Errorf("%w: validation result Todo is outside Goal", domain.ErrValidation)
		}
		run, err := s.store.Runs().Get(txctx, working.SourceRunID)
		if err != nil {
			return err
		}
		if working.WorkItemID == "" {
			working.WorkItemID = run.WorkItemID
		}
		if !run.Status.IsTerminal() || run.WorkItemID != working.WorkItemID || run.WorkspaceID != goal.WorkspaceID {
			return fmt.Errorf("%w: validation result source Run is not terminal/in scope", domain.ErrStateConflict)
		}
		if eval, _ := run.Input["evaluation"].(bool); !eval {
			return fmt.Errorf("%w: only an evaluation Run can produce a validation result", domain.ErrValidation)
		}
		key, ok := runGovernanceTurnKey(run)
		if !ok || key.GoalID != goal.ID || key.TodoID != todo.ID {
			return fmt.Errorf("%w: validation result requires exact governed evaluation Turn lineage", domain.ErrValidation)
		}
		text, textErr := s.runFinalText(txctx, run.ID)
		if textErr != nil {
			return fmt.Errorf("%w: validation verdict source is unreadable", domain.ErrValidation)
		}
		block, found := extractLastFencedBlock(text, "verdict")
		if !found {
			return fmt.Errorf("%w: validation verdict block is missing", domain.ErrValidation)
		}
		pass, _, verdictErr := parseVerdict(block)
		if verdictErr != nil {
			return fmt.Errorf("%w: validation verdict is invalid", domain.ErrValidation)
		}
		wantStatus := domain.ValidationResultFailed
		if pass {
			wantStatus = domain.ValidationResultPassed
		}
		if working.Status != wantStatus {
			return fmt.Errorf("%w: validation result status disagrees with canonical verdict", domain.ErrIdempotencyConflict)
		}
		if working.CriteriaDigest == "" {
			working.CriteriaDigest, err = canonicalGovernancePlanDigest(goal.AcceptanceContract)
			if err != nil {
				return err
			}
		}
		want, err := canonicalGovernancePlanDigest(goal.AcceptanceContract)
		if err != nil {
			return err
		}
		if working.CriteriaDigest != want {
			return fmt.Errorf("%w: validation result criteria digest mismatch", domain.ErrValidation)
		}
		if working.Status == domain.ValidationResultPassed && run.Status != domain.RunSucceeded {
			return fmt.Errorf("%w: passed validation requires succeeded source Run", domain.ErrStateConflict)
		}
		if existing, getErr := s.store.ValidationResults().GetBySourceRun(txctx, working.SourceRunID); getErr == nil {
			if !validationResultSemanticEqual(existing, &working) {
				return domain.ErrIdempotencyConflict
			}
			stored = existing
			return nil
		} else if !errors.Is(getErr, domain.ErrNotFound) {
			return getErr
		}
		if existing, getErr := s.store.ValidationResults().Get(txctx, working.ID); getErr == nil {
			if !validationResultEqual(existing, &working) {
				return domain.ErrIdempotencyConflict
			}
			stored = existing
			return nil
		} else if !errors.Is(getErr, domain.ErrNotFound) {
			return getErr
		}
		if err := s.store.ValidationResults().Create(txctx, &working); err != nil {
			return err
		}
		stored = &working
		created = true
		if err := s.emit(txctx, goal.WorkspaceID, domain.EventValidationRecorded,
			domain.AggregateValidationResult, working.ID, working.Version, nil,
			map[string]any{
				"goal_id": working.GoalID, "todo_id": working.TodoID,
				"validation_result_id": working.ID, "source_run_id": working.SourceRunID,
				"status": string(working.Status), "produced_by": working.ProducedBy,
				"criteria_digest": working.CriteriaDigest, "summary": working.Summary,
				"recorded_at": working.RecordedAt.UTC().Format(time.RFC3339Nano),
			}); err != nil {
			return err
		}
		return s.activityFor(txctx, goal.WorkspaceID, goal.RootWorkItemID, "validation.result_recorded",
			fmt.Sprintf("validation result %s recorded", working.ID))
	})
	if err != nil {
		return nil, err
	}
	if created && s.notifier != nil {
		if goal, goalErr := s.store.Goals().Get(ctx, stored.GoalID); goalErr == nil {
			s.notifier.Notify(goal.WorkspaceID)
		}
	}
	return stored, nil
}

func validationResultEqual(a, b *domain.ValidationResult) bool {
	if a == nil || b == nil {
		return false
	}
	return a.ID == b.ID && a.GoalID == b.GoalID && a.TodoID == b.TodoID &&
		a.WorkItemID == b.WorkItemID && a.SourceRunID == b.SourceRunID &&
		a.CriteriaDigest == b.CriteriaDigest && a.Status == b.Status &&
		a.Summary == b.Summary && a.ProducedBy == b.ProducedBy &&
		a.RecordedAt.Equal(b.RecordedAt)
}

func validationResultSemanticEqual(a, b *domain.ValidationResult) bool {
	if a == nil || b == nil {
		return false
	}
	return a.GoalID == b.GoalID && a.TodoID == b.TodoID && a.WorkItemID == b.WorkItemID &&
		a.SourceRunID == b.SourceRunID && a.CriteriaDigest == b.CriteriaDigest &&
		a.Status == b.Status && a.Summary == b.Summary && a.ProducedBy == b.ProducedBy
}

package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// prepareKnowledgeSubmissionLocked materializes the librarian's revision into
// immutable candidates. It runs in the same transaction as its finish receipt;
// the original producer envelope remains untouched and is never auto-published.
func (s *Service) prepareKnowledgeSubmissionLocked(ctx context.Context, submission *domain.KnowledgeSubmission) (*domain.KnowledgeSubmission, error) {
	if submission == nil {
		return nil, fmt.Errorf("%w: submission required", domain.ErrValidation)
	}
	if len(submission.ResultVersionIDs) > 0 || submission.Status == domain.KnowledgeSubmissionMerged || submission.Status == domain.KnowledgeSubmissionAccepted {
		return submission, nil
	}
	request := submission.Request
	if err := s.normalizeKnowledgeCandidate(ctx, &request); err != nil {
		return nil, err
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if request.NoChange {
		if err := s.store.Knowledge().UpdateSubmissionStatus(ctx, submission.ID, domain.KnowledgeSubmissionMerged, nil, nil, "no durable knowledge change", submission.Version); err != nil {
			return nil, err
		}
		return s.store.Knowledge().GetSubmission(ctx, submission.WorkspaceID, submission.AgentID, submission.ID)
	}
	now := time.Now().UTC()
	items := make([]*domain.KnowledgeItem, len(request.Changes))
	for n, change := range request.Changes {
		if len(change.Sources) == 0 {
			return nil, fmt.Errorf("%w: change %d has no evidence", domain.ErrValidation, n)
		}
		if change.ItemID != "" {
			item, err := s.store.Knowledge().GetItem(ctx, request.WorkspaceID, change.OwnerAgentID, change.ItemID)
			if err != nil {
				return nil, err
			}
			if item.CurrentVersion != change.BaseVersion {
				return nil, domain.ErrVersionConflict
			}
			if item.Visibility != change.Visibility || item.OwnerAgentID != change.OwnerAgentID {
				return nil, fmt.Errorf("%w: revision cannot change ownership or visibility", domain.ErrValidation)
			}
			items[n] = item
		} else {
			if change.BaseVersion != 0 {
				return nil, domain.ErrVersionConflict
			}
			item := &domain.KnowledgeItem{ID: domain.NewID(domain.PrefixKnowledgeItem), WorkspaceID: request.WorkspaceID, OwnerAgentID: change.OwnerAgentID, Visibility: change.Visibility, Kind: change.Kind, Title: change.Title, Summary: change.Summary, Tags: change.Tags, Aliases: change.Aliases, Scope: change.Scope, Status: domain.KnowledgeStatusCandidate, Version: 1, CreatedAt: now, UpdatedAt: now}
			if err := s.store.Knowledge().CreateItem(ctx, item); err != nil {
				return nil, err
			}
			items[n] = item
		}
	}
	resolveItem := func(ref string, current int) (string, error) {
		if ref == "" {
			return items[current].ID, nil
		}
		if strings.HasPrefix(ref, "@change:") {
			n, err := strconv.Atoi(strings.TrimPrefix(ref, "@change:"))
			if err != nil || n < 0 || n >= len(items) {
				return "", fmt.Errorf("%w: invalid local relationship reference %q", domain.ErrValidation, ref)
			}
			return items[n].ID, nil
		}
		if _, err := s.store.Knowledge().GetItem(ctx, request.WorkspaceID, request.AgentID, ref); err != nil {
			return "", err
		}
		return ref, nil
	}
	var itemIDs, versionIDs []string
	for n, change := range request.Changes {
		item := items[n]
		version := &domain.KnowledgeVersion{ID: domain.NewID(domain.PrefixKnowledgeVersion), ItemID: item.ID, BaseVersion: change.BaseVersion, Status: domain.KnowledgeStatusDraft, Kind: change.Kind, Title: change.Title, Summary: change.Summary, BodyMarkdown: change.Body, Tags: change.Tags, Aliases: change.Aliases, Scope: change.Scope, CreatedByAgentID: request.AgentID, CreatedByRunID: request.RunID, CreatedByWorkItemID: request.WorkItemID, CreatedAt: now}
		sources := make([]*domain.KnowledgeSource, 0, len(change.Sources))
		sourceIDs := make([]string, 0, len(change.Sources))
		for _, input := range change.Sources {
			if strings.TrimSpace(input.Excerpt) == "" && strings.TrimSpace(input.Digest) == "" {
				return nil, fmt.Errorf("%w: evidence requires a fixed excerpt or digest", domain.ErrValidation)
			}
			source, err := s.validateKnowledgePublicationSource(ctx, request, input)
			if err != nil {
				return nil, err
			}
			sources = append(sources, source)
			sourceIDs = append(sourceIDs, source.ID)
		}
		relations := make([]*domain.KnowledgeRelation, 0, len(change.Relations))
		for _, input := range change.Relations {
			from, err := resolveItem(input.FromItemID, n)
			if err != nil {
				return nil, err
			}
			to, err := resolveItem(input.ToItemID, n)
			if err != nil {
				return nil, err
			}
			if from != item.ID && to != item.ID {
				return nil, fmt.Errorf("%w: relationship must involve its source knowledge item", domain.ErrValidation)
			}
			// The exact submitted source set is captured with this version; a
			// model cannot attach an unrelated source ID to forge evidence.
			relations = append(relations, &domain.KnowledgeRelation{ID: domain.NewID(domain.PrefixKnowledgeRelation), WorkspaceID: request.WorkspaceID, SourceVersionID: version.ID, FromItemID: from, ToItemID: to, Kind: input.Kind, Condition: input.Condition, Rationale: input.Rationale, SourceIDs: append([]string(nil), sourceIDs...), CreatedAt: now})
		}
		if err := s.store.Knowledge().CreateVersionBundle(ctx, version, sources, relations); err != nil {
			return nil, err
		}
		itemIDs = append(itemIDs, item.ID)
		versionIDs = append(versionIDs, version.ID)
	}
	if err := s.store.Knowledge().UpdateSubmissionStatus(ctx, submission.ID, domain.KnowledgeSubmissionNeedsReview, itemIDs, versionIDs, "", submission.Version); err != nil {
		return nil, err
	}
	return s.store.Knowledge().GetSubmission(ctx, submission.WorkspaceID, submission.AgentID, submission.ID)
}

func (s *Service) validateKnowledgePublicationSource(ctx context.Context, request domain.KnowledgeSubmitCandidate, input domain.KnowledgeSourceInput) (*domain.KnowledgeSource, error) {
	metadata := maps.Clone(input.Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["verification"] = "submitted_excerpt"
	metadata["digest_verified"] = false
	source := &domain.KnowledgeSource{ID: domain.NewID(domain.PrefixKnowledgeSource), WorkspaceID: request.WorkspaceID, SubmittedByAgentID: request.AgentID, Kind: input.Kind, Ref: input.Ref, Locator: input.Locator, Excerpt: input.Excerpt, Digest: input.Digest, Metadata: metadata, CreatedAt: time.Now().UTC()}
	checkRun := func(id string) (*domain.ExecutionRun, error) {
		run, err := s.store.Runs().Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if run.WorkspaceID != request.WorkspaceID {
			return nil, domain.ErrNotFound
		}
		if isKnowledgeLibrarianRun(run) {
			return nil, fmt.Errorf("%w: librarian output cannot be its own primary knowledge evidence", domain.ErrValidation)
		}
		if !run.Status.IsTerminal() {
			return nil, fmt.Errorf("%w: knowledge evidence requires a terminal source Run", domain.ErrStateConflict)
		}
		return run, nil
	}
	switch input.Kind {
	case domain.KnowledgeSourceRun:
		run, err := checkRun(input.Ref)
		if err != nil {
			return nil, err
		}
		text, err := s.runFinalText(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		if input.Excerpt != "" && !strings.Contains(strings.Join(strings.Fields(text), " "), strings.Join(strings.Fields(input.Excerpt), " ")) {
			return nil, fmt.Errorf("%w: submitted excerpt is absent from source Run", domain.ErrValidation)
		}
		sum := sha256.Sum256([]byte(text))
		digest := "sha256:" + hex.EncodeToString(sum[:])
		if input.Digest != "" && strings.TrimPrefix(input.Digest, "sha256:") != hex.EncodeToString(sum[:]) {
			return nil, fmt.Errorf("%w: source Run digest mismatch", domain.ErrValidation)
		}
		source.Digest = digest
		metadata["digest_verified"] = true
		metadata["verification"] = "run_output_verified"
		metadata["run_status"] = string(run.Status)
	case domain.KnowledgeSourceArtifact:
		artifact, err := s.Artifact(ctx, input.Ref)
		if err != nil {
			return nil, err
		}
		if _, err = checkRun(artifact.RunID); err != nil {
			return nil, err
		}
		if input.Digest != "" && strings.TrimPrefix(input.Digest, "sha256:") != artifact.Sha256 {
			return nil, fmt.Errorf("%w: artifact digest mismatch", domain.ErrValidation)
		}
		source.Digest = "sha256:" + artifact.Sha256
		metadata["digest_verified"] = true
		metadata["verification"] = "artifact_manifest_verified"
		metadata["content_read"] = false
		metadata["artifact_status"] = string(artifact.Status)
	case domain.KnowledgeSourceWorkItem:
		item, err := s.store.WorkItems().Get(ctx, input.Ref)
		if err != nil {
			return nil, err
		}
		if item.WorkspaceID != request.WorkspaceID {
			return nil, domain.ErrNotFound
		}
		metadata["verification"] = "work_item_reference_verified"
		metadata["work_item_version"] = item.Version
	case domain.KnowledgeSourceAgent:
		agent, err := s.store.Agents().Get(ctx, input.Ref)
		if err != nil {
			return nil, err
		}
		if agent.WorkspaceID != request.WorkspaceID {
			return nil, domain.ErrNotFound
		}
		metadata["verification"] = "agent_identity_only"
	case domain.KnowledgeSourceCode, domain.KnowledgeSourceTest:
		metadata["verification"] = "submitted_excerpt_not_repository_verified"
		metadata["repository_read"] = false
	case domain.KnowledgeSourceDocument, domain.KnowledgeSourceUser:
		// Explicit imports retain their quoted source. No arbitrary network or
		// filesystem read is implied by a model-supplied document reference.
	default:
		return nil, fmt.Errorf("%w: unsupported knowledge source", domain.ErrValidation)
	}
	return source, nil
}

func (s *Service) knowledgePreparedVersion(ctx context.Context, submission *domain.KnowledgeSubmission, versionID string) (*domain.KnowledgeVersion, string, error) {
	owners := []string{submission.AgentID}
	for _, c := range submission.Request.Changes {
		if c.OwnerAgentID != "" {
			owners = append(owners, c.OwnerAgentID)
		}
	}
	for _, owner := range owners {
		version, err := s.store.Knowledge().GetVersion(ctx, submission.WorkspaceID, owner, versionID)
		if err == nil {
			return version, owner, nil
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return nil, "", err
		}
	}
	return nil, "", domain.ErrNotFound
}

// PublishKnowledgeSubmission is an explicit management command. Receiving a
// Run's text alone can never create an effective knowledge version.
func (s *Service) PublishKnowledgeSubmission(ctx context.Context, workspaceID, actorAgentID, submissionID string) (*domain.KnowledgeSubmission, error) {
	var out *domain.KnowledgeSubmission
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		var err error
		out, err = s.publishKnowledgeSubmissionLocked(ctx, workspaceID, actorAgentID, submissionID,
			map[string]any{"kind": "user", "id": "user_demo", "knowledge_owner_agent_id": actorAgentID})
		return err
	})
	if err == nil && s.notifier != nil {
		s.notifier.Notify(workspaceID)
	}
	return out, err
}

// publishKnowledgeSubmissionLocked is shared by explicit management publish
// and the narrow Agent-confirmed requirement path. The caller owns the
// transaction; all version, evidence, ownership and manager authorization
// checks remain exactly the same for both paths.
func (s *Service) publishKnowledgeSubmissionLocked(ctx context.Context, workspaceID, actorAgentID, submissionID string, auditActor map[string]any) (*domain.KnowledgeSubmission, error) {
	sub, err := s.GetKnowledgeSubmission(ctx, workspaceID, actorAgentID, submissionID)
	if err != nil {
		return nil, err
	}
	if actorAgentID != sub.AgentID {
		if _, err = s.knowledgeManager(ctx, workspaceID, actorAgentID); err != nil {
			return nil, err
		}
	}
	if sub.Status == domain.KnowledgeSubmissionAccepted || sub.Status == domain.KnowledgeSubmissionMerged {
		return sub, nil
	}
	if sub.Status != domain.KnowledgeSubmissionNeedsReview || len(sub.ResultVersionIDs) == 0 || len(sub.ResultVersionIDs) != len(sub.ResultItemIDs) {
		return nil, fmt.Errorf("%w: submission must be curated with evidence before publication", domain.ErrStateConflict)
	}
	for n, id := range sub.ResultVersionIDs {
		version, owner, err := s.knowledgePreparedVersion(ctx, sub, id)
		if err != nil {
			return nil, err
		}
		if version.ItemID != sub.ResultItemIDs[n] {
			return nil, fmt.Errorf("%w: prepared publication item mismatch", domain.ErrValidation)
		}
		sources, err := s.store.Knowledge().ListVersionSources(ctx, workspaceID, owner, id)
		if err != nil {
			return nil, err
		}
		if len(sources) == 0 {
			return nil, fmt.Errorf("%w: publication has no source evidence", domain.ErrValidation)
		}
		if err = s.store.Knowledge().PublishVersion(ctx, version.ItemID, id, version.BaseVersion, time.Now().UTC()); err != nil {
			return nil, err
		}
	}
	if err = s.store.Knowledge().UpdateSubmissionStatus(ctx, sub.ID, domain.KnowledgeSubmissionAccepted, sub.ResultItemIDs, sub.ResultVersionIDs, "", sub.Version); err != nil {
		return nil, err
	}
	if auditActor == nil {
		auditActor = map[string]any{"kind": "user", "id": "user_demo", "knowledge_owner_agent_id": actorAgentID}
	}
	if err = s.store.Audit().Append(ctx, workspaceID, auditActor, "knowledge.publish", submissionID, map[string]any{"version_ids": sub.ResultVersionIDs}); err != nil {
		return nil, err
	}
	return s.store.Knowledge().GetSubmission(ctx, workspaceID, sub.AgentID, sub.ID)
}

// publishKnowledgeSubmissionForAgent is the only automatic publication entry
// point. Its audit identity is the original Agent/Run, while the configured
// librarian remains the manager that executes the normal publication checks.
func (s *Service) publishKnowledgeSubmissionForAgent(ctx context.Context, workspaceID, managerAgentID, originAgentID, originRunID, recordIntent, submissionID string) (*domain.KnowledgeSubmission, error) {
	var out *domain.KnowledgeSubmission
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		var err error
		out, err = s.publishKnowledgeSubmissionLocked(ctx, workspaceID, managerAgentID, submissionID, map[string]any{
			"kind": "agent", "id": originAgentID, "run_id": originRunID,
			"knowledge_owner_agent_id": managerAgentID,
			"record_intent":            recordIntent,
		})
		return err
	})
	if err == nil && s.notifier != nil {
		s.notifier.Notify(workspaceID)
	}
	return out, err
}

func (s *Service) RepealKnowledgeItem(ctx context.Context, workspaceID, actorAgentID, itemID string, expectedVersion int64, reason string) (*domain.KnowledgeItem, error) {
	var out *domain.KnowledgeItem
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		item, err := s.store.Knowledge().GetItem(ctx, workspaceID, actorAgentID, itemID)
		if err != nil {
			return err
		}
		if item.OwnerAgentID != actorAgentID {
			if _, err = s.knowledgeManager(ctx, workspaceID, actorAgentID); err != nil {
				return err
			}
		}
		if err = s.store.Knowledge().RepealItem(ctx, itemID, expectedVersion, reason, time.Now().UTC()); err != nil {
			return err
		}
		if err = s.store.Audit().Append(ctx, workspaceID, map[string]any{"kind": "user", "id": "user_demo", "knowledge_owner_agent_id": actorAgentID}, "knowledge.repeal", itemID, map[string]any{"reason": reason, "expected_version": expectedVersion}); err != nil {
			return err
		}
		out, err = s.store.Knowledge().GetItem(ctx, workspaceID, actorAgentID, itemID)
		return err
	})
	if err == nil && s.notifier != nil {
		s.notifier.Notify(workspaceID)
	}
	return out, err
}

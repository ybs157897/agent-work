package application

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type knowledgeCurationOriginProfile struct {
	ItemID     string
	OwnerAgent string
	Visibility domain.KnowledgeVisibility
	Scope      domain.KnowledgeScope
}

// inheritKnowledgeCurationDefaults carries the trusted origin envelope into
// every proposed revision. Model output may omit these fields, but it cannot
// widen visibility/scope or move ownership. A split of one homogeneous origin
// change remains unambiguous; mixed origins require stable item IDs.
func inheritKnowledgeCurationDefaults(origin *domain.KnowledgeSubmission, changes []domain.KnowledgeChange) error {
	if origin == nil || origin.AgentID == "" || origin.WorkspaceID == "" {
		return fmt.Errorf("%w: curation origin is required", domain.ErrValidation)
	}
	if len(changes) == 0 {
		return nil
	}
	profiles := make([]knowledgeCurationOriginProfile, 0, len(origin.Request.Changes))
	for _, change := range origin.Request.Changes {
		owner := strings.TrimSpace(change.OwnerAgentID)
		if owner == "" {
			owner = origin.AgentID
		}
		visibility := change.Visibility
		if visibility == "" {
			visibility = domain.KnowledgeVisibilityWorkspace
		}
		if visibility == domain.KnowledgeVisibilityPrivate && owner == "" {
			return fmt.Errorf("%w: private curation origin has no owner", domain.ErrValidation)
		}
		profiles = append(profiles, knowledgeCurationOriginProfile{
			ItemID: change.ItemID, OwnerAgent: owner, Visibility: visibility,
			Scope: cloneKnowledgeScope(change.Scope),
		})
	}
	if len(profiles) == 0 {
		return fmt.Errorf("%w: curation origin has no changes", domain.ErrValidation)
	}
	homogeneous := true
	for i := 1; i < len(profiles); i++ {
		if !knowledgeCurationProfileEqual(profiles[0], profiles[i]) {
			homogeneous = false
			break
		}
	}
	for i := range changes {
		change := &changes[i]
		profile, err := selectKnowledgeCurationOriginProfile(profiles, homogeneous, change.ItemID)
		if err != nil {
			return err
		}
		if change.OwnerAgentID == "" {
			change.OwnerAgentID = profile.OwnerAgent
		} else if change.OwnerAgentID != profile.OwnerAgent {
			return fmt.Errorf("%w: curation change %d cannot change owner from %q", domain.ErrValidation, i, profile.OwnerAgent)
		}
		if change.Visibility == "" {
			change.Visibility = profile.Visibility
		} else if change.Visibility != profile.Visibility {
			return fmt.Errorf("%w: curation change %d cannot change visibility from %q", domain.ErrValidation, i, profile.Visibility)
		}
		if change.Scope == nil {
			change.Scope = cloneKnowledgeScope(profile.Scope)
		} else if !knowledgeScopesEqual(change.Scope, profile.Scope) {
			return fmt.Errorf("%w: curation change %d cannot widen origin scope", domain.ErrValidation, i)
		}
	}
	return nil
}

func selectKnowledgeCurationOriginProfile(profiles []knowledgeCurationOriginProfile, homogeneous bool, itemID string) (knowledgeCurationOriginProfile, error) {
	if itemID != "" {
		for _, profile := range profiles {
			if profile.ItemID == itemID {
				return profile, nil
			}
		}
		return knowledgeCurationOriginProfile{}, fmt.Errorf("%w: curation item %s is outside the origin change set", domain.ErrValidation, itemID)
	}
	if homogeneous {
		return profiles[0], nil
	}
	return knowledgeCurationOriginProfile{}, fmt.Errorf("%w: mixed origin owners/scopes require explicit item identity", domain.ErrValidation)
}

func knowledgeCurationProfileEqual(left, right knowledgeCurationOriginProfile) bool {
	return left.OwnerAgent == right.OwnerAgent && left.Visibility == right.Visibility && knowledgeScopesEqual(left.Scope, right.Scope)
}

func cloneKnowledgeScope(scope domain.KnowledgeScope) domain.KnowledgeScope {
	if scope == nil {
		return domain.KnowledgeScope{}
	}
	return domain.KnowledgeScope(mapsCloneAny(scope))
}

func knowledgeScopesEqual(left, right domain.KnowledgeScope) bool {
	return mustKnowledgeJSON(cloneKnowledgeScope(left)) == mustKnowledgeJSON(cloneKnowledgeScope(right))
}

// mergeKnowledgeFinishRelations attaches top-level curation relations to the
// candidate change that owns a changed endpoint. Publication preparation only
// consumes change.Relations, so silently leaving finish.Relations at the job
// result would lose the graph edge.
func mergeKnowledgeFinishRelations(finish *KnowledgeFinishDecision) error {
	if finish == nil || len(finish.Relations) == 0 {
		return nil
	}
	if len(finish.Changes) == 0 {
		return fmt.Errorf("%w: curation relations have no changed endpoint", domain.ErrValidation)
	}
	for _, relation := range finish.Relations {
		target, err := curationRelationChangedEndpoint(finish.Changes, relation)
		if err != nil {
			return err
		}
		if target < 0 {
			return fmt.Errorf("%w: curation relation %s -> %s has no changed endpoint", domain.ErrValidation, relation.FromItemID, relation.ToItemID)
		}
		if !containsKnowledgeRelation(finish.Changes[target].Relations, relation) {
			finish.Changes[target].Relations = append(finish.Changes[target].Relations, relation)
		}
	}
	return nil
}

func curationRelationChangedEndpoint(changes []domain.KnowledgeChange, relation domain.KnowledgeRelationInput) (int, error) {
	if index, present := parseKnowledgeChangeRef(relation.FromItemID); present {
		if index < 0 || index >= len(changes) {
			return -1, fmt.Errorf("%w: relation from_item_id %q is outside revised_changes", domain.ErrValidation, relation.FromItemID)
		}
		return index, nil
	}
	for i, change := range changes {
		if change.ItemID != "" && change.ItemID == relation.FromItemID {
			return i, nil
		}
	}
	if index, present := parseKnowledgeChangeRef(relation.ToItemID); present {
		if index < 0 || index >= len(changes) {
			return -1, fmt.Errorf("%w: relation to_item_id %q is outside revised_changes", domain.ErrValidation, relation.ToItemID)
		}
		return index, nil
	}
	for i, change := range changes {
		if change.ItemID != "" && change.ItemID == relation.ToItemID {
			return i, nil
		}
	}
	return -1, nil
}

func parseKnowledgeChangeRef(value string) (int, bool) {
	if !strings.HasPrefix(value, "@change:") {
		return 0, false
	}
	index, err := strconv.Atoi(strings.TrimPrefix(value, "@change:"))
	if err != nil {
		return -1, true
	}
	return index, true
}

func containsKnowledgeRelation(relations []domain.KnowledgeRelationInput, candidate domain.KnowledgeRelationInput) bool {
	for _, relation := range relations {
		if relation.FromItemID == candidate.FromItemID && relation.ToItemID == candidate.ToItemID &&
			relation.Kind == candidate.Kind && relation.Condition == candidate.Condition &&
			relation.Rationale == candidate.Rationale && sameKnowledgeStrings(relation.SourceIDs, candidate.SourceIDs) {
			return true
		}
	}
	return false
}

func sameKnowledgeStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

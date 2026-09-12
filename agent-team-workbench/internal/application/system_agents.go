package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// currentKnowledgeLibrarianPresentation overlays the current public prompt on
// profiles created before the built-in persona was revised. The SQLite
// protection trigger intentionally keeps the persisted system identity fixed;
// an in-memory overlay lets existing workspaces receive the current prompt
// without a destructive data rewrite.
func currentKnowledgeLibrarianPresentation(a *domain.AgentProfile) *domain.AgentProfile {
	if a == nil || !a.Kind.IsKnowledgeLibrarian() {
		return a
	}
	copy := *a
	copy.Instructions = KnowledgeLibrarianHarnessPrompt
	copy.PromptVersion = KnowledgeLibrarianHarnessPromptVersion
	copy.InstructionsEditable = false
	return &copy
}

// EnsureBuiltinAgents provisions every workspace-scoped system Agent. It is
// called during control-plane startup after workspace seed and before external
// agents/ import. The operation is idempotent and does not create a session or
// Run.
func (s *Service) EnsureBuiltinAgents(ctx context.Context) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("%w: system Agent provisioning requires a store", domain.ErrCapabilityMissing)
	}
	workspaceIDs, err := s.store.Workspaces().ListIDs(ctx)
	if err != nil {
		return err
	}
	for _, workspaceID := range workspaceIDs {
		if _, err := s.EnsureBuiltinKnowledgeLibrarian(ctx, workspaceID); err != nil {
			return err
		}
	}
	return nil
}

// EnsureBuiltinKnowledgeLibrarian returns the one deterministic librarian
// identity for a workspace. If an older KnowledgeLibrarianConfig points at an
// ordinary Agent, that Agent's runtime/model preference is copied once when
// the new profile is created; the old profile and all historical references
// remain untouched. Config ownership/migration stays with the knowledge
// service, which can atomically repoint its existing config row to this ID.
func (s *Service) EnsureBuiltinKnowledgeLibrarian(ctx context.Context, workspaceID string) (*domain.AgentProfile, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("%w: system Agent provisioning requires a store", domain.ErrCapabilityMissing)
	}
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return nil, fmt.Errorf("%w: workspace id required", domain.ErrValidation)
	}
	var librarian *domain.AgentProfile
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.store.Workspaces().Get(ctx, workspaceID); err != nil {
			return err
		}
		profileID := domain.KnowledgeLibrarianAgentID(workspaceID)
		existing, err := s.store.Agents().Get(ctx, profileID)
		if err == nil {
			if existing.WorkspaceID != workspaceID || !existing.Kind.IsKnowledgeLibrarian() {
				return fmt.Errorf("%w: built-in Knowledge Librarian identity conflict", domain.ErrStateConflict)
			}
			librarian = currentKnowledgeLibrarianPresentation(existing)
			return nil
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return err
		}

		runtimePreference := domain.RuntimePreference{Mode: "default"}
		var modelOverride domain.ModelRef
		now := time.Now().UTC()
		librarian = &domain.AgentProfile{
			ID: profileID, WorkspaceID: workspaceID,
			Kind: domain.AgentProfileKindKnowledgeLibrarian,
			Slug: "knowledge-librarian",
			Name: domain.KnowledgeLibrarianDisplayName, Role: domain.KnowledgeLibrarianRole,
			Skills:               []string{"知识检索", "知识整理", "来源核验"},
			Instructions:         KnowledgeLibrarianHarnessPrompt,
			PromptVersion:        KnowledgeLibrarianHarnessPromptVersion,
			InstructionsEditable: false,
			Availability:         domain.AgentEnabled, Presence: domain.PresenceIdle,
			RuntimePreference: runtimePreference, ModelOverride: modelOverride,
			Policy:  domain.AgentPolicy{ApprovalPolicy: "auto", Sandbox: "workspace-write"},
			Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		return s.store.Agents().Create(ctx, librarian)
	})
	if err != nil {
		return nil, err
	}
	return currentKnowledgeLibrarianPresentation(librarian), nil
}

func isEmptyRuntimeModel(a *domain.AgentProfile) bool {
	if a == nil {
		return true
	}
	return a.RuntimePreference.Preferred == "" && len(a.RuntimePreference.Fallbacks) == 0 &&
		a.RuntimePreference.AgentPreset == "" && a.ModelOverride.Ref == "" &&
		a.ModelOverride.Provider == "" && a.ModelOverride.Model == "" &&
		a.ModelOverride.ReasoningEffort == ""
}

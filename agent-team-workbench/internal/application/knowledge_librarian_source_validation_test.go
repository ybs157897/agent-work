package application

import (
	"context"
	"errors"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type sourceValidationKnowledgeRepo struct {
	KnowledgeRepo
	source     *domain.KnowledgeSource
	submission *domain.KnowledgeSubmission
}

func (r sourceValidationKnowledgeRepo) GetSource(context.Context, string, string, string) (*domain.KnowledgeSource, error) {
	if r.source == nil {
		return nil, domain.ErrNotFound
	}
	return r.source, nil
}

func (r sourceValidationKnowledgeRepo) GetSubmissionForWorkspace(context.Context, string, string) (*domain.KnowledgeSubmission, error) {
	if r.submission == nil {
		return nil, domain.ErrNotFound
	}
	return r.submission, nil
}

type sourceValidationStore struct {
	Store
	knowledge KnowledgeRepo
}

func (s sourceValidationStore) Knowledge() KnowledgeRepo { return s.knowledge }

func TestValidateKnowledgeCurationSourcesRejectsIDSubstringForgery(t *testing.T) {
	svc := &Service{store: sourceValidationStore{knowledge: sourceValidationKnowledgeRepo{source: &domain.KnowledgeSource{
		ID: "kbs_real", WorkspaceID: "ws_source_validation", SubmittedByAgentID: "agent_requester",
		Kind: domain.KnowledgeSourceCode, Ref: "orders/cancel.go", Excerpt: "authorized evidence",
	}}}}
	job := &domain.KnowledgeJob{WorkspaceID: "ws_source_validation", RequestingAgentID: "agent_requester", EvidenceIDs: []string{"kbs_real"}}
	changes := []domain.KnowledgeChange{{Sources: []domain.KnowledgeSourceInput{{
		Kind: domain.KnowledgeSourceCode, Ref: "orders/cancel.go?kbs_real", Excerpt: "forged evidence",
	}}}}
	if err := svc.validateKnowledgeCurationSources(context.Background(), job, changes); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("forged ID substring was accepted: %v", err)
	}
}

func TestValidateKnowledgeCurationSourcesAcceptsSeedExcerptSubstring(t *testing.T) {
	original := domain.KnowledgeSourceInput{Kind: domain.KnowledgeSourceDocument, Ref: "specification@v1", Locator: "A-B规则", Excerpt: "功能A依赖功能B；功能B提供必要的后续处理。"}
	svc := &Service{store: sourceValidationStore{knowledge: sourceValidationKnowledgeRepo{submission: &domain.KnowledgeSubmission{
		ID: "kss_origin", WorkspaceID: "ws_source_validation", AgentID: "agent_producer",
		Request: domain.KnowledgeSubmitCandidate{WorkspaceID: "ws_source_validation", AgentID: "agent_producer", ClientKey: "origin", Changes: []domain.KnowledgeChange{{Sources: []domain.KnowledgeSourceInput{original}}}},
	}}}}
	job := &domain.KnowledgeJob{WorkspaceID: "ws_source_validation", RequestingAgentID: "agent_librarian", SubmissionID: "kss_origin"}
	changes := []domain.KnowledgeChange{{Sources: []domain.KnowledgeSourceInput{{
		Kind: original.Kind, Ref: original.Ref, Locator: original.Locator, Excerpt: "功能B提供必要的后续处理",
	}}}}
	if err := svc.validateKnowledgeCurationSources(context.Background(), job, changes); err != nil {
		t.Fatalf("valid seed excerpt substring was rejected: %v", err)
	}
}

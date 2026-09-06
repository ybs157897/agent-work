package application

import (
	"context"
	"errors"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type publicationSourceRunRepo struct {
	RunRepo
	run *domain.ExecutionRun
}

func (r publicationSourceRunRepo) Get(context.Context, string) (*domain.ExecutionRun, error) {
	return r.run, nil
}

type publicationSourceStore struct {
	Store
	runs RunRepo
}

func (s publicationSourceStore) Runs() RunRepo { return s.runs }

func TestKnowledgePublicationRejectsChangingSourceRun(t *testing.T) {
	for _, status := range []domain.RunStatus{domain.RunQueued, domain.RunRunning, domain.RunSucceeding} {
		t.Run(string(status), func(t *testing.T) {
			run := &domain.ExecutionRun{ID: "run_source", WorkspaceID: "ws_source", Status: status}
			svc := &Service{store: publicationSourceStore{runs: publicationSourceRunRepo{run: run}}}
			_, err := svc.validateKnowledgePublicationSource(context.Background(), domain.KnowledgeSubmitCandidate{WorkspaceID: run.WorkspaceID}, domain.KnowledgeSourceInput{Kind: domain.KnowledgeSourceRun, Ref: run.ID, Excerpt: "still changing"})
			if !errors.Is(err, domain.ErrStateConflict) {
				t.Fatalf("mutable source evidence must be rejected before reading output: %v", err)
			}
		})
	}
}

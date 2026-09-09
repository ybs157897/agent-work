package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type preserveCatalogAnalysisRepo struct {
	ChatAnalysisRepo
	revision *domain.ChatAnalysisRevision
	attempt  *domain.ChatAnalysisAttempt
}

func (r preserveCatalogAnalysisRepo) GetRevision(context.Context, string, string, int64) (*domain.ChatAnalysisRevision, error) {
	return r.revision, nil
}

func (r preserveCatalogAnalysisRepo) GetAttemptByRun(context.Context, string) (*domain.ChatAnalysisAttempt, error) {
	return r.attempt, nil
}

type preserveCatalogRunRepo struct {
	RunRepo
	run *domain.ExecutionRun
}

func (r preserveCatalogRunRepo) Get(context.Context, string) (*domain.ExecutionRun, error) {
	return r.run, nil
}

type preserveCatalogStore struct {
	Store
	analyses ChatAnalysisRepo
	runs     RunRepo
}

func (s preserveCatalogStore) ChatAnalyses() ChatAnalysisRepo { return s.analyses }
func (s preserveCatalogStore) Runs() RunRepo                  { return s.runs }

func TestMergeBaseAnalysisConversationCatalogRejectsForeignScopeAndDigest(t *testing.T) {
	ctx := context.Background()
	const workspaceID = "ws_catalog"
	const chatID = "wi_catalog"
	const agentID = "agent_catalog"
	const runID = "run_catalog"
	sha := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	baseCatalog := chatAnalysisCatalog{Sources: []chatAnalysisCatalogSource{{Kind: analysisConversationKind, Ref: "conversation:run:old", SHA256: sha, Available: true}}}
	raw, _ := json.Marshal(baseCatalog)
	makeService := func(run *domain.ExecutionRun, attempt *domain.ChatAnalysisAttempt) *Service {
		return &Service{store: preserveCatalogStore{
			analyses: preserveCatalogAnalysisRepo{revision: &domain.ChatAnalysisRevision{RunID: runID}, attempt: attempt},
			runs:     preserveCatalogRunRepo{run: run},
		}}
	}
	baseRun := &domain.ExecutionRun{ID: runID, WorkspaceID: workspaceID, WorkItemID: chatID, AgentProfileID: agentID}
	attempt := &domain.ChatAnalysisAttempt{SourceCatalogJSON: string(raw)}
	wi := &domain.WorkItem{ID: chatID, WorkspaceID: workspaceID, AgentProfileID: agentID}
	for _, tc := range []struct {
		name string
		run  *domain.ExecutionRun
		cat  chatAnalysisCatalog
		want error
	}{
		{name: "foreign workspace", run: &domain.ExecutionRun{ID: runID, WorkspaceID: "ws_other", WorkItemID: chatID, AgentProfileID: agentID}, cat: chatAnalysisCatalog{}, want: domain.ErrWorkspaceContextMismatch},
		{name: "foreign chat", run: &domain.ExecutionRun{ID: runID, WorkspaceID: workspaceID, WorkItemID: "wi_other", AgentProfileID: agentID}, cat: chatAnalysisCatalog{}, want: domain.ErrWorkspaceContextMismatch},
		{name: "foreign agent", run: &domain.ExecutionRun{ID: runID, WorkspaceID: workspaceID, WorkItemID: chatID, AgentProfileID: "agent_other"}, cat: chatAnalysisCatalog{}, want: domain.ErrWorkspaceContextMismatch},
		{name: "same ref wrong digest", run: baseRun, cat: chatAnalysisCatalog{Sources: []chatAnalysisCatalogSource{{Kind: analysisConversationKind, Ref: "conversation:run:old", SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Available: true}}}, want: domain.ErrWorkspaceContextMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := makeService(tc.run, attempt)
			err := svc.mergeBaseAnalysisConversationCatalog(ctx, wi, 1, &tc.cat)
			if !errors.Is(err, tc.want) {
				t.Fatalf("merge error=%v, want %v", err, tc.want)
			}
		})
	}
	merged := chatAnalysisCatalog{}
	if err := makeService(baseRun, attempt).mergeBaseAnalysisConversationCatalog(ctx, wi, 1, &merged); err != nil || len(merged.Sources) != 1 || merged.Sources[0].SHA256 != sha {
		t.Fatalf("valid base catalog should merge: %+v err=%v", merged, err)
	}
}

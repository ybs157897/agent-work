package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/orchestrator"
)

func TestPublishPublicationDraftRetriesSameHTTPKeyAfterInfrastructureFailure(t *testing.T) {
	server := newPlanTestServer(t)
	wsID, leadID, _ := seedPlanHTTPEnv(t, server)
	ctx := context.Background()
	chat, err := server.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "HTTP 发布故障恢复", RecordKind: domain.RecordKindChat, AgentProfileID: leadID,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := server.svc.CreateRun(ctx, chat.ID, application.CreateRunParams{
		AgentProfileID: chat.AgentProfileID, Instruction: "整理一条确认事项",
		OutputContract: orchestrator.OutputContractChatAnalysisV1,
	})
	if err != nil {
		t.Fatal(err)
	}
	analysisInput, _ := run.Input["analysis"].(map[string]any)
	var catalog []struct {
		Kind   string `json:"kind"`
		Ref    string `json:"ref"`
		SHA256 string `json:"sha256"`
	}
	rawCatalog, _ := json.Marshal(analysisInput["source_catalog"])
	if err := json.Unmarshal(rawCatalog, &catalog); err != nil || len(catalog) == 0 {
		t.Fatalf("analysis source catalog=%s err=%v", string(rawCatalog), err)
	}
	if err := server.svc.RecordRunStatus(ctx, run.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	analysis := `{"version":"chat-analysis/v1","summary":"HTTP 发布摘要","sources":[{"id":"conversation-source","kind":"` + catalog[0].Kind + `","ref":"` + catalog[0].Ref + `","sha256":"` + catalog[0].SHA256 + `","read_status":"read"}],"items":[{"id":"scope","kind":"requirement","title":"范围","detail":"需要确认的范围。","source_ids":["conversation-source"],"basis":"observed"}],"questions":[]}`
	if err := server.svc.RecordRunEvent(ctx, run.ID, domain.EventMessageCompleted, map[string]any{
		"role": "assistant", "text": "```atw-analysis\n" + analysis + "\n```",
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.svc.RecordRunStatus(ctx, run.ID, domain.RunSucceeding, nil); err != nil {
		t.Fatal(err)
	}
	if err := server.svc.RecordRunStatus(ctx, run.ID, domain.RunSucceeded, nil); err != nil {
		t.Fatal(err)
	}
	view, err := server.svc.GetChatAnalysis(ctx, chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	decision, _, err := server.svc.SaveChatAnalysisDecision(ctx, application.SaveChatAnalysisDecisionParams{
		ChatWorkItemID: chat.ID, ExpectedVersion: view.Projection.Version, Revision: view.Projection.Revision,
		ItemID: "scope", Outcome: domain.ChatAnalysisDecisionConfirmed, Conclusion: "确认发布范围",
		Basis: "HTTP 故障恢复测试中的人类确认", ProductVersion: "http-publication-test", ClientKey: "http-decision-1", ActorID: "user_demo",
	})
	if err != nil {
		t.Fatal(err)
	}
	baseline := testPublicationBaseline()
	server.svc.SetPublicationBaselineResolver(func(context.Context, *domain.ExecutionContextSnapshot) (domain.ProjectBaseline, error) {
		return baseline, nil
	})
	draft, _, err := server.svc.CreatePublicationDraft(ctx, application.CreatePublicationDraftParams{
		ChatWorkItemID: chat.ID, ExpectedVersion: decision.Projection.Version, Revision: decision.Projection.Revision,
		ItemIDs: []string{"scope"}, Title: "HTTP 发布任务", ClientKey: "http-draft-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	var failNext atomic.Bool
	failNext.Store(true)
	server.svc.SetPublicationBaselineResolver(func(context.Context, *domain.ExecutionContextSnapshot) (domain.ProjectBaseline, error) {
		if failNext.CompareAndSwap(true, false) {
			return domain.ProjectBaseline{}, errors.New("sqlite: transient disk I/O failure")
		}
		return baseline, nil
	})
	body := `{"expected_version":` + jsonInt(draft.Draft.Version) + `,"client_key":"http-publish-1"}`
	path := "/api/v1/work-items/" + chat.ID + "/analysis/drafts/" + draft.Draft.ID + "/publish"
	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	firstReq.Header.Set("Content-Type", "application/json")
	firstReq.Header.Set("Idempotency-Key", "http-publish-request-1")
	server.Routes().ServeHTTP(first, firstReq)
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("transient publication failure should be retryable 500, got %d: %s", first.Code, first.Body.String())
	}
	var firstProblem Problem
	if err := json.Unmarshal(first.Body.Bytes(), &firstProblem); err != nil {
		t.Fatal(err)
	}
	if !firstProblem.Retryable {
		t.Fatalf("transient publication failure must be retryable: %+v", firstProblem)
	}

	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	secondReq.Header.Set("Content-Type", "application/json")
	secondReq.Header.Set("Idempotency-Key", "http-publish-request-1")
	server.Routes().ServeHTTP(second, secondReq)
	if second.Code != http.StatusOK {
		t.Fatalf("same key after recovery should publish successfully, got %d: %s", second.Code, second.Body.String())
	}
	var secondBody struct {
		Draft struct {
			TaskID string `json:"task_id"`
			Status string `json:"status"`
		} `json:"draft"`
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil {
		t.Fatal(err)
	}
	if secondBody.Draft.Status != string(domain.PublicationDraftPublished) ||
		secondBody.Draft.TaskID == "" || secondBody.Task.ID != secondBody.Draft.TaskID {
		t.Fatalf("recovered publication response lost task identity: %s", second.Body.String())
	}
	items, _, err := server.svc.WorkItems(ctx, wsID, application.WorkItemFilter{RecordKind: domain.RecordKindTask})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != secondBody.Draft.TaskID {
		t.Fatalf("same HTTP key must create exactly one Task: items=%+v response=%s", items, second.Body.String())
	}
}

func testPublicationBaseline() domain.ProjectBaseline {
	empty := sha256.Sum256(nil)
	digest := hex.EncodeToString(empty[:])
	return domain.ProjectBaseline{
		RepositoryIdentity: "repo-http-publication",
		RefKind:            domain.RefBranch,
		BranchName:         "main",
		CheckoutRef:        "refs/heads/main",
		Head:               strings.Repeat("a", 40),
		StagedDigest:       digest,
		UnstagedDigest:     digest,
		UntrackedDigest:    digest,
		ContentDigest:      digest,
	}
}

func jsonInt(value int) string {
	return strconv.Itoa(value)
}

package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestKnowledgeJobChatsAreHiddenFromPublicListDeepLinkAndSend(t *testing.T) {
	s := newPlanTestServer(t)
	wsID, agentID, _ := seedPlanHTTPEnv(t, s)
	ctx := context.Background()
	internal, err := s.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "内部知识调查", RecordKind: domain.RecordKindChat, AgentProfileID: agentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	public, err := s.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "用户对话", RecordKind: domain.RecordKindChat, AgentProfileID: agentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.KnowledgeJobs().Create(ctx, &domain.KnowledgeJob{
		WorkspaceID: wsID, RequestingAgentID: "human:shared", AgentProfileID: agentID,
		WorkItemID: internal.ID, Mode: domain.KnowledgeJobInquiry, Status: domain.KnowledgeJobQueued,
		Question: "internal", ClientKey: "internal-chat",
	}); err != nil {
		t.Fatal(err)
	}

	list := httptest.NewRecorder()
	s.Routes().ServeHTTP(list, httptest.NewRequest(http.MethodGet,
		"/api/v1/workspaces/"+wsID+"/work-items?record_kind=chat", nil))
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), internal.ID) || !strings.Contains(list.Body.String(), public.ID) {
		t.Fatalf("public chat list must exclude knowledge job relation: status=%d body=%s", list.Code, list.Body.String())
	}

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/work-items/"+internal.ID, nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/work-items/"+internal.ID+"/runs", nil),
	} {
		rec := httptest.NewRecorder()
		s.Routes().ServeHTTP(rec, request)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("knowledge work item deep link must be hidden: path=%s status=%d body=%s", request.URL.Path, rec.Code, rec.Body.String())
		}
	}

	send := httptest.NewRecorder()
	sendReq := httptest.NewRequest(http.MethodPost, "/api/v1/work-items/"+internal.ID+"/runs",
		strings.NewReader(`{"input":{"instruction":"继续调查"}}`))
	sendReq.Header.Set("Idempotency-Key", "hidden-knowledge-chat-send")
	s.Routes().ServeHTTP(send, sendReq)
	if send.Code != http.StatusNotFound {
		t.Fatalf("knowledge work item must reject public send: status=%d body=%s", send.Code, send.Body.String())
	}
}

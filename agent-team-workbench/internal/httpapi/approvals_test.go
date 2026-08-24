// approvals_test.go resolve 命令 scope 参数的 HTTP 契约：scope=thread 建授权、
// 非法 scope 400、缺省等价 once 不建授权。
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// postResolve 提交 resolve 命令并取回响应码与 JSON 体。
func postResolve(t *testing.T, mux http.Handler, approvalID, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/runs/run_any/approvals/"+approvalID+"/commands/resolve", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "appr-key-"+time.Now().Format("150405.000000000"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("响应非 JSON: %s", rec.Body.String())
		}
	}
	return rec.Code, out
}

// seedApproval 经服务面建 run + pending 审批（waiting_approval 只从 running 进入）。
func seedApproval(t *testing.T, s *Server, wsID, agentID string) *domain.ApprovalRequest {
	t.Helper()
	ctx := context.Background()
	wi, err := s.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "审批任务", AgentProfileID: agentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: agentID, Instruction: "审批",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := s.svc.RecordRunStatus(ctx, run.ID, to, nil); err != nil {
			t.Fatalf("前置迁移 %s 失败: %v", to, err)
		}
	}
	a, err := s.svc.RequestApproval(ctx, run.ID, domain.ApprovalKindCommand, "high", "Codex 请求执行命令：git push")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestResolveApprovalScopeCreatesGrant scope=thread + approved → 200 且授权落库
// （thread 锚定本 work item）。
func TestResolveApprovalScopeCreatesGrant(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, agentID, _ := seedPlanHTTPEnv(t, s)
	a := seedApproval(t, s, wsID, agentID)

	code, body := postResolve(t, mux, a.ID, `{"decision":"approved","scope":"thread"}`)
	if code != http.StatusOK {
		t.Fatalf("resolve scope=thread 应 200，实际 %d: %v", code, body)
	}
	g, err := s.store.ApprovalGrants().Matching(context.Background(), wsID, agentID, a.WorkItemID,
		domain.ApprovalKindCommand, "Codex 请求执行命令：git push")
	if err != nil {
		t.Fatal(err)
	}
	if g == nil {
		t.Fatal("scope=thread 决议应创建 thread 授权")
	}
	if g.Scope != domain.ApprovalScopeThread || g.WorkItemID != a.WorkItemID {
		t.Fatalf("授权应锚定本 work item： %+v", g)
	}
}

// TestResolveApprovalScopeValidation 非法 scope 400；缺省（不传）等价 once 不建授权。
func TestResolveApprovalScopeValidation(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, agentID, _ := seedPlanHTTPEnv(t, s)

	bad := seedApproval(t, s, wsID, agentID)
	code, body := postResolve(t, mux, bad.ID, `{"decision":"approved","scope":"always"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("非法 scope 应 400，实际 %d: %v", code, body)
	}

	plain := seedApproval(t, s, wsID, agentID)
	code, body = postResolve(t, mux, plain.ID, `{"decision":"approved"}`)
	if code != http.StatusOK {
		t.Fatalf("缺省 scope 应 200（等价 once），实际 %d: %v", code, body)
	}
	g, err := s.store.ApprovalGrants().Matching(context.Background(), wsID, agentID, plain.WorkItemID,
		domain.ApprovalKindCommand, "Codex 请求执行命令：git push")
	if err != nil {
		t.Fatal(err)
	}
	if g != nil {
		t.Fatalf("缺省 once 与非法请求都不得建授权，实际 %+v", g)
	}
}

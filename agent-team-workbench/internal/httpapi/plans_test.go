// plans_test.go M1 编排端点的 HTTP 契约测试：201 PlanDTO（含步骤结果）、
// 校验失败 400（契约钉死，非通用 422）、GET plan、树端点与 parent_id 过滤。
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
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
	"github.com/ybs/agent-team-workbench/internal/sse"
)

type planTestNotifier struct{}

func (planTestNotifier) Notify(string) {}

// newPlanTestServer 搭建带完整 Service 的路由（无 dispatcher：子任务 run 留 queued）。
func newPlanTestServer(t *testing.T) *Server {
	t.Helper()
	store := openIdempotencyTestDB(t)
	svc := application.NewService(store, nil, planTestNotifier{}, atwruntime.NewRegistry())
	return NewServer(svc, store, sse.NewHub())
}

func seedPlanHTTPEnv(t *testing.T, s *Server) (wsID, leadID, workerID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_plan", Name: "plan", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	lead := &domain.AgentProfile{
		ID: "agent_lead", WorkspaceID: ws.ID, Name: "Lead", Role: "architect",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	worker := &domain.AgentProfile{
		ID: "agent_worker", WorkspaceID: ws.ID, Name: "Worker", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.Agents().Create(ctx, lead); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Agents().Create(ctx, worker); err != nil {
		t.Fatal(err)
	}
	return ws.ID, lead.ID, worker.ID
}

// postPlan 提交 plan 并断言状态码。
func postPlan(t *testing.T, mux http.Handler, wsID, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+wsID+"/plans", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "plan-key-"+time.Now().Format("150405.000000000"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应非 JSON: %s", rec.Body.String())
	}
	return rec.Code, out
}

// TestPlanEndpointsCreateAndGet 双 dispatch 提交 → 201 PlanDTO（steps 带 result ids、
// status=finished）；GET /plans/{id} 同一投影。
func TestPlanEndpointsCreateAndGet(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, leadID, workerID := seedPlanHTTPEnv(t, s)
	main, err := s.svc.CreateWorkItem(context.Background(), wsID, application.CreateWorkItemParams{Title: "主任务"})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"work_item_id":"` + main.ID + `","agent_profile_id":"` + leadID + `","steps":[` +
		`{"verb":"dispatch","agent_id":"` + workerID + `","title":"A","instruction":"做 A"},` +
		`{"verb":"dispatch","agent_id":"` + workerID + `","title":"B","instruction":"做 B"}]}`
	code, plan := postPlan(t, mux, wsID, body)
	if code != http.StatusCreated {
		t.Fatalf("创建 plan status = %d: %s", code, plan)
	}
	if plan["status"] != "finished" {
		t.Fatalf("同步执行完应 finished，实际 %v", plan["status"])
	}
	steps := plan["steps"].([]any)
	if len(steps) != 2 {
		t.Fatalf("steps 数 = %d", len(steps))
	}
	for _, raw := range steps {
		st := raw.(map[string]any)
		if st["status"] != "executed" {
			t.Fatalf("step %v 应 executed", st)
		}
		if st["result_work_item_id"] == "" || st["result_run_id"] == "" {
			t.Fatalf("dispatch step 应带 result ids: %v", st)
		}
	}
	planID := plan["id"].(string)

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/plans/"+planID, nil)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET plan status = %d", getRec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != planID || got["status"] != "finished" {
		t.Fatalf("GET plan 投影异常: %v", got)
	}
}

// TestPlanEndpointsUnknownVerb400 未知 verb → 400（契约钉死），非通用 422。
func TestPlanEndpointsUnknownVerb400(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, leadID, workerID := seedPlanHTTPEnv(t, s)
	main, err := s.svc.CreateWorkItem(context.Background(), wsID, application.CreateWorkItemParams{Title: "主任务"})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"work_item_id":"` + main.ID + `","agent_profile_id":"` + leadID + `","steps":[` +
		`{"verb":"join","agent_id":"` + workerID + `"}]}`
	code, prob := postPlan(t, mux, wsID, body)
	if code != http.StatusBadRequest {
		t.Fatalf("未知 verb status = %d（期望 400）: %v", code, prob)
	}
	if prob["code"] != "validation_failed" {
		t.Fatalf("problem code = %v", prob["code"])
	}
}

// TestPlanEndpointsDeferNoOutlet400 defer 无出口 → 400；plan 不落库。
func TestPlanEndpointsDeferNoOutlet400(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, leadID, _ := seedPlanHTTPEnv(t, s)
	main, err := s.svc.CreateWorkItem(context.Background(), wsID, application.CreateWorkItemParams{Title: "主任务"})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"work_item_id":"` + main.ID + `","agent_profile_id":"` + leadID + `","steps":[` +
		`{"verb":"defer","reason":"等子任务"}]}`
	code, prob := postPlan(t, mux, wsID, body)
	if code != http.StatusBadRequest {
		t.Fatalf("defer 无出口 status = %d（期望 400）: %v", code, prob)
	}
}

// TestPlanEndpointsLatestForWorkItem GET /work-items/{id}/plan：有 plan 返回
// 最新一份（supersede 后为新 plan）；无 plan 404 problem+json。
func TestPlanEndpointsLatestForWorkItem(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, leadID, workerID := seedPlanHTTPEnv(t, s)
	main, err := s.svc.CreateWorkItem(context.Background(), wsID, application.CreateWorkItemParams{Title: "主任务"})
	if err != nil {
		t.Fatal(err)
	}

	// 无 plan → 404 problem。
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/work-items/"+main.ID+"/plan", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("无 plan status = %d（期望 404）: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not_found") {
		t.Fatalf("404 应为 problem+json: %s", rec.Body.String())
	}

	// supersede 后返回最新 plan。
	first := `{"work_item_id":"` + main.ID + `","agent_profile_id":"` + leadID + `","steps":[` +
		`{"verb":"dispatch","agent_id":"` + workerID + `","title":"A","instruction":"做 A"},` +
		`{"verb":"defer","reason":"等子任务"}]}`
	if code, plan := postPlan(t, mux, wsID, first); code != http.StatusCreated {
		t.Fatalf("plan A status = %d: %v", code, plan)
	}
	second := `{"work_item_id":"` + main.ID + `","agent_profile_id":"` + leadID + `","steps":[` +
		`{"verb":"finish","summary":"收口"}]}`
	code, planB := postPlan(t, mux, wsID, second)
	if code != http.StatusCreated {
		t.Fatalf("plan B status = %d: %v", code, planB)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/work-items/"+main.ID+"/plan", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("有 plan status = %d: %s", rec.Code, rec.Body.String())
	}
	var latest map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &latest); err != nil {
		t.Fatal(err)
	}
	if latest["id"] != planB["id"] {
		t.Fatalf("最新 plan = %v，应为 supersede 后的 %v", latest["id"], planB["id"])
	}
}

// TestPlanEndpointsTreeAndParentFilter 树端点先序含 parent_id；listWorkItems
// parent_id=none 只看根任务。
func TestPlanEndpointsTreeAndParentFilter(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, leadID, workerID := seedPlanHTTPEnv(t, s)
	main, err := s.svc.CreateWorkItem(context.Background(), wsID, application.CreateWorkItemParams{Title: "主任务"})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"work_item_id":"` + main.ID + `","agent_profile_id":"` + leadID + `","steps":[` +
		`{"verb":"dispatch","agent_id":"` + workerID + `","title":"A","instruction":"做 A"}]}`
	if code, plan := postPlan(t, mux, wsID, body); code != http.StatusCreated {
		t.Fatalf("创建 plan status = %d: %v", code, plan)
	}

	treeReq := httptest.NewRequest(http.MethodGet, "/api/v1/work-items/"+main.ID+"/tree", nil)
	treeRec := httptest.NewRecorder()
	mux.ServeHTTP(treeRec, treeReq)
	if treeRec.Code != http.StatusOK {
		t.Fatalf("tree status = %d", treeRec.Code)
	}
	var tree struct {
		Items []struct {
			ID       string `json:"id"`
			ParentID string `json:"parent_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(treeRec.Body.Bytes(), &tree); err != nil {
		t.Fatal(err)
	}
	if len(tree.Items) != 2 || tree.Items[0].ID != main.ID || tree.Items[1].ParentID != main.ID {
		t.Fatalf("树结构异常: %s", treeRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+wsID+"/work-items?parent_id=none", nil)
	listRec := httptest.NewRecorder()
	mux.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d", listRec.Code)
	}
	if strings.Contains(listRec.Body.String(), `"parent_id"`) {
		t.Fatalf("parent_id=none 不应返回子任务: %s", listRec.Body.String())
	}
	if !strings.Contains(listRec.Body.String(), main.ID) {
		t.Fatalf("根任务缺失: %s", listRec.Body.String())
	}
}

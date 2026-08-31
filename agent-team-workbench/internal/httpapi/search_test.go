// search_test.go FTS 检索端点的 HTTP 契约测试（会话元模型 S4）：
// GET /api/v1/workspaces/{id}/search 的形状、过滤参数与空 query 语义。
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
)

// getSearchJSONWith 请求检索端点并解析 JSON 响应。
func getSearchJSONWith(t *testing.T, mux http.Handler, path string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
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

// TestSearchEndpoint 防回归：索引链路写入后端点可命中；空 q 返回空 items
// （不 500）；未知 workspace 404；kind 过滤生效。
func TestSearchEndpoint(t *testing.T) {
	ctx := context.Background()
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, _, _ := seedPlanHTTPEnv(t, s)
	wi, err := s.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "检索任务"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.svc.RecordDecision(ctx, wi.ID, application.RecordDecisionParams{
		Quote: "Chose SQLite for the storage layer.",
	}); err != nil {
		t.Fatal(err)
	}

	// 命中形状。
	code, body := getSearchJSONWith(t, mux, "/api/v1/workspaces/"+wsID+"/search?q=SQLite")
	if code != http.StatusOK {
		t.Fatalf("search 应 200，实际 %d: %v", code, body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("应命中 1 条: %v", body)
	}
	item := items[0].(map[string]any)
	for _, key := range []string{"kind", "work_item_id", "source_id", "title", "snippet"} {
		if _, ok := item[key]; !ok {
			t.Fatalf("命中项缺字段 %s: %v", key, item)
		}
	}
	if item["kind"] != "decision" || item["work_item_id"] != wi.ID {
		t.Fatalf("命中项归属异常: %v", item)
	}
	for _, kind := range []string{"task", "chat", "other"} {
		code, _ = getSearchJSONWith(t, mux, "/api/v1/workspaces/"+wsID+"/search?q=SQLite&record_kind="+kind)
		if kind == "task" && code != http.StatusOK {
			t.Fatalf("record_kind=task 应 200，实际 %d", code)
		}
		if kind != "task" && code != http.StatusUnprocessableEntity {
			t.Fatalf("record_kind=%s 应响亮返回 422，实际 %d", kind, code)
		}
	}

	// kind 过滤：artifact 无命中。
	code, body = getSearchJSONWith(t, mux, "/api/v1/workspaces/"+wsID+"/search?q=SQLite&kind=artifact")
	if code != http.StatusOK {
		t.Fatalf("search 应 200，实际 %d", code)
	}
	if items, _ = body["items"].([]any); len(items) != 0 {
		t.Fatalf("artifact kind 应无命中: %v", body)
	}

	// 空 q / 纯符号 q → 空 items（不 500）。
	for _, q := range []string{"", "%20", "@@@(*"} {
		code, body = getSearchJSONWith(t, mux, "/api/v1/workspaces/"+wsID+"/search?q="+q)
		if code != http.StatusOK {
			t.Fatalf("q=%q 应 200，实际 %d", q, code)
		}
		if items, _ = body["items"].([]any); len(items) != 0 {
			t.Fatalf("q=%q 应空结果: %v", q, body)
		}
	}

	// 未知 workspace → 404。
	if code, _ := getSearchJSONWith(t, mux, "/api/v1/workspaces/ws_missing/search?q=x"); code != http.StatusNotFound {
		t.Fatalf("未知 workspace 应 404，实际 %d", code)
	}
}

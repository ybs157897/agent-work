// handlers_entity_idempotency_test.go 实体级 client_key 的 HTTP 契约防回归：
// 同 client_key 重复创建返回 200 + Idempotent-Replayed + 同一实体；parent_id 透传落库。
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// postWorkItem 带独立 Idempotency-Key 提交创建（规避命令级重放，专测实体级语义）。
func postWorkItem(t *testing.T, mux http.Handler, wsID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/v1/workspaces/%s/work-items", wsID), strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem-"+time.Now().Format("150405.000000000"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestCreateWorkItemClientKeyReplay(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, agentID, _ := seedPlanHTTPEnv(t, s)

	body := `{"title":"分叉会话","agent_profile_id":"` + agentID + `","client_key":"fork:wi_1:msg_1"}`
	first := postWorkItem(t, mux, wsID, body)
	if first.Code != http.StatusCreated {
		t.Fatalf("首次创建应 201，实际 %d: %s", first.Code, first.Body.String())
	}
	var firstBody map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatal(err)
	}

	second := postWorkItem(t, mux, wsID, body)
	if second.Code != http.StatusOK {
		t.Fatalf("同 client_key 重放应 200，实际 %d: %s", second.Code, second.Body.String())
	}
	if second.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("重放响应应带 Idempotent-Replayed: true，实际头: %v", second.Header())
	}
	var secondBody map[string]any
	if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil {
		t.Fatal(err)
	}
	if secondBody["id"] != firstBody["id"] {
		t.Fatalf("同 key 应返回同一实体: %v vs %v", firstBody["id"], secondBody["id"])
	}

	third := postWorkItem(t, mux, wsID,
		`{"title":"另一张卡","agent_profile_id":"`+agentID+`","client_key":"fork:wi_1:msg_2"}`)
	if third.Code != http.StatusCreated {
		t.Fatalf("不同 client_key 应新建 201，实际 %d", third.Code)
	}
}

func TestCreateWorkItemParentIDPassthrough(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, agentID, _ := seedPlanHTTPEnv(t, s)

	parent := postWorkItem(t, mux, wsID, `{"title":"父任务","agent_profile_id":"`+agentID+`"}`)
	if parent.Code != http.StatusCreated {
		t.Fatalf("父任务创建失败: %d", parent.Code)
	}
	var parentBody map[string]any
	if err := json.Unmarshal(parent.Body.Bytes(), &parentBody); err != nil {
		t.Fatal(err)
	}

	child := postWorkItem(t, mux, wsID,
		`{"title":"分叉会话","agent_profile_id":"`+agentID+`","parent_id":"`+parentBody["id"].(string)+`"}`)
	if child.Code != http.StatusCreated {
		t.Fatalf("合法 parent_id 应 201，实际 %d: %s", child.Code, child.Body.String())
	}
	var childBody map[string]any
	if err := json.Unmarshal(child.Body.Bytes(), &childBody); err != nil {
		t.Fatal(err)
	}
	if childBody["parent_id"] != parentBody["id"] {
		t.Fatalf("parent_id 应透传落库: 实际 %v", childBody["parent_id"])
	}

	orphan := postWorkItem(t, mux, wsID,
		`{"title":"孤儿","agent_profile_id":"`+agentID+`","parent_id":"wi_missing"}`)
	if orphan.Code != http.StatusUnprocessableEntity {
		t.Fatalf("不存在的 parent 应 422（ErrValidation 的既有映射），实际 %d", orphan.Code)
	}
}

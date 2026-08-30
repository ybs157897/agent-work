package codexapp

import (
	"encoding/json"
	"testing"
)

func TestApprovalResponsesMatchGeneratedV2SchemaShapes(t *testing.T) {
	params := approvalRequestParams{Permissions: map[string]any{
		"network": map[string]any{"enabled": true},
	}}
	cases := []struct {
		method   string
		approved bool
		want     string
	}{
		{"item/commandExecution/requestApproval", true, `{"decision":"accept"}`},
		{"item/commandExecution/requestApproval", false, `{"decision":"cancel"}`},
		{"item/fileChange/requestApproval", true, `{"decision":"accept"}`},
		{"item/permissions/requestApproval", false, `{"permissions":{},"scope":"turn"}`},
	}
	for _, tc := range cases {
		body, err := json.Marshal(approvalResponse(tc.method, tc.approved, params))
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != tc.want {
			t.Fatalf("%s approved=%v: %s != %s", tc.method, tc.approved, body, tc.want)
		}
	}
	approved := approvalResponse("item/permissions/requestApproval", true, params)
	permissions := approved["permissions"].(map[string]any)
	if permissions["network"] == nil {
		t.Fatalf("批准权限时必须只回传请求的权限子集: %#v", approved)
	}
}

func TestParseItemEventDistinguishesMessagesAndTools(t *testing.T) {
	message := parseItemEvent(json.RawMessage(`{"item":{"id":"m1","type":"agentMessage","text":"done"}}`))
	if message.isTool() || message.Text != "done" {
		t.Fatalf("message = %+v", message)
	}
	tool := parseItemEvent(json.RawMessage(`{"item":{"id":"c1","type":"commandExecution","status":"failed"}}`))
	if !tool.isTool() || tool.Tool != "shell" || toolCompletionEvent(tool) != "tool.failed" {
		t.Fatalf("tool = %+v", tool)
	}
	collab := parseItemEvent(json.RawMessage(`{"item":{"id":"a1","type":"collabAgentToolCall","tool":"spawn_agent","status":"completed","receiverThreadIds":["thread-child"],"agentsStates":{"thread-child":{"status":"running"}},"prompt":"计算 2+2"}}`))
	if !collab.isTool() || collab.Tool != "agent" || collab.argsSummary() != "spawn_agent: 计算 2+2" {
		t.Fatalf("collab = %+v summary=%q", collab, collab.argsSummary())
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(collab.resultOutput()), &result); err != nil || result["receiverThreadIds"] == nil || result["agentsStates"] == nil {
		t.Fatalf("collab result = %q", collab.resultOutput())
	}
}

func TestInitializeNegotiatesExperimentalAPI(t *testing.T) {
	params := initializeParams()
	capabilities := params["capabilities"].(map[string]any)
	if capabilities["experimentalApi"] != true {
		t.Fatalf("initialize capabilities = %#v", capabilities)
	}
	if initializedNotification()["method"] != "initialized" {
		t.Fatal("initialize response 后必须发送 initialized notification")
	}
}

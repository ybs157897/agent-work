package codexapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

type knowledgeToolWriteCloser struct{ bytes.Buffer }

func (knowledgeToolWriteCloser) Close() error { return nil }

func dynamicAccessInput() map[string]any {
	return map[string]any{
		"transport":    knowledgeDynamicTransport,
		"tool_schema":  knowledgeDynamicSchema,
		"access_file":  "/private/run.json",
		"token_digest": strings.Repeat("a", 64),
	}
}

func TestKnowledgeDynamicToolsRequireTrustedRunMarker(t *testing.T) {
	run := newRun(nil)
	if got := knowledgeDynamicTools(run); got != nil {
		t.Fatalf("unmarked Run received dynamic tools: %#v", got)
	}
	run.Input["knowledge_access"] = dynamicAccessInput()
	tools := knowledgeDynamicTools(run)
	if len(tools) != 1 {
		t.Fatalf("dynamic tool count = %d, want 1", len(tools))
	}
	tool := tools[0]
	if tool["type"] != "function" || tool["name"] != knowledgeDynamicTool {
		t.Fatalf("dynamic tool identity = %#v", tool)
	}
	schema, ok := tool["inputSchema"].(map[string]any)
	if !ok || schema["additionalProperties"] != false {
		t.Fatalf("dynamic tool schema is not strict: %#v", tool["inputSchema"])
	}
	properties, _ := schema["properties"].(map[string]any)
	for _, name := range []string{"action", "question", "content", "title", "publish_intent", "item_id", "version"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("dynamic tool schema missing %q: %#v", name, properties)
		}
	}
	encoded, err := json.Marshal(tools)
	if err != nil || !strings.Contains(string(encoded), `"inputSchema"`) {
		t.Fatalf("dynamic tool schema cannot be encoded: %v %s", err, encoded)
	}
}

func TestKnowledgeDynamicToolCallRejectsWrongThreadOrTurn(t *testing.T) {
	for name, params := range map[string]map[string]any{
		"wrong thread": {"callId": "call-1", "threadId": "other", "turnId": "turn-1", "tool": knowledgeDynamicTool, "arguments": map[string]any{"action": "ask", "question": "q"}},
		"wrong turn":   {"callId": "call-1", "threadId": "thread-1", "turnId": "other", "tool": knowledgeDynamicTool, "arguments": map[string]any{"action": "ask", "question": "q"}},
		"unknown tool": {"callId": "call-1", "threadId": "thread-1", "turnId": "turn-1", "tool": "atw_other", "arguments": map[string]any{"action": "ask", "question": "q"}},
	} {
		t.Run(name, func(t *testing.T) {
			var out knowledgeToolWriteCloser
			run := newRun(nil)
			run.Input["knowledge_access"] = dynamicAccessInput()
			stream := &execStream{
				ex:  &atwruntime.ExecContext{Ctx: context.Background(), Run: run, Callbacks: &recordCallbacks{}},
				ctx: context.Background(), stdin: &out, pendingRequests: map[int64]string{}, approvals: map[string]chan bool{},
				threadID: "thread-1", turnID: "turn-1",
			}
			paramsJSON, err := json.Marshal(params)
			if err != nil {
				t.Fatal(err)
			}
			id := int64(7)
			stream.handleKnowledgeDynamicTool(&rpcFrame{ID: &id, Params: paramsJSON})
			var response map[string]any
			if err := json.Unmarshal(out.Bytes(), &response); err != nil {
				t.Fatalf("invalid rejection response: %v %q", err, out.String())
			}
			if response["error"] == nil || response["result"] != nil {
				t.Fatalf("wrong thread/turn was accepted: %#v", response)
			}
		})
	}
}

func TestKnowledgeDynamicToolResponseFailsClosedWhenOutputTooLarge(t *testing.T) {
	var out knowledgeToolWriteCloser
	stream := &execStream{ctx: context.Background(), stdin: &out, pendingRequests: map[int64]string{}, approvals: map[string]chan bool{}}
	stream.sendDynamicToolResponse(9, true, strings.Repeat("x", maxDynamicToolOutputRunes+1))
	var response struct {
		Result struct {
			Success      bool `json:"success"`
			ContentItems []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"contentItems"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Result.Success || len(response.Result.ContentItems) != 1 || response.Result.ContentItems[0].Type != "inputText" || !strings.Contains(response.Result.ContentItems[0].Text, "过大") {
		t.Fatalf("oversized result did not fail closed: %#v", response)
	}
}

func TestKnowledgeDynamicToolsOmittedOnResume(t *testing.T) {
	run := newRun(nil)
	run.Input["knowledge_access"] = dynamicAccessInput()
	var out knowledgeToolWriteCloser
	stream := &execStream{
		ex: &atwruntime.ExecContext{Ctx: context.Background(), Run: run}, ctx: context.Background(), stdin: &out,
		module: New(Config{}), model: "gpt-fake", pendingRequests: map[int64]string{}, approvals: map[string]chan bool{},
	}
	if err := stream.requestThread(); err != nil {
		t.Fatal(err)
	}
	var fresh map[string]any
	if err := json.Unmarshal(out.Bytes(), &fresh); err != nil {
		t.Fatal(err)
	}
	params, _ := fresh["params"].(map[string]any)
	if params["dynamicTools"] == nil {
		t.Fatalf("fresh thread omitted dynamicTools: %#v", params)
	}
	out.Reset()
	stream.resumeThreadID = "thread-existing"
	if err := stream.requestThread(); err != nil {
		t.Fatal(err)
	}
	var resumed map[string]any
	if err := json.Unmarshal(out.Bytes(), &resumed); err != nil {
		t.Fatal(err)
	}
	params, _ = resumed["params"].(map[string]any)
	if _, present := params["dynamicTools"]; present {
		t.Fatalf("resume sent unsupported dynamicTools field: %#v", params)
	}
	if params["threadId"] != "thread-existing" {
		t.Fatalf("resume thread id mismatch: %#v", params)
	}
}

func TestExecuteKnowledgeDynamicToolCallUsesRunBoundRecordWithoutApproval(t *testing.T) {
	t.Setenv("CODEX_FAKE_DYNAMIC", "1")
	t.Setenv("CODEX_EXPECT_DYNAMIC", "1")
	t.Setenv("CODEX_FAKE_DYNAMIC_ACTION", "record")
	var gotAuth, gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode dynamic record body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"submission":{"id":"kss_fixture"},"curation_status":"received"}`))
	}))
	defer server.Close()

	run := newRun(nil)
	token := "codex-dynamic-test-token"
	accessPath := filepath.Join(t.TempDir(), "capability.json")
	accessURL := strings.TrimRight(server.URL, "/") + "/api/v1/knowledge-agent/runs/" + url.PathEscape(run.ID)
	access, err := json.Marshal(map[string]string{"url": accessURL, "token": token})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(accessPath, access, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(token))
	run.Input["knowledge_access"] = map[string]any{
		"transport":    knowledgeDynamicTransport,
		"tool_schema":  knowledgeDynamicSchema,
		"access_file":  accessPath,
		"token_digest": hex.EncodeToString(digest[:]),
	}

	m := newTestModule(t)
	ctl := make(chan atwruntime.Control, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cb := &recordCallbacks{ctl: ctl}
	r := &execRunner{m: m, cb: cb, ctl: ctl, cancel: cancel, done: make(chan atwruntime.ExecResult, 1)}
	go func() {
		r.done <- m.Execute(&atwruntime.ExecContext{
			Ctx: ctx, Run: run, Instruction: atwruntime.EffectiveInstruction(run),
			Session: atwruntime.SessionState{}, Callbacks: cb, Controls: ctl,
		})
	}()
	res := r.wait(t, 15*time.Second)
	if res.Outcome != atwruntime.OutcomeSucceeded {
		t.Fatalf("dynamic knowledge tool Run failed: %s (%+v), events=%v", res.Outcome, res.Failure, cb.eventNames())
	}
	if gotAuth != "Bearer "+token || !strings.HasSuffix(gotPath, "/api/v1/knowledge-agent/runs/"+run.ID+"/records") {
		t.Fatalf("dynamic tool did not use Run-bound auth/path: auth=%q path=%q", gotAuth, gotPath)
	}
	if gotBody["content"] != "fixture confirmed requirement" || gotBody["title"] != "fixture requirement" || gotBody["agent_id"] != nil || gotBody["workspace_id"] != nil {
		t.Fatalf("dynamic tool escaped strict record contract: %#v", gotBody)
	}
	started, ok := cb.findEvent(domain.EventToolStarted)
	if !ok || started.data["tool"] != knowledgeDynamicTool || started.data["call_id"] != "dynamic_1" {
		t.Fatalf("dynamic tool start event missing: %+v", started)
	}
	completed, ok := cb.findEvent(domain.EventToolCompleted)
	if !ok || completed.data["tool"] != knowledgeDynamicTool || !strings.Contains(completed.data["output"].(string), "kss_fixture") {
		t.Fatalf("dynamic tool completion event missing result: %+v", completed)
	}
	cb.mu.Lock()
	approvalCount := len(cb.approvals)
	cb.mu.Unlock()
	if approvalCount != 0 {
		t.Fatalf("native knowledge tool unexpectedly requested shell approval: %d", approvalCount)
	}
}

func TestExecuteKnowledgeDynamicToolResumeOmitsUnsupportedDynamicToolsField(t *testing.T) {
	t.Setenv("CODEX_EXPECT_RESUME", "1")
	t.Setenv("CODEX_EXPECT_NO_DYNAMIC_ON_RESUME", "1")
	run := newRun(nil)
	run.Input["knowledge_access"] = dynamicAccessInput()
	m := newTestModule(t)
	ctl := make(chan atwruntime.Control, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cb := &recordCallbacks{ctl: ctl}
	r := &execRunner{m: m, cb: cb, ctl: ctl, cancel: cancel, done: make(chan atwruntime.ExecResult, 1)}
	go func() {
		r.done <- m.Execute(&atwruntime.ExecContext{
			Ctx: ctx, Run: run, Instruction: atwruntime.EffectiveInstruction(run),
			Session: atwruntime.SessionState{Ref: "codex://thread-existing"}, Callbacks: cb, Controls: ctl,
		})
	}()
	res := r.wait(t, 15*time.Second)
	if res.Outcome != atwruntime.OutcomeSucceeded {
		t.Fatalf("resume with persisted dynamic tools failed: %s (%+v), events=%v", res.Outcome, res.Failure, cb.eventNames())
	}
}

func TestExecuteKnowledgeDynamicToolCancellationStopsHTTPWait(t *testing.T) {
	t.Setenv("CODEX_FAKE_DYNAMIC", "1")
	t.Setenv("CODEX_EXPECT_DYNAMIC", "1")
	t.Setenv("CODEX_FAKE_DYNAMIC_ACTION", "ask")
	started := make(chan struct{})
	cancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		select {
		case <-started:
		default:
			close(started)
		}
		<-r.Context().Done()
		select {
		case <-cancelled:
		default:
			close(cancelled)
		}
	}))
	defer server.Close()

	run := newRun(nil)
	token := "codex-dynamic-cancel-token"
	accessPath := filepath.Join(t.TempDir(), "capability.json")
	accessURL := strings.TrimRight(server.URL, "/") + "/api/v1/knowledge-agent/runs/" + url.PathEscape(run.ID)
	access, err := json.Marshal(map[string]string{"url": accessURL, "token": token})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(accessPath, access, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(token))
	run.Input["knowledge_access"] = map[string]any{
		"transport":    knowledgeDynamicTransport,
		"tool_schema":  knowledgeDynamicSchema,
		"access_file":  accessPath,
		"token_digest": hex.EncodeToString(digest[:]),
	}

	m := newTestModule(t)
	ctl := make(chan atwruntime.Control, 8)
	ctx, cancel := context.WithCancel(context.Background())
	cb := &recordCallbacks{ctl: ctl}
	r := &execRunner{m: m, cb: cb, ctl: ctl, cancel: cancel, done: make(chan atwruntime.ExecResult, 1)}
	go func() {
		r.done <- m.Execute(&atwruntime.ExecContext{
			Ctx: ctx, Run: run, Instruction: atwruntime.EffectiveInstruction(run),
			Session: atwruntime.SessionState{}, Callbacks: cb, Controls: ctl,
		})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("dynamic knowledge request did not reach HTTP bridge")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("Run cancellation did not cancel the knowledge HTTP request")
	}
	res := r.wait(t, 15*time.Second)
	if res.Outcome != atwruntime.OutcomeInterrupted {
		t.Fatalf("cancelled dynamic knowledge Run outcome = %s (%+v), events=%v", res.Outcome, res.Failure, cb.eventNames())
	}
}

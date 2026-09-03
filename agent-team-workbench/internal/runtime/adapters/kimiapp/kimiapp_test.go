// kimiapp_test.go — 用回放桩 kap-server（REST envelope + WS 事件流）固化
// kimiapp adapter 的协议行为：fresh/resume（命中/40401）、审批、取消（REST
// abort）、usage 累计、steering（prompts + ::steer）、ping/pong、错误分类。
// 帧格式与 packages/protocol（ws-control/events/error-codes）一致。
package kimiapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

const testToken = "unit-test-token"

// ── 回放桩 kap-server ─────────────────────────────────────────────────

type kapCall struct {
	Path string
	Body map[string]any
}

type fakeKap struct {
	t   *testing.T
	srv *httptest.Server

	mu            sync.Mutex
	calls         []kapCall
	sessions      map[string]bool
	modes         map[string]agentConfig
	nextSess      int
	nextPrmpt     int
	failCode      int // 非 0 时 prompt 提交返回该 envelope code
	ignoreProfile bool

	wsMu      sync.Mutex // 串行化 WS 写
	wsConn    *websocket.Conn
	ready     chan struct{} // 首次 subscribe 成功后关闭（push 前置条件）
	readyOnce sync.Once
	promptsCh chan string
	pongs     chan string
}

func newFakeKap(t *testing.T) *fakeKap {
	t.Helper()
	f := &fakeKap{
		t:         t,
		sessions:  map[string]bool{},
		modes:     map[string]agentConfig{},
		ready:     make(chan struct{}),
		promptsCh: make(chan string, 8),
		pongs:     make(chan string, 8),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeKap(w, http.StatusOK, 0, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/v1/ws", f.serveWS)
	mux.HandleFunc("/api/v1/", f.serveREST)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func writeKap(w http.ResponseWriter, status, code int, data any) {
	msg := "success"
	if code != 0 {
		msg = "error"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(restEnvelope{Code: code, Msg: msg, Data: mustJSON(data)})
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func (f *fakeKap) record(path string, body map[string]any) {
	f.mu.Lock()
	f.calls = append(f.calls, kapCall{Path: path, Body: body})
	f.mu.Unlock()
}

// waitCall 轮询等待指定前缀的 REST 调用并返回其 body。
func (f *fakeKap) waitCall(pathPrefix string) map[string]any {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for _, c := range f.calls {
			if strings.HasPrefix(c.Path, pathPrefix) {
				f.mu.Unlock()
				return c.Body
			}
		}
		f.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("未见 REST 调用 %s（calls=%+v）", pathPrefix, f.calls)
	return nil
}

func (f *fakeKap) callCount(pathPrefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c.Path, pathPrefix) {
			n++
		}
	}
	return n
}

// callExact 统计路径完全匹配的调用次数（区分 create 与子资源）。
func (f *fakeKap) callExact(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Path == path {
			n++
		}
	}
	return n
}

// serveREST 处理 /api/v1/ 业务路由（鉴权 + envelope）。
func (f *fakeKap) serveREST(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		writeKap(w, http.StatusUnauthorized, codeAuthFailed, nil)
		return
	}
	var body map[string]any
	if r.Body != nil {
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}
	}
	path := r.URL.Path
	f.record(path, body)
	switch {
	case r.Method == http.MethodPost && path == "/api/v1/sessions":
		f.mu.Lock()
		f.nextSess++
		id := fmt.Sprintf("s_%d", f.nextSess)
		f.sessions[id] = true
		f.mu.Unlock()
		writeKap(w, http.StatusOK, 0, map[string]any{"id": id, "busy": false})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/status"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/sessions/"), "/status")
		f.mu.Lock()
		mode, known := f.modes[id]
		f.mu.Unlock()
		if !known {
			mode = agentConfig{PermissionMode: "manual", SwarmMode: false}
		}
		writeKap(w, http.StatusOK, 0, map[string]any{
			"busy": false, "permission": mode.PermissionMode, "swarm_mode": mode.SwarmMode,
		})
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/api/v1/sessions/") && !strings.Contains(path, "/prompts") && !strings.Contains(path, ":abort") && !strings.Contains(path, "/approvals"):
		id := strings.TrimPrefix(path, "/api/v1/sessions/")
		f.mu.Lock()
		known := f.sessions[id]
		f.mu.Unlock()
		if !known {
			writeKap(w, http.StatusOK, codeSessionNotFound, nil)
			return
		}
		writeKap(w, http.StatusOK, 0, map[string]any{"id": id, "busy": false})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/profile"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/sessions/"), "/profile")
		config, _ := body["agent_config"].(map[string]any)
		mode := agentConfig{PermissionMode: "manual"}
		if permission, ok := config["permission_mode"].(string); ok {
			mode.PermissionMode = permission
		}
		if swarm, ok := config["swarm_mode"].(bool); ok {
			mode.SwarmMode = swarm
		}
		f.mu.Lock()
		if !f.ignoreProfile {
			f.modes[id] = mode
		}
		f.mu.Unlock()
		writeKap(w, http.StatusOK, 0, map[string]any{"updated": true})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/prompts"):
		f.mu.Lock()
		if f.failCode != 0 {
			code := f.failCode
			f.mu.Unlock()
			writeKap(w, http.StatusOK, code, nil)
			return
		}
		f.nextPrmpt++
		pid := fmt.Sprintf("p_%d", f.nextPrmpt)
		f.mu.Unlock()
		select {
		case f.promptsCh <- pid:
		default:
		}
		writeKap(w, http.StatusOK, 0, map[string]any{"prompt_id": pid, "status": "running"})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/prompts::steer"):
		writeKap(w, http.StatusOK, 0, map[string]any{"steered": true, "prompt_ids": []string{}})
	case r.Method == http.MethodPost && strings.Contains(path, ":abort"):
		writeKap(w, http.StatusOK, 0, map[string]any{"aborted": true})
	case r.Method == http.MethodPost && strings.Contains(path, "/approvals/"):
		writeKap(w, http.StatusOK, 0, map[string]any{"resolved": true, "resolved_at": "now"})
	default:
		writeKap(w, http.StatusNotFound, 0, nil)
	}
}

// serveWS 处理事件流升级：server_hello → subscribe/ack → 事件下发；读循环
// 兼收 pong（带 nonce 校验）。
func (f *fakeKap) serveWS(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		writeKap(w, http.StatusUnauthorized, codeAuthFailed, nil)
		return
	}
	conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	f.wsMu.Lock()
	f.wsConn = conn
	f.wsMu.Unlock()
	hello := map[string]any{
		"type": "server_hello", "timestamp": "now",
		"payload": map[string]any{
			"ws_connection_id": "ws_1", "protocol_version": 2,
			"heartbeat_ms": 60000, "max_event_buffer_size": 1000,
		},
	}
	if err := conn.WriteMessage(websocket.TextMessage, mustJSON(hello)); err != nil {
		return
	}
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var frame wsFrame
		if json.Unmarshal(raw, &frame) != nil {
			continue
		}
		switch frame.Type {
		case "subscribe":
			var sub subscribePayload
			_ = json.Unmarshal(frame.Payload, &sub)
			ack := map[string]any{
				"type": "ack", "id": frame.ID, "code": 0, "msg": "success",
				"payload": map[string]any{
					"accepted": sub.SessionIDs, "not_found": []string{}, "resync_required": []string{},
				},
			}
			if err := conn.WriteMessage(websocket.TextMessage, mustJSON(ack)); err != nil {
				return
			}
			f.readyOnce.Do(func() { close(f.ready) })
		case "pong":
			var pp pongPayload
			_ = json.Unmarshal(frame.Payload, &pp)
			select {
			case f.pongs <- pp.Nonce:
			default:
			}
		}
	}
}

// push 下发一帧事件（要求订阅已建立）。
func (f *fakeKap) push(frame map[string]any) {
	f.t.Helper()
	select {
	case <-f.ready:
	case <-time.After(3 * time.Second):
		f.t.Fatal("WS 订阅未建立")
	}
	f.wsMu.Lock()
	conn := f.wsConn
	f.wsMu.Unlock()
	if conn == nil || conn.WriteMessage(websocket.TextMessage, mustJSON(frame)) != nil {
		f.t.Logf("push %s: 连接不可用", frame["type"])
	}
}

// kapEvent 构造事件帧：durable 带 seq/epoch，volatile 带 volatile+offset。
func kapEvent(sessionID, evType string, payload map[string]any, seq int64, volatile bool) map[string]any {
	return kapEventForAgent(sessionID, evType, payload, seq, volatile, "main")
}

func kapEventForAgent(sessionID, evType string, payload map[string]any, seq int64, volatile bool, agentID string) map[string]any {
	payload["type"] = evType
	payload["sessionId"] = sessionID
	payload["agentId"] = agentID
	frame := map[string]any{
		"type": evType, "session_id": sessionID, "timestamp": "now",
		"payload": mustJSON(payload),
	}
	if volatile {
		frame["volatile"] = true
		frame["offset"] = seq
	} else {
		frame["seq"] = seq
		frame["epoch"] = "e1"
	}
	return frame
}

// pushHappyTurn 推一段标准 turn：started → thinking/delta → step.usage → tool → ended。
func pushHappyTurn(f *fakeKap, sessionID string, turn int64, promptID string, seqBase int64) {
	f.push(kapEvent(sessionID, "turn.started", map[string]any{"turnId": turn, "promptId": promptID}, seqBase, false))
	f.push(kapEvent(sessionID, "assistant.delta", map[string]any{"turnId": turn, "delta": "ALPHA"}, seqBase+1, true))
	f.push(kapEvent(sessionID, "thinking.delta", map[string]any{"turnId": turn, "delta": "THINK"}, seqBase+2, true))
	f.push(kapEvent(sessionID, "turn.step.completed", map[string]any{
		"turnId": turn, "step": 1,
		"usage": map[string]any{"inputOther": 100, "output": 20, "inputCacheRead": 8, "inputCacheCreation": 2},
	}, seqBase+3, false))
	f.push(kapEvent(sessionID, "turn.step.completed", map[string]any{
		"turnId": turn, "step": 2,
		"usage": map[string]any{"inputOther": 50, "output": 30, "inputCacheRead": 8, "inputCacheCreation": 0},
	}, seqBase+4, false))
	f.push(kapEvent(sessionID, "tool.call.started", map[string]any{
		"turnId": turn, "toolCallId": "tc_1", "name": "shell", "args": map[string]any{"cmd": "ls"},
	}, seqBase+5, false))
	f.push(kapEvent(sessionID, "tool.result", map[string]any{
		"turnId": turn, "toolCallId": "tc_1", "output": mustJSON("done"), "isError": false,
	}, seqBase+6, false))
	f.push(kapEvent(sessionID, "tool.call.started", map[string]any{
		"turnId": turn, "toolCallId": "tc_2", "name": "shell", "args": map[string]any{"cmd": "boom"},
	}, seqBase+7, false))
	f.push(kapEvent(sessionID, "tool.result", map[string]any{
		"turnId": turn, "toolCallId": "tc_2", "output": mustJSON("boom"), "isError": true,
	}, seqBase+8, false))
	f.push(kapEvent(sessionID, "turn.ended", map[string]any{"turnId": turn, "reason": "completed"}, seqBase+9, false))
}

// ── 回调记录桩 ────────────────────────────────────────────────────────

type recordedEvent struct {
	kind string
	data map[string]any
}

type approvalReq struct {
	id, kind, risk, summary string
}

type recordCallbacks struct {
	mu        sync.Mutex
	events    []recordedEvent
	sessions  []runtime.SessionUpdate
	usages    []runtime.Usage
	logs      []string
	approvals chan approvalReq
}

func newRecordCallbacks() *recordCallbacks {
	return &recordCallbacks{approvals: make(chan approvalReq, 8)}
}

func (c *recordCallbacks) OnEvent(kind string, data map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, recordedEvent{kind, data})
}
func (c *recordCallbacks) OnProgress(float64) {}
func (c *recordCallbacks) OnLog(stream, line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, stream+" "+line)
}
func (c *recordCallbacks) OnSpawn(pid, pgid int) {}
func (c *recordCallbacks) OnUsage(u runtime.Usage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.usages = append(c.usages, u)
}
func (c *recordCallbacks) OnSession(u runtime.SessionUpdate) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions = append(c.sessions, u)
}
func (c *recordCallbacks) RequestApproval(kind, risk, summary string) string {
	req := approvalReq{id: "eng_" + kind, kind: kind, risk: risk, summary: summary}
	c.approvals <- req
	return req.id
}

// usageFrames 取 OnUsage 过程观测帧（按到达序的快照副本）。
func (c *recordCallbacks) usageFrames() []runtime.Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]runtime.Usage(nil), c.usages...)
}

func (c *recordCallbacks) find(kind string) (recordedEvent, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if e.kind == kind {
			return e, true
		}
	}
	return recordedEvent{}, false
}

func (c *recordCallbacks) count(kind string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, e := range c.events {
		if e.kind == kind {
			n++
		}
	}
	return n
}

// ── 用例 ──────────────────────────────────────────────────────────────

func newTestExec(ctx context.Context, ref string, cb runtime.Callbacks, controls chan runtime.Control) *runtime.ExecContext {
	return &runtime.ExecContext{
		Ctx:         ctx,
		Run:         &domain.ExecutionRun{ID: "run_test", AgentProfileID: "agent_test", AdapterID: "kimi-appserver"},
		Resolved:    domain.ResolvedExecutionContext{CWD: os.TempDir(), AuthorizedRoot: os.TempDir()},
		Instruction: "本轮指令：记住 ALPHA",
		Session:     runtime.SessionState{Ref: ref},
		Callbacks:   cb,
		Controls:    controls,
	}
}

func sameLegacyUsage(left, right runtime.Usage) bool {
	return left.InputTokens == right.InputTokens && left.OutputTokens == right.OutputTokens &&
		left.CachedTokens == right.CachedTokens && left.Basis == right.Basis
}

func newTestModule(f *fakeKap) *Module {
	return New(Config{
		BaseURL: f.srv.URL, Token: testToken,
		Model: "test-model",
	})
}

func TestShouldApplyKimiModelSnapshotAllowsLocalDefaultModel(t *testing.T) {
	tests := []struct {
		name string
		snap runtime.ModelSnapshot
		want bool
	}{
		{name: "empty snapshot", snap: runtime.ModelSnapshot{}, want: false},
		{name: "local kimi account", snap: runtime.ModelSnapshot{Provider: "kimi"}, want: false},
		{name: "explicit local model", snap: runtime.ModelSnapshot{Provider: "kimi", Model: "kimi-test"}, want: true},
		{name: "invalid custom provider remains visible", snap: runtime.ModelSnapshot{Provider: "custom"}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldApplyKimiModelSnapshot(tt.snap); got != tt.want {
				t.Fatalf("shouldApplyKimiModelSnapshot() = %v, want %v", got, tt.want)
			}
		})
	}
}

// runKapExecute 起 Execute，prompt 到达后执行 script（可推帧/发控制），再收结果。
func runKapExecute(t *testing.T, m *Module, ex *runtime.ExecContext, f *fakeKap, script func(pid string)) runtime.ExecResult {
	t.Helper()
	done := make(chan runtime.ExecResult, 1)
	go func() { done <- m.Execute(ex) }()
	var pid string
	select {
	case pid = <-f.promptsCh:
	case <-time.After(5 * time.Second):
		t.Fatal("未见 prompt 提交")
	}
	if script != nil {
		script(pid)
	}
	select {
	case r := <-done:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("Execute 超时未返回")
		return runtime.ExecResult{}
	}
}

func TestManifestCapabilities(t *testing.T) {
	m, err := New(Config{}).Manifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m.AdapterID != "kimi-appserver" || m.AdapterVersion != "1.0.0" ||
		m.Protocol.Name != "kimi-kap-server" || m.Protocol.Version != "2" ||
		m.SchemaDigest != "sha256:kimi-kap-server-v2" {
		t.Fatalf("manifest 标识漂移: %+v", m)
	}
	want := map[string]runtime.CapabilityLevel{
		"streaming":                               runtime.CapSupported,
		runtime.CapabilityStructuredTransport:     runtime.CapSupported,
		runtime.CapabilitySchemaConstrainedOutput: runtime.CapUnavailable,
		runtime.CapabilityControlToolCall:         runtime.CapUnavailable,
		"multi_turn":                              runtime.CapSupported,
		"resume":                                  runtime.CapSupported,
		"steering":                                runtime.CapSupported,
		"approval":                                runtime.CapSupported,
		"subagents":                               runtime.CapSupported,
		"swarm":                                   runtime.CapSupported,
		"interrupt":                               runtime.CapSupported,
		"workspace_files":                         runtime.CapSupported,
		"system_prompt":                           runtime.CapAdapterTranslated,
		"modes":                                   runtime.CapAdapterTranslated,
		"permissions":                             runtime.CapAdapterTranslated,
		"multi_vendor":                            runtime.CapAdapterTranslated,
		"structured_output":                       runtime.CapAdapterTranslated,
		"terminal":                                runtime.CapUnavailable,
	}
	if len(m.Capabilities) != len(want) {
		t.Fatalf("capabilities 键集漂移: %+v", m.Capabilities)
	}
	for key, level := range want {
		if m.Capabilities[key] != level {
			t.Errorf("capability %s = %s, want %s", key, m.Capabilities[key], level)
		}
	}
}

func TestFreshTurnHappyPath(t *testing.T) {
	f := newFakeKap(t)
	m := newTestModule(f)
	cb := newRecordCallbacks()

	res := runKapExecute(t, m, newTestExec(context.Background(), "", cb, make(chan runtime.Control, 8)), f,
		func(pid string) { pushHappyTurn(f, "s_1", 7, pid, 1) })

	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望成功，得到 %s（%+v）", res.Outcome, res.Failure)
	}
	createBody := f.waitCall("/api/v1/sessions")
	meta, _ := createBody["metadata"].(map[string]any)
	// metadata.cwd 必须来自 ExecContext.Resolved（fixture 用 os.TempDir）。
	if meta == nil || meta["cwd"] != os.TempDir() {
		t.Fatalf("create metadata.cwd 不符（应取 Resolved.CWD）: %v", createBody)
	}
	if _, ok := createBody["agent_config"]; ok {
		t.Fatalf("create.agent_config 在 KAP 中是 no-op，不应发送: %v", createBody)
	}
	profileBody := f.waitCall("/api/v1/sessions/s_1/profile")
	profileConfig, _ := profileBody["agent_config"].(map[string]any)
	if profileConfig == nil || profileConfig["permission_mode"] != "yolo" || profileConfig["swarm_mode"] != true {
		t.Fatalf("fresh 应通过 profile 证明 yolo + swarm 生效: %v", profileBody)
	}
	promptBody := f.waitCall("/api/v1/sessions/s_1/prompts")
	content, _ := promptBody["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("prompt content 不符: %v", promptBody)
	}
	part, _ := content[0].(map[string]any)
	if part["type"] != "text" || !strings.Contains(part["text"].(string), "记住 ALPHA") {
		t.Fatalf("prompt 文本不符: %v", part)
	}
	if promptBody["model"] != "test-model" || promptBody["permission_mode"] != "yolo" {
		t.Fatalf("prompt model/permission_mode 不符: %v", promptBody)
	}
	if _, ok := promptBody["swarm_mode"]; ok {
		t.Fatalf("prompt.swarm_mode 在 KAP 中是 no-op，不应发送: %v", promptBody)
	}
	for _, key := range []string{
		"schema", "output_schema", "outputSchema", "response_format",
		"tools", "tool_definitions", "toolDefinitions",
	} {
		if _, ok := promptBody[key]; ok {
			t.Fatalf("prompt.%s 当前 adapter 不应发送: %v", key, promptBody)
		}
	}
	if res.Session == nil || res.Session.Ref != "kimiapp://s_1" || res.Session.Params["kap_session"] != "s_1" {
		t.Fatalf("SessionUpdate 不符: %+v", res.Session)
	}
	if len(cb.sessions) < 1 || cb.sessions[0].Ref != "kimiapp://s_1" {
		t.Fatalf("OnSession 应在创建即报: %+v", cb.sessions)
	}
	// delta 契约：raw.chunk.{type,text}（前端 extractDeltaChunk）。
	delta, ok := cb.find(domain.EventMessageDelta)
	if !ok {
		t.Fatal("未见 message.delta")
	}
	raw, _ := delta.data["raw"].(map[string]any)
	chunk, _ := raw["chunk"].(map[string]any)
	if chunk["type"] != "text-delta" || chunk["text"] != "ALPHA" {
		t.Fatalf("delta chunk 契约不符: %v", delta.data)
	}
	if got := cb.count(domain.EventMessageDelta); got != 2 { // text + reasoning
		t.Fatalf("message.delta 期望 2（text+reasoning），得到 %d", got)
	}
	// message.completed 由 delta 累计合成。
	completed, ok := cb.find(domain.EventMessageCompleted)
	if !ok || completed.data["text"] != "ALPHA" {
		t.Fatalf("message.completed 不符: %+v", completed)
	}
	started, ok := cb.find(domain.EventToolStarted)
	if !ok {
		t.Fatal("未见 tool.started")
	}
	// 工具契约：tool/call_id + args_summary（args.command 提取）。
	if started.data["tool"] != "shell" || started.data["call_id"] != "tc_1" || started.data["args_summary"] != "ls" {
		t.Fatalf("tool.started 契约不符: %+v", started.data)
	}
	completedTool, ok := cb.find(domain.EventToolCompleted)
	if !ok {
		t.Fatal("未见 tool.completed")
	}
	if completedTool.data["call_id"] != "tc_1" || completedTool.data["output"] != "done" {
		t.Fatalf("tool.completed 契约不符: %+v", completedTool.data)
	}
	failedTool, ok := cb.find(domain.EventToolFailed) // isError=true → tool.failed
	if !ok {
		t.Fatal("未见 tool.failed")
	}
	if failedTool.data["call_id"] != "tc_2" || failedTool.data["output"] != "boom" {
		t.Fatalf("tool.failed 契约不符: %+v", failedTool.data)
	}
	// usage：两 step 累计（input=inputOther+cacheRead+cacheCreation；cached=cacheRead）。
	if res.Usage == nil {
		t.Fatal("未见 usage")
	}
	if res.Usage.InputTokens != 168 || res.Usage.OutputTokens != 50 || res.Usage.CachedTokens != 16 {
		t.Fatalf("usage 累计不符: %+v", res.Usage)
	}
	if res.Usage.Basis != runtime.UsagePerRun {
		t.Fatalf("usage basis 不符: %+v", res.Usage)
	}
	if res.Usage.ProviderReport == nil || res.Usage.Canonical == nil {
		t.Fatalf("per_run provider usage 应同时带 report/canonical: %+v", res.Usage)
	}
	if res.Usage.ProviderReport.Provenance.AgentID != "agent_test" {
		t.Fatalf("provider report 必须绑定控制面 Run agent，而非伪造 provider main: %+v", res.Usage.ProviderReport.Provenance)
	}
	got := res.Usage.ProviderReport.Counters
	if got.InputTokensTotal == nil || *got.InputTokensTotal != 168 ||
		got.InputUncachedTokens == nil || *got.InputUncachedTokens != 150 ||
		got.CacheReadTokens == nil || *got.CacheReadTokens != 16 ||
		got.CacheWriteTokens == nil || *got.CacheWriteTokens != 2 ||
		got.OutputTokens == nil || *got.OutputTokens != 50 {
		t.Fatalf("Kimi appserver 原生桶映射错误: %+v", got)
	}
	// OnUsage 过程观测：逐 step 上报累计值（终帧与 ExecResult.Usage 结算一致）。
	frames := cb.usageFrames()
	want := []runtime.Usage{
		{InputTokens: 110, OutputTokens: 20, CachedTokens: 8, Basis: runtime.UsagePerRun},
		{InputTokens: 168, OutputTokens: 50, CachedTokens: 16, Basis: runtime.UsagePerRun},
	}
	if len(frames) != len(want) {
		t.Fatalf("OnUsage 帧数不符（want %d）: %+v", len(want), frames)
	}
	for i, w := range want {
		if !sameLegacyUsage(frames[i], w) {
			t.Fatalf("OnUsage 第 %d 帧不符（累计值覆盖语义）: got %+v want %+v", i+1, frames[i], w)
		}
		if frames[i].ProviderReport == nil || frames[i].Canonical == nil {
			t.Fatalf("OnUsage 第 %d 帧缺少 provider/canonical usage: %+v", i+1, frames[i])
		}
	}
}

func pushPendingToolTurn(f *fakeKap, pid, reason string, calls ...map[string]any) {
	f.push(kapEvent("s_1", "turn.started", map[string]any{"turnId": 1, "promptId": pid}, 1, false))
	for i, call := range calls {
		f.push(kapEvent("s_1", "tool.call.started", call, int64(2+i), false))
	}
	f.push(kapEvent("s_1", "turn.ended", map[string]any{"turnId": 1, "reason": reason}, int64(2+len(calls)), false))
}

func TestPendingToolOnSuccessfulTurnIsInterruptedOnlyForDisplay(t *testing.T) {
	f := newFakeKap(t)
	m := newTestModule(f)
	cb := newRecordCallbacks()
	res := runKapExecute(t, m, newTestExec(context.Background(), "", cb, make(chan runtime.Control, 8)), f,
		func(pid string) {
			pushPendingToolTurn(f, pid, "completed", map[string]any{
				"turnId": 1, "toolCallId": "tc_pending", "name": "shell",
			})
		})
	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("pending tool 不应改变成功 turn outcome: %s (%+v)", res.Outcome, res.Failure)
	}
	failed, ok := cb.find(domain.EventToolFailed)
	if !ok || failed.data["call_id"] != "tc_pending" || failed.data["tool"] != "shell" ||
		failed.data["status"] != "interrupted" || failed.data["failure_reason"] != "turn_ended_before_tool_result" {
		t.Fatalf("pending tool synthetic terminal 不符: %+v", failed)
	}
}

func TestToolResultClosesPendingWithoutDuplicateTerminal(t *testing.T) {
	f := newFakeKap(t)
	m := newTestModule(f)
	cb := newRecordCallbacks()
	res := runKapExecute(t, m, newTestExec(context.Background(), "", cb, make(chan runtime.Control, 8)), f,
		func(pid string) {
			f.push(kapEvent("s_1", "turn.started", map[string]any{"turnId": 1, "promptId": pid}, 1, false))
			f.push(kapEvent("s_1", "tool.call.started", map[string]any{
				"turnId": 1, "toolCallId": "tc_done", "name": "shell",
			}, 2, false))
			result := map[string]any{"turnId": 1, "toolCallId": "tc_done", "output": mustJSON("ok"), "isError": false}
			f.push(kapEvent("s_1", "tool.result", result, 3, false))
			f.push(kapEvent("s_1", "tool.result", result, 4, false))
			f.push(kapEvent("s_1", "turn.ended", map[string]any{"turnId": 1, "reason": "completed"}, 5, false))
		})
	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望成功，得到 %s", res.Outcome)
	}
	if got := cb.count(domain.EventToolCompleted); got != 1 {
		t.Fatalf("重复 tool.result 不应重复 terminal，得到 %d", got)
	}
	if got := cb.count(domain.EventToolFailed); got != 0 {
		t.Fatalf("已完成 tool 不应在 turn ended 被 synthetic failed，得到 %d", got)
	}
}

func TestPendingToolsCloseAsFailedForFailedOrBlockedTurn(t *testing.T) {
	for _, reason := range []string{"failed", "blocked"} {
		t.Run(reason, func(t *testing.T) {
			f := newFakeKap(t)
			m := newTestModule(f)
			cb := newRecordCallbacks()
			res := runKapExecute(t, m, newTestExec(context.Background(), "", cb, make(chan runtime.Control, 8)), f,
				func(pid string) {
					pushPendingToolTurn(f, pid, reason, map[string]any{
						"turnId": 1, "toolCallId": "tc_fail", "name": "read",
					})
				})
			if res.Outcome != runtime.OutcomeFailed {
				t.Fatalf("%s turn 应失败，得到 %s", reason, res.Outcome)
			}
			failed, ok := cb.find(domain.EventToolFailed)
			if !ok || failed.data["status"] != "failed" || failed.data["tool"] != "read" {
				t.Fatalf("%s pending tool terminal 不符: %+v", reason, failed)
			}
		})
	}
}

func TestPendingToolCancelClosesAsInterrupted(t *testing.T) {
	f := newFakeKap(t)
	m := newTestModule(f)
	cb := newRecordCallbacks()
	res := runKapExecute(t, m, newTestExec(context.Background(), "", cb, make(chan runtime.Control, 8)), f,
		func(pid string) {
			pushPendingToolTurn(f, pid, "cancelled", map[string]any{
				"turnId": 1, "toolCallId": "tc_cancel", "name": "shell",
			})
		})
	if res.Outcome != runtime.OutcomeInterrupted {
		t.Fatalf("cancelled turn 应中断，得到 %s", res.Outcome)
	}
	failed, ok := cb.find(domain.EventToolFailed)
	if !ok || failed.data["status"] != "interrupted" {
		t.Fatalf("cancelled pending tool terminal 不符: %+v", failed)
	}
}

func TestMultiplePendingToolsEachCloseOnce(t *testing.T) {
	f := newFakeKap(t)
	m := newTestModule(f)
	cb := newRecordCallbacks()
	res := runKapExecute(t, m, newTestExec(context.Background(), "", cb, make(chan runtime.Control, 8)), f,
		func(pid string) {
			pushPendingToolTurn(f, pid, "completed",
				map[string]any{"turnId": 1, "toolCallId": "tc_a", "name": "read"},
				map[string]any{"turnId": 1, "toolCallId": "tc_b", "name": "grep"})
		})
	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望成功，得到 %s", res.Outcome)
	}
	if got := cb.count(domain.EventToolFailed); got != 2 {
		t.Fatalf("两个 pending tool 应各自产生一次 terminal，得到 %d", got)
	}
	for _, id := range []string{"tc_a", "tc_b"} {
		var found bool
		cb.mu.Lock()
		for _, event := range cb.events {
			if event.kind == domain.EventToolFailed && event.data["call_id"] == id {
				found = event.data["failure_reason"] == "turn_ended_before_tool_result"
				break
			}
		}
		cb.mu.Unlock()
		if !found {
			t.Fatalf("未找到 %s 的 synthetic terminal", id)
		}
	}
}

// 防回归：prompt 排队/resume 期间，同会话旧 turn 的 tool.*/approval 帧不得
// 归入本 run（只放行 activeTurn 的帧）。
func TestForeignTurnEventsDropped(t *testing.T) {
	f := newFakeKap(t)
	m := newTestModule(f)
	cb := newRecordCallbacks()

	res := runKapExecute(t, m, newTestExec(context.Background(), "", cb, make(chan runtime.Control, 8)), f,
		func(pid string) {
			// 本 turn 尚未 started（activeSeen=false）时旧 turn 的在途帧。
			f.push(kapEvent("s_1", "tool.call.started", map[string]any{
				"turnId": 98, "toolCallId": "tc_old", "name": "shell",
			}, 1, false))
			f.push(kapEvent("s_1", "event.approval.requested", map[string]any{
				"approval_id": "ap_old", "session_id": "s_1", "turn_id": 98,
				"tool_call_id": "tc_old", "tool_name": "shell", "action": "rm -rf /",
			}, 2, false))
			// 本 turn 开始后，排队中的旧 turn 帧仍会混入同一会话流。
			f.push(kapEvent("s_1", "turn.started", map[string]any{"turnId": 1, "promptId": pid}, 3, false))
			f.push(kapEvent("s_1", "tool.result", map[string]any{
				"turnId": 98, "toolCallId": "tc_old", "output": mustJSON("stale"),
			}, 4, false))
			f.push(kapEvent("s_1", "tool.call.started", map[string]any{
				"turnId": 1, "toolCallId": "tc_new", "name": "shell", "args": map[string]any{"command": "ls"},
			}, 5, false))
			f.push(kapEvent("s_1", "tool.result", map[string]any{
				"turnId": 1, "toolCallId": "tc_new", "output": mustJSON("fresh"),
			}, 6, false))
			f.push(kapEvent("s_1", "turn.ended", map[string]any{"turnId": 1, "reason": "completed"}, 7, false))
		})

	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望成功，得到 %s（%+v）", res.Outcome, res.Failure)
	}
	if got := cb.count(domain.EventToolStarted); got != 1 {
		t.Fatalf("旧 turn 的 tool.call.started 应被过滤，got %d", got)
	}
	if got := cb.count(domain.EventToolCompleted); got != 1 {
		t.Fatalf("旧 turn 的 tool.result 应被过滤，got %d", got)
	}
	select {
	case req := <-cb.approvals:
		t.Fatalf("旧 turn 的审批不应进入 engine: %+v", req)
	default:
	}
	started, _ := cb.find(domain.EventToolStarted)
	if started.data["call_id"] != "tc_new" || started.data["args_summary"] != "ls" {
		t.Fatalf("本 turn tool.started 契约不符: %+v", started.data)
	}
}

// tool.started 的 args 键（canonical 契约）：完整入参的紧凑 JSON 原文
// （≤maxToolArgs 截断），供前端 IN/OUT 展开卡还原参数；空参数或非对象/
// 数组形态不带键（args_summary 一行摘要无法还原完整入参）。
func TestToolStartedArgsPayload(t *testing.T) {
	f := newFakeKap(t)
	m := newTestModule(f)
	cb := newRecordCallbacks()

	res := runKapExecute(t, m, newTestExec(context.Background(), "", cb, make(chan runtime.Control, 8)), f,
		func(pid string) {
			f.push(kapEvent("s_1", "turn.started", map[string]any{"turnId": 1, "promptId": pid}, 1, false))
			f.push(kapEvent("s_1", "tool.call.started", map[string]any{
				"turnId": 1, "toolCallId": "tc_obj", "name": "shell",
				"args": map[string]any{"cmd": "ls", "workdir": "/tmp"},
			}, 2, false))
			f.push(kapEvent("s_1", "tool.call.started", map[string]any{
				"turnId": 1, "toolCallId": "tc_long", "name": "shell",
				"args": map[string]any{"data": strings.Repeat("y", 2200)},
			}, 3, false))
			f.push(kapEvent("s_1", "tool.call.started", map[string]any{
				"turnId": 1, "toolCallId": "tc_empty", "name": "shell",
			}, 4, false))
			f.push(kapEvent("s_1", "tool.call.started", map[string]any{
				"turnId": 1, "toolCallId": "tc_str", "name": "shell", "args": "grep foo",
			}, 5, false))
			f.push(kapEvent("s_1", "turn.ended", map[string]any{"turnId": 1, "reason": "completed"}, 6, false))
		})

	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望成功，得到 %s（%+v）", res.Outcome, res.Failure)
	}
	cb.mu.Lock()
	byID := map[string]recordedEvent{}
	for _, e := range cb.events {
		if e.kind == domain.EventToolStarted {
			id, _ := e.data["call_id"].(string)
			byID[id] = e
		}
	}
	cb.mu.Unlock()
	if len(byID) != 4 {
		t.Fatalf("tool.started 期望 4 帧，得到 %d: %+v", len(byID), byID)
	}
	// 正例：对象参数完整原文（帧线格式即紧凑 JSON）。
	if got := byID["tc_obj"].data["args"]; got != `{"cmd":"ls","workdir":"/tmp"}` {
		t.Fatalf("args 应为完整入参原文: %v", got)
	}
	// 边界：超 maxToolArgs 截断（字符串层，不保证截断后仍是合法 JSON）。
	if got, _ := byID["tc_long"].data["args"].(string); len(got) != 2000 {
		t.Fatalf("args 应截断到 2000，得到 %d", len(got))
	}
	// 边界：空参数与非对象/数组形态不带键。
	for _, id := range []string{"tc_empty", "tc_str"} {
		if _, has := byID[id].data["args"]; has {
			t.Fatalf("%s 不应携带 args 键: %+v", id, byID[id].data)
		}
	}
}

func TestWriteToolResultCarriesReliableChangeStats(t *testing.T) {
	f := newFakeKap(t)
	m := newTestModule(f)
	cb := newRecordCallbacks()

	res := runKapExecute(t, m, newTestExec(context.Background(), "", cb, make(chan runtime.Control, 8)), f,
		func(pid string) {
			f.push(kapEvent("s_1", "turn.started", map[string]any{"turnId": 1, "promptId": pid}, 1, false))
			f.push(kapEvent("s_1", "tool.call.started", map[string]any{
				"turnId": 1, "toolCallId": "tc_write", "name": "Write",
				"args": map[string]any{"path": "knowledge/prd/roadmap.md", "content": "body"},
			}, 2, false))
			f.push(kapEvent("s_1", "tool.result", map[string]any{
				"turnId": 1, "toolCallId": "tc_write",
				"output": mustJSON("Wrote 24,832 bytes to knowledge/prd/roadmap.md"), "isError": false,
			}, 3, false))
			f.push(kapEvent("s_1", "turn.ended", map[string]any{"turnId": 1, "reason": "completed"}, 4, false))
		})

	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望成功，得到 %s（%+v）", res.Outcome, res.Failure)
	}
	completed, ok := cb.find(domain.EventToolCompleted)
	if !ok {
		t.Fatal("未见 tool.completed")
	}
	stats, ok := completed.data["change_stats"].(map[string]any)
	if !ok {
		t.Fatalf("Write tool.completed 缺少 change_stats: %+v", completed.data)
	}
	if stats["operation"] != "write" || stats["files"] != 1 || stats["bytes"] != int64(24832) || stats["path"] != "knowledge/prd/roadmap.md" {
		t.Fatalf("change_stats 不符: %+v", stats)
	}
	if _, exists := stats["additions"]; exists {
		t.Fatalf("没有真实 diff 时不得伪造 additions: %+v", stats)
	}
	if _, exists := stats["deletions"]; exists {
		t.Fatalf("没有真实 diff 时不得伪造 deletions: %+v", stats)
	}
}

func TestCaptureFileBeforeDistinguishesMissingAndExistingEmpty(t *testing.T) {
	root := t.TempDir()
	missing, ok := captureFileBefore(root, "Write", json.RawMessage(`{"path":"new.txt","content":"x"}`))
	if !ok || missing.BeforeExists || missing.RelPath != "new.txt" || missing.Root != root {
		t.Fatalf("missing snapshot: %+v ok=%v", missing, ok)
	}
	if err := os.WriteFile(filepath.Join(root, "empty.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	empty, ok := captureFileBefore(root, "Edit", json.RawMessage(`{"path":"empty.txt","old_string":"","new_string":"x"}`))
	if !ok || !empty.BeforeExists || empty.Before != "" {
		t.Fatalf("empty snapshot: %+v ok=%v", empty, ok)
	}
}

func TestCaptureFileBeforeRejectsUnsafeAndUnpreviewableFiles(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, ok := captureFileBefore(root, "Write", json.RawMessage(`{"path":"../outside.txt"}`)); ok {
		t.Fatal("path traversal accepted")
	}
	if _, ok := captureFileBefore(root, "Write", json.RawMessage(`{"path":"link/out.txt"}`)); ok {
		t.Fatal("parent symlink accepted")
	}
	large := filepath.Join(root, "large.txt")
	if err := os.WriteFile(large, []byte(strings.Repeat("x", maxFileSnapshotBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := captureFileBefore(root, "Write", json.RawMessage(`{"path":"large.txt"}`)); ok {
		t.Fatal("large file snapshot accepted")
	}
	binary := filepath.Join(root, "binary.dat")
	if err := os.WriteFile(binary, []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := captureFileBefore(root, "Edit", json.RawMessage(`{"path":"binary.dat"}`)); ok {
		t.Fatal("binary snapshot accepted")
	}
}

// 防回归：persona/plan 语义只能靠 prompt 文本注入（kap 无 system_prompt 应用
// 通道、prompt.plan_mode 不应用）——fresh 会话首 prompt 带 persona，plan 模式
// 每个 prompt 带指令；resume 轮不重注 persona（会话上下文已含首轮注入）。
func TestPersonaAndPlanInjectedIntoPrompt(t *testing.T) {
	f := newFakeKap(t)
	m := newTestModule(f)
	cb := newRecordCallbacks()

	ex := newTestExec(context.Background(), "", cb, make(chan runtime.Control, 8))
	ex.Run.Input = map[string]any{"system_prompt": "你是代码评审员", "mode": "plan"}
	res := runKapExecute(t, m, ex, f, func(pid string) { pushHappyTurn(f, "s_1", 7, pid, 1) })
	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望成功，得到 %s（%+v）", res.Outcome, res.Failure)
	}
	promptBody := f.waitCall("/api/v1/sessions/s_1/prompts")
	content, _ := promptBody["content"].([]any)
	part, _ := content[0].(map[string]any)
	text, _ := part["text"].(string)
	if !strings.Contains(text, "你是代码评审员") || !strings.Contains(text, "本轮指令：记住 ALPHA") {
		t.Fatalf("persona 未注入 fresh 首 prompt: %q", text)
	}
	if !strings.Contains(text, "Plan mode") {
		t.Fatalf("plan 指令未注入: %q", text)
	}
	if _, ok := promptBody["plan_mode"]; ok {
		t.Fatalf("plan_mode 服务端不应用，不应前向: %v", promptBody)
	}
}

func TestResumeTurnSkipsPersonaKeepsPlan(t *testing.T) {
	f := newFakeKap(t)
	f.mu.Lock()
	f.sessions["s_known"] = true
	f.mu.Unlock()
	m := newTestModule(f)
	cb := newRecordCallbacks()

	ex := newTestExec(context.Background(), "kimiapp://s_known", cb, make(chan runtime.Control, 8))
	ex.Run.Input = map[string]any{"system_prompt": "你是代码评审员", "mode": "plan"}
	res := runKapExecute(t, m, ex, f, func(pid string) { pushHappyTurn(f, "s_known", 3, pid, 1) })
	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望成功，得到 %s（%+v）", res.Outcome, res.Failure)
	}
	promptBody := f.waitCall("/api/v1/sessions/s_known/prompts")
	content, _ := promptBody["content"].([]any)
	part, _ := content[0].(map[string]any)
	text, _ := part["text"].(string)
	if strings.Contains(text, "你是代码评审员") {
		t.Fatalf("resume 轮不应重注 persona（会话上下文已含首轮注入）: %q", text)
	}
	if !strings.Contains(text, "Plan mode") {
		t.Fatalf("plan 指令每个 plan prompt 都带: %q", text)
	}
}

func TestResumeHitReusesSession(t *testing.T) {
	f := newFakeKap(t)
	f.mu.Lock()
	f.sessions["s_known"] = true
	f.mu.Unlock()
	m := newTestModule(f)
	cb := newRecordCallbacks()

	res := runKapExecute(t, m, newTestExec(context.Background(), "kimiapp://s_known", cb, make(chan runtime.Control, 8)), f,
		func(pid string) { pushHappyTurn(f, "s_known", 3, pid, 1) })

	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望成功，得到 %s（%+v）", res.Outcome, res.Failure)
	}
	if f.callExact("/api/v1/sessions") != 0 { // 无 create
		t.Fatalf("resume 命中不应 create，calls=%+v", f.calls)
	}
	if f.callExact("/api/v1/sessions/s_known") != 1 {
		t.Fatal("resume 应先 GET 探测原会话")
	}
	profileBody := f.waitCall("/api/v1/sessions/s_known/profile")
	profileConfig, _ := profileBody["agent_config"].(map[string]any)
	if profileConfig == nil || profileConfig["permission_mode"] != "yolo" || profileConfig["swarm_mode"] != true {
		t.Fatalf("resume 应在 prompt 前重申默认 profile: %v", profileBody)
	}
	if f.callExact("/api/v1/sessions/s_known/prompts") != 1 {
		t.Fatal("应向原会话提交 prompt")
	}
	if res.Session == nil || res.Session.Ref != "kimiapp://s_known" {
		t.Fatalf("resume 应沿用原 ref: %+v", res.Session)
	}
	if len(cb.sessions) == 0 || cb.sessions[0].Ref != "kimiapp://s_known" {
		t.Fatalf("resume 轮应重报同 ref: %+v", cb.sessions)
	}
}

func TestManualPolicyPreservesManualPermissionWithSwarm(t *testing.T) {
	f := newFakeKap(t)
	m := newTestModule(f)
	cb := newRecordCallbacks()
	ex := newTestExec(context.Background(), "", cb, make(chan runtime.Control, 8))
	ex.Run.Input = map[string]any{"policy": map[string]any{"approval_policy": "manual"}}
	res := runKapExecute(t, m, ex, f, func(pid string) { pushHappyTurn(f, "s_1", 7, pid, 1) })
	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望成功，得到 %s（%+v）", res.Outcome, res.Failure)
	}
	createBody := f.waitCall("/api/v1/sessions")
	if _, ok := createBody["agent_config"]; ok {
		t.Fatalf("manual fresh create 也不应发送 no-op agent_config: %v", createBody)
	}
	profileBody := f.waitCall("/api/v1/sessions/s_1/profile")
	profileConfig, _ := profileBody["agent_config"].(map[string]any)
	if profileConfig["permission_mode"] != "manual" || profileConfig["swarm_mode"] != true {
		t.Fatalf("manual profile 应保留且仍开启 swarm: %v", profileBody)
	}
	promptBody := f.waitCall("/api/v1/sessions/s_1/prompts")
	if promptBody["permission_mode"] != "manual" {
		t.Fatalf("manual prompt 配置不符: %v", promptBody)
	}
	if _, ok := promptBody["swarm_mode"]; ok {
		t.Fatalf("manual prompt 也不应发送 no-op swarm_mode: %v", promptBody)
	}
}

func TestSessionDefaultsFailClosedWhenProfileIsIgnored(t *testing.T) {
	f := newFakeKap(t)
	f.mu.Lock()
	f.sessions["s_ignored"] = true
	f.ignoreProfile = true
	f.mu.Unlock()
	failure := applySessionDefaults(
		context.Background(),
		newRestClient(f.srv.URL, testToken),
		"s_ignored",
		agentConfig{PermissionMode: "yolo", SwarmMode: true},
	)
	if failure == nil || failure.Code != "session_profile_not_applied" || failure.Family != runtime.FamilyConfig {
		t.Fatalf("profile no-op 应 fail closed: %+v", failure)
	}
}

func TestResumeMissFailsSessionUnknown(t *testing.T) {
	f := newFakeKap(t)
	m := newTestModule(f)
	cb := newRecordCallbacks()

	ex := newTestExec(context.Background(), "kimiapp://s_gone", cb, make(chan runtime.Control, 8))
	done := make(chan runtime.ExecResult, 1)
	go func() { done <- m.Execute(ex) }()
	select {
	case res := <-done:
		if res.Outcome != runtime.OutcomeFailed {
			t.Fatalf("期望失败，得到 %s", res.Outcome)
		}
		if res.Failure == nil || res.Failure.Family != runtime.FamilySessionUnknown || res.Failure.Retryable {
			t.Fatalf("期望 session_unknown 不可重试: %+v", res.Failure)
		}
		if res.Failure.Code != "resume_session_not_found" {
			t.Fatalf("code 不符: %+v", res.Failure)
		}
		if res.Session != nil {
			t.Fatalf("resume miss 不应上报 SessionUpdate: %+v", res.Session)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Execute 超时未返回")
	}
	if f.callExact("/api/v1/sessions") != 0 { // 不静默降级 create
		t.Fatalf("不应静默降级 create，calls=%+v", f.calls)
	}
	if f.callCount("/prompts") != 0 {
		t.Fatal("resume miss 不应提交 prompt")
	}
}

func TestApprovalResolveRoundtrip(t *testing.T) {
	f := newFakeKap(t)
	m := newTestModule(f)
	cb := newRecordCallbacks()
	controls := make(chan runtime.Control, 8)

	res := runKapExecute(t, m, newTestExec(context.Background(), "", cb, controls), f, func(pid string) {
		f.push(kapEvent("s_1", "turn.started", map[string]any{"turnId": 1, "promptId": pid}, 1, false))
		f.push(kapEvent("s_1", "event.approval.requested", map[string]any{
			"approval_id": "ap_1", "session_id": "s_1", "turn_id": 1,
			"tool_call_id": "tc_1", "tool_name": "shell", "action": "run rm -rf",
		}, 2, false))
		req := <-cb.approvals
		// 防回归：risk 字段曾是工具名（如 "shell"）；kap 审批一律登记 high。
		if req.kind != "tool" || req.risk != "high" || req.summary != "shell: run rm -rf" {
			t.Fatalf("审批请求不符: %+v", req)
		}
		controls <- runtime.Control{Kind: runtime.ControlApproval, ApprovalID: req.id, Approved: true}
		body := f.waitCall("/api/v1/sessions/s_1/approvals/ap_1")
		if body["decision"] != "approved" {
			t.Fatalf("审批决议 body 不符: %v", body)
		}
		f.push(kapEvent("s_1", "turn.ended", map[string]any{"turnId": 1, "reason": "completed"}, 3, false))
	})

	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望成功，得到 %s（%+v）", res.Outcome, res.Failure)
	}
}

func TestCancelPostsAbortAndInterrupts(t *testing.T) {
	f := newFakeKap(t)
	m := newTestModule(f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cb := newRecordCallbacks()

	res := runKapExecute(t, m, newTestExec(ctx, "", cb, make(chan runtime.Control, 8)), f, func(pid string) {
		f.push(kapEvent("s_1", "turn.started", map[string]any{"turnId": 1, "promptId": pid}, 1, false))
		f.push(kapEvent("s_1", "assistant.delta", map[string]any{"turnId": 1, "delta": "WORKING"}, 2, true))
		// 等 delta 落地证明事件泵已在运行（避开「取消落在 prompt 提交在途
		// 窗口」的竞态——该窗口由会话级兜底 abort 覆盖，非本用例靶点）。
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) && cb.count(domain.EventMessageDelta) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		if cb.count(domain.EventMessageDelta) == 0 {
			t.Fatal("事件泵未见 delta，未就绪")
		}
		cancel()
		f.waitCall("/api/v1/sessions/s_1/prompts/" + pid + ":abort")
		f.push(kapEvent("s_1", "turn.ended", map[string]any{"turnId": 1, "reason": "cancelled"}, 3, false))
	})

	if res.Outcome != runtime.OutcomeInterrupted {
		t.Fatalf("期望 interrupted，得到 %s（%+v）", res.Outcome, res.Failure)
	}
	if res.Session == nil || res.Session.Ref != "kimiapp://s_1" {
		t.Fatalf("取消也应保留会话锚点: %+v", res.Session)
	}
}

func TestSteerCallsSteerEndpoint(t *testing.T) {
	f := newFakeKap(t)
	m := newTestModule(f)
	cb := newRecordCallbacks()
	controls := make(chan runtime.Control, 8)

	res := runKapExecute(t, m, newTestExec(context.Background(), "", cb, controls), f, func(pid string) {
		f.push(kapEvent("s_1", "turn.started", map[string]any{"turnId": 1, "promptId": pid}, 1, false))
		controls <- runtime.Control{Kind: runtime.ControlInput, Instruction: "补充：改用 B 方案"}
		// steering 先提交第二个 prompt，再 ::steer 升级该 prompt。
		var steerPID string
		select {
		case steerPID = <-f.promptsCh:
		case <-time.After(3 * time.Second):
			t.Fatal("steering prompt 未提交")
		}
		body := f.waitCall("/api/v1/sessions/s_1/prompts::steer")
		ids, _ := body["prompt_ids"].([]any)
		if len(ids) != 1 || ids[0] != steerPID {
			t.Fatalf("::steer 应升级 steering prompt %s: %v", steerPID, body)
		}
		f.push(kapEvent("s_1", "turn.ended", map[string]any{"turnId": 1, "reason": "completed"}, 2, false))
	})

	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望成功，得到 %s（%+v）", res.Outcome, res.Failure)
	}
	if got := f.callExact("/api/v1/sessions/s_1/prompts"); got != 2 {
		t.Fatalf("prompt 提交期望 2（主+steer），得到 %d（calls=%+v）", got, f.calls)
	}
	if f.callExact("/api/v1/sessions/s_1/prompts::steer") != 1 {
		t.Fatal("应恰好调用一次 ::steer")
	}
}

func TestPingPongAnswered(t *testing.T) {
	f := newFakeKap(t)
	m := newTestModule(f)
	cb := newRecordCallbacks()

	res := runKapExecute(t, m, newTestExec(context.Background(), "", cb, make(chan runtime.Control, 8)), f, func(pid string) {
		// 服务端心跳 ping：客户端必须回同 nonce 的 pong，否则静默 2 周期被断。
		f.push(map[string]any{"type": "ping", "timestamp": "now", "payload": map[string]any{"nonce": "nonce-42"}})
		select {
		case got := <-f.pongs:
			if got != "nonce-42" {
				t.Fatalf("pong nonce 不符: %q", got)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("未见 pong")
		}
		f.push(kapEvent("s_1", "turn.started", map[string]any{"turnId": 1, "promptId": pid}, 1, false))
		f.push(kapEvent("s_1", "turn.ended", map[string]any{"turnId": 1, "reason": "completed"}, 2, false))
	})

	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望成功，得到 %s（%+v）", res.Outcome, res.Failure)
	}
}

func TestPromptErrorClassifiedTransient(t *testing.T) {
	f := newFakeKap(t)
	f.mu.Lock()
	f.failCode = codeInternalError
	f.mu.Unlock()
	m := newTestModule(f)
	cb := newRecordCallbacks()

	done := make(chan runtime.ExecResult, 1)
	go func() { done <- m.Execute(newTestExec(context.Background(), "", cb, make(chan runtime.Control, 8))) }()
	select {
	case res := <-done:
		if res.Outcome != runtime.OutcomeFailed {
			t.Fatalf("期望失败，得到 %s", res.Outcome)
		}
		if res.Failure == nil || res.Failure.Family != runtime.FamilyTransientUpstream || !res.Failure.Retryable {
			t.Fatalf("50001 应分类 transient_upstream 可重试: %+v", res.Failure)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Execute 超时未返回")
	}
}

func TestTurnErrorClassified(t *testing.T) {
	f := newFakeKap(t)
	m := newTestModule(f)
	cb := newRecordCallbacks()

	res := runKapExecute(t, m, newTestExec(context.Background(), "", cb, make(chan runtime.Control, 8)), f, func(pid string) {
		f.push(kapEvent("s_1", "turn.started", map[string]any{"turnId": 1, "promptId": pid}, 1, false))
		f.push(kapEvent("s_1", "turn.ended", map[string]any{
			"turnId": 1, "reason": "failed",
			"error": map[string]any{"code": "provider.rate_limit", "message": "rate limited", "retryable": false},
		}, 2, false))
	})

	if res.Outcome != runtime.OutcomeFailed {
		t.Fatalf("期望失败，得到 %s", res.Outcome)
	}
	if res.Failure == nil || res.Failure.Family != runtime.FamilyProviderQuota || res.Failure.Code != "provider.rate_limit" {
		t.Fatalf("provider 限流应分类 provider_quota: %+v", res.Failure)
	}
}

func TestStaleTurnEndIgnored(t *testing.T) {
	f := newFakeKap(t)
	m := newTestModule(f)
	cb := newRecordCallbacks()

	res := runKapExecute(t, m, newTestExec(context.Background(), "", cb, make(chan runtime.Control, 8)), f, func(pid string) {
		// resume 场景：在途旧 turn（turnId=9）的收尾先到，不应终结本轮。
		f.push(kapEvent("s_1", "turn.ended", map[string]any{"turnId": 9, "reason": "cancelled"}, 1, false))
		f.push(kapEvent("s_1", "turn.started", map[string]any{"turnId": 10, "promptId": pid}, 2, false))
		f.push(kapEvent("s_1", "turn.ended", map[string]any{"turnId": 9, "reason": "cancelled"}, 3, false))
		f.push(kapEvent("s_1", "turn.ended", map[string]any{"turnId": 10, "reason": "completed"}, 4, false))
	})

	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("旧 turn 收尾应被忽略，得到 %s（%+v）", res.Outcome, res.Failure)
	}
}

func TestChildEventsWithSameTurnIDCannotAffectMain(t *testing.T) {
	f := newFakeKap(t)
	m := newTestModule(f)
	cb := newRecordCallbacks()

	res := runKapExecute(t, m, newTestExec(context.Background(), "", cb, make(chan runtime.Control, 8)), f, func(pid string) {
		// KAP AgentSwarm members can reuse the parent's turnId (including 0).
		f.push(kapEvent("s_1", "turn.started", map[string]any{"turnId": 0, "promptId": pid}, 1, false))
		f.push(kapEventForAgent("s_1", "turn.started", map[string]any{"turnId": 0}, 2, false, "child-1"))
		f.push(kapEventForAgent("s_1", "assistant.delta", map[string]any{"turnId": 0, "delta": "child output"}, 3, true, "child-1"))
		f.push(kapEventForAgent("s_1", "thinking.delta", map[string]any{"turnId": 0, "delta": "child thought"}, 4, true, "child-1"))
		f.push(kapEventForAgent("s_1", "turn.step.completed", map[string]any{"turnId": 0, "usage": map[string]any{"inputOther": 99, "output": 99}}, 5, false, "child-1"))
		f.push(kapEventForAgent("s_1", "tool.call.started", map[string]any{"turnId": 0, "toolCallId": "child-tool", "name": "shell"}, 6, false, "child-1"))
		f.push(kapEventForAgent("s_1", "tool.result", map[string]any{"turnId": 0, "toolCallId": "child-tool", "output": mustJSON("child result")}, 7, false, "child-1"))
		f.push(kapEventForAgent("s_1", "turn.ended", map[string]any{"turnId": 0, "reason": "completed"}, 8, false, "child-1"))
		f.push(kapEventForAgent("s_1", "turn.started", map[string]any{"turnId": 0}, 14, false, "child-2"))
		f.push(kapEventForAgent("s_1", "tool.call.started", map[string]any{"turnId": 0, "toolCallId": "child-tool", "name": "read"}, 15, false, "child-2"))
		f.push(kapEventForAgent("s_1", "tool.result", map[string]any{"turnId": 0, "toolCallId": "child-tool", "output": mustJSON("child 2 result")}, 16, false, "child-2"))
		f.push(kapEventForAgent("s_1", "turn.ended", map[string]any{"turnId": 0, "reason": "completed"}, 17, false, "child-2"))
		f.push(kapEventForAgent("s_1", "turn.started", map[string]any{"turnId": 1}, 18, false, "child-1"))
		f.push(kapEventForAgent("s_1", "assistant.delta", map[string]any{"turnId": 1, "delta": "child second"}, 19, true, "child-1"))
		f.push(kapEventForAgent("s_1", "turn.ended", map[string]any{"turnId": 1, "reason": "completed"}, 20, false, "child-1"))
		// Session 级流无法仅凭 turnId 归属事件；缺 agentId 同样 fail closed。
		f.push(kapEventForAgent("s_1", "assistant.delta", map[string]any{"turnId": 0, "delta": "anonymous output"}, 9, true, ""))
		f.push(kapEventForAgent("s_1", "turn.ended", map[string]any{"turnId": 0, "reason": "completed"}, 10, false, ""))

		f.push(kapEvent("s_1", "assistant.delta", map[string]any{"turnId": 0, "delta": "main output"}, 11, true))
		f.push(kapEvent("s_1", "turn.step.completed", map[string]any{"turnId": 0, "usage": map[string]any{"inputOther": 3, "output": 2}}, 12, false))
		f.push(kapEvent("s_1", "turn.ended", map[string]any{"turnId": 0, "reason": "completed"}, 13, false))
	})

	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("子 agent 收尾不应提前终止主 turn，得到 %s（%+v）", res.Outcome, res.Failure)
	}
	if res.Usage == nil || res.Usage.InputTokens != 3 || res.Usage.OutputTokens != 2 {
		t.Fatalf("子 agent usage 不应污染主 turn: %+v", res.Usage)
	}
	if res.Usage.ProviderReport == nil || res.Usage.ProviderReport.Provenance.AgentID != "agent_test" {
		t.Fatalf("子 agent 用量不得伪装成主 agent report: %+v", res.Usage)
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()
	mainDelta, childText, childThinking := 0, 0, 0
	childTools := map[string]int{}
	completed := map[string]int{}
	for _, event := range cb.events {
		if event.kind == domain.EventMessageCompleted {
			id, _ := event.data["agent_id"].(string)
			completed[id]++
			if id == "child-1" && completed[id] == 2 && event.data["text"] != "child second" {
				t.Fatalf("child 第二轮 message 未重置: %+v", event.data)
			}
		}
		if event.kind == domain.EventMessageDelta {
			raw, _ := event.data["raw"].(map[string]any)
			chunk, _ := raw["chunk"].(map[string]any)
			if event.data["agent_id"] == "child-1" {
				if chunk["type"] == "reasoning-delta" {
					childThinking++
				} else {
					childText++
				}
			} else {
				mainDelta++
				if chunk["text"] != "main output" {
					t.Fatalf("main transcript 不符: %+v", event.data)
				}
			}
		}
		if event.kind == domain.EventToolStarted || event.kind == domain.EventToolCompleted || event.kind == domain.EventToolFailed {
			if event.data["call_id"] == "child-tool" {
				childTools[event.data["agent_id"].(string)]++
			}
		}
	}
	if mainDelta != 1 || childText != 2 || childThinking != 1 {
		t.Fatalf("主/子 transcript 数量不符: main=%d childText=%d childThinking=%d", mainDelta, childText, childThinking)
	}
	if childTools["child-1"] != 2 || childTools["child-2"] != 2 {
		t.Fatalf("相同 callId 的 child tool 未按 agent 隔离: %+v", childTools)
	}
	if completed["child-1"] != 2 {
		t.Fatalf("child 两轮应各自产生 completed: %+v", completed)
	}
}

func TestUnmappedFramesLogged(t *testing.T) {
	f := newFakeKap(t)
	m := newTestModule(f)
	cb := newRecordCallbacks()

	res := runKapExecute(t, m, newTestExec(context.Background(), "", cb, make(chan runtime.Control, 8)), f, func(pid string) {
		f.push(kapEvent("s_1", "prompt.submitted", map[string]any{"promptId": pid}, 1, false))
		f.push(kapEvent("s_1", "event.session.meta_updated", map[string]any{"title": "t"}, 2, false))
		f.push(kapEvent("s_1", "turn.started", map[string]any{"turnId": 1, "promptId": pid}, 3, false))
		f.push(kapEvent("s_1", "turn.ended", map[string]any{"turnId": 1, "reason": "completed"}, 4, false))
	})

	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望成功，得到 %s（%+v）", res.Outcome, res.Failure)
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()
	logged := 0
	for _, l := range cb.logs {
		if strings.Contains(l, "prompt.submitted") || strings.Contains(l, "event.session.meta_updated") {
			logged++
		}
	}
	if logged < 2 {
		t.Fatalf("未映射帧应显式 OnLog（得到 %d）: %+v", logged, cb.logs)
	}
}

func TestSubagentLifecycleProjectsSelfContainedSnapshots(t *testing.T) {
	cb := newRecordCallbacks()
	p := &eventPump{
		ex:    &runtime.ExecContext{Callbacks: cb},
		state: &turnState{pendingTools: map[string]string{"tc-swarm": "AgentSwarm", "tc-child": "Agent"}},
	}
	frames := []wsFrame{
		{Type: "subagent.spawned", Seq: 1, Payload: json.RawMessage(`{"subagentId":"sa-1","subagentName":"research","parentToolCallId":"tc-swarm","description":"查资料","swarmIndex":2,"runInBackground":true}`)},
		{Type: "subagent.suspended", Seq: 2, Payload: json.RawMessage(`{"subagentId":"sa-1","reason":"等待依赖"}`)},
		{Type: "subagent.completed", Seq: 3, Payload: json.RawMessage(`{"subagentId":"sa-1","resultSummary":"已完成"}`)},
	}
	for _, frame := range frames {
		p.handle(frame)
	}
	// cursor 边界重复投递同一 durable seq 时不重复写 canonical 事件。
	p.handle(frames[2])
	cb.mu.Lock()
	if len(cb.events) != 3 {
		t.Fatalf("期望 3 个快照，得到 %+v", cb.events)
	}
	for i, want := range []string{"queued", "waiting", "completed"} {
		if cb.events[i].kind != domain.EventSubagentUpdated || cb.events[i].data["runtime"] != "kimi" || cb.events[i].data["role"] != "member" || cb.events[i].data["status"] != want {
			t.Fatalf("快照 %d 契约不符: %+v", i, cb.events[i])
		}
		if cb.events[i].data["swarm_index"] != 2 || cb.events[i].data["parent_tool_call_id"] != "tc-swarm" {
			t.Fatalf("快照 %d 身份丢失: %+v", i, cb.events[i])
		}
		if i == 1 && cb.events[i].data["reason"] != "等待依赖" {
			t.Fatalf("suspended reason 丢失: %+v", cb.events[i].data)
		}
		if i == 2 && cb.events[i].data["summary"] != "已完成" {
			t.Fatalf("completed summary 丢失: %+v", cb.events[i].data)
		}
	}
	cb.mu.Unlock()
	p.handle(wsFrame{Type: "subagent.completed", Seq: 4, Payload: json.RawMessage(`{"subagentId":"unknown","resultSummary":"不应创建"}`)})
	p.handle(wsFrame{Type: "subagent.spawned", Seq: 5, Payload: json.RawMessage(`{"subagentId":"foreign","subagentName":"Agent","parentToolCallId":"old-call"}`)})
	cb.mu.Lock()
	if len(cb.events) != 3 {
		t.Fatalf("未知 lifecycle/旧 parent 不应创建快照: %+v", cb.events)
	}
	cb.mu.Unlock()
	p.handle(wsFrame{Type: "subagent.spawned", Seq: 6, Payload: json.RawMessage(`{"subagentId":"child-1","subagentName":"Agent","parentToolCallId":"tc-child","description":"普通 child"}`)})
	p.handle(wsFrame{Type: "subagent.failed", Seq: 7, Payload: json.RawMessage(`{"subagentId":"child-1","error":{"message":"boom"}}`)})
	cb.mu.Lock()
	if len(cb.events) != 5 || cb.events[4].data["role"] != "child" || cb.events[4].data["status"] != "failed" || cb.events[4].data["error"] != "boom" {
		t.Fatalf("普通 child 契约不符: %+v", cb.events[3])
	}
	cb.mu.Unlock()
}

func TestSubagentInvalidSwarmIndexDoesNotBecomeMember(t *testing.T) {
	cb := newRecordCallbacks()
	p := &eventPump{
		ex:    &runtime.ExecContext{Callbacks: cb},
		state: &turnState{pendingTools: map[string]string{"tc-swarm": "AgentSwarm"}},
	}
	p.handle(wsFrame{Type: "subagent.spawned", Seq: 1, Payload: json.RawMessage(`{"subagentId":"sa-zero","parentToolCallId":"tc-swarm","swarmIndex":0}`)})
	p.handle(wsFrame{Type: "subagent.spawned", Seq: 2, Payload: json.RawMessage(`{"subagentId":"sa-one","parentToolCallId":"tc-swarm","swarmIndex":1}`)})

	cb.mu.Lock()
	defer cb.mu.Unlock()
	if len(cb.events) != 2 {
		t.Fatalf("期望两个子 Agent 快照，得到 %+v", cb.events)
	}
	if cb.events[0].data["role"] != "child" {
		t.Fatalf("0-based swarmIndex 不得进入蜂巢: %+v", cb.events[0].data)
	}
	if _, ok := cb.events[0].data["swarm_index"]; ok {
		t.Fatalf("非法 swarmIndex 不应透传: %+v", cb.events[0].data)
	}
	if cb.events[1].data["role"] != "member" || cb.events[1].data["swarm_index"] != 1 {
		t.Fatalf("1-based swarmIndex 应成为蜂群成员: %+v", cb.events[1].data)
	}
}

func TestSwarmMetadataIncludesItemsAndResume(t *testing.T) {
	m := swarmMetadata("tc-1", "ignored", json.RawMessage(`{"description":"并行调研","items":["A","B"],"resume_agent_ids":{"sa-3":"C"}}`))
	if m["runtime"] != "kimi" || m["id"] != "tc-1" || m["title"] != "并行调研" || m["total"] != 3 {
		t.Fatalf("蜂群头契约不符: %+v", m)
	}
	items := m["items"].([]map[string]any)
	if items[0]["index"] != 1 || items[0]["description"] != "续接已有子 Agent" || items[2]["index"] != 3 || items[2]["description"] != "B" {
		t.Fatalf("蜂群占位项不符: %+v", items)
	}
}

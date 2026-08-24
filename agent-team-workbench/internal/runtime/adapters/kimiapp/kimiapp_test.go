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

	mu        sync.Mutex
	calls     []kapCall
	sessions  map[string]bool
	nextSess  int
	nextPrmpt int
	failCode  int // 非 0 时 prompt 提交返回该 envelope code

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
	payload["type"] = evType
	payload["sessionId"] = sessionID
	payload["agentId"] = "main"
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
		Run:         &domain.ExecutionRun{ID: "run_test", AdapterID: "kimi-appserver"},
		Instruction: "本轮指令：记住 ALPHA",
		Session:     runtime.SessionState{Ref: ref},
		Callbacks:   cb,
		Controls:    controls,
	}
}

func newTestModule(f *fakeKap) *Module {
	return New(Config{
		BaseURL: f.srv.URL, Token: testToken,
		WorkspaceRoot: "/tmp/atw-kimiapp-test", Model: "test-model",
	})
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
	if meta == nil || meta["cwd"] != "/tmp/atw-kimiapp-test" {
		t.Fatalf("create metadata.cwd 不符: %v", createBody)
	}
	if _, ok := createBody["agent_config"]; ok {
		t.Fatalf("agent_config 是服务端不应用的死透传，不应再发送: %v", createBody)
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
	if promptBody["model"] != "test-model" || promptBody["permission_mode"] != "auto" {
		t.Fatalf("prompt model/permission_mode 不符: %v", promptBody)
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
		if frames[i] != w {
			t.Fatalf("OnUsage 第 %d 帧不符（累计值覆盖语义）: got %+v want %+v", i+1, frames[i], w)
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

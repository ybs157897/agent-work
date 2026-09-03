// gateway_test.go — 用回放桩网关（health + 一元 RPC + mux 下行流）固化
// dsh 网关 adapter 的协议行为：fresh/resume、降级重建、在途旧 turn 防护、
// 取消、审批/提问回应、turn 错误分类。帧格式与 2026-08-23 wire spike 一致。
package dsh

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// ── 回放桩网关 ────────────────────────────────────────────────────────

type fakeCall struct {
	Method  string
	Payload map[string]any
}

type fakeRespond struct {
	RpcID string
	Value map[string]any
}

type fakeGateway struct {
	t         *testing.T
	srv       *httptest.Server
	mu        sync.Mutex
	calls     []fakeCall
	responds  []fakeRespond
	handlers  map[string]func(payload map[string]any) (any, *rpcWireError)
	wsConn    *websocket.Conn
	wsReady   chan struct{}
	respondCh chan fakeRespond
}

func newFakeGateway(t *testing.T) *fakeGateway {
	t.Helper()
	f := &fakeGateway{
		t:         t,
		handlers:  map[string]func(map[string]any) (any, *rpcWireError){},
		wsReady:   make(chan struct{}),
		respondCh: make(chan fakeRespond, 8),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // health 探活
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/api/")
		body, _ := io.ReadAll(r.Body)
		var req struct {
			RpcID   string          `json:"rpcId"`
			Payload json.RawMessage `json:"payload"`
		}
		_ = json.Unmarshal(body, &req)
		var payload map[string]any
		_ = json.Unmarshal(req.Payload, &payload)
		f.mu.Lock()
		f.calls = append(f.calls, fakeCall{Method: method, Payload: payload})
		h := f.handlers[method]
		f.mu.Unlock()
		resp := map[string]any{"type": "server-response", "rpcId": req.RpcID}
		if h != nil {
			if value, werr := h(payload); werr != nil {
				resp["result"] = map[string]any{"ok": false, "error": werr}
			} else {
				resp["result"] = map[string]any{"ok": true, "value": value}
			}
		} else {
			resp["result"] = map[string]any{"ok": true, "value": map[string]any{}}
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/api/respond", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			RpcID  string `json:"rpcId"`
			Result struct {
				Value json.RawMessage `json:"value"`
			} `json:"result"`
		}
		_ = json.Unmarshal(body, &req)
		var value map[string]any
		_ = json.Unmarshal(req.Result.Value, &value)
		fr := fakeRespond{RpcID: req.RpcID, Value: value}
		f.mu.Lock()
		f.responds = append(f.responds, fr)
		f.mu.Unlock()
		select {
		case f.respondCh <- fr:
		default:
		}
		_, _ = w.Write([]byte(`{"accepted":true}`))
	})
	mux.HandleFunc("/api/events.mux", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "" {
			// 与真实网关一致：非升级 GET 返回 426（supervisor 就绪探针依赖）。
			w.WriteHeader(http.StatusUpgradeRequired)
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.wsConn = conn
		f.mu.Unlock()
		close(f.wsReady)
		go func() {
			for { // 客户端不应上行；仅为感知断开
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGateway) handle(method string, h func(payload map[string]any) (any, *rpcWireError)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[method] = h
}

func (f *fakeGateway) waitCall(method string) map[string]any {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for _, c := range f.calls {
			if c.Method == method {
				f.mu.Unlock()
				return c.Payload
			}
		}
		f.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("未见 RPC 调用 %s（calls=%+v）", method, f.calls)
	return nil
}

func (f *fakeGateway) called(method string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.Method == method {
			return true
		}
	}
	return false
}

func (f *fakeGateway) push(rpcID string, frame map[string]any) {
	f.t.Helper()
	select {
	case <-f.wsReady:
	case <-time.After(3 * time.Second):
		f.t.Fatal("mux 订阅未建立")
	}
	payload, _ := json.Marshal(frame)
	msg, _ := json.Marshal(map[string]any{
		"type": "server-request", "rpcId": rpcID, "method": frame["type"],
		"payload": json.RawMessage(payload),
	})
	f.mu.Lock()
	conn := f.wsConn
	f.mu.Unlock()
	if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
		f.t.Logf("push %s: %v", frame["type"], err)
	}
}

func sessEvent(sessionID, evType string, data map[string]any, seq int64) map[string]any {
	return map[string]any{
		"type": "session/event", "sessionId": sessionID,
		"event": map[string]any{"type": evType, "seq": seq, "data": data},
	}
}

// pushHappyTurn 推一段标准 turn：start → chunk → message → end(completed)。
func pushHappyTurn(f *fakeGateway, sessionID string, turn int, seqBase int64) {
	f.push("", sessEvent(sessionID, "turn/start", map[string]any{"turn": float64(turn)}, seqBase))
	f.push("", sessEvent(sessionID, "assistant/chunk", map[string]any{
		"turn": float64(turn), "step": 1,
		"chunk": map[string]any{"type": "text-delta", "index": 0, "text": "ALPHA"},
	}, seqBase+1))
	f.push("", sessEvent(sessionID, "assistant/message", map[string]any{
		"turn": float64(turn), "step": 1,
		"message": map[string]any{"content": []map[string]any{{"type": "text", "text": "记住 ALPHA"}}},
		"usage":   map[string]any{"inputTokens": 100, "outputTokens": 20, "cacheReadTokens": 8},
	}, seqBase+2))
	f.push("", sessEvent(sessionID, "turn/end", map[string]any{
		"turn": float64(turn), "reason": map[string]any{"kind": "completed"},
	}, seqBase+3))
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

// ── 用例 ──────────────────────────────────────────────────────────────

func newTestExec(ctx context.Context, ref string, cb runtime.Callbacks, controls chan runtime.Control) *runtime.ExecContext {
	return &runtime.ExecContext{
		Ctx:         ctx,
		Run:         &domain.ExecutionRun{ID: "run_test", AgentProfileID: "agent_test", AdapterID: "dsh"},
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

// runExecuteScript 起 Execute，prompt 到达后执行 script（推帧），再收结果。
func runExecuteScript(t *testing.T, g *Gateway, ex *runtime.ExecContext, f *fakeGateway, script func()) runtime.ExecResult {
	t.Helper()
	done := make(chan runtime.ExecResult, 1)
	go func() { done <- g.Execute(ex) }()
	f.waitCall("session.prompt")
	script()
	select {
	case r := <-done:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("Execute 超时未返回")
		return runtime.ExecResult{}
	}
}

func newTestGateway(f *fakeGateway) *Gateway {
	return NewGateway(GatewayConfig{
		BaseURL: f.srv.URL, Model: "test-model",
	})
}

func TestManifestCapabilities(t *testing.T) {
	m, err := NewGateway(GatewayConfig{}).Manifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m.AdapterID != "dsh" || m.AdapterVersion != "2.0.0" ||
		m.Protocol.Name != "dsh-web-gateway" || m.Protocol.Version != "1" ||
		m.SchemaDigest != "sha256:dsh-web-gateway-v1" {
		t.Fatalf("manifest 标识漂移: %+v", m)
	}
	want := map[string]runtime.CapabilityLevel{
		"streaming":                               runtime.CapSupported,
		runtime.CapabilityStructuredTransport:     runtime.CapSupported,
		runtime.CapabilitySchemaConstrainedOutput: runtime.CapUnavailable,
		runtime.CapabilityControlToolCall:         runtime.CapUnavailable,
		"interrupt":                               runtime.CapSupported,
		"resume":                                  runtime.CapSupported,
		"multi_turn":                              runtime.CapSupported,
		"steering":                                runtime.CapSupported,
		"approval":                                runtime.CapSupported,
		"system_prompt":                           runtime.CapSupported,
		"workspace_files":                         runtime.CapSupported,
		"terminal":                                runtime.CapUnavailable,
		"structured_output":                       runtime.CapAdapterTranslated,
		"permissions":                             runtime.CapSupported,
		"modes":                                   runtime.CapAdapterTranslated,
		"multi_vendor":                            runtime.CapAdapterTranslated,
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

func TestGatewayFreshTurn(t *testing.T) {
	f := newFakeGateway(t)
	f.handle("session.create", func(payload map[string]any) (any, *rpcWireError) {
		return map[string]any{"sessionId": "s_fresh"}, nil
	})
	g := newTestGateway(f)
	cb := newRecordCallbacks()

	res := runExecuteScript(t, g, newTestExec(context.Background(), "", cb, make(chan runtime.Control, 8)), f,
		func() { pushHappyTurn(f, "s_fresh", 1, 1) })

	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望成功，得到 %s（%+v）", res.Outcome, res.Failure)
	}
	// session.create.cwd 必须来自 ExecContext.Resolved（fixture 用 os.TempDir）。
	if p := f.waitCall("session.create"); p["cwd"] != os.TempDir() {
		t.Fatalf("session.create cwd 不符（应取 Resolved.CWD）: %v", p)
	}
	prompt := f.waitCall("session.prompt")
	if prompt["sessionId"] != "s_fresh" || prompt["mode"] != "queue" {
		t.Fatalf("session.prompt 载荷不符: %v", prompt)
	}
	for _, key := range []string{
		"schema", "output_schema", "outputSchema", "response_format",
		"tools", "tool_definitions", "toolDefinitions",
	} {
		if _, ok := prompt[key]; ok {
			t.Fatalf("session.prompt.%s 当前 adapter 不应发送: %v", key, prompt)
		}
	}
	if !f.called("session.selectModel") {
		t.Fatal("fresh 会话应尝试 selectModel")
	}
	if res.Session == nil || res.Session.Ref != "dsh://s_fresh" ||
		res.Session.Params["gateway_session"] != "s_fresh" {
		t.Fatalf("SessionUpdate 不符: %+v", res.Session)
	}
	if res.Usage == nil || res.Usage.InputTokens != 108 || res.Usage.OutputTokens != 20 ||
		res.Usage.CachedTokens != 8 || res.Usage.Basis != runtime.UsagePerRun {
		t.Fatalf("Usage 不符（input 应含 cacheRead）: %+v", res.Usage)
	}
	if delta, ok := cb.find(domain.EventMessageDelta); !ok ||
		delta.data["raw"].(map[string]any)["chunk"] == nil {
		t.Fatalf("message.delta 应携带 raw.chunk: %+v", delta)
	}
	if msg, ok := cb.find(domain.EventMessageCompleted); !ok || msg.data["text"] != "记住 ALPHA" {
		t.Fatalf("message.completed 文本不符: %+v", msg)
	}
}

func TestGatewayResumeHit(t *testing.T) {
	f := newFakeGateway(t)
	g := newTestGateway(f)
	cb := newRecordCallbacks()

	res := runExecuteScript(t, g, newTestExec(context.Background(), "dsh://s_known", cb, make(chan runtime.Control, 8)), f,
		func() { pushHappyTurn(f, "s_known", 1, 1) })

	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望成功，得到 %s（%+v）", res.Outcome, res.Failure)
	}
	if f.called("session.create") {
		t.Fatal("resume 命中时不应 session.create")
	}
	if p := f.waitCall("session.history"); p["sessionId"] != "s_known" {
		t.Fatalf("session.history 载荷不符: %v", p)
	}
	if p := f.waitCall("session.prompt"); p["sessionId"] != "s_known" {
		t.Fatalf("session.prompt 应打到原会话: %v", p)
	}
	// F2：resume 轮也必须发布 SessionUpdate（同 ref、Params/DisplayID 同前）——
	// 应用层 runs_count 靠每个新 run 首报 +1，缺报会导致轮换阈值永不触发。
	if res.Session == nil || res.Session.Ref != "dsh://s_known" ||
		res.Session.DisplayID != "s_known" ||
		res.Session.Params["gateway_session"] != "s_known" {
		t.Fatalf("resume 轮 SessionUpdate 不符: %+v", res.Session)
	}
	if len(cb.sessions) == 0 || cb.sessions[0].Ref != "dsh://s_known" {
		t.Fatalf("resume 轮应经 OnSession 早期上报同 ref: %+v", cb.sessions)
	}
}

// F1：resume ref 携带但网关侧会话缺失 → session_unknown 不可重试失败，
// 绝不静默降级 fresh（降级后的 fresh 会话收不到历史=失忆）。
func TestGatewayResumeMissFailsSessionUnknown(t *testing.T) {
	f := newFakeGateway(t)
	f.handle("session.history", func(payload map[string]any) (any, *rpcWireError) {
		return nil, &rpcWireError{Code: "session-not-found", Message: "no such session"}
	})
	f.handle("session.create", func(payload map[string]any) (any, *rpcWireError) {
		t.Error("resume 未命中不得静默 session.create 降级")
		return map[string]any{"sessionId": "s_reborn"}, nil
	})
	g := newTestGateway(f)
	cb := newRecordCallbacks()

	// resolveSession 失败在 prompt 之前返回：直接同步执行并限时收结果。
	done := make(chan runtime.ExecResult, 1)
	go func() {
		done <- g.Execute(newTestExec(context.Background(), "dsh://s_gone", cb, make(chan runtime.Control, 8)))
	}()
	var res runtime.ExecResult
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Execute 超时未返回")
	}

	if res.Outcome != runtime.OutcomeFailed {
		t.Fatalf("期望 failed，得到 %s（%+v）", res.Outcome, res.Failure)
	}
	if res.Failure == nil || res.Failure.Family != runtime.FamilySessionUnknown ||
		res.Failure.Retryable || res.Failure.Code != "resume_session-not-found" {
		t.Fatalf("会话缺失应 session_unknown/不可重试/明确码: %+v", res.Failure)
	}
	if f.called("session.create") {
		t.Fatal("不得降级 session.create")
	}
	if res.Session != nil || len(cb.sessions) != 0 {
		t.Fatalf("失败路径不应发布 SessionUpdate: %+v / %+v", res.Session, cb.sessions)
	}
}

// F1 区分：resume 探测的网关侧临时错误（agent-busy 等）不是会话缺失，
// 保持 transient/io 分类，不得误报 session_unknown 触发清锚点自愈。
func TestGatewayResumeProbeTransientStaysIO(t *testing.T) {
	f := newFakeGateway(t)
	f.handle("session.history", func(payload map[string]any) (any, *rpcWireError) {
		return nil, &rpcWireError{Code: "agent-busy", Message: "agent busy"}
	})
	g := newTestGateway(f)
	cb := newRecordCallbacks()

	done := make(chan runtime.ExecResult, 1)
	go func() {
		done <- g.Execute(newTestExec(context.Background(), "dsh://s_busy", cb, make(chan runtime.Control, 8)))
	}()
	var res runtime.ExecResult
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Execute 超时未返回")
	}

	if res.Outcome != runtime.OutcomeFailed {
		t.Fatalf("期望 failed，得到 %s（%+v）", res.Outcome, res.Failure)
	}
	if res.Failure == nil || res.Failure.Family != runtime.FamilyIO || !res.Failure.Retryable {
		t.Fatalf("agent-busy 应 io/可重试: %+v", res.Failure)
	}
	if f.called("session.create") {
		t.Fatal("临时错误不得降级 session.create")
	}
}

func TestGatewayStaleTurnEndIgnored(t *testing.T) {
	f := newFakeGateway(t)
	g := newTestGateway(f)
	cb := newRecordCallbacks()
	ex := newTestExec(context.Background(), "dsh://s_known", cb, make(chan runtime.Control, 8))

	done := make(chan runtime.ExecResult, 1)
	go func() { done <- g.Execute(ex) }()
	f.waitCall("session.prompt")
	// 在途旧 turn（编号 3）先于本轮 turn/start 到达且以 error 收尾：必须被忽略。
	f.push("", sessEvent("s_known", "turn/end", map[string]any{
		"turn": float64(3), "reason": map[string]any{"kind": "error", "error": map[string]any{"code": "stale", "message": "旧 turn 错误"}},
	}, 1))
	f.push("", sessEvent("s_known", "turn/start", map[string]any{"turn": float64(4)}, 2))
	f.push("", sessEvent("s_known", "turn/end", map[string]any{
		"turn": float64(4), "reason": map[string]any{"kind": "completed"},
	}, 3))

	select {
	case res := <-done:
		if res.Outcome != runtime.OutcomeSucceeded {
			t.Fatalf("旧 turn/end(error) 不应终结本轮，得到 %s（%+v）", res.Outcome, res.Failure)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Execute 超时未返回")
	}
}

func TestGatewayCancelPostsSessionCancel(t *testing.T) {
	f := newFakeGateway(t)
	f.handle("session.create", func(payload map[string]any) (any, *rpcWireError) {
		return map[string]any{"sessionId": "s_fresh"}, nil
	})
	g := newTestGateway(f)
	cb := newRecordCallbacks()
	ctx, cancel := context.WithCancel(context.Background())
	ex := newTestExec(ctx, "", cb, make(chan runtime.Control, 8))

	done := make(chan runtime.ExecResult, 1)
	go func() { done <- g.Execute(ex) }()
	f.waitCall("session.prompt")
	f.push("", sessEvent("s_fresh", "turn/start", map[string]any{"turn": float64(1)}, 1))
	cancel()
	if p := f.waitCall("session.cancel"); p["sessionId"] != "s_fresh" {
		t.Fatalf("session.cancel 载荷不符: %v", p)
	}
	f.push("", sessEvent("s_fresh", "turn/end", map[string]any{
		"turn": float64(1), "reason": map[string]any{"kind": "aborted"},
	}, 2))

	select {
	case res := <-done:
		if res.Outcome != runtime.OutcomeInterrupted {
			t.Fatalf("取消后期望 interrupted，得到 %s", res.Outcome)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Execute 超时未返回")
	}
}

func TestGatewayApprovalResponds(t *testing.T) {
	f := newFakeGateway(t)
	g := newTestGateway(f)
	cb := newRecordCallbacks()
	controls := make(chan runtime.Control, 8)
	ex := newTestExec(context.Background(), "dsh://s_known", cb, controls)

	done := make(chan runtime.ExecResult, 1)
	go func() { done <- g.Execute(ex) }()
	f.waitCall("session.prompt")
	f.push("rpc_appr_1", map[string]any{
		"type": "approval/requested", "sessionId": "s_known",
		"approvalId": "apr_1", "toolName": "bash", "reason": "rm -rf /tmp/x",
	})
	select {
	case req := <-cb.approvals:
		// risk 固定 "high"（与 kimiapp/codexapp 一致）：toolName 不得误传 risk 位。
		if req.kind != "tool" || req.risk != "high" || !strings.Contains(req.summary, "rm -rf") {
			t.Fatalf("审批映射不符: %+v", req)
		}
		controls <- runtime.Control{Kind: runtime.ControlApproval, ApprovalID: req.id, Approved: true}
	case <-time.After(5 * time.Second):
		t.Fatal("未见 RequestApproval")
	}
	select {
	case r := <-f.respondCh:
		if r.RpcID != "rpc_appr_1" || r.Value["outcome"] != "allowed-once" ||
			r.Value["approvalId"] != "apr_1" || r.Value["sessionId"] != "s_known" {
			t.Fatalf("respond 载荷不符: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("未见 /api/respond")
	}
	pushHappyTurn(f, "s_known", 1, 1)
	select {
	case res := <-done:
		if res.Outcome != runtime.OutcomeSucceeded {
			t.Fatalf("审批通过后应成功，得到 %s（%+v）", res.Outcome, res.Failure)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Execute 超时未返回")
	}
}

func TestGatewayQuestionResponds(t *testing.T) {
	f := newFakeGateway(t)
	g := newTestGateway(f)
	cb := newRecordCallbacks()
	controls := make(chan runtime.Control, 8)
	ex := newTestExec(context.Background(), "dsh://s_known", cb, controls)

	done := make(chan runtime.ExecResult, 1)
	go func() { done <- g.Execute(ex) }()
	f.waitCall("session.prompt")
	f.push("rpc_q_1", map[string]any{
		"type": "question/requested", "sessionId": "s_known",
		"questions": []map[string]any{{
			"id": "q1", "question": "选择部署环境",
			"options": []map[string]any{{"label": "staging"}, {"label": "prod"}},
		}},
	})
	select {
	case req := <-cb.approvals:
		if req.kind != "question" || req.risk != "ask_user" {
			t.Fatalf("提问映射不符: %+v", req)
		}
		controls <- runtime.Control{Kind: runtime.ControlApproval, ApprovalID: req.id, Approved: true}
	case <-time.After(5 * time.Second):
		t.Fatal("未见提问审批")
	}
	select {
	case r := <-f.respondCh:
		answers, _ := r.Value["answer"].(map[string]any)
		items, _ := answers["answers"].([]any)
		if r.RpcID != "rpc_q_1" || len(items) != 1 {
			t.Fatalf("提问 respond 载荷不符: %+v", r)
		}
		first, _ := items[0].(map[string]any)
		selected, _ := first["selected"].([]any)
		if first["id"] != "q1" || len(selected) != 1 || selected[0] != "staging" {
			t.Fatalf("提问答案不符: %+v", first)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("未见 /api/respond")
	}
	pushHappyTurn(f, "s_known", 1, 1)
	select {
	case res := <-done:
		if res.Outcome != runtime.OutcomeSucceeded {
			t.Fatalf("提问回答后应成功，得到 %s（%+v）", res.Outcome, res.Failure)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Execute 超时未返回")
	}
}

func TestGatewayTurnErrorClassified(t *testing.T) {
	f := newFakeGateway(t)
	g := newTestGateway(f)
	cb := newRecordCallbacks()

	ex := newTestExec(context.Background(), "dsh://s_known", cb, make(chan runtime.Control, 8))
	done := make(chan runtime.ExecResult, 1)
	go func() { done <- g.Execute(ex) }()
	f.waitCall("session.prompt")
	f.push("", sessEvent("s_known", "turn/start", map[string]any{"turn": float64(1)}, 1))
	f.push("", sessEvent("s_known", "turn/end", map[string]any{
		"turn":   float64(1),
		"reason": map[string]any{"kind": "error", "error": map[string]any{"code": "insufficient_balance", "message": "余额不足"}},
	}, 2))

	select {
	case res := <-done:
		if res.Outcome != runtime.OutcomeFailed {
			t.Fatalf("期望失败，得到 %s", res.Outcome)
		}
		if res.Failure == nil || res.Failure.Family != runtime.FamilyProviderQuota || res.Failure.Retryable {
			t.Fatalf("错误分类不符: %+v", res.Failure)
		}
		// F7c：adapter 侧不再发 run.failed 事件（由权威层按 Failure 发出，防双发）。
		if _, ok := cb.find(domain.EventRunFailed); ok {
			t.Fatal("adapter 不得发 run.failed 事件（权威层职责，防双发）")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Execute 超时未返回")
	}
}

// F7d：同一 turn 的 assistant/chunk 与 assistant/message 都携带 usage 时，
// 以 message.usage 为权威计数一次，chunk.usage 仅在整轮无 message.usage 时兜底。
// OnUsage 过程观测与结算同源（usageTotals 口径），观测末帧 == 结算值。
func TestGatewayUsageDeduplication(t *testing.T) {
	cases := []struct {
		name      string
		chunkUsg  map[string]any // chunk 帧 usage（nil=不携带）
		msgUsages []map[string]any
		wantIn    int64
		wantOut   int64
		wantCache int64
		wantFrms  []runtime.Usage // OnUsage 过程观测帧（按到达序）
	}{
		{
			name:     "双帧同值只计一次（message 权威）",
			chunkUsg: map[string]any{"inputTokens": 50, "outputTokens": 10, "cacheReadTokens": 4},
			msgUsages: []map[string]any{
				{"inputTokens": 100, "outputTokens": 20, "cacheReadTokens": 8},
			},
			wantIn: 108, wantOut: 20, wantCache: 8,
			// 先兜底（chunk 累计 54/10/4），message 到达后切权威口径（108/20/8）。
			wantFrms: []runtime.Usage{
				{InputTokens: 54, OutputTokens: 10, CachedTokens: 4, Basis: runtime.UsagePerRun},
				{InputTokens: 108, OutputTokens: 20, CachedTokens: 8, Basis: runtime.UsagePerRun},
			},
		},
		{
			name:     "仅 chunk 携带 usage 时兜底采纳",
			chunkUsg: map[string]any{"inputTokens": 40, "outputTokens": 6, "cacheReadTokens": 2},
			wantIn:   42, wantOut: 6, wantCache: 2,
			wantFrms: []runtime.Usage{
				{InputTokens: 42, OutputTokens: 6, CachedTokens: 2, Basis: runtime.UsagePerRun},
			},
		},
		{
			name: "多条 message 各自带 usage 时累加",
			msgUsages: []map[string]any{
				{"inputTokens": 10, "outputTokens": 2, "cacheReadTokens": 1},
				{"inputTokens": 20, "outputTokens": 3, "cacheReadTokens": 0},
			},
			wantIn: 31, wantOut: 5, wantCache: 1,
			wantFrms: []runtime.Usage{
				{InputTokens: 11, OutputTokens: 2, CachedTokens: 1, Basis: runtime.UsagePerRun},
				{InputTokens: 31, OutputTokens: 5, CachedTokens: 1, Basis: runtime.UsagePerRun},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeGateway(t)
			g := newTestGateway(f)
			cb := newRecordCallbacks()

			res := runExecuteScript(t, g, newTestExec(context.Background(), "dsh://s_known", cb, make(chan runtime.Control, 8)), f,
				func() {
					f.push("", sessEvent("s_known", "turn/start", map[string]any{"turn": float64(1)}, 1))
					chunk := map[string]any{"turn": float64(1), "step": 1,
						"chunk": map[string]any{"type": "text-delta", "index": 0, "text": "A"}}
					if tc.chunkUsg != nil {
						chunk["usage"] = tc.chunkUsg
					}
					f.push("", sessEvent("s_known", "assistant/chunk", chunk, 2))
					for i, u := range tc.msgUsages {
						f.push("", sessEvent("s_known", "assistant/message", map[string]any{
							"turn": float64(1), "step": i + 1,
							"message": map[string]any{"content": []map[string]any{{"type": "text", "text": "x"}}},
							"usage":   u,
						}, int64(3+i)))
					}
					f.push("", sessEvent("s_known", "turn/end", map[string]any{
						"turn": float64(1), "reason": map[string]any{"kind": "completed"},
					}, int64(3+len(tc.msgUsages))))
				})

			if res.Outcome != runtime.OutcomeSucceeded {
				t.Fatalf("期望成功，得到 %s（%+v）", res.Outcome, res.Failure)
			}
			if res.Usage == nil || res.Usage.InputTokens != tc.wantIn || res.Usage.OutputTokens != tc.wantOut ||
				res.Usage.CachedTokens != tc.wantCache || res.Usage.Basis != runtime.UsagePerRun {
				t.Fatalf("Usage 去重口径不符（want in=%d out=%d cached=%d）: %+v",
					tc.wantIn, tc.wantOut, tc.wantCache, res.Usage)
			}
			frames := cb.usageFrames()
			if len(frames) != len(tc.wantFrms) {
				t.Fatalf("OnUsage 帧数不符（want %d）: %+v", len(tc.wantFrms), frames)
			}
			for i, w := range tc.wantFrms {
				if !sameLegacyUsage(frames[i], w) {
					t.Fatalf("OnUsage 第 %d 帧不符（口径应与结算同源）: got %+v want %+v", i+1, frames[i], w)
				}
				if frames[i].ProviderReport == nil || frames[i].Canonical == nil {
					t.Fatalf("OnUsage 第 %d 帧缺少 provider/canonical usage: %+v", i+1, frames[i])
				}
			}
		})
	}
}

// 工具事件 canonical 契约（notes/implemented/architecture/
// 2026-08-23-tool-event-canonical-contract.md）：started 带
// {tool, call_id, args_summary?, args?}（summary 取 command 类参数、≤200；
// args 为完整入参原文、≤2000，见 TestGatewayToolStartedArgsPayload），
// completed/failed 带 {call_id, output?}（≤2000）——此前整帧塞 raw，
// 前端读平铺 output 导致 DSH 工具输出永不可见；isError 藏在
// message.content[0] 的 tool-result 块内，顶层读取永远漏判。
func TestGatewayToolEventsCanonicalContract(t *testing.T) {
	f := newFakeGateway(t)
	g := newTestGateway(f)
	cb := newRecordCallbacks()

	long := strings.Repeat("x", 3000)
	res := runExecuteScript(t, g, newTestExec(context.Background(), "dsh://s_known", cb, make(chan runtime.Control, 8)), f,
		func() {
			f.push("", sessEvent("s_known", "turn/start", map[string]any{"turn": float64(1)}, 1))
			f.push("", sessEvent("s_known", "tool/call", map[string]any{
				"turn": float64(1), "step": 1, "callId": "tc_1", "name": "bash",
				"arguments": `{"command":"rm -rf /tmp/x","workdir":"/tmp"}`,
			}, 2))
			f.push("", sessEvent("s_known", "tool/result", map[string]any{
				"turn": float64(1), "step": 1,
				"message": map[string]any{"content": []map[string]any{{
					"type": "tool-result", "toolCallId": "tc_1",
					"content": []map[string]any{{"type": "text", "text": long}},
				}}},
			}, 3))
			f.push("", sessEvent("s_known", "tool/result", map[string]any{
				"turn": float64(1), "step": 2,
				"message": map[string]any{"content": []map[string]any{{
					"type": "tool-result", "toolCallId": "tc_2", "isError": true,
					"content": []map[string]any{{"type": "text", "text": "boom"}},
				}}},
			}, 4))
			f.push("", sessEvent("s_known", "turn/end", map[string]any{
				"turn": float64(1), "reason": map[string]any{"kind": "completed"},
			}, 5))
		})

	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望成功，得到 %s（%+v）", res.Outcome, res.Failure)
	}
	started, ok := cb.find(domain.EventToolStarted)
	if !ok || started.data["tool"] != "bash" || started.data["call_id"] != "tc_1" ||
		started.data["args_summary"] != "rm -rf /tmp/x" {
		t.Fatalf("tool.started 契约漂移（args_summary 应取 command 键）: %+v", started.data)
	}
	// 正例：args 为 arguments 原文（模型紧凑 JSON，不重新 marshal）。
	if got := started.data["args"]; got != `{"command":"rm -rf /tmp/x","workdir":"/tmp"}` {
		t.Fatalf("args 应为完整入参原文: %v", got)
	}
	completed, ok := cb.find(domain.EventToolCompleted)
	if !ok || completed.data["call_id"] != "tc_1" {
		t.Fatalf("tool.completed 缺 call_id: %+v", completed.data)
	}
	if out, _ := completed.data["output"].(string); len(out) != 2000 {
		t.Fatalf("output 应截断 2000，得到 %d", len(out))
	}
	if _, has := completed.data["raw"]; has {
		t.Fatal("tool.completed 不得整帧塞 raw（前端读平铺 output）")
	}
	failed, ok := cb.find(domain.EventToolFailed)
	if !ok || failed.data["call_id"] != "tc_2" || failed.data["output"] != "boom" {
		t.Fatalf("tool.failed 契约漂移（isError 应映射 failed）: %+v", failed.data)
	}
}

// tool.started args 键的边界：完整入参原文超 maxToolArgs 截断；arguments
// 缺失/非法 JSON 不带键（args_summary 是一行摘要，无法还原完整入参）。
func TestGatewayToolStartedArgsPayload(t *testing.T) {
	f := newFakeGateway(t)
	g := newTestGateway(f)
	cb := newRecordCallbacks()

	res := runExecuteScript(t, g, newTestExec(context.Background(), "dsh://s_known", cb, make(chan runtime.Control, 8)), f,
		func() {
			f.push("", sessEvent("s_known", "turn/start", map[string]any{"turn": float64(1)}, 1))
			f.push("", sessEvent("s_known", "tool/call", map[string]any{
				"turn": float64(1), "step": 1, "callId": "tc_long", "name": "bash",
				"arguments": `{"data":"` + strings.Repeat("y", 2200) + `"}`,
			}, 2))
			f.push("", sessEvent("s_known", "tool/call", map[string]any{
				"turn": float64(1), "step": 2, "callId": "tc_empty", "name": "bash",
			}, 3))
			f.push("", sessEvent("s_known", "tool/call", map[string]any{
				"turn": float64(1), "step": 3, "callId": "tc_bad", "name": "bash",
				"arguments": "not-json",
			}, 4))
			f.push("", sessEvent("s_known", "turn/end", map[string]any{
				"turn": float64(1), "reason": map[string]any{"kind": "completed"},
			}, 5))
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
	if len(byID) != 3 {
		t.Fatalf("tool.started 期望 3 帧，得到 %d: %+v", len(byID), byID)
	}
	if got, _ := byID["tc_long"].data["args"].(string); len(got) != 2000 {
		t.Fatalf("args 应截断到 2000，得到 %d", len(got))
	}
	for _, id := range []string{"tc_empty", "tc_bad"} {
		if _, has := byID[id].data["args"]; has {
			t.Fatalf("%s 不应携带 args 键: %+v", id, byID[id].data)
		}
	}
}

// F7f：session/queue、session/projection 帧不被静默吞掉——显式记 OnLog，
// 且不影响本轮事件流继续消费。
func TestGatewayQueueProjectionFramesLogged(t *testing.T) {
	f := newFakeGateway(t)
	g := newTestGateway(f)
	cb := newRecordCallbacks()

	res := runExecuteScript(t, g, newTestExec(context.Background(), "dsh://s_known", cb, make(chan runtime.Control, 8)), f,
		func() {
			f.push("", map[string]any{"type": "session/queue", "sessionId": "s_known", "position": float64(2)})
			f.push("", map[string]any{"type": "session/projection", "sessionId": "s_known", "lastSeq": float64(7)})
			pushHappyTurn(f, "s_known", 1, 1)
		})

	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("未知投影帧不应影响本轮，得到 %s（%+v）", res.Outcome, res.Failure)
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()
	var queueSeen, projectionSeen bool
	for _, l := range cb.logs {
		if strings.Contains(l, "session/queue") {
			queueSeen = true
		}
		if strings.Contains(l, "session/projection") {
			projectionSeen = true
		}
	}
	if !queueSeen || !projectionSeen {
		t.Fatalf("session/queue 与 session/projection 应记 OnLog: %v", cb.logs)
	}
}

// P1-5 回归：input_total 只在三个输入分量全知时派生。cache-write 未知时不得
// 把 write 隐式当 0（低估 total-token quota）；任一分量未知或求和溢出时
// InputTokensTotal 保持未知（nil 即 unknown，不是 0）。
func TestGatewayUsageBucketsCountersInputTotalDerivation(t *testing.T) {
	i64 := func(v int64) *int64 { return &v }
	cases := []struct {
		name      string
		buckets   dshUsageBuckets
		wantTotal *int64
	}{
		{
			name: "cache-write 未知 → total 保持未知",
			buckets: dshUsageBuckets{
				uncached: 100, uncachedKnown: true,
				cacheRead: 8, cacheReadKnown: true,
				output: 20, outputKnown: true, seen: true,
			},
			wantTotal: nil,
		},
		{
			name: "三分量全知 → total=uncached+read+write",
			buckets: dshUsageBuckets{
				uncached: 100, uncachedKnown: true,
				cacheRead: 8, cacheReadKnown: true,
				cacheWrite: 12, cacheWriteKnown: true,
				output: 20, outputKnown: true, seen: true,
			},
			wantTotal: i64(120),
		},
		{
			name: "分量求和溢出 → total 保持未知（不伪造）",
			buckets: dshUsageBuckets{
				uncached: math.MaxInt64, uncachedKnown: true,
				cacheRead: 1, cacheReadKnown: true,
				cacheWrite: 1, cacheWriteKnown: true, seen: true,
			},
			wantTotal: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.buckets.counters()
			if tc.wantTotal == nil {
				if got.InputTokensTotal != nil {
					t.Fatalf("InputTokensTotal 应保持未知(nil)，得到 %d", *got.InputTokensTotal)
				}
				return
			}
			if got.InputTokensTotal == nil || *got.InputTokensTotal != *tc.wantTotal {
				t.Fatalf("InputTokensTotal 不符: want %d got %+v", *tc.wantTotal, got.InputTokensTotal)
			}
		})
	}
}

// P1-5 端到端：pushHappyTurn 的 usage 无 cacheWriteTokens，provider 报告的
// input_total 必须保持未知（旧实现派生 108=100+8，把 write 隐式当 0）。
func TestGatewayProviderReportTotalUnknownWhenCacheWriteMissing(t *testing.T) {
	f := newFakeGateway(t)
	g := newTestGateway(f)
	cb := newRecordCallbacks()

	res := runExecuteScript(t, g, newTestExec(context.Background(), "dsh://s_known", cb, make(chan runtime.Control, 8)), f,
		func() { pushHappyTurn(f, "s_known", 1, 1) })

	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望成功，得到 %s（%+v）", res.Outcome, res.Failure)
	}
	if res.Usage == nil || res.Usage.ProviderReport == nil {
		t.Fatalf("结算应携带 provider usage 报告: %+v", res.Usage)
	}
	counters := res.Usage.ProviderReport.Counters
	if counters.InputTokensTotal != nil {
		t.Fatalf("cache-write 未知时 input_total 必须保持 nil，得到 %d", *counters.InputTokensTotal)
	}
	if counters.InputUncachedTokens == nil || *counters.InputUncachedTokens != 100 ||
		counters.CacheReadTokens == nil || *counters.CacheReadTokens != 8 ||
		counters.CacheWriteTokens != nil {
		t.Fatalf("已知分量必须逐项保留，未知分量保持 nil: %+v", counters)
	}
}

// P1 回归：legacy 与 canonical 共用的数值入口 fail closed——负数/NaN/Inf/
// 非整数/越界/非数值一律 unknown，合法值正常。
func TestGatewayUsageValueFailsClosed(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  int64
		ok    bool
	}{
		{"合法 float64", float64(42), 42, true},
		{"合法 int", 42, 42, true},
		{"合法 int64", int64(42), 42, true},
		{"float64 零", float64(0), 0, true},
		{"负数", float64(-1), 0, false},
		{"NaN", math.NaN(), 0, false},
		{"正无穷", math.Inf(1), 0, false},
		{"负无穷", math.Inf(-1), 0, false},
		{"非整数", float64(1.5), 0, false},
		{"2^63 越界", float64(1 << 63), 0, false},
		{"字符串", "42", 0, false},
		{"字符串数字", "1024", 0, false},
		{"json.Number", json.Number("1024"), 0, false},
		{"布尔", true, 0, false},
		{"nil", nil, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := dshUsageValue(tc.value)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("dshUsageValue(%v) = (%d,%v), want (%d,%v)", tc.value, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestGatewayLegacyTotalsFailClosedOnAggregateOverflow(t *testing.T) {
	legacy := dshLegacyUsage{}
	legacy.add(map[string]any{
		"inputTokens": int64(math.MaxInt64 - 4), "cacheReadTokens": int64(10), "outputTokens": int64(7),
	})
	in, out, cached := legacy.totals()
	if in != 0 || out != 7 || cached != 10 || !legacy.inputOverflow {
		t.Fatalf("overflowing legacy input must stay zero without corrupting healthy projections: %+v totals=%d/%d/%d", legacy, in, out, cached)
	}
	legacy.add(map[string]any{"inputTokens": int64(1), "outputTokens": int64(math.MaxInt64)})
	legacy.add(map[string]any{"outputTokens": int64(1)})
	if in, out, cached := legacy.totals(); in != 0 || out != 0 || cached != 10 || !legacy.outputOverflow {
		t.Fatalf("invalidated legacy dimensions must never resume after overflow: %+v totals=%d/%d/%d", legacy, in, out, cached)
	}

	buckets := dshUsageBuckets{}
	buckets.add(map[string]any{"inputTokens": int64(math.MaxInt64 - 4)})
	buckets.add(map[string]any{"inputTokens": int64(10)})
	if buckets.uncachedKnown || buckets.counters().InputUncachedTokens != nil {
		t.Fatalf("cross-frame overflow must make the canonical bucket unknown: %+v", buckets)
	}
}

// P1 端到端：chunk usage 混入非法数值（负数/非整数）时 legacy 三值只累计
// 合法分量——非法分量不累计也不伪造。旧实现把 int64(-50)/int64(1.5) 原样
// 计入，产生负的累计用量污染 task_sessions。
func TestGatewayLegacyUsageSkipsIllegalNumbers(t *testing.T) {
	f := newFakeGateway(t)
	g := newTestGateway(f)
	cb := newRecordCallbacks()

	res := runExecuteScript(t, g, newTestExec(context.Background(), "dsh://s_known", cb, make(chan runtime.Control, 8)), f,
		func() {
			f.push("", sessEvent("s_known", "turn/start", map[string]any{"turn": float64(1)}, 1))
			f.push("", sessEvent("s_known", "assistant/chunk", map[string]any{
				"turn": float64(1), "step": 1,
				"chunk": map[string]any{"type": "text-delta", "index": 0, "text": "A"},
				"usage": map[string]any{"inputTokens": float64(30), "outputTokens": float64(5), "cacheReadTokens": float64(2)},
			}, 2))
			f.push("", sessEvent("s_known", "assistant/chunk", map[string]any{
				"turn": float64(1), "step": 2,
				"chunk": map[string]any{"type": "text-delta", "index": 1, "text": "B"},
				"usage": map[string]any{"inputTokens": float64(-50), "outputTokens": float64(1.5), "cacheReadTokens": float64(-4)},
			}, 3))
			f.push("", sessEvent("s_known", "turn/end", map[string]any{
				"turn": float64(1), "reason": map[string]any{"kind": "completed"},
			}, 4))
		})

	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望成功，得到 %s（%+v）", res.Outcome, res.Failure)
	}
	if res.Usage == nil {
		t.Fatal("结算应携带 usage")
	}
	// legacy 口径：InputTokens 含 cacheRead（与 pushHappyTurn 的 108=100+8 一致）。
	// 合法分量 30+2/5/2；chunk2 的 -50、1.5、-4 一律不计。
	if res.Usage.InputTokens != 32 || res.Usage.OutputTokens != 5 || res.Usage.CachedTokens != 2 {
		t.Fatalf("legacy 三值应只含合法分量（32/5/2）: %+v", res.Usage)
	}
}

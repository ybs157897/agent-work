// adapters_conformance_test.go — 全部 8 个 adapter 挂入公共 conformance 套件
// （协议文档 §9.2）：claudecode/kimi/codexapp 用各自的回放桩（假 CLI /
// testdata fake_server.py），dsh/kimiapp 用最小回放网关（httptest + WS 事件流），
// zcode 为 probe-only 桩（执行面显式拒绝）走专属断言。
package conformance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	runlib "runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/claudecode"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/codexapp"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/dsh"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/kimi"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/kimiapp"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/zcode"
)

// ── 通用假 CLI（回放桩可执行文件）────────────────────────────────────

func writeFakeCLI(t *testing.T, name string, lines ...string) string {
	t.Helper()
	if runlib.GOOS == "windows" {
		t.Skip("shell 假 CLI 仅在 unix 环境执行")
	}
	path := filepath.Join(t.TempDir(), name)
	script := "#!/bin/sh\n" + strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// ── claudecode ────────────────────────────────────────────────────────

const (
	claudeConfInit = `{"type":"system","subtype":"init","session_id":"sess_conf_claude","model":"fake","cwd":"/tmp"}`
	claudeConfMsg  = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"conformance claude 输出"}]}}`
	claudeConfDone = `{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"sess_conf_claude","usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":1,"cache_read_input_tokens":2}}`
)

func TestConformanceClaudeCode(t *testing.T) {
	fast := writeFakeCLI(t, "claude-fast",
		"echo '"+claudeConfInit+"'",
		"echo '"+claudeConfMsg+"'",
		"echo '"+claudeConfDone+"'")
	held := writeFakeCLI(t, "claude-held",
		"echo '"+claudeConfInit+"'",
		"sleep 300")
	runSuite(t, "claude-code",
		claudecode.New(claudecode.Config{BinPath: fast, WorkspaceRoot: t.TempDir(), GracePeriod: time.Second}),
		claudecode.New(claudecode.Config{BinPath: held, WorkspaceRoot: t.TempDir(), GracePeriod: time.Second}),
		// CLI 会话 scheme 是 claude://，与 adapterID claude-code 不同。
		suiteOpts{scheme: "claude", requireUsage: true},
	)
}

// ── kimi ──────────────────────────────────────────────────────────────

func TestConformanceKimi(t *testing.T) {
	fast := writeFakeCLI(t, "kimi-fast",
		`printf '{"role":"meta","type":"system.version","version":"0.38.0"}\n'`,
		`printf '{"role":"assistant","text":"conformance kimi 输出"}\n'`,
		`printf '{"role":"result","type":"result","text":"done","is_error":false}\n'`,
		`printf '{"role":"meta","type":"session.resume_hint","session_id":"sess_conf_kimi"}\n'`)
	held := writeFakeCLI(t, "kimi-held",
		`printf '{"role":"meta","type":"system.version","version":"0.38.0"}\n'`,
		"sleep 300")
	runSuite(t, "kimi",
		kimi.New(kimi.Config{BinPath: fast, WorkspaceRoot: t.TempDir(), GracePeriod: time.Second}),
		kimi.New(kimi.Config{BinPath: held, WorkspaceRoot: t.TempDir(), GracePeriod: time.Second}),
		// print mode 未接 token 用量解析：不捏造 usage 记录。
		suiteOpts{scheme: "kimi", requireUsage: false},
	)
}

// ── codexapp（fake_server.py 回放桩；无 python3 时跳过）──────────────

func codexConfModule(t *testing.T) *codexapp.Module {
	t.Helper()
	if runlib.GOOS == "windows" {
		t.Skip("fake server 需要 unix 进程语义")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 不可用")
	}
	_, here, _, _ := runlib.Caller(0)
	script := filepath.Join(filepath.Dir(here), "..", "..", "..", "testdata", "providers", "codex", "fake_server.py")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("fake server 缺失: %v", err)
	}
	return codexapp.New(codexapp.Config{
		BinPath: python, Args: []string{script},
		WorkspaceRoot: t.TempDir(), GracePeriod: time.Second,
	})
}

func TestConformanceCodexApp(t *testing.T) {
	runSuite(t, "codex-appserver",
		codexConfModule(t),
		codexConfModule(t),
		// thread scheme 是 codex://；app-server 协议未暴露用量统计。
		suiteOpts{
			scheme: "codex", requireUsage: false,
			beforeHeld: func(t *testing.T) { t.Setenv("CODEX_FAKE_HANG", "1") },
		},
	)
}

// ── dsh（最小回放网关：health + 426 mux 探针 + WS 下行 + 一元 RPC）────

const dshConfSession = "s_conf_dsh"

// confGateway dsh 回放桩：held=false 时每个 session.prompt 后自动回放标准
// turn（start → chunk → message(usage) → end(completed)）；held=true 时
// prompt 后仅 turn/start，session.cancel 到达后回放 turn/end(aborted)。
type confGateway struct {
	t      *testing.T
	held   bool
	srv    *httptest.Server
	stop   chan struct{}
	wsConn *websocket.Conn
	wsOnce sync.Once

	mu         sync.Mutex
	prompts    int // 已见 session.prompt 数
	cancels    int // 已见 session.cancel 数
	pushedTurn int // 已回放的 turn 数
}

func newConfGateway(t *testing.T, held bool) *confGateway {
	t.Helper()
	f := &confGateway{t: t, held: held, stop: make(chan struct{})}
	wsReady := make(chan struct{})
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
		value := map[string]any{}
		switch method {
		case "session.create":
			value["sessionId"] = dshConfSession
		case "session.prompt":
			f.mu.Lock()
			f.prompts++
			f.mu.Unlock()
		case "session.cancel":
			f.mu.Lock()
			f.cancels++
			f.mu.Unlock()
		}
		resp := map[string]any{"type": "server-response", "rpcId": req.RpcID,
			"result": map[string]any{"ok": true, "value": value}}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/api/events.mux", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "" {
			w.WriteHeader(http.StatusUpgradeRequired) // supervisor 就绪探针
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.wsConn = conn
		f.mu.Unlock()
		f.wsOnce.Do(func() { close(wsReady) })
		go func() { // 客户端不应上行；仅为感知断开
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(func() {
		close(f.stop)
		f.srv.Close()
	})

	// 回放驱动：见 prompt 回放 turn；held 模式另见 cancel 后收尾 aborted。
	go func() {
		select {
		case <-wsReady:
		case <-time.After(10 * time.Second):
			return
		}
		for {
			f.mu.Lock()
			prompts, pushed, cancels := f.prompts, f.pushedTurn, f.cancels
			f.mu.Unlock()
			if prompts > pushed {
				f.pushTurn(prompts)
				f.mu.Lock()
				f.pushedTurn = prompts
				f.mu.Unlock()
			}
			if f.held && cancels > 0 {
				f.push(map[string]any{
					"type": "session/event", "sessionId": dshConfSession,
					"event": map[string]any{"type": "turn/end", "seq": 99, "data": map[string]any{
						"turn": float64(prompts), "reason": map[string]any{"kind": "aborted"},
					}},
				})
				return
			}
			select {
			case <-f.stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	return f
}

func (f *confGateway) push(frame map[string]any) {
	payload, _ := json.Marshal(frame)
	msg, _ := json.Marshal(map[string]any{
		"type": "server-request", "rpcId": "", "method": frame["type"],
		"payload": json.RawMessage(payload),
	})
	f.mu.Lock()
	conn := f.wsConn
	f.mu.Unlock()
	if conn != nil {
		_ = conn.WriteMessage(websocket.TextMessage, msg)
	}
}

// pushTurn 回放一个标准 turn（编号 = 第 prompts 次 prompt）。
func (f *confGateway) pushTurn(prompts int) {
	turn := float64(prompts)
	sess := func(evType string, data map[string]any) {
		f.push(map[string]any{
			"type": "session/event", "sessionId": dshConfSession,
			"event": map[string]any{"type": evType, "seq": turn, "data": data},
		})
	}
	sess("turn/start", map[string]any{"turn": turn})
	if f.held {
		return // held：turn 挂起，等待 session.cancel 后由驱动回放 aborted
	}
	sess("assistant/chunk", map[string]any{"turn": turn, "step": 1,
		"chunk": map[string]any{"type": "text-delta", "index": 0, "text": "conformance dsh"}})
	sess("assistant/message", map[string]any{
		"turn": turn, "step": 1,
		"message": map[string]any{"content": []map[string]any{{"type": "text", "text": "conformance dsh 输出"}}},
		"usage":   map[string]any{"inputTokens": 100, "outputTokens": 20, "cacheReadTokens": 8},
	})
	sess("turn/end", map[string]any{"turn": turn, "reason": map[string]any{"kind": "completed"}})
}

func TestConformanceDsh(t *testing.T) {
	fastGW := newConfGateway(t, false)
	heldGW := newConfGateway(t, true)
	runSuite(t, "dsh",
		dsh.NewGateway(dsh.GatewayConfig{BaseURL: fastGW.srv.URL, WorkspaceRoot: "/tmp/atw-conf-dsh"}),
		dsh.NewGateway(dsh.GatewayConfig{BaseURL: heldGW.srv.URL, WorkspaceRoot: "/tmp/atw-conf-dsh"}),
		suiteOpts{scheme: "dsh", requireUsage: true},
	)
}

// ── kimiapp（最小回放 kap-server：REST envelope + WS 事件流）──────────

const (
	kimiConfSession = "s_conf_kimi"
	kimiConfToken   = "conf-kimi-token"
)

// confKap kimiapp 回放桩：held=false 时每个 prompt 后自动回放标准 turn
// （turn.started → assistant.delta → turn.step.completed(usage) →
// turn.ended(completed)）；held=true 时 prompt 后仅 turn.started，
// REST abort 到达后回放 turn.ended(cancelled)。
type confKap struct {
	t    *testing.T
	held bool
	srv  *httptest.Server
	stop chan struct{}

	wsMu   sync.Mutex
	wsConn *websocket.Conn

	mu        sync.Mutex
	prompts   int    // 已见 prompt 提交数
	lastPID   string // 最近一次分配的 prompt_id（turn.started 回显用）
	aborts    int    // 已见 REST abort 数
	pushed    int    // 已回放 turn.started 的 turn 数
	endedTurn bool   // held：是否已回放 turn.ended
}

func newConfKap(t *testing.T, held bool) *confKap {
	t.Helper()
	f := &confKap{t: t, held: held, stop: make(chan struct{})}
	wsReady := make(chan struct{})
	var wsOnce sync.Once
	write := func(w http.ResponseWriter, status, code int, data any) {
		body, _ := json.Marshal(data)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"code":` + itoa(code) + `,"msg":"success","data":` + string(body) + `}`))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/healthz", func(w http.ResponseWriter, r *http.Request) {
		write(w, http.StatusOK, 0, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/v1/ws", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+kimiConfToken {
			write(w, http.StatusUnauthorized, 40101, nil)
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		hello, _ := json.Marshal(map[string]any{
			"type": "server_hello", "timestamp": "now",
			"payload": map[string]any{
				"ws_connection_id": "ws_conf", "protocol_version": 2,
				"heartbeat_ms": 60000, "max_event_buffer_size": 1000,
			},
		})
		f.wsMu.Lock()
		f.wsConn = conn
		f.wsMu.Unlock()
		wsOnce.Do(func() { close(wsReady) })
		if err := conn.WriteMessage(websocket.TextMessage, hello); err != nil {
			return
		}
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var frame struct {
				Type    string          `json:"type"`
				ID      string          `json:"id"`
				Payload json.RawMessage `json:"payload"`
			}
			if json.Unmarshal(raw, &frame) != nil {
				continue
			}
			if frame.Type == "subscribe" {
				var sub struct {
					SessionIDs []string `json:"session_ids"`
				}
				_ = json.Unmarshal(frame.Payload, &sub)
				ack, _ := json.Marshal(map[string]any{
					"type": "ack", "id": frame.ID, "code": 0, "msg": "success",
					"payload": map[string]any{
						"accepted": sub.SessionIDs, "not_found": []string{}, "resync_required": []string{},
					},
				})
				f.wsMu.Lock()
				err := conn.WriteMessage(websocket.TextMessage, ack)
				f.wsMu.Unlock()
				if err != nil {
					return
				}
			}
		}
	})
	mux.HandleFunc("/api/v1/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+kimiConfToken {
			write(w, http.StatusUnauthorized, 40101, nil)
			return
		}
		path := r.URL.Path
		switch {
		case r.Method == http.MethodPost && path == "/api/v1/sessions":
			write(w, http.StatusOK, 0, map[string]any{"id": kimiConfSession})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/prompts"):
			f.mu.Lock()
			f.prompts++
			f.lastPID = "p_" + itoa(f.prompts)
			pid := f.lastPID
			f.mu.Unlock()
			write(w, http.StatusOK, 0, map[string]any{"prompt_id": pid, "status": "running"})
		case r.Method == http.MethodPost && strings.Contains(path, ":abort"):
			f.mu.Lock()
			f.aborts++
			f.mu.Unlock()
			write(w, http.StatusOK, 0, map[string]any{"aborted": true})
		default:
			write(w, http.StatusOK, 0, map[string]any{})
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(func() {
		close(f.stop)
		f.srv.Close()
	})

	// 回放驱动：见 prompt 回放 turn；held 模式见 abort 后收尾 cancelled。
	go func() {
		select {
		case <-wsReady:
		case <-time.After(10 * time.Second):
			return
		}
		for {
			f.mu.Lock()
			prompts, pushed, lastPID := f.prompts, f.pushed, f.lastPID
			aborts, ended := f.aborts, f.endedTurn
			f.mu.Unlock()
			if prompts > pushed {
				f.pushTurn(prompts, lastPID)
				f.mu.Lock()
				f.pushed = prompts
				f.mu.Unlock()
			}
			if f.held && aborts > 0 && !ended {
				f.mu.Lock()
				f.endedTurn = true
				turn := f.pushed
				f.mu.Unlock()
				f.pushEvent(int64(turn)*10+9, "turn.ended", map[string]any{
					"turnId": float64(turn), "reason": "cancelled",
				}, false)
				return
			}
			select {
			case <-f.stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	return f
}

func (f *confKap) push(frame map[string]any) {
	payload, _ := json.Marshal(frame)
	f.wsMu.Lock()
	conn := f.wsConn
	f.wsMu.Unlock()
	if conn != nil {
		_ = conn.WriteMessage(websocket.TextMessage, payload)
	}
}

// pushEvent 发一帧事件：durable 带 seq（整数），volatile 带 volatile+offset。
func (f *confKap) pushEvent(seq int64, evType string, payload map[string]any, volatile bool) {
	payload["type"] = evType
	payload["sessionId"] = kimiConfSession
	payload["agentId"] = "main"
	frame := map[string]any{
		"type": evType, "session_id": kimiConfSession, "timestamp": "now",
		"payload": payload,
	}
	if volatile {
		frame["volatile"] = true
		frame["offset"] = seq
	} else {
		frame["seq"] = seq
	}
	f.push(frame)
}

// pushTurn 回放一个标准 turn（编号 = 第 n 次 prompt；promptId 回显）。
func (f *confKap) pushTurn(n int, promptID string) {
	turn := float64(n)
	base := int64(n) * 10
	f.pushEvent(base, "turn.started", map[string]any{"turnId": turn, "promptId": promptID}, false)
	if f.held {
		return // held：turn 挂起，等待 REST abort 后由驱动回放 cancelled
	}
	f.pushEvent(base+1, "assistant.delta", map[string]any{"turnId": turn, "delta": "conformance kimiapp"}, true)
	f.pushEvent(base+2, "turn.step.completed", map[string]any{
		"turnId": turn, "step": 1,
		"usage": map[string]any{"inputOther": 100, "output": 20, "inputCacheRead": 8, "inputCacheCreation": 2},
	}, false)
	f.pushEvent(base+3, "turn.ended", map[string]any{"turnId": turn, "reason": "completed"}, false)
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestConformanceKimiApp(t *testing.T) {
	fastKap := newConfKap(t, false)
	heldKap := newConfKap(t, true)
	runSuite(t, "kimi-appserver",
		kimiapp.New(kimiapp.Config{BaseURL: fastKap.srv.URL, Token: kimiConfToken, WorkspaceRoot: t.TempDir()}),
		kimiapp.New(kimiapp.Config{BaseURL: heldKap.srv.URL, Token: kimiConfToken, WorkspaceRoot: t.TempDir()}),
		// ref 方案 kimiapp://<session_id>；usage 由 turn.step.completed 逐 turn 累计。
		suiteOpts{scheme: "kimiapp", requireUsage: true},
	)
}

// ── zcode（probe-only：能力全部 unavailable，执行面显式拒绝）──────────

// quietCallbacks 记录全部回调（zcode 期望零回调）。
type quietCallbacks struct {
	mu       sync.Mutex
	events   []string
	sessions int
	logs     int
}

func (c *quietCallbacks) OnEvent(eventType string, data map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, eventType)
}
func (c *quietCallbacks) OnProgress(float64)              {}
func (c *quietCallbacks) OnLog(stream, line string)       { c.mu.Lock(); c.logs++; c.mu.Unlock() }
func (c *quietCallbacks) OnSpawn(pid, processGroupID int) {}
func (c *quietCallbacks) OnUsage(u runtime.Usage)         {}
func (c *quietCallbacks) OnSession(update runtime.SessionUpdate) {
	c.mu.Lock()
	c.sessions++
	c.mu.Unlock()
}
func (c *quietCallbacks) RequestApproval(kind, risk, summary string) string { return "" }

// TestConformanceZcodeProbeOnly：runSuite 的成功路径不适用于 probe-only 桩，
// 以显式断言固化其能力声明（全部 unavailable）与拒绝行为（不静默降级）。
func TestConformanceZcodeProbeOnly(t *testing.T) {
	m := zcode.New()
	ctx := context.Background()

	mf, err := m.Manifest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if mf.AdapterID != "zcode-probe" || mf.AdapterVersion == "" || mf.SchemaDigest == "" {
		t.Fatalf("manifest 标识错误: %+v", mf)
	}
	if len(mf.Capabilities) == 0 {
		t.Fatal("能力声明不能为空")
	}
	for name, level := range mf.Capabilities {
		if level != runtime.CapUnavailable {
			t.Fatalf("probe-only 桩能力 %s 必须为 unavailable，实际 %s", name, level)
		}
	}

	probe, err := m.Probe(ctx, runtime.ProbeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if probe.OK || probe.Error == "" {
		t.Fatalf("未核验协议应 probe 失败: %+v", probe)
	}

	cb := &quietCallbacks{}
	res := m.Execute(&runtime.ExecContext{
		Ctx: ctx, Run: &domain.ExecutionRun{ID: "run_zcode", AdapterID: "zcode-probe"},
		Instruction: "任何指令", Callbacks: cb, Controls: make(chan runtime.Control, 1),
	})
	if res.Outcome != runtime.OutcomeFailed || res.Failure == nil ||
		res.Failure.Code != "start_unsupported" || res.Failure.Family != runtime.FamilyConfig ||
		res.Failure.Retryable {
		t.Fatalf("执行面应显式拒绝 config/start_unsupported/不可重试: %+v %+v", res.Outcome, res.Failure)
	}
	if res.Session != nil || res.Usage != nil {
		t.Fatalf("拒绝路径不得捏造会话/用量: %+v %+v", res.Session, res.Usage)
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if len(cb.events) != 0 || cb.sessions != 0 || cb.logs != 0 {
		t.Fatalf("拒绝路径不应产生任何回调: events=%v sessions=%d logs=%d", cb.events, cb.sessions, cb.logs)
	}
}

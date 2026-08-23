package runnergateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ybs/agent-team-workbench/internal/application"
)

// hello 线上契约：runnerd 上报的 adapter_version/schema_digest 必须被完整解析，
// 这是分派前能力校验的 runner 侧真相。
func TestHelloPayloadParsesAdapterDigests(t *testing.T) {
	payload := []byte(`{
		"protocol_versions": [1],
		"runner_version": "0.3.0",
		"slots": 2,
		"adapters": [
			{"adapter_id": "dsh", "adapter_version": "2.0.0", "schema_digest": "sha256:dsh-web-gateway-v1"},
			{"adapter_id": "kimi-appserver", "adapter_version": "1.0.0", "schema_digest": "sha256:kimi-kap-server-v2"}
		]
	}`)
	var hp helloPayload
	if err := json.Unmarshal(payload, &hp); err != nil {
		t.Fatalf("hello payload 解析失败: %v", err)
	}
	if len(hp.Adapters) != 2 {
		t.Fatalf("adapters = %d，期望 2", len(hp.Adapters))
	}
	if hp.Adapters[0].ID != "dsh" || hp.Adapters[0].Version != "2.0.0" ||
		hp.Adapters[0].SchemaDigest != "sha256:dsh-web-gateway-v1" {
		t.Fatalf("digest 解析错误: %+v", hp.Adapters[0])
	}
}

func newDigestGateway(t *testing.T) *Gateway {
	t.Helper()
	g := New(nil, nil, nil)
	rc := &runnerConn{
		gw: g, runnerID: "r1",
		adapters: []adapterInfo{
			{ID: "dsh", Version: "2.0.0", SchemaDigest: "sha256:local-v1"},
			{ID: "mock", Version: "1.0.0", SchemaDigest: "sha256:mock"},
		},
		activeRuns: map[string]string{},
	}
	g.mu.Lock()
	g.conns["r1"] = rc
	g.mu.Unlock()
	return g
}

// digest 匹配才可承接；分叉时必须拒绝远程分派（调用方回落进程内模块或
// capability_missing 拒绝）；控制面无本地真相（空 digest）时退化为在线性判断。
func TestVerifyAdapterDigestMatchAndMismatch(t *testing.T) {
	g := newDigestGateway(t)

	if !g.VerifyAdapterDigest("dsh", "sha256:local-v1") {
		t.Fatal("digest 一致时应可承接")
	}
	if g.VerifyAdapterDigest("dsh", "sha256:remote-v9") {
		t.Fatal("digest 分叉时不得远程分派（能力快照与实际 manifest 不一致）")
	}
	if !g.VerifyAdapterDigest("dsh", "") {
		t.Fatal("控制面无本地 manifest 真相时应退化为在线性判断")
	}
	if g.VerifyAdapterDigest("codex-appserver", "sha256:local-v1") {
		t.Fatal("adapter 不在线时应返回 false")
	}
}

// OnlineAdapters 供分派器诊断/接线：runner 侧摘要完整可见。
func TestOnlineAdaptersExposesDigests(t *testing.T) {
	g := newDigestGateway(t)

	desc := g.OnlineAdapters("dsh")
	if len(desc) != 1 || desc[0].RunnerID != "r1" || desc[0].SchemaDigest != "sha256:local-v1" {
		t.Fatalf("OnlineAdapters = %+v，期望 r1/sha256:local-v1", desc)
	}
}

// fakeWorkspaceRepo / fakeUpsertStore：真实握手路径只触及 ListIDs 与 Runners().Upsert。
type fakeWorkspaceRepo struct {
	application.WorkspaceRepo
}

func (f *fakeWorkspaceRepo) ListIDs(ctx context.Context) ([]string, error) {
	return nil, nil
}

type fakeUpsertStore struct {
	application.Store
	runners *fakeRunnerRepo
}

func (f *fakeUpsertStore) Runners() application.RunnerRepo { return f.runners }

func (f *fakeUpsertStore) Workspaces() application.WorkspaceRepo { return &fakeWorkspaceRepo{} }

// 真实 WSS 握手：ServeHTTP 必须把 hello 里的 schema_digest 存进连接态，
// VerifyAdapterDigest 才能据此拦截分叉 runner。
func TestHandshakeCapturesAdapterDigests(t *testing.T) {
	repo := &fakeRunnerRepo{}
	g := New(&fakeUpsertStore{runners: repo}, nil, nil)
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/runner/v1/connect"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	payload, _ := json.Marshal(helloPayload{
		ProtocolVersions: []int{1}, RunnerVersion: "0.3.0", Slots: 2,
		Adapters: []adapterInfo{{ID: "dsh", Version: "2.0.0", SchemaDigest: "sha256:handshake-v1"}},
	})
	hello, _ := json.Marshal(Envelope{
		V: 1, MessageID: "msg_hello_t", Kind: "request", Method: "runner.hello",
		RunnerID: "r_hs", SentAt: time.Now().UTC(), Payload: payload,
	})
	if err := conn.WriteMessage(websocket.TextMessage, hello); err != nil {
		t.Fatal(err)
	}
	// welcome 到达即注册完成。
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatal(err)
	}

	if !g.VerifyAdapterDigest("dsh", "sha256:handshake-v1") {
		t.Fatal("握手应捕获 runner 上报的 schema_digest")
	}
	if g.VerifyAdapterDigest("dsh", "sha256:other") {
		t.Fatal("握手上报的 digest 与查询值不一致时应拒绝")
	}
}

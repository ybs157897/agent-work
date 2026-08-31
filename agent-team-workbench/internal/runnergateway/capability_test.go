package runnergateway

import (
	"encoding/json"
	"testing"
	"time"
)

// hello 线上契约：runnerd 上报的 v2 身份与 adapter digest 必须被完整解析，
// 这是分派前能力校验的 runner 侧真相。
func TestHelloPayloadParsesV2IdentityAndDigests(t *testing.T) {
	payload := []byte(`{
		"host_id": "host_build_1",
		"runner_id": "runner_build_1",
		"connection_epoch": "epoch_01HX",
		"protocol_versions": [2],
		"runner_version": "1.0.0",
		"os": "darwin",
		"arch": "arm64",
		"slots": 2,
		"adapters": [
			{"adapter_id": "dsh", "adapter_version": "2.0.0", "schema_digest": "sha256:dsh-web-gateway-v1"},
			{"adapter_id": "kimi-appserver", "adapter_version": "1.0.0", "schema_digest": "sha256:kimi-kap-server-v2"}
		],
		"mounts": [
			{
				"alias": "agent-work",
				"repository_identity": "repo_ybs_agent_work",
				"registry_generation": "gen_1",
				"supported_ref_kinds": ["root", "branch", "worktree"],
				"checkouts": [{"ref": "wt_opaque", "kind": "worktree", "branch": "codex/task", "head": "abc123"}]
			}
		]
	}`)
	var hp helloPayload
	if err := json.Unmarshal(payload, &hp); err != nil {
		t.Fatalf("hello payload 解析失败: %v", err)
	}
	if hp.HostID != "host_build_1" || hp.ConnectionEpoch != "epoch_01HX" {
		t.Fatalf("v2 身份字段解析错误: %+v", hp)
	}
	if len(hp.Adapters) != 2 {
		t.Fatalf("adapters = %d，期望 2", len(hp.Adapters))
	}
	if hp.Adapters[0].ID != "dsh" || hp.Adapters[0].Version != "2.0.0" ||
		hp.Adapters[0].SchemaDigest != "sha256:dsh-web-gateway-v1" {
		t.Fatalf("digest 解析错误: %+v", hp.Adapters[0])
	}
	if len(hp.Mounts) != 1 || hp.Mounts[0].Alias != "agent-work" || len(hp.Mounts[0].Checkouts) != 1 {
		t.Fatalf("mount 广告解析错误: %+v", hp.Mounts)
	}
	if !hp.supportsV2() {
		t.Fatal("protocol_versions=[2] 应支持 v2")
	}
	hp.ProtocolVersions = []int{1}
	if hp.supportsV2() {
		t.Fatal("protocol_versions=[1] 不得通过 v2 协商")
	}
}

func newDigestGateway(t *testing.T) *Gateway {
	t.Helper()
	g := New(nil, nil, nil)
	rc := &runnerConn{
		gw: g, runnerID: "r1", hostID: "host_1", epoch: "epoch_1", slots: 1,
		adapters: []adapterInfo{
			{ID: "dsh", Version: "2.0.0", SchemaDigest: "sha256:local-v1"},
			{ID: "mock", Version: "1.0.0", SchemaDigest: "sha256:mock"},
		},
		activeRuns: map[string]*activeRun{},
	}
	g.mu.Lock()
	g.conns["r1"] = rc
	g.mu.Unlock()
	return g
}

// digest 匹配才可承接；分叉时必须拒绝远程分派；控制面无本地真相（空 digest）
// 时退化为在线性判断。
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

// ParseHostCredential：enrollment 凭据解析（含反例）。
func TestParseHostCredential(t *testing.T) {
	hostID, secret, ok := ParseHostCredential("atw_host_host_build1_s3cret")
	if !ok || hostID != "host_build1" || secret != "s3cret" {
		t.Fatalf("解析错误: %q %q %v", hostID, secret, ok)
	}
	if _, _, ok := ParseHostCredential("bearer whatever"); ok {
		t.Fatal("非 atw_host_ 前缀必须拒绝")
	}
	if _, _, ok := ParseHostCredential("atw_host_nohostprefix_secret"); ok {
		t.Fatal("host 段必须以 host_ 开头")
	}
	if _, _, ok := ParseHostCredential("atw_host_host__"); ok {
		t.Fatal("空 secret 必须拒绝")
	}
	// enrollment 口径：sha256(secret) hex。
	if got := enrollmentDigest("s3cret"); len(got) != 64 {
		t.Fatalf("enrollment digest 应为 sha256 hex，实际 %q", got)
	}
}

// ConnectPath 是 v2 唯一路由；v1 路径字符串不得回流。
func TestConnectPathIsV2(t *testing.T) {
	if ConnectPath != "/runner/v2/connect" {
		t.Fatalf("ConnectPath = %q", ConnectPath)
	}
	if ProtocolVersion != 2 {
		t.Fatalf("ProtocolVersion = %d", ProtocolVersion)
	}
}

// welcome 帧契约：v=2 + selected_version=2 + heartbeat/lease 参数。
func TestWelcomePayloadShape(t *testing.T) {
	g := New(nil, nil, nil)
	rc := &runnerConn{gw: g, runnerID: "r1", send: make(chan []byte, 4)}
	g.welcome(rc)

	select {
	case raw := <-rc.send:
		var env Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		if env.V != ProtocolVersion || env.Method != "server.welcome" {
			t.Fatalf("welcome 形态错误: %+v", env)
		}
		var wp welcomePayload
		if err := json.Unmarshal(env.Payload, &wp); err != nil {
			t.Fatal(err)
		}
		if wp.SelectedVersion != 2 || wp.HeartbeatIntervalSeconds < 1 ||
			wp.LeasePolicy.TTLSeconds < 1 || wp.LeasePolicy.RenewIntervalSeconds < 1 {
			t.Fatalf("welcome 参数缺失: %+v", wp)
		}
	case <-time.After(time.Second):
		t.Fatal("welcome 未入队")
	}
}

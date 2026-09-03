package runnergateway

// v2 握手与 host-aware 分派行为测试（RFC §7.1/§7.5/§8.1，§15.2）。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

const (
	testHostID  = "host_build1"
	testSecret  = "s3cret"
	testCredent = "atw_host_" + testHostID + "_" + testSecret
)

// newHandshakeGateway 构造带 enrollment host 的网关与 WSS 服务端。
func newHandshakeGateway(t *testing.T) (*Gateway, *fakeStore, string) {
	t.Helper()
	store := &fakeStore{
		hosts:   newFakeHostRepo(testEnrollmentHost(testHostID, testSecret)),
		runners: newFakeRunnerRepo(),
		runs:    &fakeRunRepo{},
	}
	g := New(store, newFakeEngine(), nil)
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	return g, store, "ws" + strings.TrimPrefix(srv.URL, "http") + ConnectPath
}

// helloEnvelope 构造 v2 hello 帧。
func helloEnvelope(hostID, runnerID, epoch string, versions []int, mounts []domain.HostMount) []byte {
	if versions == nil {
		versions = []int{2}
	}
	payload, _ := json.Marshal(helloPayload{
		HostID: hostID, RunnerID: runnerID, BootID: "boot_" + runnerID, ConnectionEpoch: epoch,
		ProtocolVersions: versions, RunnerVersion: "1.0.0", Slots: 2,
		Adapters: []adapterInfo{{ID: "mock", Version: "1.0.0", SchemaDigest: "sha256:mock-v1"}},
		Mounts:   mounts,
	})
	b, _ := json.Marshal(Envelope{
		V: ProtocolVersion, MessageID: "msg_hello_t", Kind: "request", Method: "runner.hello",
		RunnerID: runnerID, Payload: payload,
	})
	return b
}

// dialWithCredential 建立 WSS 连接（带 enrollment 凭据头）。
func dialWithCredential(t *testing.T, url, credential string) *websocket.Conn {
	t.Helper()
	header := map[string][]string{}
	if credential != "" {
		header["Authorization"] = []string{"Bearer " + credential}
	}
	conn, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// 未 enrollment 的 Host：WSS 升级前 401，连接建立失败。
func TestUnenrolledHostRejectedAtUpgrade(t *testing.T) {
	_, _, url := newHandshakeGateway(t)
	if _, _, err := websocket.DefaultDialer.Dial(url, map[string][]string{
		"Authorization": {"Bearer atw_host_host_unknown_secret"},
	}); err == nil {
		t.Fatal("未 enrollment 的 Host 必须在升级阶段被 401 拒绝")
	}
}

// 凭据 secret 与 enrollment_ref 不匹配：拒绝。
func TestBadSecretRejected(t *testing.T) {
	_, _, url := newHandshakeGateway(t)
	if _, _, err := websocket.DefaultDialer.Dial(url, map[string][]string{
		"Authorization": {"Bearer atw_host_" + testHostID + "_wrong"},
	}); err == nil {
		t.Fatal("secret 与 enrollment_ref 不匹配必须拒绝")
	}
}

// v1 hello 拒绝（protocol_versions 不含 2）：断连，不注册 Runner。
func TestV1HelloRejected(t *testing.T) {
	g, store, url := newHandshakeGateway(t)
	conn := dialWithCredential(t, url, testCredent)

	_ = conn.WriteMessage(websocket.TextMessage, helloEnvelope(testHostID, "runner_v1", "epoch_1", []int{1}, nil))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("protocol_versions=[1] 必须 401/断连")
	}
	time.Sleep(50 * time.Millisecond)
	if upserts, _, _, _ := store.runners.Snapshot(); len(upserts) != 0 {
		t.Fatalf("v1 hello 不得注册 runner，实际 %d", len(upserts))
	}
	_ = g
}

// envelope v≠2 拒绝（protocol_versions=[2] 但 envelope v=1 的组合必须死）。
func TestV1EnvelopeRejected(t *testing.T) {
	_, store, url := newHandshakeGateway(t)
	conn := dialWithCredential(t, url, testCredent)

	payload, _ := json.Marshal(helloPayload{
		HostID: testHostID, RunnerID: "runner_v1env", BootID: "boot_runner_v1env", ConnectionEpoch: "epoch_1",
		ProtocolVersions: []int{2}, Slots: 1,
	})
	b, _ := json.Marshal(Envelope{V: 1, MessageID: "msg_1", Kind: "request", Method: "runner.hello", Payload: payload})
	_ = conn.WriteMessage(websocket.TextMessage, b)
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("envelope v=1 必须断连")
	}
	time.Sleep(50 * time.Millisecond)
	if upserts, _, _, _ := store.runners.Snapshot(); len(upserts) != 0 {
		t.Fatalf("v=1 envelope 不得注册 runner，实际 %d", len(upserts))
	}
}

// hello.host_id 与凭据 subject 不一致：断连（错误 Host 接单防线）。
func TestHelloHostMismatchRejected(t *testing.T) {
	_, store, url := newHandshakeGateway(t)
	conn := dialWithCredential(t, url, testCredent)

	_ = conn.WriteMessage(websocket.TextMessage, helloEnvelope("host_other", "runner_x", "epoch_1", nil, nil))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("hello.host_id 与凭据不符必须断连")
	}
	time.Sleep(50 * time.Millisecond)
	if upserts, _, _, _ := store.runners.Snapshot(); len(upserts) != 0 {
		t.Fatalf("身份不符的 hello 不得注册 runner，实际 %d", len(upserts))
	}
}

// 合法 hello：welcome（v2）、runner upsert（ExecutionHostID/ConnectionEpoch）、
// mount 广告投影、host ready；hello 不创建 Host。
func TestHelloRegistersRunnerAndMounts(t *testing.T) {
	g, store, url := newHandshakeGateway(t)
	conn := dialWithCredential(t, url, testCredent)

	mounts := []domain.HostMount{{
		Alias: "agent-work", RepositoryIdentity: "repo_aw",
		RegistryGeneration: "gen_1",
		SupportedRefKinds:  []domain.RefKind{domain.RefRoot, domain.RefBranch, domain.RefWorktree},
		Checkouts:          []domain.MountCheckout{{Ref: "root", Kind: "root", Branch: "main", Head: "abc"}},
	}}
	_ = conn.WriteMessage(websocket.TextMessage, helloEnvelope(testHostID, "runner_hs", "epoch_1", nil, mounts))

	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("welcome 未到达: %v", err)
	}
	var welcome Envelope
	if err := json.Unmarshal(raw, &welcome); err != nil {
		t.Fatal(err)
	}
	if welcome.V != ProtocolVersion || welcome.Method != "server.welcome" {
		t.Fatalf("welcome 形态错误: v=%d method=%s", welcome.V, welcome.Method)
	}
	var wp welcomePayload
	if err := json.Unmarshal(welcome.Payload, &wp); err != nil {
		t.Fatal(err)
	}
	if wp.SelectedVersion != 2 || wp.HeartbeatIntervalSeconds < 1 || wp.LeasePolicy.TTLSeconds < 1 {
		t.Fatalf("welcome payload 缺 lease/heartbeat 参数: %+v", wp)
	}

	// runner upsert 带 host/epoch；host 投影 ready。
	deadline := time.After(2 * time.Second)
	var upserts []*application.Runner
	var hostStatuses [][2]string
	var hostMounts map[string]*domain.HostMount
	for {
		upserts, hostStatuses, _, _ = store.runners.Snapshot()
		_, hostStatuses, hostMounts = store.hosts.Snapshot()
		if len(upserts) > 0 && len(hostStatuses) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("注册投影未发生")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	up := upserts[0]
	if up.ID != "runner_hs" || up.ExecutionHostID != testHostID || up.ConnectionEpoch != "epoch_1" {
		t.Fatalf("runner upsert 身份错误: %+v", up)
	}
	if hostStatuses[0] != [2]string{testHostID, string(domain.HostStatusReady)} {
		t.Fatalf("host 状态投影错误: %v", hostStatuses)
	}
	m := hostMounts[testHostID+"|agent-work"]
	if m == nil || m.RegistryGeneration != "gen_1" || len(m.Checkouts) != 1 {
		t.Fatalf("mount 广告投影缺失: %+v", m)
	}
	// hello 不创建 Host：只 enrollment 过的那一个存在。
	if hostCount, _, _ := store.hosts.Snapshot(); hostCount != 1 {
		t.Fatalf("hello 不得创建 Host，实际 hosts=%d", hostCount)
	}

	// 握手捕获 adapter digest（能力校验的 runner 侧真相）。
	if !g.VerifyAdapterDigest("mock", "sha256:mock-v1") {
		t.Fatal("握手应捕获 runner 上报的 schema_digest")
	}
}

// hello 内同 host+alias 歧义：重复 alias 的 mount 投影为 unavailable。
func TestHelloDuplicateAliasProjectedUnavailable(t *testing.T) {
	_, store, url := newHandshakeGateway(t)
	conn := dialWithCredential(t, url, testCredent)

	dup := func(gen string) domain.HostMount {
		return domain.HostMount{Alias: "agent-work", RepositoryIdentity: "repo_aw", RegistryGeneration: gen}
	}
	_ = conn.WriteMessage(websocket.TextMessage, helloEnvelope(testHostID, "runner_dup", "epoch_1", nil, []domain.HostMount{dup("g1"), dup("g2")}))

	deadline := time.After(2 * time.Second)
	var m *domain.HostMount
	for {
		_, _, hostMounts := store.hosts.Snapshot()
		m = hostMounts[testHostID+"|agent-work"]
		if m != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("mount 投影未发生")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	if m.Status != domain.MountStatusUnavailable {
		t.Fatalf("歧义 alias 应投影 unavailable，实际 %q", m.Status)
	}
}

// 新 epoch 顶替旧连接：旧 transport 关闭（读循环退出）、新 epoch 注册投影。
func TestNewEpochSupersedesOldConnection(t *testing.T) {
	g, store, url := newHandshakeGateway(t)

	conn1 := dialWithCredential(t, url, testCredent)
	_ = conn1.WriteMessage(websocket.TextMessage, helloEnvelope(testHostID, "runner_sup", "epoch_1", nil, nil))
	if _, _, err := conn1.ReadMessage(); err != nil {
		t.Fatalf("第一个 welcome 未到达: %v", err)
	}

	conn2 := dialWithCredential(t, url, testCredent)
	_ = conn2.WriteMessage(websocket.TextMessage, helloEnvelope(testHostID, "runner_sup", "epoch_2", nil, nil))
	if _, _, err := conn2.ReadMessage(); err != nil {
		t.Fatalf("第二个 welcome 未到达: %v", err)
	}

	// 新 epoch 注册投影。
	deadline := time.After(2 * time.Second)
	for {
		upserts, _, _, _ := store.runners.Snapshot()
		if len(upserts) >= 2 && upserts[len(upserts)-1].ConnectionEpoch == "epoch_2" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("新 epoch 应写入 runner: %+v", upserts)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	// 旧 transport 已被顶替关闭。
	_ = conn1.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn1.ReadMessage(); err == nil {
		t.Fatal("被顶替的旧连接必须被网关关闭")
	}
	// conns 表只留当前连接。
	g.mu.Lock()
	cur := g.conns["runner_sup"]
	g.mu.Unlock()
	if cur == nil || cur.epoch != "epoch_2" {
		t.Fatalf("当前连接应为 epoch_2，实际 %+v", cur)
	}
}

// A reconnect must restore the database-owned lease/fence before accepting
// pending events. The old connection's deferred disconnect is deliberately
// invoked after the replacement to prove it cannot mark the new epoch offline.
func TestReconnectRestoresActiveLeaseAndLeavesNewEpochOnline(t *testing.T) {
	engine := newFakeEngine()
	runners := newFakeRunnerRepo()
	runners.leases = append(runners.leases, &application.RunLease{
		LeaseID: "lease_resume", RunID: "run_resume", RunnerID: "runner_resume", FencingToken: 9,
	})
	hosts := newFakeHostRepo(testEnrollmentHost(testHostID, testSecret))
	g := New(&fakeStore{hosts: hosts, runners: runners, runs: &fakeRunRepo{}}, engine, nil)

	old := &runnerConn{
		gw: g, runnerID: "runner_resume", hostID: testHostID, epoch: "epoch_1",
		send: make(chan []byte, 4), activeRuns: map[string]*activeRun{
			"run_resume": {LeaseID: "lease_resume", FencingToken: 9},
		},
	}
	g.mu.Lock()
	g.conns[old.runnerID] = old
	g.mu.Unlock()

	current := &runnerConn{
		gw: g, runnerID: old.runnerID, hostID: testHostID, epoch: "epoch_2", slots: 2,
		adapters: []adapterInfo{{ID: "mock", Version: "1", SchemaDigest: "d"}},
		send:     make(chan []byte, 8), activeRuns: map[string]*activeRun{},
	}
	if err := g.registerRunner(context.Background(), current); err != nil {
		t.Fatalf("register replacement: %v", err)
	}
	current.mu.Lock()
	lease := current.activeRuns["run_resume"]
	current.mu.Unlock()
	if lease == nil || lease.LeaseID != "lease_resume" || lease.FencingToken != 9 {
		t.Fatalf("new epoch 未恢复 DB lease/fence: %+v", lease)
	}

	// The old read loop can return after the new conn is installed; it must be a
	// no-op for health and run state rather than taking the replacement offline.
	g.handleDisconnect(old)
	_, runnerStatuses, _, _ := runners.Snapshot()
	for _, status := range runnerStatuses {
		if status == [2]string{"runner_resume", "offline"} {
			t.Fatalf("superseded connection 不得将新 epoch 下线: %v", runnerStatuses)
		}
	}
	_, hostStatuses, _ := hosts.Snapshot()
	for _, status := range hostStatuses {
		if status == [2]string{testHostID, string(domain.HostStatusOffline)} {
			t.Fatalf("superseded connection 不得将 Host 下线: %v", hostStatuses)
		}
	}

	payload, _ := json.Marshal(eventPayload{
		RunID: "run_resume", LeaseID: "lease_resume", RunnerID: "runner_resume", ConnectionEpoch: "epoch_2",
		FencingToken: 9, EventID: "revt_resume", ProducerSeq: 1,
		Event: runnerEvent{Kind: "run.status_changed", Data: map[string]any{"status": "succeeding"}},
	})
	g.handleMessage(current, Envelope{
		V: ProtocolVersion, MessageID: "msg_resume", Kind: "event", Method: "run.event",
		RunnerID: "runner_resume", RunID: "run_resume", Payload: payload,
	})
	if len(engine.inputs) != 1 || engine.inputs[0].ConnectionEpoch != "epoch_2" {
		t.Fatalf("重连 pending event 应进入 ApplyRunnerEvent: %+v", engine.inputs)
	}
	ack := readAck(t, current)
	if ack.EventID != "revt_resume" || ack.AckedProducerSeq != 1 {
		t.Fatalf("重连 event ACK 错误: %+v", ack)
	}
}

// Gateway 终态释放 activeRuns 后，合法的迟到 usage.updated 仍须到达
// Application；lease/run/runner/fencing/terminal 的最终裁决不在 Gateway 重复。
func TestTerminalUsageEventReachesEngineAfterGatewayRelease(t *testing.T) {
	engine := newFakeEngine()
	g, rc := newEventGateway(t, engine)

	g.handleMessage(rc, eventEnvelope("run_1", "lease_1", "epoch_1", 7, "revt_terminal", 1,
		"run.status_changed", map[string]any{"status": "succeeded"}))
	readAck(t, rc)
	rc.mu.Lock()
	_, active := rc.activeRuns["run_1"]
	rc.mu.Unlock()
	if active {
		t.Fatal("终态 status ACK 后 Gateway 必须清理 activeRuns")
	}

	g.handleMessage(rc, eventEnvelope("run_1", "lease_1", "epoch_1", 7, "revt_late_usage", 2,
		domain.EventUsageUpdated, map[string]any{
			"input_tokens": 3, "output_tokens": 2, "cached_tokens": 0, "basis": "per_run",
		}))
	ack := readAck(t, rc)
	if len(engine.inputs) != 2 || engine.inputs[1].Kind != domain.EventUsageUpdated {
		t.Fatalf("终态后迟到 usage.updated 应到达 Application: inputs=%+v", engine.inputs)
	}
	if ack.EventID != "revt_late_usage" || ack.AckedProducerSeq != 2 || ack.LeaseID != "lease_1" {
		t.Fatalf("迟到 usage ACK 身份错误: %+v", ack)
	}
}

// 终态后例外仍绑定当前连接身份；错误 Runner/epoch 不得进入 Application。
func TestTerminalUsageWrongConnectionIdentityRemainsStale(t *testing.T) {
	for _, tc := range []struct {
		name   string
		runner string
		epoch  string
	}{
		{name: "runner", runner: "runner_other", epoch: "epoch_1"},
		{name: "epoch", runner: "runner_1", epoch: "epoch_old"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := newFakeEngine()
			g, rc := newEventGateway(t, engine)
			g.handleMessage(rc, eventEnvelope("run_1", "lease_1", "epoch_1", 7, "revt_terminal_"+tc.name, 1,
				"run.status_changed", map[string]any{"status": "succeeded"}))
			readAck(t, rc)

			payload, _ := json.Marshal(eventPayload{
				RunID: "run_1", LeaseID: "lease_1", RunnerID: tc.runner, ConnectionEpoch: tc.epoch,
				FencingToken: 7, EventID: "revt_wrong_" + tc.name, ProducerSeq: 2,
				Event: runnerEvent{Kind: domain.EventUsageUpdated, Data: map[string]any{
					"input_tokens": 3, "output_tokens": 2, "cached_tokens": 0, "basis": "per_run",
				}},
			})
			g.handleMessage(rc, Envelope{
				V: ProtocolVersion, MessageID: "msg_wrong_" + tc.name, Kind: "event", Method: "run.event",
				RunnerID: "runner_1", RunID: "run_1", Payload: payload,
			})
			if len(engine.inputs) != 1 {
				t.Fatalf("错误 %s 身份的迟到 usage 不得进入 Application: %+v", tc.name, engine.inputs)
			}
			ack := readAck(t, rc)
			if ack.EventID != "revt_wrong_"+tc.name || ack.AckedProducerSeq != 2 {
				t.Fatalf("错误身份 stale ACK 失真: %+v", ack)
			}
		})
	}
}

// activeRuns 已清理时，终态观测例外只覆盖 usage.updated；status/session/message
// 等其他事件仍必须由 Gateway 直接 stale ACK。
func TestTerminalNonUsageEventRemainsStale(t *testing.T) {
	for _, kind := range []string{"run.status_changed", "run.session", domain.EventMessageDelta} {
		t.Run(kind, func(t *testing.T) {
			engine := newFakeEngine()
			g, rc := newEventGateway(t, engine)
			g.handleMessage(rc, eventEnvelope("run_1", "lease_1", "epoch_1", 7, "revt_terminal_"+kind, 1,
				"run.status_changed", map[string]any{"status": "succeeded"}))
			readAck(t, rc)

			data := map[string]any{}
			if kind == "run.status_changed" {
				data["status"] = "running"
			}
			g.handleMessage(rc, eventEnvelope("run_1", "lease_1", "epoch_1", 7, "revt_nonusage_"+kind, 2, kind, data))
			if len(engine.inputs) != 1 {
				t.Fatalf("非 usage %s 不得进入 Application: %+v", kind, engine.inputs)
			}
			ack := readAck(t, rc)
			if ack.EventID != "revt_nonusage_"+kind || ack.AckedProducerSeq != 2 {
				t.Fatalf("非 usage stale ACK 失真: %+v", ack)
			}
		})
	}
}

// 迟到 usage 重放仍由 Application dedup；Gateway 对 duplicate ACK，不重复应用。
func TestTerminalUsageDuplicateStillAcks(t *testing.T) {
	engine := newFakeEngine()
	g, rc := newEventGateway(t, engine)
	g.handleMessage(rc, eventEnvelope("run_1", "lease_1", "epoch_1", 7, "revt_terminal_dup", 1,
		"run.status_changed", map[string]any{"status": "succeeded"}))
	readAck(t, rc)
	usage := eventEnvelope("run_1", "lease_1", "epoch_1", 7, "revt_usage_dup", 2,
		domain.EventUsageUpdated, map[string]any{"input_tokens": 3, "output_tokens": 2, "cached_tokens": 0, "basis": "per_run"})
	g.handleMessage(rc, usage)
	g.handleMessage(rc, usage)
	first := readAck(t, rc)
	second := readAck(t, rc)
	if len(engine.inputs) != 3 || engine.applied != 2 {
		t.Fatalf("迟到 usage 重放应两次进 Application 但只应用一次: inputs=%d applied=%d", len(engine.inputs), engine.applied)
	}
	if first.EventID != "revt_usage_dup" || second.EventID != "revt_usage_dup" ||
		first.AckedProducerSeq != 2 || second.AckedProducerSeq != 2 {
		t.Fatalf("duplicate ACK 应回显同一事件身份: first=%+v second=%+v", first, second)
	}
}

// Enrollment credential rotation must revoke the old connection before a new
// offer can create a lease or reach the remote Host.
func TestCredentialRotationRevokesCurrentConnectionBeforeDispatch(t *testing.T) {
	g, store, url := newHandshakeGateway(t)
	conn := dialWithCredential(t, url, testCredent)
	_ = conn.WriteMessage(websocket.TextMessage, helloEnvelope(testHostID, "runner_revoked", "epoch_1", nil, nil))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("welcome 未到达: %v", err)
	}

	store.hosts.mu.Lock()
	store.hosts.hosts[testHostID].EnrollmentRef = enrollmentDigest("rotated_secret")
	store.hosts.mu.Unlock()

	err := g.Dispatch(context.Background(), &domain.ExecutionRun{ID: "run_revoked", AgentProfileID: "agent_revoked"}, rootSnapshotFor(testHostID), "mock")
	if err == nil {
		t.Fatal("credential 已轮换的连接不得继续 dispatch")
	}
	g.mu.Lock()
	_, stillConnected := g.conns["runner_revoked"]
	g.mu.Unlock()
	if stillConnected {
		t.Fatal("credential 已轮换后当前连接必须被撤销")
	}
	_, _, leases, _ := store.runners.Snapshot()
	if len(leases) != 0 {
		t.Fatalf("已撤销 connection 不得创建 lease: %+v", leases)
	}
}

// A Runner ID is permanently bound to its enrolled Host. A credential for a
// different valid Host must not be able to reuse that ID, replace its epoch,
// or recover its active lease/fence.
func TestRunnerIDCannotMoveAcrossHosts(t *testing.T) {
	const (
		hostA   = "host_runa"
		hostB   = "host_runb"
		secretA = "secret-a"
		secretB = "secret-b"
		runner  = "runner_bound"
	)
	store := &fakeStore{
		hosts: newFakeHostRepo(
			testEnrollmentHost(hostA, secretA),
			testEnrollmentHost(hostB, secretB),
		),
		runners: newFakeRunnerRepo(),
		runs:    &fakeRunRepo{},
	}
	store.runners.leases = append(store.runners.leases, &application.RunLease{
		LeaseID: "lease_bound", RunID: "run_bound", RunnerID: runner, FencingToken: 11,
	})
	g := New(store, newFakeEngine(), nil)
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + ConnectPath

	connA := dialWithCredential(t, url, "atw_host_"+hostA+"_"+secretA)
	if err := connA.WriteMessage(websocket.TextMessage, helloEnvelope(hostA, runner, "epoch_a", nil, nil)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := connA.ReadMessage(); err != nil {
		t.Fatalf("host A welcome: %v", err)
	}
	g.mu.Lock()
	current := g.conns[runner]
	g.mu.Unlock()
	if current == nil || current.hostID != hostA || current.activeRuns["run_bound"] == nil {
		t.Fatalf("host A 应持有恢复的 lease: %+v", current)
	}

	connB := dialWithCredential(t, url, "atw_host_"+hostB+"_"+secretB)
	if err := connB.WriteMessage(websocket.TextMessage, helloEnvelope(hostB, runner, "epoch_b", nil, nil)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := connB.ReadMessage(); err == nil {
		t.Fatal("host B 复用 host A 的 runner_id 必须被拒绝")
	}
	g.mu.Lock()
	current = g.conns[runner]
	g.mu.Unlock()
	if current == nil || current.hostID != hostA || current.epoch != "epoch_a" {
		t.Fatalf("host B 不得顶替 host A runner: %+v", current)
	}
	if lease := current.activeRuns["run_bound"]; lease == nil || lease.FencingToken != 11 {
		t.Fatalf("host B 不得接管 host A lease: %+v", lease)
	}
}

func TestBootChangeDoesNotRestoreOrRenewOrphanedLease(t *testing.T) {
	engine := newFakeEngine()
	runners := newFakeRunnerRepo()
	runners.upserts = append(runners.upserts, &application.Runner{
		ID: "runner_boot", ExecutionHostID: testHostID, BootID: "boot_old", ConnectionEpoch: "epoch_old",
	})
	runners.leases = append(runners.leases, &application.RunLease{
		LeaseID: "lease_boot", RunID: "run_boot", RunnerID: "runner_boot", FencingToken: 4,
	})
	g := New(&fakeStore{
		hosts:   newFakeHostRepo(testEnrollmentHost(testHostID, testSecret)),
		runners: runners,
		runs:    &fakeRunRepo{},
	}, engine, nil)
	engine.runs["run_boot"] = domain.RunRunning
	rc := &runnerConn{
		gw: g, runnerID: "runner_boot", hostID: testHostID, bootID: "boot_new", epoch: "epoch_new", slots: 1,
		adapters: []adapterInfo{{ID: "mock", Version: "1", SchemaDigest: "d"}},
		send:     make(chan []byte, 4), activeRuns: map[string]*activeRun{},
	}
	if err := g.registerRunner(context.Background(), rc); err != nil {
		t.Fatal(err)
	}
	g.settleRestartedRuns(rc.restartedRunIDs)
	if !runners.leases[0].Released {
		t.Fatal("不同 boot_id 必须释放旧 lease")
	}
	if _, restored := rc.activeRuns["run_boot"]; restored {
		t.Fatal("不同 boot_id 不得恢复旧进程的 active lease")
	}
	if len(engine.statuses) != 2 || engine.statuses[0] != domain.RunReconnecting || engine.statuses[1] != domain.RunLost {
		t.Fatalf("旧 running Run 应收敛 reconnecting→lost: %v", engine.statuses)
	}

	// 同一 boot 的网络重连则必须恢复 lease，且不可再次收敛为 lost。
	runners.leases = append(runners.leases, &application.RunLease{
		LeaseID: "lease_same_boot", RunID: "run_same_boot", RunnerID: "runner_boot", FencingToken: 5,
	})
	engine.statuses = nil
	second := &runnerConn{
		gw: g, runnerID: "runner_boot", hostID: testHostID, bootID: "boot_new", epoch: "epoch_reconnect", slots: 1,
		adapters: []adapterInfo{{ID: "mock", Version: "1", SchemaDigest: "d"}},
		send:     make(chan []byte, 4), activeRuns: map[string]*activeRun{},
	}
	if err := g.registerRunner(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if got := second.activeRuns["run_same_boot"]; got == nil || got.FencingToken != 5 {
		t.Fatalf("同 boot 重连应恢复 lease: %+v", got)
	}
	if len(engine.statuses) != 0 {
		t.Fatalf("同 boot 网络重连不得收敛 Run: %v", engine.statuses)
	}
}

// Restart recovery can synchronously trigger a retry dispatch through terminal
// hooks. server.welcome must nevertheless be first on the new connection so a
// runner never observes run.offer before protocol negotiation completes.
func TestRestartRecoveryQueuesWelcomeBeforeRetryOffer(t *testing.T) {
	runners := newFakeRunnerRepo()
	runners.upserts = append(runners.upserts, &application.Runner{
		ID: "runner_restart", ExecutionHostID: testHostID, BootID: "boot_old", ConnectionEpoch: "epoch_old",
	})
	runners.leases = append(runners.leases, &application.RunLease{
		LeaseID: "lease_restart", RunID: "run_restart", RunnerID: "runner_restart", FencingToken: 1,
	})
	store := &fakeStore{
		hosts:   newFakeHostRepo(testEnrollmentHost(testHostID, testSecret)),
		runners: runners,
		runs:    &fakeRunRepo{},
	}
	engine := newFakeEngine()
	engine.runs["run_restart"] = domain.RunRunning
	g := New(store, engine, nil)
	engine.onStatus = func(_ string, to domain.RunStatus) {
		if to == domain.RunLost {
			if err := g.Dispatch(context.Background(), &domain.ExecutionRun{ID: "run_retry", AgentProfileID: "agent_retry", Input: map[string]any{}}, rootSnapshotFor(testHostID), "mock"); err != nil {
				t.Errorf("restart recovery retry dispatch: %v", err)
			}
		}
	}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + ConnectPath
	conn := dialWithCredential(t, url, testCredent)
	if err := conn.WriteMessage(websocket.TextMessage, helloEnvelope(testHostID, "runner_restart", "epoch_new", nil, nil)); err != nil {
		t.Fatal(err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var first Envelope
	if err := json.Unmarshal(raw, &first); err != nil {
		t.Fatal(err)
	}
	if first.Method != "server.welcome" {
		t.Fatalf("重启收口触发 retry 时首帧必须 welcome，实际 %s", first.Method)
	}
	_, raw, err = conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var second Envelope
	if err := json.Unmarshal(raw, &second); err != nil {
		t.Fatal(err)
	}
	if second.Method != "run.offer" {
		t.Fatalf("welcome 后应才发 retry offer，实际 %s", second.Method)
	}
}

// 被顶替但 transport 尚在排空的旧连接：迟到帧 ACK 但不应用（防 Runner 永久
// 重放的兜底路径；transport 已关时网关直接丢弃，无帧可发）。
func TestSupersededConnLateFrameAckedNotApplied(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRunnerRepo()
	g := New(&fakeStore{runners: repo}, engine, nil)

	old := &runnerConn{
		gw: g, runnerID: "runner_sup", hostID: testHostID, epoch: "epoch_1",
		send: make(chan []byte, 4), activeRuns: map[string]*activeRun{},
	}
	g.mu.Lock()
	g.conns["runner_sup"] = old
	g.mu.Unlock()

	// 新 epoch 注册：替换 conns 并标记旧连接被顶替（registerRunner 的
	// 顶替语义；此处不关 transport，模拟帧在关闭前已被读出）。
	g.mu.Lock()
	old.superseded = true
	g.conns["runner_sup"] = &runnerConn{
		gw: g, runnerID: "runner_sup", hostID: testHostID, epoch: "epoch_2",
		send: make(chan []byte, 4), activeRuns: map[string]*activeRun{},
	}
	g.mu.Unlock()

	payload, _ := json.Marshal(eventPayload{
		RunID: "run_1", LeaseID: "lease_1", RunnerID: "runner_sup", ConnectionEpoch: "epoch_1",
		FencingToken: 7, EventID: "revt_1", ProducerSeq: 1,
		Event: runnerEvent{Kind: "message.delta"},
	})
	g.handleMessage(old, Envelope{
		V: ProtocolVersion, MessageID: "msg_e", Kind: "event", Method: "run.event",
		RunnerID: "runner_sup", RunID: "run_1", Payload: payload,
	})

	if len(engine.inputs) != 0 {
		t.Fatal("旧 epoch 帧不得应用")
	}
	select {
	case raw := <-old.send:
		if !strings.Contains(string(raw), `"ack"`) {
			t.Fatalf("旧 epoch 帧应回 ACK，实际 %s", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("旧 epoch 帧未收到 ACK")
	}
}

// ── host-aware 分派（RFC §7.5）───────────────────────────────────────

// rootSnapshotFor 构造指向指定 host 的 root 快照（digest 自洽）。
func rootSnapshotFor(hostID string) *domain.ExecutionContextSnapshot {
	s := &domain.ExecutionContextSnapshot{
		ID: domain.NewID(domain.PrefixCtxSnapshot), RunID: domain.NewID(domain.PrefixRun),
		SchemaVersion: domain.SnapshotSchemaV1, WorkspaceID: "ws_1",
		WorkspaceLocationID: "wsloc_1", LocationVersion: 1,
		MountGeneration: "gen_1", ExecutionHostID: hostID, MountAlias: "agent-work",
		RepositoryIdentity: "repo_aw", RefKind: domain.RefRoot, ContextGeneration: 1,
		Source: domain.SnapshotSourceCurrent,
	}
	s.SnapshotDigest = s.ComputeDigest()
	return s
}

// newDispatchGateway 注册两个 host 的连接（都广告 mock）。
func newDispatchGateway(t *testing.T) (*Gateway, *runnerConn, *runnerConn) {
	t.Helper()
	engine := newFakeEngine()
	g := New(&fakeStore{
		hosts: newFakeHostRepo(
			testEnrollmentHost("host_a", "secret_a"),
			testEnrollmentHost("host_b", "secret_b"),
		),
		runners: newFakeRunnerRepo(),
	}, engine, nil)
	mk := func(runnerID, hostID string) *runnerConn {
		rc := &runnerConn{
			gw: g, runnerID: runnerID, hostID: hostID, epoch: "epoch_" + runnerID, slots: 2,
			adapters: []adapterInfo{{ID: "mock", Version: "1", SchemaDigest: "d"}},
			send:     make(chan []byte, 8), activeRuns: map[string]*activeRun{},
		}
		g.mu.Lock()
		g.conns[runnerID] = rc
		g.mu.Unlock()
		return rc
	}
	rcA := mk("runner_a", "host_a")
	rcB := mk("runner_b", "host_b")
	t.Cleanup(func() {
		g.mu.Lock()
		g.conns = map[string]*runnerConn{}
		g.mu.Unlock()
	})
	return g, rcA, rcB
}

// readOffer 从连接缓冲取 run.offer 并解析；超时 Fatal。
func readOffer(t *testing.T, rc *runnerConn) offerPayload {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case raw := <-rc.send:
			var env Envelope
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatal(err)
			}
			if env.V != ProtocolVersion || env.Method != "run.offer" {
				t.Fatalf("期望 v2 run.offer，实际 v=%d method=%s", env.V, env.Method)
			}
			var p offerPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatal(err)
			}
			return p
		case <-deadline:
			t.Fatal("等待 run.offer 超时")
			return offerPayload{}
		}
	}
}

// 两 Host 同 adapter：快照绑定哪个 Host 就只投哪个 Host 的 runner。
func TestDispatchRoutesToExactHost(t *testing.T) {
	g, rcA, rcB := newDispatchGateway(t)
	run := &domain.ExecutionRun{ID: "run_1", AgentProfileID: "agent_dispatch", Input: map[string]any{"instruction": "hi"}}

	if err := g.Dispatch(context.Background(), run, rootSnapshotFor("host_b"), "mock"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	// 只投 host_b。
	select {
	case <-rcA.send:
		t.Fatal("offer 不得投给非快照 Host 的 runner")
	default:
	}
	p := readOffer(t, rcB)
	if p.RunSpec.RunID != "run_1" || p.RunSpec.AdapterID != "mock" {
		t.Fatalf("offer 内容错误: %+v", p.RunSpec)
	}
	// agent_profile_id 随 run_spec 下发：远程 adapter 绑定 provider usage
	// report provenance.agent_id 的身份来源（缺失则 report 造不出来）。
	if p.RunSpec.AgentProfileID != "agent_dispatch" {
		t.Fatalf("run_spec.agent_profile_id 丢失: %+v", p.RunSpec)
	}
	// offer 携带完整 context_snapshot 且不含宿主路径字段。
	ws := p.RunSpec.ContextSnapshot
	if ws.ExecutionHostID != "host_b" || ws.SnapshotDigest == "" || ws.MountGeneration != "gen_1" {
		t.Fatalf("offer context_snapshot 缺身份: %+v", ws)
	}
	// 广告字段只含 opaque 身份：workspace_alias 命名与契约一致。
	if ws.WorkspaceAlias != "agent-work" || ws.RefKind != domain.RefRoot {
		t.Fatalf("context_snapshot 字段错误: %+v", ws)
	}
}

// 无匹配 Host：报错且不创建租约（禁止跨 Host/本机回退）。
func TestDispatchNoMatchDoesNotFallback(t *testing.T) {
	g, _, _ := newDispatchGateway(t)
	store := g.store.(*fakeStore)

	err := g.Dispatch(context.Background(), &domain.ExecutionRun{ID: "run_x", AgentProfileID: "agent_dispatch"}, rootSnapshotFor("host_nobody"), "mock")
	if err == nil {
		t.Fatal("无匹配 host 必须报错")
	}
	if !strings.Contains(err.Error(), "host_nobody") {
		t.Fatalf("错误应指明目标 host: %v", err)
	}
	if len(store.runners.leases) != 0 {
		t.Fatalf("不得创建租约，实际 %d", len(store.runners.leases))
	}
}

func TestDispatchRequiresRunAndAgentIdentity(t *testing.T) {
	g, _, _ := newDispatchGateway(t)
	snapshot := rootSnapshotFor("host_a")
	for _, run := range []*domain.ExecutionRun{
		nil,
		{ID: "run_missing_agent"},
		{AgentProfileID: "agent_dispatch"},
	} {
		if err := g.Dispatch(context.Background(), run, snapshot, "mock"); !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("missing Run/agent identity must fail closed: run=%+v err=%v", run, err)
		}
	}
}

// adapter 不匹配同样不回退。
func TestDispatchAdapterMismatchDoesNotFallback(t *testing.T) {
	g, _, _ := newDispatchGateway(t)
	if err := g.Dispatch(context.Background(), &domain.ExecutionRun{ID: "run_x", AgentProfileID: "agent_dispatch"}, rootSnapshotFor("host_a"), "codex-appserver"); err == nil {
		t.Fatal("无 runner 广告该 adapter 必须报错")
	}
}

// 容量满：不投递。
func TestDispatchRespectsCapacity(t *testing.T) {
	g, rcA, _ := newDispatchGateway(t)
	rcA.slots = 1
	rcA.mu.Lock()
	rcA.activeRuns["run_full"] = &activeRun{LeaseID: "lease_0", FencingToken: 1}
	rcA.mu.Unlock()

	if err := g.Dispatch(context.Background(), &domain.ExecutionRun{ID: "run_new", AgentProfileID: "agent_dispatch"}, rootSnapshotFor("host_a"), "mock"); err == nil {
		t.Fatal("容量满必须报错")
	}
	select {
	case raw := <-rcA.send:
		t.Fatalf("容量满不得下发 offer: %s", raw)
	default:
	}
}

func TestDispatchOfferEnqueueFailureReleasesLeaseAndDropsDeadConnection(t *testing.T) {
	for _, mode := range []string{"closed", "backpressure"} {
		t.Run(mode, func(t *testing.T) {
			g, rcA, _ := newDispatchGateway(t)
			if mode == "closed" {
				rcA.mu.Lock()
				rcA.closed = true
				rcA.mu.Unlock()
			} else {
				for i := 0; i < cap(rcA.send); i++ {
					rcA.send <- []byte("full")
				}
			}

			err := g.Dispatch(context.Background(), &domain.ExecutionRun{ID: "run_enqueue_" + mode, AgentProfileID: "agent_dispatch"}, rootSnapshotFor("host_a"), "mock")
			if err == nil {
				t.Fatal("offer enqueue 失败必须返回错误")
			}
			rcA.mu.Lock()
			_, active := rcA.activeRuns["run_enqueue_"+mode]
			rcA.mu.Unlock()
			if active {
				t.Fatal("未送达 offer 不得保留内存 active lease")
			}
			store := g.store.(*fakeStore)
			if len(store.runners.leases) != 1 || !store.runners.leases[0].Released {
				t.Fatalf("未送达 offer 必须释放 DB lease: %+v", store.runners.leases)
			}
			g.mu.Lock()
			_, stillCurrent := g.conns[rcA.runnerID]
			g.mu.Unlock()
			if stillCurrent {
				t.Fatal("enqueue 失败的死连接不得继续参与路由")
			}
		})
	}
}

// snapshot 缺失：拒绝分派。
func TestDispatchRequiresSnapshot(t *testing.T) {
	g, _, _ := newDispatchGateway(t)
	if err := g.Dispatch(context.Background(), &domain.ExecutionRun{ID: "run_x", AgentProfileID: "agent_dispatch"}, nil, "mock"); err == nil {
		t.Fatal("无 snapshot 不得分派")
	}
}

// Dispatch 落内存 activeRuns：run.command 的 v2 framing 从这里来。
func TestForwardControlCarriesLeaseFraming(t *testing.T) {
	g, rcA, _ := newDispatchGateway(t)
	if err := g.Dispatch(context.Background(), &domain.ExecutionRun{ID: "run_1", AgentProfileID: "agent_dispatch"}, rootSnapshotFor("host_a"), "mock"); err != nil {
		t.Fatal(err)
	}
	readOffer(t, rcA) // 清掉 offer 帧

	if !g.ForwardInput(context.Background(), "run_1", "改一下命名") {
		t.Fatal("活动租约应命中")
	}
	p := readCommand(t, rcA, "input")
	if p.LeaseID == "" || p.ConnectionEpoch == "" || p.FencingToken == 0 {
		t.Fatalf("run.command framing 缺失: %+v", p)
	}
	if p.Body["instruction"] != "改一下命名" {
		t.Fatalf("input body 失真: %+v", p.Body)
	}
	if g.ForwardInput(context.Background(), "run_missing", "x") {
		t.Fatal("无活动租约不应命中")
	}
}

func readCommand(t *testing.T, rc *runnerConn, want string) commandPayload {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case raw := <-rc.send:
			var env Envelope
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatal(err)
			}
			if env.Method != "run.command" {
				continue
			}
			var p commandPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if p.Command != want {
				t.Fatalf("期望 %s，实际 %s", want, p.Command)
			}
			return p
		case <-deadline:
			t.Fatalf("等待 run.command(%s) 超时", want)
			return commandPayload{}
		}
	}
}

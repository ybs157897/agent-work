package runnergateway

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// dedupKey 是事件身份（与 runnerd 的 pendingKey 同口径）。
type dedupKey struct {
	RunID   string
	LeaseID string
	Seq     int64
}

// fakeEngine 记录全部 ApplyRunner* 调用；按 (run, lease, producer_seq) 内建
// 去重（模拟真实 ApplyRunnerEvent 单事务 dedup），用于验证「重连重发不重复
// 落库」在网关层的可观察行为：每帧都进应用命令，但只有首帧 applied。
type fakeEngine struct {
	inputs  []application.RunnerEventInput
	accepts []application.RunnerAcceptInput
	rejects []application.RunnerRejectInput
	applied int // dedup 后真实应用的次数
	dedup   map[dedupKey]bool

	ack application.RunnerEventAck // 成功调用返回值（Outcome 可注入）
	err error                      // 非空时返回瞬态错误（不 ACK）
	// statuses 记录 disconnect/sweeper 路径的状态迁移。
	statuses []domain.RunStatus
	runs     map[string]domain.RunStatus
	onStatus func(runID string, to domain.RunStatus)
	// events 记录 RecordRunEvent 调用（Run Journal phase 事件断言用）。
	events []recordedRunEvent
	// recordSink 非空时 RecordRunEvent 同步镜像进 sink：测试把 fakeStore 的
	// fakeEventRepo 接进来，使断言能经 ListRunEventsIncludeInternal 按生产
	// run_events 的读取路径全量取回。
	recordSink func(runID, evType string, data map[string]any)
}

// recordedRunEvent 是一次 RecordRunEvent 调用的实参快照。
type recordedRunEvent struct {
	RunID     string
	EventType string
	Data      map[string]any
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{dedup: map[dedupKey]bool{}, runs: map[string]domain.RunStatus{}}
}

func (f *fakeEngine) ApplyRunnerEvent(ctx context.Context, in application.RunnerEventInput) (application.RunnerEventAck, error) {
	f.inputs = append(f.inputs, in)
	key := dedupKey{RunID: in.RunID, LeaseID: in.LeaseID, Seq: in.ProducerSeq}
	if f.dedup[key] {
		ack := f.ack
		ack.RunID, ack.LeaseID, ack.RunnerID = in.RunID, in.LeaseID, in.RunnerID
		ack.FencingToken, ack.AckedProducerSeq, ack.EventID = in.FencingToken, in.ProducerSeq, in.EventID
		ack.Outcome = application.RunnerEventDuplicate
		return ack, nil
	}
	f.dedup[key] = true
	f.applied++
	if f.err != nil {
		return application.RunnerEventAck{}, f.err
	}
	ack := f.ack
	ack.RunID, ack.LeaseID, ack.RunnerID = in.RunID, in.LeaseID, in.RunnerID
	ack.FencingToken, ack.AckedProducerSeq, ack.EventID = in.FencingToken, in.ProducerSeq, in.EventID
	if ack.Outcome == "" {
		ack.Outcome = application.RunnerEventApplied
	}
	return ack, nil
}

func (f *fakeEngine) ApplyRunnerAccept(ctx context.Context, in application.RunnerAcceptInput) error {
	f.accepts = append(f.accepts, in)
	return nil
}

func (f *fakeEngine) ApplyRunnerReject(ctx context.Context, in application.RunnerRejectInput) error {
	f.rejects = append(f.rejects, in)
	return nil
}

func (f *fakeEngine) RecordRunStatus(ctx context.Context, runID string, to domain.RunStatus, data map[string]any) error {
	f.statuses = append(f.statuses, to)
	f.runs[runID] = to
	if f.onStatus != nil {
		f.onStatus(runID, to)
	}
	return nil
}

func (f *fakeEngine) RecordRunProgress(ctx context.Context, runID string, progress float64) error {
	return nil
}

func (f *fakeEngine) RecordRunEvent(ctx context.Context, runID, evType string, data map[string]any) error {
	f.events = append(f.events, recordedRunEvent{RunID: runID, EventType: evType, Data: data})
	if f.recordSink != nil {
		f.recordSink(runID, evType, data)
	}
	return nil
}

func (f *fakeEngine) RecordRunSessionUpdate(ctx context.Context, runID string, update runtime.SessionUpdate) error {
	return nil
}

func (f *fakeEngine) RecordRunUsage(ctx context.Context, runID string, usage runtime.Usage) error {
	return nil
}

func (f *fakeEngine) RequestApproval(ctx context.Context, runID, kind, risk, summary string) (*domain.ApprovalRequest, error) {
	return nil, domain.ErrNotFound
}

func (f *fakeEngine) RecordArtifact(ctx context.Context, runID string, art *domain.Artifact) error {
	return nil
}

func (f *fakeEngine) Run(ctx context.Context, id string) (*domain.ExecutionRun, error) {
	status, ok := f.runs[id]
	if !ok {
		status = domain.RunRunning
	}
	return &domain.ExecutionRun{ID: id, Status: status}, nil
}

// newEventGateway 构造带一条已注册连接（run_1 活动租约）的网关。
func newEventGateway(t *testing.T, engine *fakeEngine) (*Gateway, *runnerConn) {
	t.Helper()
	repo := newFakeRunnerRepo()
	g := New(&fakeStore{runners: repo, runs: &fakeRunRepo{}}, engine, nil)
	rc := &runnerConn{
		gw: g, runnerID: "runner_1", hostID: "host_1", epoch: "epoch_1",
		adapters:   []adapterInfo{{ID: "mock", Version: "1", SchemaDigest: "d"}},
		send:       make(chan []byte, 16),
		activeRuns: map[string]*activeRun{"run_1": {LeaseID: "lease_1", FencingToken: 7}},
	}
	g.mu.Lock()
	g.conns["runner_1"] = rc
	g.mu.Unlock()
	t.Cleanup(func() {
		g.mu.Lock()
		delete(g.conns, "runner_1")
		g.mu.Unlock()
	})
	return g, rc
}

// eventEnvelope 构造一条 v2 run.event 帧。
func eventEnvelope(runID, leaseID, epoch string, fencing int64, eventID string, seq int64, kind string, data map[string]any) Envelope {
	payload, _ := json.Marshal(eventPayload{
		RunID: runID, LeaseID: leaseID, RunnerID: "runner_1", ConnectionEpoch: epoch,
		FencingToken: fencing, EventID: eventID, ProducerSeq: seq,
		Event: runnerEvent{Kind: kind, Data: data},
	})
	return Envelope{
		V: ProtocolVersion, MessageID: "msg_" + eventID, Kind: "event", Method: "run.event",
		RunnerID: "runner_1", RunID: runID, Payload: payload,
	}
}

// readAck 从连接发送缓冲取一帧 ack 并解析 payload；超时 Fatal。
func readAck(t *testing.T, rc *runnerConn) ackPayload {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case raw := <-rc.send:
			var env Envelope
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatalf("帧解析失败: %v", err)
			}
			if env.V != ProtocolVersion || env.Method != "ack" {
				t.Fatalf("期望 v2 ack 帧，实际 v=%d method=%s", env.V, env.Method)
			}
			var p ackPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("ack payload 解析失败: %v", err)
			}
			return p
		case <-deadline:
			t.Fatal("等待 ack 帧超时")
			return ackPayload{}
		}
	}
}

// assertNoFrame 断言发送缓冲为空（短暂等待）。
func assertNoFrame(t *testing.T, rc *runnerConn) {
	t.Helper()
	select {
	case raw := <-rc.send:
		t.Fatalf("不应有下行帧，实际: %s", raw)
	case <-time.After(50 * time.Millisecond):
	}
}

// run.event 正常路径：transport 校验通过 → ApplyRunnerEvent → ACK 带全身份。
func TestRunEventAppliesAndAcks(t *testing.T) {
	engine := newFakeEngine()
	g, rc := newEventGateway(t, engine)

	g.handleMessage(rc, eventEnvelope("run_1", "lease_1", "epoch_1", 7, "revt_1", 1, "message.delta", map[string]any{"text": "hi"}))

	if len(engine.inputs) != 1 {
		t.Fatalf("ApplyRunnerEvent 应调用 1 次，实际 %d", len(engine.inputs))
	}
	in := engine.inputs[0]
	if in.RunID != "run_1" || in.LeaseID != "lease_1" || in.RunnerID != "runner_1" ||
		in.ConnectionEpoch != "epoch_1" || in.FencingToken != 7 ||
		in.EventID != "revt_1" || in.ProducerSeq != 1 || in.Kind != "message.delta" {
		t.Fatalf("事件命令字段失真: %+v", in)
	}
	ack := readAck(t, rc)
	if ack.RunID != "run_1" || ack.LeaseID != "lease_1" || ack.RunnerID != "runner_1" ||
		ack.FencingToken != 7 || ack.AckedProducerSeq != 1 || ack.EventID != "revt_1" {
		t.Fatalf("ACK 身份字段失真: %+v", ack)
	}
}

// 应用层瞬态失败：不 ACK（Runner 保留 pending 重试）。
func TestRunEventTransientFailureNotAcked(t *testing.T) {
	engine := newFakeEngine()
	engine.err = errors.New("db locked")
	g, rc := newEventGateway(t, engine)

	g.handleMessage(rc, eventEnvelope("run_1", "lease_1", "epoch_1", 7, "revt_1", 1, "message.delta", nil))

	if len(engine.inputs) != 1 {
		t.Fatalf("应用命令应被调用，实际 %d", len(engine.inputs))
	}
	assertNoFrame(t, rc)
}

// duplicate/stale 由 Ack.Outcome 表达、照常 ACK；重连重发不重复落库：
// 同一事件身份重发 N 次，fake dedup 后 applied 只 1 次，但每次都有 ACK。
func TestRunEventResendDedupStillAcks(t *testing.T) {
	engine := newFakeEngine()
	g, rc := newEventGateway(t, engine)

	for i := 0; i < 3; i++ {
		g.handleMessage(rc, eventEnvelope("run_1", "lease_1", "epoch_1", 7, "revt_1", 1, "message.delta", nil))
	}
	if engine.applied != 1 {
		t.Fatalf("重发后应用次数应仍为 1，实际 %d", engine.applied)
	}
	if len(engine.inputs) != 3 {
		t.Fatalf("每帧都应进入应用命令（dedup 在应用层），实际 %d", len(engine.inputs))
	}
	ack := readAck(t, rc)
	ack = readAck(t, rc)
	ack = readAck(t, rc)
	_ = ack
	assertNoFrame(t, rc)
}

// 与活动租约不匹配的帧（旧 lease/fencing、未知 run）：stale ACK 但不应用。
func TestStaleEventAckedButNotApplied(t *testing.T) {
	engine := newFakeEngine()
	g, rc := newEventGateway(t, engine)

	g.handleMessage(rc, eventEnvelope("run_1", "lease_OLD", "epoch_1", 7, "revt_9", 9, "message.delta", nil))

	if len(engine.inputs) != 0 {
		t.Fatalf("旧 lease 帧不得进入应用层，实际 %d", len(engine.inputs))
	}
	ack := readAck(t, rc)
	if ack.EventID != "revt_9" || ack.AckedProducerSeq != 9 || ack.LeaseID != "lease_OLD" {
		t.Fatalf("stale ACK 应回显帧身份: %+v", ack)
	}
}

// 旧 epoch 连接（被新连接顶替）的迟到帧：ACK 但不应用。
func TestSupersededConnEventAckedButNotApplied(t *testing.T) {
	engine := newFakeEngine()
	g, rc := newEventGateway(t, engine)

	g.mu.Lock()
	rc.superseded = true
	g.mu.Unlock()

	g.handleMessage(rc, eventEnvelope("run_1", "lease_1", "epoch_1", 7, "revt_1", 1, "message.delta", nil))

	if len(engine.inputs) != 0 {
		t.Fatalf("旧 epoch 帧不得进入应用层，实际 %d", len(engine.inputs))
	}
	readAck(t, rc)
}

// 终态 status 应用后：网关内存 activeRuns 与审批翻译表被清理（DB 释放归
// 应用层事务）。
func TestTerminalStatusReleasesGatewayMemory(t *testing.T) {
	engine := newFakeEngine()
	g, rc := newEventGateway(t, engine)
	g.mu.Lock()
	g.runnerApprovals["run_1"] = map[string]string{"srv_appr": "apr_local"}
	g.mu.Unlock()

	g.handleMessage(rc, eventEnvelope("run_1", "lease_1", "epoch_1", 7, "revt_2", 2,
		"run.status_changed", map[string]any{"status": "succeeded"}))

	readAck(t, rc)
	rc.mu.Lock()
	_, stillActive := rc.activeRuns["run_1"]
	rc.mu.Unlock()
	g.mu.Lock()
	_, stillMapped := g.runnerApprovals["run_1"]
	g.mu.Unlock()
	if stillActive || stillMapped {
		t.Fatalf("终态后应清理网关内存：activeRuns=%v approvals=%v", stillActive, stillMapped)
	}
}

// approval.requested 应用成功后：ForwardApproval 把服务端审批 ID 翻译回
// runner 侧 ID 下发 approval.resolve。
func TestApprovalTranslationAfterApply(t *testing.T) {
	engine := newFakeEngine()
	g, rc := newEventGateway(t, engine)
	g.store.(*fakeStore).runs.approvals = []*domain.ApprovalRequest{
		{ID: "srv_appr_1", RunID: "run_1", Status: domain.ApprovalPending,
			RequestedBy: map[string]any{runnerApprovalIDKey: "apr_local_1"}},
	}

	g.handleMessage(rc, eventEnvelope("run_1", "lease_1", "epoch_1", 7, "revt_3", 3,
		"approval.requested", map[string]any{"kind": "shell", "approval_id": "apr_local_1"}))
	readAck(t, rc)

	g.ForwardApproval(context.Background(), "run_1", "srv_appr_1", true)
	deadline := time.After(2 * time.Second)
	for {
		select {
		case raw := <-rc.send:
			var env Envelope
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatalf("帧解析失败: %v", err)
			}
			if env.Method != "run.command" {
				continue
			}
			var p commandPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("command payload 解析失败: %v", err)
			}
			// v2 framing：lease/epoch/fencing 必须随帧下发。
			if p.LeaseID != "lease_1" || p.ConnectionEpoch != "epoch_1" || p.FencingToken != 7 {
				t.Fatalf("run.command framing 缺失: %+v", p)
			}
			if p.Command != "approval.resolve" || p.Body["approval_id"] != "apr_local_1" {
				t.Fatalf("审批翻译错误: %+v", p.Body)
			}
			return
		case <-deadline:
			t.Fatal("等待 approval.resolve 超时")
		}
	}
}

func TestDuplicateApprovalEventRebuildsPersistentRunnerMapping(t *testing.T) {
	engine := newFakeEngine()
	g, rc := newEventGateway(t, engine)
	g.store.(*fakeStore).runs.approvals = []*domain.ApprovalRequest{
		{ID: "srv_appr_1", RunID: "run_1", Status: domain.ApprovalPending,
			RequestedBy: map[string]any{runnerApprovalIDKey: "apr_local_1"}},
	}
	event := eventEnvelope("run_1", "lease_1", "epoch_1", 7, "revt_appr", 1,
		"approval.requested", map[string]any{"kind": "shell", "approval_id": "apr_local_1"})
	g.handleMessage(rc, event)
	readAck(t, rc)
	g.mu.Lock()
	delete(g.runnerApprovals, "run_1") // 模拟 Gateway memory restart。
	g.mu.Unlock()
	g.handleMessage(rc, event) // duplicate 仍须从持久行恢复 mapping。
	readAck(t, rc)

	g.ForwardApproval(context.Background(), "run_1", "srv_appr_1", true)
	p := readCommand(t, rc, "approval.resolve")
	if p.Body["approval_id"] != "apr_local_1" || p.Body["approved"] != true {
		t.Fatalf("duplicate 后审批映射未从持久行恢复: %+v", p.Body)
	}
}

func TestSameBootReconnectReplaysOfflineApprovalAfterWelcome(t *testing.T) {
	store := &fakeStore{
		hosts:   newFakeHostRepo(testEnrollmentHost("host_a", "secret_a")),
		runners: newFakeRunnerRepo(),
		runs: &fakeRunRepo{approvals: []*domain.ApprovalRequest{
			{ID: "srv_resolved", RunID: "run_approved", Status: domain.ApprovalApproved,
				RequestedBy: map[string]any{runnerApprovalIDKey: "apr_local_resolved"}},
		}},
	}
	g := New(store, newFakeEngine(), nil)
	rc := &runnerConn{
		gw: g, runnerID: "runner_a", hostID: "host_a", bootID: "boot_a", epoch: "epoch_2",
		send: make(chan []byte, 8), activeRuns: map[string]*activeRun{
			"run_approved": {LeaseID: "lease_approved", FencingToken: 4},
		},
	}
	g.mu.Lock()
	g.conns[rc.runnerID] = rc
	g.mu.Unlock()
	g.welcome(rc)
	g.restoreAndReplayApprovals(rc)

	first := <-rc.send
	var welcome Envelope
	if err := json.Unmarshal(first, &welcome); err != nil {
		t.Fatal(err)
	}
	if welcome.Method != "server.welcome" {
		t.Fatalf("approval replay 前必须 welcome，实际 %s", welcome.Method)
	}
	p := readCommand(t, rc, "approval.resolve")
	if p.RunID != "run_approved" || p.LeaseID != "lease_approved" || p.Body["approval_id"] != "apr_local_resolved" || p.Body["approved"] != true {
		t.Fatalf("离线决议重连后重放错误: %+v", p)
	}
}

// run.reject：调 ApplyRunnerReject（lease 释放/落 failed 由应用层单事务做），
// 成功后清理内存 activeRuns；重复语义由应用层幂等保证。
func TestRunRejectAppliesAndCleansMemory(t *testing.T) {
	engine := newFakeEngine()
	g, rc := newEventGateway(t, engine)

	payload, _ := json.Marshal(rejectPayload{
		RunID: "run_1", LeaseID: "lease_1", RunnerID: "runner_1", ConnectionEpoch: "epoch_1",
		FencingToken: 7, Reason: RejectWorkspace, Detail: "workspace_mount_not_advertised: x",
	})
	g.handleMessage(rc, Envelope{
		V: ProtocolVersion, MessageID: "msg_r", Kind: "response", Method: "run.reject",
		RunnerID: "runner_1", RunID: "run_1", Payload: payload,
	})

	if len(engine.rejects) != 1 {
		t.Fatalf("ApplyRunnerReject 应调用 1 次，实际 %d", len(engine.rejects))
	}
	in := engine.rejects[0]
	if in.ReasonCode != RejectWorkspace || in.ReasonFamily != RejectWorkspace ||
		in.ReasonMessage != "workspace_mount_not_advertised: x" || in.FencingToken != 7 {
		t.Fatalf("reject 命令字段失真: %+v", in)
	}
	rc.mu.Lock()
	_, stillActive := rc.activeRuns["run_1"]
	rc.mu.Unlock()
	if stillActive {
		t.Fatal("reject 成功后应清理内存 activeRuns")
	}
}

// run.accept：transport 校验通过后调 ApplyRunnerAccept。
func TestRunAcceptApplies(t *testing.T) {
	engine := newFakeEngine()
	g, rc := newEventGateway(t, engine)

	payload, _ := json.Marshal(acceptPayload{
		RunID: "run_1", LeaseID: "lease_1", RunnerID: "runner_1", ConnectionEpoch: "epoch_1",
		FencingToken: 7, SnapshotDigest: "abc",
	})
	g.handleMessage(rc, Envelope{
		V: ProtocolVersion, MessageID: "msg_a", Kind: "response", Method: "run.accept",
		RunnerID: "runner_1", RunID: "run_1", Payload: payload,
	})

	if len(engine.accepts) != 1 || engine.accepts[0].SnapshotDigest != "abc" || engine.accepts[0].FencingToken != 7 {
		t.Fatalf("accept 命令字段失真: %+v", engine.accepts)
	}
}

// 缺事件身份的毒帧：无法构造合法 ACK，直接丢弃。
func TestMalformedEventDropped(t *testing.T) {
	engine := newFakeEngine()
	g, rc := newEventGateway(t, engine)

	payload, _ := json.Marshal(map[string]any{
		"run_id": "run_1", "lease_id": "lease_1", "runner_id": "runner_1",
		"connection_epoch": "epoch_1", "fencing_token": 7,
	})
	g.handleMessage(rc, Envelope{
		V: ProtocolVersion, MessageID: "msg_x", Kind: "event", Method: "run.event",
		RunnerID: "runner_1", RunID: "run_1", Payload: payload,
	})
	if len(engine.inputs) != 0 {
		t.Fatal("缺 event_id/producer_seq 的帧不得进入应用层")
	}
	assertNoFrame(t, rc)
}

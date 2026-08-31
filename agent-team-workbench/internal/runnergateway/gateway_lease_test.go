package runnergateway

import (
	"context"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// heartbeat 续租走 RenewLeasesByRunnerIfEpoch：当前连接 + 当前 epoch 才续租。
func TestHeartbeatRenewsLeasesWithEpoch(t *testing.T) {
	repo := newFakeRunnerRepo()
	g := New(&fakeStore{runners: repo}, nil, nil)
	rc := &runnerConn{gw: g, runnerID: "runner_a", hostID: "host_a", epoch: "epoch_1", activeRuns: map[string]*activeRun{}}
	g.mu.Lock()
	g.conns["runner_a"] = rc
	g.mu.Unlock()

	before := time.Now().UTC()
	g.handleMessage(rc, Envelope{V: ProtocolVersion, Method: "heartbeat", RunnerID: "runner_a", ConnectionEpoch: "epoch_1"})
	after := time.Now().UTC()

	if len(repo.renews) != 1 {
		t.Fatalf("heartbeat 应触发 1 次续租，实际 %d", len(repo.renews))
	}
	call := repo.renews[0]
	if call.RunnerID != "runner_a" || call.Epoch != "epoch_1" {
		t.Fatalf("续租参数错误: %+v", call)
	}
	if call.Until.Before(before.Add(leaseTTL)) || call.Until.After(after.Add(leaseTTL)) {
		t.Fatalf("renewUntil = %v，期望 ≈ now+%v", call.Until, leaseTTL)
	}
	// 状态刷新语义保持。
	if len(repo.statuses) != 1 || repo.statuses[0] != [2]string{"runner_a", "connected"} {
		t.Fatalf("SetStatus 调用异常: %v", repo.statuses)
	}
}

// 旧 epoch 心跳不得续租（0 行生效）：envelope epoch 与连接不符时网关直接跳过；
// 即使放行，repo 层 epoch 失配也返回 0 行。
func TestHeartbeatWithStaleEpochDoesNotRenew(t *testing.T) {
	repo := newFakeRunnerRepo()
	g := New(&fakeStore{runners: repo}, nil, nil)
	rc := &runnerConn{gw: g, runnerID: "runner_a", hostID: "host_a", epoch: "epoch_2", activeRuns: map[string]*activeRun{}}
	g.mu.Lock()
	g.conns["runner_a"] = rc
	g.mu.Unlock()

	g.handleMessage(rc, Envelope{V: ProtocolVersion, Method: "heartbeat", RunnerID: "runner_a", ConnectionEpoch: "epoch_1"})
	if len(repo.renews) != 0 {
		t.Fatalf("旧 epoch 心跳不应触发续租调用，实际 %d", len(repo.renews))
	}

	// repo 层防线：epoch 失配返回 0 行。
	n, err := repo.RenewLeasesByRunnerIfEpoch(context.Background(), "runner_a", "epoch_1", "boot_a", time.Now())
	if err != nil || n != 0 {
		t.Fatalf("旧 epoch 续租应 0 行，实际 n=%d err=%v", n, err)
	}
}

// 被顶替的旧连接：heartbeat 不续租、不刷状态。
func TestHeartbeatOnSupersededConnIgnored(t *testing.T) {
	repo := newFakeRunnerRepo()
	g := New(&fakeStore{runners: repo}, nil, nil)
	old := &runnerConn{gw: g, runnerID: "runner_a", hostID: "host_a", epoch: "epoch_1", activeRuns: map[string]*activeRun{}}
	g.mu.Lock()
	g.conns["runner_a"] = old
	g.mu.Unlock()

	old.superseded = true
	g.handleMessage(old, Envelope{V: ProtocolVersion, Method: "heartbeat", RunnerID: "runner_a", ConnectionEpoch: "epoch_1"})
	if len(repo.renews) != 0 || len(repo.statuses) != 0 {
		t.Fatalf("被顶替连接的心跳应被忽略：renews=%d statuses=%d", len(repo.renews), len(repo.statuses))
	}
}

// ack 只刷状态不续租（续租节奏由 heartbeat 承担，与 welcome 广告一致）。
func TestAckDoesNotRenewLeases(t *testing.T) {
	repo := newFakeRunnerRepo()
	g := New(&fakeStore{runners: repo}, nil, nil)
	rc := &runnerConn{gw: g, runnerID: "runner_a", hostID: "host_a", epoch: "epoch_1", activeRuns: map[string]*activeRun{}}

	g.handleMessage(rc, Envelope{V: ProtocolVersion, Method: "ack", RunnerID: "runner_a", RunID: "run_1"})

	if len(repo.renews) != 0 {
		t.Fatalf("ack 不应触发续租，实际 %d 次", len(repo.renews))
	}
	if len(repo.statuses) != 1 || repo.statuses[0][1] != "connected" {
		t.Fatalf("ack 应仍刷新状态: %v", repo.statuses)
	}
}

// 连接断开：runner 与 host 状态投影 offline（host_local 永不经网关，其
// offline 投影只作用于经网关的远程 host）。
func TestDisconnectProjectsOffline(t *testing.T) {
	hosts := newFakeHostRepo(testEnrollmentHost("host_a", "s3cret"))
	repo := newFakeRunnerRepo()
	engine := newFakeEngine()
	g := New(&fakeStore{hosts: hosts, runners: repo}, engine, nil)
	rc := &runnerConn{
		gw: g, runnerID: "runner_a", hostID: "host_a", epoch: "epoch_1",
		adapters:   []adapterInfo{{ID: "mock", Version: "1", SchemaDigest: "d"}},
		activeRuns: map[string]*activeRun{"run_1": {LeaseID: "lease_1", FencingToken: 7}},
		send:       make(chan []byte, 8),
	}
	g.mu.Lock()
	g.conns[rc.runnerID] = rc
	g.mu.Unlock()

	g.handleDisconnect(rc)

	if len(repo.statuses) == 0 || repo.statuses[0] != [2]string{"runner_a", "offline"} {
		t.Fatalf("runner 应投影 offline: %v", repo.statuses)
	}
	found := false
	for _, s := range hosts.statuses {
		if s == [2]string{"host_a", string(domain.HostStatusOffline)} {
			found = true
		}
	}
	if !found {
		t.Fatalf("host 应投影 offline: %v", hosts.statuses)
	}
	if len(engine.statuses) != 1 || engine.statuses[0] != domain.RunReconnecting {
		t.Fatalf("活动 run 应进入 reconnecting: %v", engine.statuses)
	}
}

func TestDisconnectAndExpiryCloseEveryNonTerminalRunState(t *testing.T) {
	states := []domain.RunStatus{
		domain.RunQueued, domain.RunStarting, domain.RunRunning, domain.RunWaitingApproval,
		domain.RunInterrupting, domain.RunCancelling, domain.RunReconnecting, domain.RunSucceeding,
	}
	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			hosts := newFakeHostRepo(testEnrollmentHost("host_a", "s3cret"))
			repo := newFakeRunnerRepo()
			engine := newFakeEngine()
			engine.runs["run_state"] = state
			g := New(&fakeStore{hosts: hosts, runners: repo}, engine, nil)
			rc := &runnerConn{
				gw: g, runnerID: "runner_a", hostID: "host_a", epoch: "epoch_1",
				activeRuns: map[string]*activeRun{"run_state": {LeaseID: "lease_state", FencingToken: 1}},
				send:       make(chan []byte, 2),
			}
			g.mu.Lock()
			g.conns[rc.runnerID] = rc
			g.mu.Unlock()
			g.handleDisconnect(rc)
			wantDisconnectWrites := 1
			if state == domain.RunReconnecting {
				wantDisconnectWrites = 0
			}
			if len(engine.statuses) != wantDisconnectWrites || (wantDisconnectWrites == 1 && engine.statuses[0] != domain.RunReconnecting) {
				t.Fatalf("%s 断连 reconnecting 收口错误: %v", state, engine.statuses)
			}
			g.markExpiredLeaseTerminal(context.Background(), "run_state")
			if len(engine.statuses) != wantDisconnectWrites+1 || engine.statuses[len(engine.statuses)-1] != domain.RunLost {
				t.Fatalf("%s lease 过期必须收敛 lost: %v", state, engine.statuses)
			}
		})
	}
}

package runnergateway

import (
	"context"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
)

// fakeRunnerRepo 只实现 heartbeat 路径触及的方法，嵌入接口其余方法留空
// （不会被调用；ExpireLeases 由 leaseSweeper 周期触发，显式 no-op 防 panic）。
type fakeRunnerRepo struct {
	application.RunnerRepo

	setStatusArgs  []string // runnerID, status 交替
	renewCalls     int
	renewRunnerIDs []string
	renewUntils    []time.Time
}

func (f *fakeRunnerRepo) SetStatus(ctx context.Context, runnerID, status string, at time.Time) error {
	f.setStatusArgs = append(f.setStatusArgs, runnerID, status)
	return nil
}

func (f *fakeRunnerRepo) ExpireLeases(ctx context.Context, now time.Time) ([]string, error) {
	return nil, nil
}

func (f *fakeRunnerRepo) RenewLeasesByRunner(ctx context.Context, runnerID string, renewUntil time.Time) (int, error) {
	f.renewCalls++
	f.renewRunnerIDs = append(f.renewRunnerIDs, runnerID)
	f.renewUntils = append(f.renewUntils, renewUntil)
	return 1, nil
}

type fakeLeaseStore struct {
	application.Store
	runners *fakeRunnerRepo
}

func (f *fakeLeaseStore) Runners() application.RunnerRepo { return f.runners }

// heartbeat 必须触发续租：runnerID 正确、renewUntil ≈ now+leaseTTL（60s）。
func TestHeartbeatRenewsLeases(t *testing.T) {
	repo := &fakeRunnerRepo{}
	g := New(&fakeLeaseStore{runners: repo}, nil, nil)
	rc := &runnerConn{gw: g, runnerID: "runner_a", workspaceID: "ws_1", activeRuns: map[string]string{}}

	before := time.Now().UTC()
	g.handleMessage(rc, Envelope{V: 1, Method: "heartbeat", RunnerID: "runner_a"})
	after := time.Now().UTC()

	if repo.renewCalls != 1 {
		t.Fatalf("heartbeat 应触发 1 次续租，实际 %d", repo.renewCalls)
	}
	if got := repo.renewRunnerIDs[0]; got != "runner_a" {
		t.Fatalf("续租 runnerID = %q，期望 runner_a", got)
	}
	want := before.Add(leaseTTL)
	got := repo.renewUntils[0]
	if got.Before(want) || got.After(after.Add(leaseTTL)) {
		t.Fatalf("renewUntil = %v，期望落在 [%v, %v]", got, want, after.Add(leaseTTL))
	}
	// 状态刷新语义保持。
	if len(repo.setStatusArgs) != 2 || repo.setStatusArgs[0] != "runner_a" || repo.setStatusArgs[1] != "connected" {
		t.Fatalf("SetStatus 调用异常: %v", repo.setStatusArgs)
	}
}

// ack 只刷状态不续租（续租节奏由 15s heartbeat 承担，与 welcome 广告一致）。
func TestAckDoesNotRenewLeases(t *testing.T) {
	repo := &fakeRunnerRepo{}
	g := New(&fakeLeaseStore{runners: repo}, nil, nil)
	rc := &runnerConn{gw: g, runnerID: "runner_a", workspaceID: "ws_1", activeRuns: map[string]string{}}

	g.handleMessage(rc, Envelope{V: 1, Method: "ack", RunnerID: "runner_a", RunID: "run_1"})

	if repo.renewCalls != 0 {
		t.Fatalf("ack 不应触发续租，实际 %d 次", repo.renewCalls)
	}
	if len(repo.setStatusArgs) != 2 || repo.setStatusArgs[1] != "connected" {
		t.Fatalf("ack 应仍刷新状态: %v", repo.setStatusArgs)
	}
}

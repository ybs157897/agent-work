package runnergateway

// v2 行为测试共享 fake：全部 in-memory，不 import sqlstore（GW 与持久化
// 零编译耦合，RFC 决策）。注册/投影发生在服务 goroutine，故记录型 fake
// 一律带锁并提供 Snapshot 断言口。

import (
	"context"
	"sync"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// fakeHostRepo 记录 host 状态投影与 mount 广告投影。
type fakeHostRepo struct {
	application.ExecutionHostRepo

	mu       sync.Mutex
	hosts    map[string]*domain.ExecutionHost
	mounts   map[string]*domain.HostMount // key: host|alias
	statuses [][2]string                  // hostID, status
}

func newFakeHostRepo(hosts ...*domain.ExecutionHost) *fakeHostRepo {
	m := make(map[string]*domain.ExecutionHost, len(hosts))
	for _, h := range hosts {
		m[h.ID] = h
	}
	return &fakeHostRepo{hosts: m, mounts: make(map[string]*domain.HostMount)}
}

func (f *fakeHostRepo) Get(ctx context.Context, id string) (*domain.ExecutionHost, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.hosts[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return h, nil
}

func (f *fakeHostRepo) SetStatus(ctx context.Context, id string, status domain.HostStatus, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, [2]string{id, string(status)})
	return nil
}

func (f *fakeHostRepo) UpsertMount(ctx context.Context, m *domain.HostMount) error {
	cp := *m
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mounts[m.ExecutionHostID+"|"+m.Alias] = &cp
	return nil
}

// Snapshot 取投影快照（测试断言用）。
func (f *fakeHostRepo) Snapshot() (hostCount int, statuses [][2]string, mounts map[string]*domain.HostMount) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]*domain.HostMount, len(f.mounts))
	for k, v := range f.mounts {
		out[k] = v
	}
	return len(f.hosts), f.statuses, out
}

// fakeRunnerRepo 记录 runner upsert / lease / 续租调用。
type fakeRunnerRepo struct {
	application.RunnerRepo

	mu       sync.Mutex
	upserts  []*application.Runner
	statuses [][2]string // runnerID, status
	leases   []*application.RunLease
	renews   []renewCall
}

type renewCall struct {
	RunnerID string
	Epoch    string
	Until    time.Time
}

func newFakeRunnerRepo() *fakeRunnerRepo { return &fakeRunnerRepo{} }

func (f *fakeRunnerRepo) Upsert(ctx context.Context, r *application.Runner) error {
	cp := *r
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts = append(f.upserts, &cp)
	return nil
}

func (f *fakeRunnerRepo) Get(ctx context.Context, runnerID string) (*application.Runner, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.upserts) - 1; i >= 0; i-- {
		if f.upserts[i].ID == runnerID {
			cp := *f.upserts[i]
			return &cp, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeRunnerRepo) SetStatus(ctx context.Context, runnerID, status string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, [2]string{runnerID, status})
	return nil
}

func (f *fakeRunnerRepo) CreateLease(ctx context.Context, l *application.RunLease) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if l.FencingToken == 0 {
		l.FencingToken = int64(len(f.leases) + 1)
	}
	cp := *l
	f.leases = append(f.leases, &cp)
	return nil
}

func (f *fakeRunnerRepo) ListActiveLeasesByRunner(ctx context.Context, runnerID string) ([]*application.RunLease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*application.RunLease, 0, len(f.leases))
	for _, l := range f.leases {
		if l.RunnerID != runnerID || l.Released {
			continue
		}
		cp := *l
		out = append(out, &cp)
	}
	return out, nil
}

func (f *fakeRunnerRepo) ReleaseLease(ctx context.Context, leaseID string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, lease := range f.leases {
		if lease.LeaseID == leaseID {
			lease.Released = true
			return nil
		}
	}
	return domain.ErrNotFound
}

func (f *fakeRunnerRepo) ReleaseActiveLeasesByRunner(ctx context.Context, runnerID string, at time.Time) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	runIDs := make([]string, 0)
	seen := make(map[string]bool)
	for _, lease := range f.leases {
		if lease.RunnerID != runnerID || lease.Released {
			continue
		}
		lease.Released = true
		if !seen[lease.RunID] {
			seen[lease.RunID] = true
			runIDs = append(runIDs, lease.RunID)
		}
	}
	return runIDs, nil
}

// RenewLeasesByRunnerIfEpoch 只在 runner 当前 epoch 与 boot 匹配时续租（0 行 = 无效）。
func (f *fakeRunnerRepo) RenewLeasesByRunnerIfEpoch(ctx context.Context, runnerID, epoch, bootID string, renewUntil time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renews = append(f.renews, renewCall{RunnerID: runnerID, Epoch: epoch, Until: renewUntil})
	for _, r := range f.upserts {
		if r.ID == runnerID && r.ConnectionEpoch == epoch && r.BootID == bootID {
			return 1, nil
		}
	}
	return 0, nil
}

func (f *fakeRunnerRepo) ExpireLeases(ctx context.Context, now time.Time) ([]string, error) {
	return nil, nil
}

// Snapshot 取记录快照（测试断言用）。
func (f *fakeRunnerRepo) Snapshot() (upserts []*application.Runner, statuses [][2]string, leases []*application.RunLease, renews []renewCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.upserts, f.statuses, f.leases, f.renews
}

// fakeRunRepo 供审批翻译查最新 pending 审批。
type fakeRunRepo struct {
	application.RunRepo

	approvals []*domain.ApprovalRequest
}

func (f *fakeRunRepo) ListApprovals(ctx context.Context, runID string) ([]*domain.ApprovalRequest, error) {
	return f.approvals, nil
}

// fakeStore 按 gateway 触面提供三个仓储，其余方法不会被调用。
type fakeStore struct {
	application.Store

	hosts   *fakeHostRepo
	runners *fakeRunnerRepo
	runs    *fakeRunRepo
}

func (f *fakeStore) ExecutionHosts() application.ExecutionHostRepo { return f.hosts }
func (f *fakeStore) Runners() application.RunnerRepo               { return f.runners }
func (f *fakeStore) Runs() application.RunRepo                     { return f.runs }
func (f *fakeStore) InTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

// testEnrollmentHost 构造 enrollment_ref 与 secret 匹配的 Host。
func testEnrollmentHost(id, secret string) *domain.ExecutionHost {
	return &domain.ExecutionHost{
		ID: id, Name: id, Kind: domain.HostKindRemote,
		Status: domain.HostStatusOffline, EnrollmentRef: enrollmentDigest(secret),
	}
}

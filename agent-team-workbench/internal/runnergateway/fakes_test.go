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
	// leaseErr 非空时 CreateLease 失败（dispatch 失败路径 / journal 断言用）。
	leaseErr error
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
	if f.leaseErr != nil {
		return f.leaseErr
	}
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

// ActiveLease 返回该 run 的最高 fencing lease 行（含已释放，与 sqlstore 语义
// 一致——过期判死发生在释放之后，释放行正是恢复证据的来源）。
func (f *fakeRunnerRepo) ActiveLease(ctx context.Context, runID string) (*application.RunLease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var best *application.RunLease
	for _, l := range f.leases {
		if l.RunID != runID {
			continue
		}
		if best == nil || l.FencingToken > best.FencingToken {
			best = l
		}
	}
	if best == nil {
		return nil, domain.ErrNotFound
	}
	cp := *best
	return &cp, nil
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

// fakeEventRepo 是 run_events 的内存投影：journal 读路径（合成闭合相位需要
// ListRunEventsIncludeInternal）与恢复事件断言共用。events 按 run 分桶追加，
// 顺序即 run_seq 序。其余 EventRepo 方法不被网关触达（内嵌接口零值兜底）。
type fakeEventRepo struct {
	application.EventRepo

	mu    sync.Mutex
	byRun map[string][]application.RunEvent
}

func newFakeEventRepo() *fakeEventRepo {
	return &fakeEventRepo{byRun: make(map[string][]application.RunEvent)}
}

// seed 预置一条事件（测试构造 journal 历史，如 phase_entered）。
func (r *fakeEventRepo) seed(runID, eventType string, payload map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byRun == nil {
		r.byRun = make(map[string][]application.RunEvent)
	}
	r.byRun[runID] = append(r.byRun[runID], application.RunEvent{
		RunSeq: int64(len(r.byRun[runID]) + 1), EventType: eventType,
		Payload: payload, OccurredAt: time.Now().UTC(),
	})
}

// append 是 engine 发出事件的镜像入口（recordSink 接线）。
func (r *fakeEventRepo) append(runID, eventType string, payload map[string]any) {
	r.seed(runID, eventType, payload)
}

func (r *fakeEventRepo) ListRunEventsIncludeInternal(ctx context.Context, runID string) ([]application.RunEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]application.RunEvent, len(r.byRun[runID]))
	copy(out, r.byRun[runID])
	return out, nil
}

func (r *fakeEventRepo) ListRunEvents(ctx context.Context, runID string) ([]application.RunEvent, error) {
	return r.ListRunEventsIncludeInternal(ctx, runID)
}

// fakeStore 按 gateway 触面提供仓储，其余方法不会被调用。events 为 nil 时
// Events() 返回空仓（合成闭合查不到相位、不发事件——与未埋点 run 一致）。
type fakeStore struct {
	application.Store

	hosts   *fakeHostRepo
	runners *fakeRunnerRepo
	runs    *fakeRunRepo
	events  *fakeEventRepo
}

func (f *fakeStore) ExecutionHosts() application.ExecutionHostRepo { return f.hosts }
func (f *fakeStore) Runners() application.RunnerRepo               { return f.runners }
func (f *fakeStore) Runs() application.RunRepo                     { return f.runs }
func (f *fakeStore) Events() application.EventRepo {
	if f.events == nil {
		return newFakeEventRepo()
	}
	return f.events
}
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

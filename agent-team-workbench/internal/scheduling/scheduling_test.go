package scheduling

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// ---- fake Store ----

type claimCall struct {
	agentID     string
	minInterval time.Duration
}

type releaseCall struct {
	agentID   string
	claimedAt time.Time
}

type fakeStore struct {
	wakeups    map[string]*domain.WakeupRequest
	agents     map[string]*domain.AgentProfile
	claimFn    func(agentID string, minInterval time.Duration, now time.Time) (bool, error)
	activeFn   func(agentID, taskKey string) (string, bool, error)
	claimCalls []claimCall
	// timer 生产面：agent 名下非终态任务（默认空）。
	tasks map[string][]TaskRef
	// 消费补偿与降级审计记录。
	requeues   []string
	releases   []releaseCall
	contextSet map[string]map[string]any
}

func newFakeStore(agent *domain.AgentProfile) *fakeStore {
	f := &fakeStore{
		wakeups: map[string]*domain.WakeupRequest{},
		agents:  map[string]*domain.AgentProfile{agent.ID: agent},
		activeFn: func(string, string) (string, bool, error) {
			return "", false, nil
		},
		contextSet: map[string]map[string]any{},
	}
	// 默认 claim 语义对齐 sqlstore：last_heartbeat_at 为空或距 now ≥ 间隔可命中，
	// 命中后写入 now（ReleaseHeartbeatClaim 的回滚判定依赖该值）。
	f.claimFn = func(agentID string, minInterval time.Duration, now time.Time) (bool, error) {
		a := f.agents[agentID]
		if a == nil {
			return false, nil
		}
		if a.LastHeartbeatAt != nil && now.Sub(*a.LastHeartbeatAt) < minInterval {
			return false, nil
		}
		t := now
		a.LastHeartbeatAt = &t
		return true, nil
	}
	return f
}

func (f *fakeStore) EnqueueWakeup(ctx context.Context, w *domain.WakeupRequest) error {
	cp := *w
	f.wakeups[w.ID] = &cp
	return nil
}

func (f *fakeStore) DueTimers(ctx context.Context, now time.Time, limit int) ([]domain.WakeupRequest, error) {
	var out []domain.WakeupRequest
	for _, w := range f.wakeups {
		if w.Status == domain.WakeupStatusQueued && !w.WakeAt.After(now) {
			out = append(out, *w)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WakeAt.Before(out[j].WakeAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// MarkWakeupStatus 模拟 sqlstore 的 CAS：仅 queued 出发；否则 ErrWakeupNotQueued。
func (f *fakeStore) MarkWakeupStatus(ctx context.Context, id string, status domain.WakeupStatus) error {
	if w, ok := f.wakeups[id]; ok {
		if w.Status != domain.WakeupStatusQueued {
			return domain.ErrWakeupNotQueued
		}
		w.Status = status
	}
	return nil
}

// RequeueWakeup 模拟 sqlstore 的消费补偿 CAS：consumed → queued。
func (f *fakeStore) RequeueWakeup(ctx context.Context, id string) error {
	if w, ok := f.wakeups[id]; ok {
		if w.Status != domain.WakeupStatusConsumed {
			return domain.ErrWakeupNotQueued
		}
		w.Status = domain.WakeupStatusQueued
		f.requeues = append(f.requeues, id)
	}
	return nil
}

func (f *fakeStore) SetWakeupContext(ctx context.Context, id string, wakeContext map[string]any) error {
	cp := map[string]any{}
	for k, v := range wakeContext {
		cp[k] = v
	}
	f.contextSet[id] = cp
	if w, ok := f.wakeups[id]; ok {
		w.Context = cp
	}
	return nil
}

func (f *fakeStore) RecentByAgentTask(ctx context.Context, agentID, taskKey string, since time.Time) ([]domain.WakeupRequest, error) {
	return nil, nil
}

func (f *fakeStore) ClaimHeartbeat(ctx context.Context, agentID string, minInterval time.Duration, now time.Time) (bool, error) {
	f.claimCalls = append(f.claimCalls, claimCall{agentID, minInterval})
	return f.claimFn(agentID, minInterval, now)
}

// ReleaseHeartbeatClaim 仅当 last_heartbeat_at 仍等于 claimedAt 时复位（对齐 sqlstore）。
func (f *fakeStore) ReleaseHeartbeatClaim(ctx context.Context, agentID string, claimedAt time.Time) error {
	f.releases = append(f.releases, releaseCall{agentID, claimedAt})
	if a, ok := f.agents[agentID]; ok && a.LastHeartbeatAt != nil && a.LastHeartbeatAt.Equal(claimedAt) {
		a.LastHeartbeatAt = nil
	}
	return nil
}

func (f *fakeStore) ActiveRunKeyForAgentTask(ctx context.Context, agentID, taskKey string) (string, bool, error) {
	return f.activeFn(agentID, taskKey)
}

func (f *fakeStore) GetAgentProfile(ctx context.Context, id string) (*domain.AgentProfile, error) {
	if a, ok := f.agents[id]; ok {
		return a, nil
	}
	return nil, domain.ErrNotFound
}

// ListHeartbeatAgents 从 agents 投影心跳候选（availability=enabled 且 heartbeat_enabled）。
func (f *fakeStore) ListHeartbeatAgents(ctx context.Context) ([]domain.AgentProfile, error) {
	var out []domain.AgentProfile
	for _, a := range f.agents {
		if a.HeartbeatEnabled && a.Availability == domain.AgentEnabled {
			out = append(out, *a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeStore) AssignedTasks(ctx context.Context, agentID string) ([]TaskRef, error) {
	return f.tasks[agentID], nil
}

func (f *fakeStore) HasQueuedTimer(ctx context.Context, agentID, taskKey string) (bool, error) {
	for _, w := range f.wakeups {
		if w.AgentProfileID == agentID && w.TaskKey == taskKey &&
			w.Source == domain.WakeupSourceTimer && w.Status == domain.WakeupStatusQueued {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) status(id string) domain.WakeupStatus {
	return f.wakeups[id].Status
}

// ---- fake RunStarter ----

type createdCall struct {
	workspaceID, agentProfileID, taskKey, instruction string
	wakeContext                                       map[string]any
}

type fakeRunStarter struct {
	err     error
	created []createdCall
}

func (f *fakeRunStarter) CreateRunForWakeup(ctx context.Context, workspaceID, agentProfileID, taskKey, instruction string, wakeContext map[string]any) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.created = append(f.created, createdCall{workspaceID, agentProfileID, taskKey, instruction, wakeContext})
	return "run_fake", nil
}

// ---- fixtures ----

func testAgent(heartbeatEnabled bool, intervalSec int) *domain.AgentProfile {
	return &domain.AgentProfile{
		ID: "agent_t", WorkspaceID: "ws_t", Slug: "tester", Name: "测试员", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1,
		HeartbeatEnabled: heartbeatEnabled, HeartbeatIntervalSec: intervalSec,
		WakeOnAssignment: true, WakeOnDemand: true,
	}
}

func newTestScheduler(store Store, starter RunStarter) *Scheduler {
	return &Scheduler{Store: store, RunStarter: starter}
}

func mustEnqueueTimer(t *testing.T, ctx context.Context, store Store, wakeAt time.Time) *domain.WakeupRequest {
	t.Helper()
	w, err := EnqueueWakeup(ctx, store, domain.WakeupSourceTimer, "ws_t", "agent_t", "wi_1",
		map[string]any{"work_item_title": "集成任务"}, wakeAt)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

var testNow = time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

// TestConsumeOneEventSourcesBypassHeartbeat：assignment/on_demand 是事件驱动唤醒，
// heartbeat 禁用或间隔未到都不应拦截。
func TestConsumeOneEventSourcesBypassHeartbeat(t *testing.T) {
	ctx := context.Background()
	for _, source := range []domain.WakeupSource{domain.WakeupSourceAssignment, domain.WakeupSourceOnDemand} {
		t.Run(string(source), func(t *testing.T) {
			// heartbeat 禁用 + claim 永不命中 → 事件源仍应穿透建 run。
			store := newFakeStore(testAgent(false, 0))
			store.claimFn = func(string, time.Duration, time.Time) (bool, error) { return false, nil }
			starter := &fakeRunStarter{}
			w, err := EnqueueWakeup(ctx, store, source, "ws_t", "agent_t", "wi_1", nil, testNow.Add(-time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := newTestScheduler(store, starter).ConsumeOne(ctx, *w, testNow)
			if err != nil || outcome != OutcomeConsumed {
				t.Fatalf("outcome=%q err=%v（事件源不应被心跳门控）", outcome, err)
			}
			if len(starter.created) != 1 || len(store.claimCalls) != 0 {
				t.Fatalf("应建 run 且不 claim: created=%d claims=%d", len(starter.created), len(store.claimCalls))
			}
		})
	}
}

// TestConsumeOneEventSourcePolicyOffCoalesces：对应开关关闭 → 兜底合并（入队侧校验之外的二道防线）。
func TestConsumeOneEventSourcePolicyOffCoalesces(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		source domain.WakeupSource
		agent  *domain.AgentProfile
	}{
		{domain.WakeupSourceAssignment, func() *domain.AgentProfile {
			a := testAgent(true, 0)
			a.WakeOnAssignment = false
			return a
		}()},
		{domain.WakeupSourceOnDemand, func() *domain.AgentProfile {
			a := testAgent(true, 0)
			a.WakeOnDemand = false
			return a
		}()},
	}
	for _, tc := range cases {
		t.Run(string(tc.source), func(t *testing.T) {
			store := newFakeStore(tc.agent)
			starter := &fakeRunStarter{}
			w, err := EnqueueWakeup(ctx, store, tc.source, "ws_t", "agent_t", "wi_1", nil, testNow.Add(-time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := newTestScheduler(store, starter).ConsumeOne(ctx, *w, testNow)
			if err != nil || outcome != OutcomeCoalesced {
				t.Fatalf("outcome=%q err=%v", outcome, err)
			}
			if len(starter.created) != 0 {
				t.Fatal("开关关闭不应建 run")
			}
		})
	}
}

// ---- EnqueueWakeup ----

func TestEnqueueWakeupValidatesInput(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(testAgent(true, 0))

	if _, err := EnqueueWakeup(ctx, store, "bogus", "ws_t", "agent_t", "wi_1", nil, testNow); err == nil {
		t.Fatal("非法 source 应报错")
	}
	if _, err := EnqueueWakeup(ctx, store, domain.WakeupSourceTimer, "ws_t", "agent_t", "", nil, testNow); err == nil {
		t.Fatal("空 taskKey 应报错")
	}
	w, err := EnqueueWakeup(ctx, store, domain.WakeupSourceOnDemand, "ws_t", "agent_t", "wi_1", nil, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(w.ID, domain.PrefixWakeup) {
		t.Fatalf("ID 前缀 = %q, 期望 %q 前缀", w.ID, domain.PrefixWakeup)
	}
	if w.Status != domain.WakeupStatusQueued {
		t.Fatalf("初始状态 = %q", w.Status)
	}
	if got := store.wakeups[w.ID]; got == nil || got.TaskKey != "wi_1" {
		t.Fatalf("未落库: %#v", got)
	}
	// wakeAt 零值 → 立即到期。
	w2, err := EnqueueWakeup(ctx, store, domain.WakeupSourceTimer, "ws_t", "agent_t", "wi_2", nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if w2.WakeAt.IsZero() {
		t.Fatal("wakeAt 零值应替换为当前时间")
	}
}

// ---- Tick / ConsumeOne 矩阵 ----

func TestTickSkipsFutureWakeup(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(testAgent(true, 0))
	starter := &fakeRunStarter{}
	w := mustEnqueueTimer(t, ctx, store, testNow.Add(10*time.Minute))

	newTestScheduler(store, starter).Tick(ctx, testNow)

	if got := store.status(w.ID); got != domain.WakeupStatusQueued {
		t.Fatalf("未到期不应被消费, status=%q", got)
	}
	if len(store.claimCalls) != 0 || len(starter.created) != 0 {
		t.Fatalf("未到期不应 claim/建 run: claims=%d created=%d", len(store.claimCalls), len(starter.created))
	}
}

func TestTickConsumesDueWakeup(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(testAgent(true, 0))
	starter := &fakeRunStarter{}
	w := mustEnqueueTimer(t, ctx, store, testNow.Add(-time.Minute))

	newTestScheduler(store, starter).Tick(ctx, testNow)

	if got := store.status(w.ID); got != domain.WakeupStatusConsumed {
		t.Fatalf("到期应 consumed, status=%q", got)
	}
	if len(starter.created) != 1 {
		t.Fatalf("应创建 1 个 run, 实际 %d", len(starter.created))
	}
	c := starter.created[0]
	if c.workspaceID != "ws_t" || c.agentProfileID != "agent_t" || c.taskKey != "wi_1" {
		t.Fatalf("run 参数错误: %#v", c)
	}
	for _, sub := range []string{"测试员", "developer", "集成任务"} {
		if !strings.Contains(c.instruction, sub) {
			t.Fatalf("instruction 未渲染模板变量 %q: %q", sub, c.instruction)
		}
	}
}

func TestConsumeOneHeartbeatDisabledCoalesces(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(testAgent(false, 0))
	starter := &fakeRunStarter{}
	w := mustEnqueueTimer(t, ctx, store, testNow.Add(-time.Minute))

	outcome, err := newTestScheduler(store, starter).ConsumeOne(ctx, *w, testNow)
	if err != nil || outcome != OutcomeCoalesced {
		t.Fatalf("outcome=%q err=%v", outcome, err)
	}
	if got := store.status(w.ID); got != domain.WakeupStatusCoalesced {
		t.Fatalf("status=%q", got)
	}
	// heartbeat 禁用应在 claim 之前短路。
	if len(store.claimCalls) != 0 || len(starter.created) != 0 {
		t.Fatalf("不应 claim/建 run: claims=%d created=%d", len(store.claimCalls), len(starter.created))
	}
}

func TestConsumeOneHeartbeatMissCoalesces(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(testAgent(true, 0))
	store.claimFn = func(string, time.Duration, time.Time) (bool, error) { return false, nil }
	starter := &fakeRunStarter{}
	w := mustEnqueueTimer(t, ctx, store, testNow.Add(-time.Minute))

	outcome, err := newTestScheduler(store, starter).ConsumeOne(ctx, *w, testNow)
	if err != nil || outcome != OutcomeCoalesced {
		t.Fatalf("outcome=%q err=%v", outcome, err)
	}
	if got := store.status(w.ID); got != domain.WakeupStatusCoalesced {
		t.Fatalf("status=%q", got)
	}
	if len(starter.created) != 0 {
		t.Fatalf("心跳未命中不应建 run")
	}
}

func TestConsumeOneHeartbeatIntervalSelection(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name             string
		intervalSec      int
		defaultSec       int
		wantIntervalSecs int
	}{
		{"profile 覆盖缺省", 600, 0, 600},
		{"profile 为 0 用全局缺省", 0, 0, domain.DefaultHeartbeatIntervalSec},
		{"scheduler 覆盖全局缺省", 0, 900, 900},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore(testAgent(true, tc.intervalSec))
			starter := &fakeRunStarter{}
			w := mustEnqueueTimer(t, ctx, store, testNow.Add(-time.Minute))
			s := newTestScheduler(store, starter)
			s.HeartbeatDefault = tc.defaultSec
			if _, err := s.ConsumeOne(ctx, *w, testNow); err != nil {
				t.Fatal(err)
			}
			if len(store.claimCalls) != 1 {
				t.Fatalf("claim 次数 = %d", len(store.claimCalls))
			}
			if got := store.claimCalls[0].minInterval; got != time.Duration(tc.wantIntervalSecs)*time.Second {
				t.Fatalf("心跳间隔 = %v, 期望 %ds", got, tc.wantIntervalSecs)
			}
		})
	}
}

func TestConsumeOneActiveRunCoalesces(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(testAgent(true, 0))
	store.activeFn = func(string, string) (string, bool, error) { return "run_live", true, nil }
	starter := &fakeRunStarter{}
	w := mustEnqueueTimer(t, ctx, store, testNow.Add(-time.Minute))

	outcome, err := newTestScheduler(store, starter).ConsumeOne(ctx, *w, testNow)
	if err != nil || outcome != OutcomeCoalesced {
		t.Fatalf("outcome=%q err=%v", outcome, err)
	}
	if got := store.status(w.ID); got != domain.WakeupStatusCoalesced {
		t.Fatalf("status=%q", got)
	}
	if len(starter.created) != 0 {
		t.Fatalf("活跃 run 期间不应建新 run")
	}
}

func TestConsumeOneZombieRunPierces(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(testAgent(true, 0))
	store.activeFn = func(string, string) (string, bool, error) { return "run_zombie", false, nil }
	starter := &fakeRunStarter{}
	w := mustEnqueueTimer(t, ctx, store, testNow.Add(-time.Minute))

	outcome, err := newTestScheduler(store, starter).ConsumeOne(ctx, *w, testNow)
	if err != nil || outcome != OutcomeConsumed {
		t.Fatalf("outcome=%q err=%v", outcome, err)
	}
	if got := store.status(w.ID); got != domain.WakeupStatusConsumed {
		t.Fatalf("status=%q", got)
	}
	if len(starter.created) != 1 {
		t.Fatalf("zombie 应穿透建新 run, 实际 %d", len(starter.created))
	}
}

func TestConsumeOneCreateFailureKeepsQueued(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(testAgent(true, 0))
	starter := &fakeRunStarter{err: errors.New("boom")}
	w := mustEnqueueTimer(t, ctx, store, testNow.Add(-5*time.Minute))

	outcome, err := newTestScheduler(store, starter).ConsumeOne(ctx, *w, testNow)
	if err != nil || outcome != OutcomeQueued {
		t.Fatalf("outcome=%q err=%v", outcome, err)
	}
	if got := store.status(w.ID); got != domain.WakeupStatusQueued {
		t.Fatalf("失败应保持 queued, status=%q", got)
	}
}

func TestConsumeOneStaleWakeupCoalesced(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(testAgent(true, 0))
	starter := &fakeRunStarter{err: errors.New("boom")}
	// 超龄：wake_at 早于 now-1h。
	w := mustEnqueueTimer(t, ctx, store, testNow.Add(-2*time.Hour))

	outcome, err := newTestScheduler(store, starter).ConsumeOne(ctx, *w, testNow)
	if err != nil || outcome != OutcomeCoalesced {
		t.Fatalf("outcome=%q err=%v", outcome, err)
	}
	if got := store.status(w.ID); got != domain.WakeupStatusCoalesced {
		t.Fatalf("超龄应 coalesced, status=%q", got)
	}
}

func TestConsumeOneMissingAgentRetriesThenExpires(t *testing.T) {
	ctx := context.Background()
	agent := testAgent(true, 0)
	store := newFakeStore(agent)
	starter := &fakeRunStarter{}
	delete(store.agents, agent.ID) // agent 已不存在
	w := mustEnqueueTimer(t, ctx, store, testNow.Add(-5*time.Minute))

	outcome, err := newTestScheduler(store, starter).ConsumeOne(ctx, *w, testNow)
	if err != nil || outcome != OutcomeQueued {
		t.Fatalf("outcome=%q err=%v", outcome, err)
	}
	stale := mustEnqueueTimer(t, ctx, store, testNow.Add(-2*time.Hour))
	outcome, err = newTestScheduler(store, starter).ConsumeOne(ctx, *stale, testNow)
	if err != nil || outcome != OutcomeCoalesced {
		t.Fatalf("超龄 outcome=%q err=%v", outcome, err)
	}
}

// ---- timer 生产（F1：心跳自主唤醒的唤醒生产者）----

// TestProduceTimersEnqueuesDueHeartbeat：到期心跳 agent（last_heartbeat_at 为空）
// 名下每个非终态任务各入队一条 source=timer 唤醒，context 携带 work_item_title。
func TestProduceTimersEnqueuesDueHeartbeat(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(testAgent(true, 0))
	store.tasks = map[string][]TaskRef{
		"agent_t": {{Key: "wi_a", Title: "任务A"}, {Key: "wi_b", Title: "任务B"}},
	}

	s := newTestScheduler(store, &fakeRunStarter{})
	s.ProduceTimers(ctx, testNow)

	var got []string
	for _, w := range store.wakeups {
		if w.Source != domain.WakeupSourceTimer {
			t.Fatalf("source = %q, 期望 timer", w.Source)
		}
		if w.Status != domain.WakeupStatusQueued || w.AgentProfileID != "agent_t" {
			t.Fatalf("wakeup 字段错误: %#v", w)
		}
		if w.Context["work_item_title"] == "" {
			t.Fatalf("context 应携带 work_item_title: %#v", w.Context)
		}
		got = append(got, w.TaskKey)
	}
	sort.Strings(got)
	if len(got) != 2 || got[0] != "wi_a" || got[1] != "wi_b" {
		t.Fatalf("应为每个任务各一条: %v", got)
	}
}

// TestProduceTimersSkipsNotDue：心跳间隔未到的 agent 不入队。
func TestProduceTimersSkipsNotDue(t *testing.T) {
	ctx := context.Background()
	agent := testAgent(true, 0) // 缺省 1800s
	recent := testNow.Add(-5 * time.Minute)
	agent.LastHeartbeatAt = &recent
	store := newFakeStore(agent)
	store.tasks = map[string][]TaskRef{"agent_t": {{Key: "wi_a", Title: "A"}}}

	newTestScheduler(store, &fakeRunStarter{}).ProduceTimers(ctx, testNow)

	if len(store.wakeups) != 0 {
		t.Fatalf("未到期不应入队: %d", len(store.wakeups))
	}
}

// TestProduceTimersIdempotentAcrossTicks：重复 tick 不重复入队（HasQueuedTimer 幂等）。
func TestProduceTimersIdempotentAcrossTicks(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(testAgent(true, 0))
	store.tasks = map[string][]TaskRef{"agent_t": {{Key: "wi_a", Title: "A"}}}

	s := newTestScheduler(store, &fakeRunStarter{})
	s.ProduceTimers(ctx, testNow)
	s.ProduceTimers(ctx, testNow.Add(time.Second))

	if len(store.wakeups) != 1 {
		t.Fatalf("重复 tick 不应堆积: %d", len(store.wakeups))
	}
}

// TestTickProducesAndConsumesTimerHeartbeat：Tick 端到端——生产 + 消费 → run 创建，
// 唤醒 consumed，心跳时间被 claim 推进。
func TestTickProducesAndConsumesTimerHeartbeat(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(testAgent(true, 0))
	store.tasks = map[string][]TaskRef{"agent_t": {{Key: "wi_a", Title: "任务A"}}}
	starter := &fakeRunStarter{}

	newTestScheduler(store, starter).Tick(ctx, testNow)

	if len(starter.created) != 1 || starter.created[0].taskKey != "wi_a" {
		t.Fatalf("应创建 1 个 run: %#v", starter.created)
	}
	for _, w := range store.wakeups {
		if w.Status != domain.WakeupStatusConsumed {
			t.Fatalf("wakeup %s status=%q", w.ID, w.Status)
		}
	}
	agent := store.agents["agent_t"]
	if agent.LastHeartbeatAt == nil || !agent.LastHeartbeatAt.Equal(testNow) {
		t.Fatalf("claim 应推进 last_heartbeat_at: %v", agent.LastHeartbeatAt)
	}
}

// ---- coalescing instruction 处置（F2）----

// TestConsumeOneCoalescedInstructionForwarded：活跃 run 合并时，context 中的
// instruction 经 ForwardInput 转发（steering），不再静默丢弃，无需降级审计。
func TestConsumeOneCoalescedInstructionForwarded(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(testAgent(true, 0))
	store.activeFn = func(string, string) (string, bool, error) { return "run_live", true, nil }
	starter := &fakeRunStarter{}
	var forwarded []string
	s := newTestScheduler(store, starter)
	s.ForwardInput = func(ctx context.Context, runID, instruction string) error {
		forwarded = append(forwarded, runID+"|"+instruction)
		return nil
	}
	w, err := EnqueueWakeup(ctx, store, domain.WakeupSourceOnDemand, "ws_t", "agent_t", "wi_1",
		map[string]any{"instruction": "顺便检查依赖版本"}, testNow.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := s.ConsumeOne(ctx, *w, testNow)
	if err != nil || outcome != OutcomeCoalesced {
		t.Fatalf("outcome=%q err=%v", outcome, err)
	}
	if len(forwarded) != 1 || forwarded[0] != "run_live|顺便检查依赖版本" {
		t.Fatalf("instruction 未转发到活跃 run: %v", forwarded)
	}
	if len(store.contextSet) != 0 {
		t.Fatalf("转发成功不应触发降级审计: %v", store.contextSet)
	}
}

// TestConsumeOneCoalescedInstructionForwardFailsAudits：转发失败（run 不支持
// steering）降级——instruction 附加到唤醒审计 context 落库。
func TestConsumeOneCoalescedInstructionForwardFailsAudits(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(testAgent(true, 0))
	store.activeFn = func(string, string) (string, bool, error) { return "run_live", true, nil }
	s := newTestScheduler(store, &fakeRunStarter{})
	s.ForwardInput = func(ctx context.Context, runID, instruction string) error {
		return errors.New("steering unsupported")
	}
	w, err := EnqueueWakeup(ctx, store, domain.WakeupSourceOnDemand, "ws_t", "agent_t", "wi_1",
		map[string]any{"instruction": "追加指令", "priority": "high"}, testNow.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := s.ConsumeOne(ctx, *w, testNow)
	if err != nil || outcome != OutcomeCoalesced {
		t.Fatalf("outcome=%q err=%v", outcome, err)
	}
	audit := store.contextSet[w.ID]
	if audit == nil {
		t.Fatal("转发失败应降级落审计 context")
	}
	if audit["coalesced_instruction"] != "追加指令" {
		t.Fatalf("审计应含原始 instruction: %#v", audit)
	}
	if audit["priority"] != "high" {
		t.Fatalf("降级不应破坏原有 context: %#v", audit)
	}
}

// ---- 原子消费与补偿（F4/F5）----

// TestConsumeOneWakeupAlreadyConsumedByRace：CAS 占位失败（已被并发消费者处理）
// → 不建 run、不重试，直接按 consumed 收尾。
func TestConsumeOneWakeupAlreadyConsumedByRace(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(testAgent(true, 0))
	starter := &fakeRunStarter{}
	w := mustEnqueueTimer(t, ctx, store, testNow.Add(-time.Minute))
	// 模拟并发消费者已占住该唤醒（DueTimers 拿到的是过期快照）。
	store.wakeups[w.ID].Status = domain.WakeupStatusConsumed

	outcome, err := newTestScheduler(store, starter).ConsumeOne(ctx, *w, testNow)
	if err != nil || outcome != OutcomeConsumed {
		t.Fatalf("outcome=%q err=%v", outcome, err)
	}
	if len(starter.created) != 0 {
		t.Fatalf("已被消费不应再建 run: %d", len(starter.created))
	}
	if len(store.requeues) != 0 {
		t.Fatalf("竞态路径不应触发回退: %v", store.requeues)
	}
}

// TestConsumeOneCreateFailureReleasesClaimAndRetries：建 run 失败 → 唤醒退回
// queued + 回滚心跳 claim；下一轮重新 claim 成功并建 run（不白等一个心跳周期）。
func TestConsumeOneCreateFailureReleasesClaimAndRetries(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(testAgent(true, 0))
	starter := &fakeRunStarter{err: errors.New("boom")}
	w := mustEnqueueTimer(t, ctx, store, testNow.Add(-5*time.Minute))
	s := newTestScheduler(store, starter)

	outcome, err := s.ConsumeOne(ctx, *w, testNow)
	if err != nil || outcome != OutcomeQueued {
		t.Fatalf("outcome=%q err=%v", outcome, err)
	}
	if got := store.status(w.ID); got != domain.WakeupStatusQueued {
		t.Fatalf("失败后应退回 queued, status=%q", got)
	}
	if len(store.requeues) != 1 || store.requeues[0] != w.ID {
		t.Fatalf("应有一次消费补偿: %v", store.requeues)
	}
	if len(store.releases) != 1 || !store.releases[0].claimedAt.Equal(testNow) {
		t.Fatalf("claim 应回滚: %v", store.releases)
	}
	if a := store.agents["agent_t"]; a.LastHeartbeatAt != nil {
		t.Fatalf("回滚后 last_heartbeat_at 应复位为 nil: %v", a.LastHeartbeatAt)
	}

	// 下一 tick：重新 claim 命中（间隔判定不再被烧掉的 claim 挡住）并成功建 run。
	starter.err = nil
	later := testNow.Add(time.Minute)
	ww := *store.wakeups[w.ID]
	outcome, err = s.ConsumeOne(ctx, ww, later)
	if err != nil || outcome != OutcomeConsumed {
		t.Fatalf("重试 outcome=%q err=%v", outcome, err)
	}
	if len(store.claimCalls) != 2 {
		t.Fatalf("应重新 claim: %d", len(store.claimCalls))
	}
	if len(starter.created) != 1 {
		t.Fatalf("重试应建 run: %d", len(starter.created))
	}
}

// ---- 模板渲染 ----

func TestRenderPromptVariables(t *testing.T) {
	agent := testAgent(true, 0)
	tpl := "{{agent.slug}}|{{agent.name}}|{{agent.role}}|{{work_item.title}}|{{context.priority}}|{{context.missing}}|{{context.count}}"
	got := RenderPrompt(tpl, agent, "标题", map[string]any{"priority": "high", "count": 3})
	want := "tester|测试员|developer|标题|high||"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRenderPromptEmptyTemplateUsesDefault(t *testing.T) {
	agent := testAgent(true, 0)
	got := RenderPrompt("", agent, "标题任务", nil)
	// 空模板 → 用缺省模板，且缺省模板里的变量同样被渲染。
	want := strings.NewReplacer(
		"{{agent.name}}", "测试员",
		"{{agent.role}}", "developer",
		"{{work_item.title}}", "标题任务",
	).Replace(domain.DefaultPromptTemplate)
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if strings.Contains(got, "{{") {
		t.Fatalf("缺省模板存在未渲染变量: %q", got)
	}
}

func TestRenderPromptInstructionTakesPriority(t *testing.T) {
	got := RenderPrompt("模板 {{agent.name}}", testAgent(true, 0), "标题",
		map[string]any{"instruction": "直接执行部署"})
	if got != "直接执行部署" {
		t.Fatalf("instruction 应优先: %q", got)
	}
	// 空串 instruction 不生效。
	got = RenderPrompt("模板 {{agent.name}}", testAgent(true, 0), "标题", map[string]any{"instruction": ""})
	if got != "模板 测试员" {
		t.Fatalf("空 instruction 应回退模板: %q", got)
	}
}

func TestRenderPromptToleratesInvalidSyntax(t *testing.T) {
	agent := testAgent(true, 0)
	ctxMap := map[string]any{"k": "v"}
	cases := []struct{ in, want string }{
		{"{{agent", "{{agent"},                   // 缺闭合
		{"{{bogus}}", "{{bogus}}"},               // 未知变量
		{"{{context.}}", "{{context.}}"},         // 空 context key
		{"{{}}", "{{}}"},                         // 空变量
		{"{{ agent.name }}", "{{ agent.name }}"}, // 带空格：按字面保留（只认紧凑语法）
		{"尾巴}}没有开头", "尾巴}}没有开头"},                 // 孤立闭合
		{"{{agent.name}}{{agent", "测试员{{agent"},  // 混合：合法变量替换 + 非法保留
		{"a}}b{{agent.role}}c", "a}}bdeveloperc"},
	}
	for _, tc := range cases {
		if got := RenderPrompt(tc.in, agent, "t", ctxMap); got != tc.want {
			t.Fatalf("RenderPrompt(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRenderPromptNilAgentSafe(t *testing.T) {
	got := RenderPrompt("[{{agent.name}}]-{{work_item.title}}-{{context.k}}", nil, "T", map[string]any{"k": "v"})
	if got != "[]-T-v" {
		t.Fatalf("nil agent 应安全降级: %q", got)
	}
}

// 编译期确认 fake 满足接口。
var (
	_ Store      = (*fakeStore)(nil)
	_ RunStarter = (*fakeRunStarter)(nil)
)

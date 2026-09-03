// Package scheduling 实现 wakeup 四源调度的核心循环（M4，对齐 Paperclip）：
//
//	生产 timer 唤醒（心跳到期 agent × 名下非终态任务）→ DueTimers → 心跳 claim →
//	活跃 run coalescing（instruction 转发 steering，失败降级落审计）→ 原子占位 →
//	渲染 prompt → 创建 run
//
// 本包只依赖 domain 与标准库；持久化与 run 创建通过接口注入
// （sqlstore.Wakeups() 满足 Store，application 层在 merge 阶段适配 RunStarter）。
package scheduling

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// Store 是调度循环需要的持久化能力（*sqlstore.WakeupRepo 满足此接口）。
type Store interface {
	EnqueueWakeup(ctx context.Context, r *domain.WakeupRequest) error
	DueTimers(ctx context.Context, now time.Time, limit int) ([]domain.WakeupRequest, error)
	// MarkWakeupStatus CAS 迁移（仅 queued 出发）：占不住返回 domain.ErrWakeupNotQueued。
	MarkWakeupStatus(ctx context.Context, id string, status domain.WakeupStatus) error
	// RequeueWakeup 消费补偿：CAS consumed → queued（建 run 失败时回退）。
	RequeueWakeup(ctx context.Context, id string) error
	// SetWakeupContext 覆写 context 列（coalescing 降级审计用）。
	SetWakeupContext(ctx context.Context, id string, wakeContext map[string]any) error
	ClaimHeartbeat(ctx context.Context, agentProfileID string, minInterval time.Duration, now time.Time) (bool, error)
	// ReleaseHeartbeatClaim 回滚一次心跳 claim（仅当仍是本次 claim 写入值时复位）。
	ReleaseHeartbeatClaim(ctx context.Context, agentProfileID string, claimedAt time.Time) error
	ActiveRunKeyForAgentTask(ctx context.Context, agentProfileID, taskKey string) (runID string, alive bool, err error)
	GetAgentProfile(ctx context.Context, id string) (*domain.AgentProfile, error)
	// RecentByAgentTask 近期唤醒记录（含已合并，审计/上下文合并用）。
	RecentByAgentTask(ctx context.Context, agentProfileID, taskKey string, since time.Time) ([]domain.WakeupRequest, error)
	// ListHeartbeatAgents 心跳自主唤醒候选（availability=enabled 且 heartbeat_enabled）。
	ListHeartbeatAgents(ctx context.Context) ([]domain.AgentProfile, error)
	// AssignedTasks agent 名下非终态任务（timer 唤醒锚点）。
	AssignedTasks(ctx context.Context, agentProfileID string) ([]TaskRef, error)
	// HasQueuedTimer 幂等判定：(agent, task_key) 是否已有 queued 的 timer 唤醒。
	HasQueuedTimer(ctx context.Context, agentProfileID, taskKey string) (bool, error)
}

// TaskRef timer 唤醒锚定的任务引用（Key 即 work item id，与 assignment 源对齐）。
type TaskRef struct {
	Key   string
	Title string
}

// InputForwarder 把 instruction 转发给运行中的 run（steering 输入）；签名对齐
// application.Service.InputForwarder / runtime.ModuleRunner.ForwardInput。
// 返回错误表示执行端不支持和无法接收（run 不在本进程、无 steering 能力等）。
type InputForwarder func(ctx context.Context, runID, instruction string) error

// RunStarter 由 application 层实现：为一次唤醒创建 run（merge 阶段接线）。
// wakeContext 原样透传，供运行中 run 的上下文合并消费。
type RunStarter interface {
	CreateRunForWakeup(ctx context.Context, workspaceID, agentProfileID, taskKey, instruction string, wakeContext map[string]any) (runID string, err error)
}

// WakeupPreflight is an optional control-plane gate run before heartbeat
// claims and active-run coalescing. It lets a durable Goal pause defer a wake
// without forwarding its instruction to an already-running Run.
type WakeupPreflight interface {
	PreflightWakeup(ctx context.Context, workspaceID, agentProfileID, taskKey string) error
}

// Logger 最小日志接口（*log.Logger 满足）；nil 表示静默。
type Logger interface {
	Printf(format string, v ...any)
}

// Outcome 单条 wakeup 的消费结果。
type Outcome string

const (
	OutcomeConsumed  Outcome = "consumed"  // 已创建 run（或已被并发消费者处理）
	OutcomeCoalesced Outcome = "coalesced" // 已合并（心跳禁用 / 间隔内 / 活跃 run / 超龄）
	OutcomeQueued    Outcome = "queued"    // 保持 queued，下轮重试
)

// ErrWakeupNoop tells the scheduler that the wakeup was intentionally consumed
// without creating a Run because its target control line is already terminal.
// It is distinct from a transient RunStarter error, which must be requeued.
var ErrWakeupNoop = errors.New("wakeup: intentional no-op")

// ErrWakeupDeferred tells the scheduler that the target is intentionally
// paused but the wakeup remains required. Unlike a terminal no-op, deferred
// requests must be requeued without applying the normal age-based coalescing.
var ErrWakeupDeferred = errors.New("wakeup: deferred")

const (
	// DefaultTickInterval 调度循环缺省周期。
	DefaultTickInterval = 30 * time.Second
	// dueTimersLimit 单轮最多消费的到期 timer 数。
	dueTimersLimit = 50
	// maxWakeupAge 重试兜底：wake_at 早于 now-1h 的失败请求直接标记 coalesced，防堆积。
	maxWakeupAge = time.Hour
	// staleLockAge 执行锁回收阈值：locked_at 超过该时长且属主 run 已终态的锁由
	// 兜底扫描清空（正常路径属主终态事务内已释放，这里只兜异常残留）。
	staleLockAge = 30 * time.Minute
	// staleLockSweepInterval 执行锁兜底扫描周期（独立于 tick 周期）。
	staleLockSweepInterval = 5 * time.Minute
)

// StaleLockReleaser 任务执行锁回收端口（*sqlstore.WorkItemRepo 满足）。
// 独立于 wakeup Store：锁回收是 work item 面能力，不并入唤醒仓储接口。
type StaleLockReleaser interface {
	ReleaseStaleLocks(ctx context.Context, olderThan time.Time) (int, error)
}

// Scheduler 周期生产并消费到期唤醒。
type Scheduler struct {
	Store            Store
	RunStarter       RunStarter
	Interval         time.Duration // 缺省 DefaultTickInterval
	HeartbeatDefault int           // 秒；缺省 domain.DefaultHeartbeatIntervalSec
	// ForwardInput 活跃 run 合并时的 steering 转发器（可选；nil 时降级落审计）。
	ForwardInput InputForwarder
	// StaleLocks 任务执行锁回收端口（可选；nil 时跳过兜底扫描）。
	StaleLocks StaleLockReleaser
	Logger     Logger // 可为 nil
	// lastLockSweep 上次锁回收扫描时刻；仅调度循环单 goroutine 内访问。
	lastLockSweep time.Time
}

func (s *Scheduler) tickInterval() time.Duration {
	if s.Interval > 0 {
		return s.Interval
	}
	return DefaultTickInterval
}

// heartbeatInterval 解析生效心跳间隔：profile.IntervalSec > 0 优先，其次全局缺省。
func (s *Scheduler) heartbeatInterval(profileSec int) time.Duration {
	if profileSec > 0 {
		return time.Duration(profileSec) * time.Second
	}
	def := s.HeartbeatDefault
	if def <= 0 {
		def = domain.DefaultHeartbeatIntervalSec
	}
	return time.Duration(def) * time.Second
}

func (s *Scheduler) logf(format string, v ...any) {
	if s.Logger != nil {
		s.Logger.Printf("[scheduling] "+format, v...)
	}
}

// Start 阻塞运行调度循环直到 ctx 结束。
func (s *Scheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(s.tickInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.Tick(ctx, now)
		}
	}
}

// Tick 先扫执行锁兜底回收，再生产本轮到期的 timer 唤醒，最后消费全部到期唤醒
// （单条失败不影响后续）。
func (s *Scheduler) Tick(ctx context.Context, now time.Time) {
	s.sweepStaleLocks(ctx, now)
	s.ProduceTimers(ctx, now)
	wakeups, err := s.Store.DueTimers(ctx, now, dueTimersLimit)
	if err != nil {
		s.logf("DueTimers 失败: %v", err)
		return
	}
	for _, w := range wakeups {
		if _, err := s.ConsumeOne(ctx, w, now); err != nil {
			s.logf("ConsumeOne %s 失败: %v", w.ID, err)
		}
	}
}

// sweepStaleLocks 执行锁兜底回收（低频，对齐 ClawTeam release_stale_locks）：
// 每 staleLockSweepInterval 一次，释放超 staleLockAge 且属主 run 已终态的锁。
// 属主活性由 SQL 面的 run 终态判定负责；失败只记日志不阻塞调度。
func (s *Scheduler) sweepStaleLocks(ctx context.Context, now time.Time) {
	if s.StaleLocks == nil {
		return
	}
	if !s.lastLockSweep.IsZero() && now.Sub(s.lastLockSweep) < staleLockSweepInterval {
		return
	}
	s.lastLockSweep = now
	released, err := s.StaleLocks.ReleaseStaleLocks(ctx, now.Add(-staleLockAge))
	if err != nil {
		s.logf("执行锁回收扫描失败: %v", err)
		return
	}
	if released > 0 {
		s.logf("执行锁回收：%d 个死属主锁已释放", released)
	}
}

// ProduceTimers 生产 timer 源唤醒：对每个开启心跳自主唤醒（heartbeat_enabled）
// 且可调度的 agent，按生效间隔判定到期（last_heartbeat_at 为空或距今 ≥ 间隔），
// 对其名下每个非终态工作项幂等入队 source=timer 的唤醒（task_key = work item id，
// 与 assignment 源对齐）。已有 queued timer 的 (agent, task_key) 跳过，避免每 tick 堆积。
func (s *Scheduler) ProduceTimers(ctx context.Context, now time.Time) {
	agents, err := s.Store.ListHeartbeatAgents(ctx)
	if err != nil {
		s.logf("ListHeartbeatAgents 失败: %v", err)
		return
	}
	for i := range agents {
		agent := &agents[i]
		interval := s.heartbeatInterval(agent.HeartbeatIntervalSec)
		if agent.LastHeartbeatAt != nil && now.Sub(*agent.LastHeartbeatAt) < interval {
			continue // 心跳未到期
		}
		tasks, err := s.Store.AssignedTasks(ctx, agent.ID)
		if err != nil {
			s.logf("agent %s: 查询名下任务失败: %v", agent.ID, err)
			continue
		}
		for _, task := range tasks {
			queued, err := s.Store.HasQueuedTimer(ctx, agent.ID, task.Key)
			if err != nil {
				s.logf("agent %s: HasQueuedTimer(%s) 失败: %v", agent.ID, task.Key, err)
				continue
			}
			if queued {
				continue
			}
			wakeContext := map[string]any{"work_item_title": task.Title}
			if _, err := EnqueueWakeup(ctx, s.Store, domain.WakeupSourceTimer,
				agent.WorkspaceID, agent.ID, task.Key, wakeContext, now); err != nil {
				s.logf("agent %s: 入队 timer 唤醒失败（task %s）: %v", agent.ID, task.Key, err)
			}
		}
	}
}

// ConsumeOne 消费单条唤醒请求，返回最终 outcome。流程：
//  1. 源门控——timer/普通 automation 走完整心跳链（enabled → claim 间隔）；
//     assignment/on_demand 是事件驱动唤醒，不受心跳间隔门控，仅兜底校验各自开关；
//     settlement automation 是控制平面必达收口，既不依赖 heartbeat，也不因活跃
//     run coalesced 丢弃，而是保持 queued 等待下一轮重试；
//  2. 活跃 run（活跃 lease 或进程内执行）→ 普通唤醒 coalesced，context 中的
//     instruction 先经 ForwardInput 转发 steering，失败降级附加进审计 context；
//     settlement automation 保持 queued；zombie 穿透继续；
//  3. CAS 占位（queued → consumed）占住本唤醒——占不住视为已被并发消费，直接返回；
//  4. 渲染 prompt → CreateRunForWakeup：成功收尾；失败补偿（唤醒退回 queued +
//     回滚心跳 claim）后按超龄策略重试/合并，不烧心跳槽。
func (s *Scheduler) ConsumeOne(ctx context.Context, w domain.WakeupRequest, now time.Time) (Outcome, error) {
	profile, err := s.Store.GetAgentProfile(ctx, w.AgentProfileID)
	if err != nil {
		s.logf("wakeup %s: 加载 agent %s 失败: %v", w.ID, w.AgentProfileID, err)
		return s.retryOrExpire(ctx, w, now, false)
	}

	policy := profile.Heartbeat()
	settlement := isSettlementWakeup(w)
	coordinatorAutomation := isCoordinatorPlanAutomation(w, profile)
	if preflight, ok := s.RunStarter.(WakeupPreflight); ok {
		if err := preflight.PreflightWakeup(ctx, w.WorkspaceID, w.AgentProfileID, w.TaskKey); err != nil {
			if errors.Is(err, ErrWakeupDeferred) {
				// The request is intentionally left queued. No heartbeat claim or
				// active-run steering has happened yet, so pause/resume preserves
				// the original wake exactly.
				return OutcomeQueued, nil
			}
			if errors.Is(err, ErrWakeupNoop) {
				if markErr := s.Store.MarkWakeupStatus(ctx, w.ID, domain.WakeupStatusConsumed); markErr != nil &&
					!errors.Is(markErr, domain.ErrWakeupNotQueued) {
					return OutcomeQueued, markErr
				}
				return OutcomeConsumed, nil
			}
			s.logf("wakeup %s: control-plane preflight 失败: %v", w.ID, err)
			return s.retryOrExpire(ctx, w, now, coordinatorAutomation)
		}
	}
	claimed := false
	switch w.Source {
	case domain.WakeupSourceTimer:
		if !policy.Enabled {
			return s.coalesce(ctx, w, "heartbeat 已禁用")
		}
		claimed, err = s.Store.ClaimHeartbeat(ctx, w.AgentProfileID, s.heartbeatInterval(policy.IntervalSec), now)
		if err != nil {
			s.logf("wakeup %s: ClaimHeartbeat 失败: %v", w.ID, err)
			return s.retryOrExpire(ctx, w, now, coordinatorAutomation)
		}
		if !claimed {
			return s.coalesce(ctx, w, "距上次心跳不足间隔")
		}
	case domain.WakeupSourceAutomation:
		if settlement || coordinatorAutomation {
			// settlement 与受保护 Coordinator 的 plan timer/children_quiet
			// 都是控制平面必达工作，不受 Coordinator 自主 heartbeat 开关
			// 或间隔约束；普通 Agent automation 仍走原有 heartbeat 门控。
			break
		}
		if !policy.Enabled {
			return s.coalesce(ctx, w, "heartbeat 已禁用")
		}
		claimed, err = s.Store.ClaimHeartbeat(ctx, w.AgentProfileID, s.heartbeatInterval(policy.IntervalSec), now)
		if err != nil {
			s.logf("wakeup %s: ClaimHeartbeat 失败: %v", w.ID, err)
			return s.retryOrExpire(ctx, w, now, coordinatorAutomation)
		}
		if !claimed {
			return s.coalesce(ctx, w, "距上次心跳不足间隔")
		}
	case domain.WakeupSourceAssignment:
		if !policy.WakeOnAssignment {
			return s.coalesce(ctx, w, "agent 未开启指派唤醒")
		}
	case domain.WakeupSourceOnDemand:
		if !policy.WakeOnDemand {
			return s.coalesce(ctx, w, "agent 未开启手动唤醒")
		}
	}

	// releaseClaim 回滚本次心跳 claim（仅当 last_heartbeat_at 仍是本次写入值），
	// 供建 run 前的失败路径使用——否则下一 tick 因间隔不足被丢弃，agent 白等一个周期。
	releaseClaim := func() {
		if !claimed {
			return
		}
		if err := s.Store.ReleaseHeartbeatClaim(ctx, w.AgentProfileID, now); err != nil {
			s.logf("wakeup %s: ReleaseHeartbeatClaim 失败: %v", w.ID, err)
		}
	}

	runID, alive, err := s.Store.ActiveRunKeyForAgentTask(ctx, w.AgentProfileID, w.TaskKey)
	if err != nil {
		s.logf("wakeup %s: 活跃 run 查询失败: %v", w.ID, err)
		releaseClaim()
		return s.retryOrExpire(ctx, w, now, coordinatorAutomation)
	}
	if alive {
		if settlement || coordinatorAutomation {
			// 汇总 run 不能被活跃 lead/同任务 run 合并吞掉；保持 queued，待
			// 活跃 run 终态释放任务锁后由下一轮消费。
			s.logf("wakeup %s: settlement 等待活跃 run %s，保持 queued", w.ID, runID)
			return OutcomeQueued, nil
		}
		// 活跃 run：instruction 先转发 steering（失败降级落审计），再记 coalesced 审计。
		s.forwardOrAudit(ctx, w, runID)
		return s.coalesce(ctx, w, "已有活跃 run "+runID)
	}

	// 原子占位：CAS queued → consumed。占不住说明已被并发消费者处理，不再建 run。
	if err := s.Store.MarkWakeupStatus(ctx, w.ID, domain.WakeupStatusConsumed); err != nil {
		if errors.Is(err, domain.ErrWakeupNotQueued) {
			s.logf("wakeup %s: 已被并发消费，跳过", w.ID)
			releaseClaim()
			return OutcomeConsumed, nil
		}
		s.logf("wakeup %s: 占位失败: %v", w.ID, err)
		releaseClaim()
		return s.retryOrExpire(ctx, w, now, coordinatorAutomation)
	}

	instruction := RenderPrompt(policy.PromptTemplate, profile, workItemTitle(w), w.Context)
	if _, err := s.RunStarter.CreateRunForWakeup(ctx, w.WorkspaceID, w.AgentProfileID, w.TaskKey, instruction, w.Context); err != nil {
		if errors.Is(err, ErrWakeupNoop) {
			releaseClaim()
			return OutcomeConsumed, nil
		}
		if errors.Is(err, ErrWakeupDeferred) {
			if rerr := s.Store.RequeueWakeup(ctx, w.ID); rerr != nil {
				s.logf("wakeup %s: paused target 回队失败: %v", w.ID, rerr)
				return OutcomeQueued, rerr
			}
			releaseClaim()
			return OutcomeQueued, nil
		}
		s.logf("wakeup %s: 创建 run 失败: %v", w.ID, err)
		// 补偿：唤醒退回 queued（不烧掉）+ 回滚 claim，下一 tick 重新消费。
		if rerr := s.Store.RequeueWakeup(ctx, w.ID); rerr != nil {
			s.logf("wakeup %s: 回退 queued 失败: %v", w.ID, rerr)
		}
		releaseClaim()
		return s.retryOrExpire(ctx, w, now, coordinatorAutomation)
	}
	return OutcomeConsumed, nil
}

// forwardOrAudit 活跃 run 合并时的 instruction 处置：非空 instruction 经
// ForwardInput 转发给活跃 run（steering）；转发失败或无转发器时降级——附加到
// 唤醒审计 context 落库（coalesced_instruction 键），不静默丢弃。
func (s *Scheduler) forwardOrAudit(ctx context.Context, w domain.WakeupRequest, runID string) {
	instruction := strings.TrimSpace(w.Instruction())
	if instruction == "" {
		return // 无显式指令（timer 模板类），合并本身即语义
	}
	if s.ForwardInput != nil {
		if err := s.ForwardInput(ctx, runID, instruction); err != nil {
			s.logf("wakeup %s: instruction 转发失败（run %s）: %v", w.ID, runID, err)
		} else {
			s.logf("wakeup %s: instruction 已转发到活跃 run %s", w.ID, runID)
			return
		}
	}
	audit := map[string]any{}
	for k, v := range w.Context {
		audit[k] = v
	}
	audit["coalesced_instruction"] = instruction
	if err := s.Store.SetWakeupContext(ctx, w.ID, audit); err != nil {
		s.logf("wakeup %s: 降级审计落库失败: %v", w.ID, err)
	}
}

// coalesce 标记合并并记录原因。
func (s *Scheduler) coalesce(ctx context.Context, w domain.WakeupRequest, reason string) (Outcome, error) {
	if err := s.Store.MarkWakeupStatus(ctx, w.ID, domain.WakeupStatusCoalesced); err != nil {
		if errors.Is(err, domain.ErrWakeupNotQueued) {
			// 已被并发消费：对方已建 run，本次合并无副作用。
			s.logf("wakeup %s 已被并发消费，合并跳过", w.ID)
			return OutcomeConsumed, nil
		}
		return OutcomeQueued, err
	}
	s.logf("wakeup %s 合并: %s", w.ID, reason)
	return OutcomeCoalesced, nil
}

// retryOrExpire 失败兜底：保持 queued 下轮重试；超过 maxWakeupAge 标记 coalesced 防堆积。
func (s *Scheduler) retryOrExpire(ctx context.Context, w domain.WakeupRequest, now time.Time, coordinatorAutomation bool) (Outcome, error) {
	// settlement 是控制平面的必达收口，不能因为暂时无法加载 agent 或创建
	// run 而在超龄策略中标记 coalesced；保持 queued 交给后续 tick 重试。
	if isSettlementWakeup(w) || coordinatorAutomation {
		return OutcomeQueued, nil
	}
	if w.WakeAt.Before(now.Add(-maxWakeupAge)) {
		return s.coalesce(ctx, w, "超龄未消费（>1h）")
	}
	return OutcomeQueued, nil
}

// isSettlementWakeup 识别 S3 内部必达汇总唤醒。该标记只由 application 的
// settlement 收口钩子写入 automation context；普通 automation 仍沿用 heartbeat
// 策略，避免为未来自动化源改变既有门控语义。
func isSettlementWakeup(w domain.WakeupRequest) bool {
	if w.Source != domain.WakeupSourceAutomation {
		return false
	}
	id, _ := w.Context[domain.WakeupContextSettlementDispatchID].(string)
	return strings.TrimSpace(id) != ""
}

// isCoordinatorPlanAutomation is the narrow heartbeat exception for the
// system Task Coordinator. Plan timer and children_quiet wakeups are control
// actions, not user Agent heartbeats; ordinary Agents carrying the same
// context remain subject to the normal automation policy.
func isCoordinatorPlanAutomation(w domain.WakeupRequest, profile *domain.AgentProfile) bool {
	if w.Source != domain.WakeupSourceAutomation || profile == nil || !profile.Kind.IsSystem() {
		return false
	}
	planID, ok := w.Context["plan_id"].(string)
	if !ok || strings.TrimSpace(planID) == "" {
		return false
	}
	trigger, _ := w.Context["trigger"].(string)
	return trigger == "defer_wake_at" || trigger == "children_quiet"
}

// workItemTitle 从唤醒 context 取工作项标题（入队时可携带），缺省用 taskKey 占位。
func workItemTitle(w domain.WakeupRequest) string {
	if title, ok := w.Context["work_item_title"].(string); ok && title != "" {
		return title
	}
	return w.TaskKey
}

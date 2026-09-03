// gateway.go — dsh 网关型 Adapter（Adapter SPI v2，协议文档 §9 dsh 行）。
//
// 形态（boujoy-harness 式）：不再为每个 Run 起 dsh-jsonrpc-agent stdio 子进程，
// 而是与长驻 `dsh web` 网关交互——首 turn POST session.create，后续 turn
// POST session.prompt(mode=queue)；事件从 WS /api/events.mux 下行流订阅并
// 映射 canonical 事件；harness 发起的审批/提问经 /api/respond 回应。
// 会话连续性由 harness 原生保证（磁盘 session.jsonl），网关重启不丢上下文。
// 取消经 session.cancel（原生精确取消，非进程级）；steering 经
// session.prompt(mode=steer)。resume ref 携带但网关侧会话缺失时返回
// Failure{Family: session_unknown}（不静默降级 fresh：EffectiveInstruction
// 在 resume 轮只含当轮输入，fresh 会话收不到历史=失忆），由应用层自愈链路
// 清锚点并以全量历史 fresh 重建。
package dsh

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// GatewayConfig 网关形态配置。BaseURL 非空时直连外部网关（supervisor
// 只探活不管理进程）；否则按 RepoDir/nodeBin 拉起本地长驻实例。
type GatewayConfig struct {
	BaseURL     string        // 直连已运行网关，如 http://127.0.0.1:3080
	Host        string        // 拉起时绑定 host（默认 127.0.0.1）
	Port        int           // 拉起时端口（默认 3090）
	NodeBin     string        // node 可执行文件（默认 node）
	BinArgs     []string      // 覆盖启动参数（测试回放桩）
	RepoDir     string        // deepseek-harness 仓库根（apps/cli/src/bin.ts）
	Home        string        // DSH_HOME 项目空间（默认 .agent-work/dsh）
	Model       string        // 缺省模型（session.selectModel）
	IdleTimeout time.Duration // 事件流空闲保护（默认 10m）
}

func (c GatewayConfig) port() int {
	if c.Port > 0 {
		return c.Port
	}
	return 3090
}

func (c GatewayConfig) nodeBin() string {
	if c.NodeBin != "" {
		return c.NodeBin
	}
	return "node"
}

// defaultArgs 拉起命令：node --import tsx/esm <repo>/apps/cli/src/bin.ts web。
func (c GatewayConfig) defaultArgs() []string {
	host := c.Host
	if host == "" {
		host = "127.0.0.1"
	}
	return []string{
		"--import", "tsx/esm",
		c.RepoDir + "/apps/cli/src/bin.ts",
		"web", "--host", host, "--port", fmt.Sprint(c.port()),
	}
}

// Gateway 实现 runtime.AdapterModule。
type Gateway struct {
	cfg GatewayConfig
	sup *Supervisor
}

var _ runtime.AdapterModule = (*Gateway)(nil)

// NewGateway 创建网关 adapter；进程拉起延迟到首次 Probe/Execute。
func NewGateway(cfg GatewayConfig) *Gateway {
	return &Gateway{cfg: cfg, sup: NewSupervisor(cfg)}
}

func (g *Gateway) Manifest(ctx context.Context) (runtime.AdapterManifest, error) {
	return runtime.AdapterManifest{
		AdapterID: "dsh", AdapterVersion: "2.0.0",
		Protocol: runtime.Protocol{Name: "dsh-web-gateway", Version: "1"},
		Capabilities: map[string]runtime.CapabilityLevel{
			"streaming":                               runtime.CapSupported,
			runtime.CapabilityStructuredTransport:     runtime.CapSupported,
			runtime.CapabilitySchemaConstrainedOutput: runtime.CapUnavailable,
			runtime.CapabilityControlToolCall:         runtime.CapUnavailable,
			// session.cancel 原生精确取消（turn 级，非进程级）。
			"interrupt":         runtime.CapSupported,
			"resume":            runtime.CapSupported, // harness 原生 session 持久化
			"multi_turn":        runtime.CapSupported,
			"steering":          runtime.CapSupported, // session.prompt(mode=steer)
			"approval":          runtime.CapSupported, // approval/requested + /api/respond
			"system_prompt":     runtime.CapSupported, // session.create persona
			"workspace_files":   runtime.CapSupported,
			"terminal":          runtime.CapUnavailable,
			"structured_output": runtime.CapAdapterTranslated,
			"permissions":       runtime.CapSupported,
			"modes":             runtime.CapAdapterTranslated, // plan 语义注入 persona
			"multi_vendor":      runtime.CapAdapterTranslated, // 网关侧 provider 设置决定
		},
		SchemaDigest: "sha256:dsh-web-gateway-v1",
	}, nil
}

// Probe 确保网关可用（含拉起）后返回 OK。
func (g *Gateway) Probe(ctx context.Context, req runtime.ProbeRequest) (runtime.ProbeResult, error) {
	m, _ := g.Manifest(ctx)
	if _, err := g.sup.Ensure(ctx); err != nil {
		return runtime.ProbeResult{OK: false, Manifest: &m, Error: err.Error()}, nil
	}
	return runtime.ProbeResult{OK: true, Manifest: &m}, nil
}

// Close 透传 supervisor 关停（control-plane 退出时回收网关进程）。
func (g *Gateway) Close() { g.sup.Close() }

// approvalWire 网关审批句柄：engine 审批 id → 下发帧回显字段。
type approvalWire struct {
	rpcID      string
	sessionID  string
	approvalID string
	kind       string // tool | question
	question   *wireQuestion
}

// turnRun 一次 Execute 的可变状态（事件循环协程写，收尾读）。
type turnRun struct {
	activeTurn int64 // prompt 接受后见到的 turn/start 编号；0=尚未开始
	activeSeen bool

	// usage 双通道去重（F7d）：dsh 事件协议中 assistant/chunk 与
	// assistant/message 都可能携带 usage，且同一 turn 两类帧的数值指向同一批
	// token 消耗。口径：assistant/message.usage 为 turn 内权威值（多条则累加），
	// chunk.usage 仅作累计兜底——turn 结束仍无任何 message.usage 时才采纳。
	usageMsgSeen      bool
	usageMsgBuckets   dshUsageBuckets
	usageChunkBuckets dshUsageBuckets
	usageMsgLegacy    dshLegacyUsage
	usageChunkLegacy  dshLegacyUsage

	failure       *runtime.Failure // turn/end error 的权威失败
	sessionUpdate *runtime.SessionUpdate
}

// dshUsageBuckets preserves the provider's nullable usage fields while the
// legacy projections above keep their historical best-effort shape. DSH
// currently exposes uncached input, cache-read input, and output; cache-write
// is intentionally unknown unless a future protocol frame reports it.
type dshUsageBuckets struct {
	uncached, cacheRead, cacheWrite, output int64
	uncachedKnown, cacheReadKnown           bool
	cacheWriteKnown, outputKnown            bool
	seen                                    bool
}

// dshLegacyUsage keeps the historical non-nullable projection without sharing
// its unsafe arithmetic. Invalid individual values are skipped; any aggregate
// overflow invalidates that projection dimension and pins it to zero.
type dshLegacyUsage struct {
	input, output, cached                         int64
	inputOverflow, outputOverflow, cachedOverflow bool
}

func dshUsageNumber(data map[string]any, key string) (int64, bool) {
	value, ok := data[key]
	if !ok || value == nil {
		return 0, false
	}
	return dshUsageValue(value)
}

// dshUsageValue 是唯一的严格数值口径：只接受非负整数（float64/int64/int），
// 其余一律 unknown——负数/NaN/Inf/非整数/越界/非数值都不伪造。
func dshUsageValue(value any) (int64, bool) {
	switch value := value.(type) {
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value ||
			value < 0 || value >= float64(math.MaxInt64) {
			return 0, false
		}
		return int64(value), true
	case int64:
		if value < 0 {
			return 0, false
		}
		return value, true
	case int:
		if value < 0 {
			return 0, false
		}
		return int64(value), true
	default:
		return 0, false
	}
}

func (u *dshUsageBuckets) add(data map[string]any) {
	uncached, uncachedKnown := dshUsageNumber(data, "inputTokens")
	read, readKnown := dshUsageNumber(data, "cacheReadTokens")
	write, writeKnown := dshUsageNumber(data, "cacheWriteTokens")
	output, outputKnown := dshUsageNumber(data, "outputTokens")
	if uncachedKnown {
		if next, ok := domain.CheckedAddNonNegative(u.uncached, uncached); ok == nil {
			u.uncached = next
		} else {
			uncachedKnown = false
		}
	}
	if readKnown {
		if next, ok := domain.CheckedAddNonNegative(u.cacheRead, read); ok == nil {
			u.cacheRead = next
		} else {
			readKnown = false
		}
	}
	if writeKnown {
		if next, ok := domain.CheckedAddNonNegative(u.cacheWrite, write); ok == nil {
			u.cacheWrite = next
		} else {
			writeKnown = false
		}
	}
	if outputKnown {
		if next, ok := domain.CheckedAddNonNegative(u.output, output); ok == nil {
			u.output = next
		} else {
			outputKnown = false
		}
	}
	if !u.seen {
		u.uncachedKnown, u.cacheReadKnown = uncachedKnown, readKnown
		u.cacheWriteKnown, u.outputKnown = writeKnown, outputKnown
	} else {
		u.uncachedKnown = u.uncachedKnown && uncachedKnown
		u.cacheReadKnown = u.cacheReadKnown && readKnown
		u.cacheWriteKnown = u.cacheWriteKnown && writeKnown
		u.outputKnown = u.outputKnown && outputKnown
	}
	u.seen = true
}

func (u *dshLegacyUsage) add(data map[string]any) {
	uncached, uncachedKnown := dshUsageNumber(data, "inputTokens")
	read, readKnown := dshUsageNumber(data, "cacheReadTokens")
	output, outputKnown := dshUsageNumber(data, "outputTokens")
	if !u.inputOverflow {
		frameInput := int64(0)
		var err error
		if uncachedKnown {
			frameInput, err = domain.CheckedAddNonNegative(frameInput, uncached)
		}
		if err == nil && readKnown {
			frameInput, err = domain.CheckedAddNonNegative(frameInput, read)
		}
		if err == nil {
			u.input, err = domain.CheckedAddNonNegative(u.input, frameInput)
		}
		if err != nil {
			u.input, u.inputOverflow = 0, true
		}
	}
	if outputKnown && !u.outputOverflow {
		if next, err := domain.CheckedAddNonNegative(u.output, output); err == nil {
			u.output = next
		} else {
			u.output, u.outputOverflow = 0, true
		}
	}
	if readKnown && !u.cachedOverflow {
		if next, err := domain.CheckedAddNonNegative(u.cached, read); err == nil {
			u.cached = next
		} else {
			u.cached, u.cachedOverflow = 0, true
		}
	}
}

func (u dshLegacyUsage) totals() (in, out, cached int64) {
	if !u.inputOverflow {
		in = u.input
	}
	if !u.outputOverflow {
		out = u.output
	}
	if !u.cachedOverflow {
		cached = u.cached
	}
	return in, out, cached
}

func dshUsageCounter(value int64, known bool) *int64 {
	if !known {
		return nil
	}
	copy := value
	return &copy
}

func (u dshUsageBuckets) counters() domain.UsageCountersV1 {
	counters := domain.UsageCountersV1{
		InputUncachedTokens: dshUsageCounter(u.uncached, u.uncachedKnown),
		CacheReadTokens:     dshUsageCounter(u.cacheRead, u.cacheReadKnown),
		CacheWriteTokens:    dshUsageCounter(u.cacheWrite, u.cacheWriteKnown),
		OutputTokens:        dshUsageCounter(u.output, u.outputKnown),
	}
	// input_total 只在三个输入分量全知时派生：任一分量未知就隐式当 0 会低估
	// total-token quota；求和溢出同样保持未知（nil 即 unknown，不伪造）。
	if counters.InputUncachedTokens != nil && counters.CacheReadTokens != nil &&
		counters.CacheWriteTokens != nil {
		total, err := domain.CheckedAddNonNegative(*counters.InputUncachedTokens, *counters.CacheReadTokens)
		if err == nil {
			total, err = domain.CheckedAddNonNegative(total, *counters.CacheWriteTokens)
		}
		if err == nil {
			counters.InputTokensTotal = &total
		}
	}
	return counters
}

func (t *turnRun) usageBuckets() (dshUsageBuckets, string, bool) {
	if t.usageMsgSeen {
		return t.usageMsgBuckets, "assistant/message", true
	}
	if t.usageChunkBuckets.seen {
		return t.usageChunkBuckets, "assistant/chunk", true
	}
	return dshUsageBuckets{}, "", false
}

// usageTotals 按「message.usage 权威、chunk 累计兜底」口径结算本轮用量。
func (t *turnRun) usageTotals() (in, out, cached int64) {
	if t.usageMsgSeen {
		return t.usageMsgLegacy.totals()
	}
	return t.usageChunkLegacy.totals()
}

// emitUsageProgress 累计后即时过程观测：按结算同源口径（usageTotals）上报
// 当前累计值；OnUsage 是覆盖语义，终态结算仍走 turnEndResult 的 ExecResult.Usage。
func emitUsageProgress(ex *runtime.ExecContext, state *turnRun) {
	in, out, cached := state.usageTotals()
	usage := runtime.Usage{
		InputTokens: in, OutputTokens: out, CachedTokens: cached, Basis: runtime.UsagePerRun,
	}
	if buckets, source, ok := state.usageBuckets(); ok {
		if report, err := runtime.NewProviderUsageReport(ex, sessionRef(state.sessionUpdate),
			"dsh", "dsh-web-gateway", "1", source,
			"inputTokens/cacheReadTokens/outputTokens; input_total=sum(input+cache_read), cache_write=unknown",
			runtime.UsagePerRun, buckets.counters()); err == nil {
			usage = runtime.AttachProviderUsage(usage, report)
		}
	}
	ex.Callbacks.OnUsage(usage)
}

func sessionRef(update *runtime.SessionUpdate) string {
	if update == nil {
		return ""
	}
	return update.Ref
}

// Execute 阻塞执行一轮：建订阅 → 解析/创建会话 → prompt → 消费 mux 流到
// turn/end → 结构化返回。
func (g *Gateway) Execute(ex *runtime.ExecContext) runtime.ExecResult {
	if strings.TrimSpace(ex.Instruction) == "" {
		return runtime.ExecResult{Outcome: runtime.OutcomeFailed,
			Failure: gwFailure(runtime.FamilyConfig, "instruction_required", "instruction required", false)}
	}
	state := &turnRun{}
	base, err := g.sup.Ensure(ex.Ctx)
	if err != nil {
		// Ctx 取消（cancel/interrupt）不是网关故障：按终态意图返回。取消可能
		// 落在 Execute 任意阶段（conformance CancelSemantics 在 running 后即下达），
		// 若把取消误报成 gateway_unavailable/io 失败，终态会变成 failed 而非 cancelled。
		if ex.Ctx.Err() != nil {
			return intentResult(ex, state)
		}
		return runtime.ExecResult{Outcome: runtime.OutcomeFailed,
			Failure: gwFailure(runtime.FamilyConfig, "gateway_unavailable", err.Error(), false)}
	}
	client := newWireClient(base)
	idle := g.cfg.IdleTimeout
	if idle <= 0 {
		idle = 10 * time.Minute
	}

	// 订阅先于 prompt：mux 是全局广播流，建立后新会话/新 turn 的帧不会丢。
	// 冷启动/重启窗口内升级握手可能被断连：短暂重试。
	var sub *muxSub
	var subErr error
	for attempt := 0; attempt < 5; attempt++ {
		if sub, subErr = client.subscribe(ex.Ctx); subErr == nil {
			break
		}
		if ex.Ctx.Err() != nil {
			break
		}
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ex.Ctx.Done():
		}
	}
	if sub == nil {
		if ex.Ctx.Err() != nil {
			return intentResult(ex, state)
		}
		return runtime.ExecResult{Outcome: runtime.OutcomeFailed,
			Failure: gwFailure(runtime.FamilyIO, "mux_subscribe_failed", subErr.Error(), true)}
	}
	defer sub.close()

	sessionID, res := g.resolveSession(ex, client, state)
	if res != nil {
		// 同上：会话解析期被取消按终态意图返回（session.create/history 因 Ctx
		// 取消而失败是取消的结果，不是 io 故障）。
		if ex.Ctx.Err() != nil {
			return intentResult(ex, state)
		}
		return *res
	}

	if err := g.promptTurn(ex.Ctx, client, sessionID, ex.Instruction); err != nil {
		if ex.Ctx.Err() != nil {
			return intentResult(ex, state)
		}
		return runtime.ExecResult{Outcome: runtime.OutcomeFailed, Failure: wireFailure(err)}
	}

	res = g.pump(ex, client, sub, sessionID, state, idle)
	return *res
}

// resolveSession 返回可用的 harness 会话 id。resume ref 携带但网关侧会话
// 缺失（session-not-found/conflict 等探测失败）时不静默降级 fresh——本 run 的
// Instruction 已按「resume 只发当轮」选定，fresh 会话收不到历史等于失忆——
// 而是返回 Failure{Family: session_unknown, Retryable: false}，交应用层自愈
// 链路清锚点并用全量历史 fresh 重试一轮。注意区分：网关连接失败/超时
// （carrier_* / agent-busy）不是会话缺失，保持 transient/io 分类，避免把网关
// 故障误报成可自愈的会话丢失；首 turn（无 resume ref）的 session.create 失败
// 也走 wireFailure 原分类。
func (g *Gateway) resolveSession(ex *runtime.ExecContext, client *wireClient, state *turnRun) (string, *runtime.ExecResult) {
	persona := personaOf(ex)
	if resumeID := runtime.SessionIDFromRef(ex.Session.Ref, "dsh"); resumeID != "" {
		// 存在性探测用 session.history：命中说明网关认识该会话（内存或磁盘）。
		if err := client.call(ex.Ctx, "session.history", map[string]any{"sessionId": resumeID, "maxMessages": 1}, nil); err != nil {
			f := wireFailure(err)
			if f.Family == runtime.FamilySessionUnknown {
				f.Code = "resume_" + f.Code
			}
			return "", &runtime.ExecResult{Outcome: runtime.OutcomeFailed, Failure: f}
		}
		// resume 命中：沿用原 ref 重报 SessionUpdate（Params/DisplayID 同前）。
		// 应用层 runs_count 幂等语义=每个新 run 首报同 ref +1，resume 轮重报
		// 是会话轮换（如 40 run 阈值）计数增长的必要输入。
		state.sessionUpdate = &runtime.SessionUpdate{
			Ref: "dsh://" + resumeID, DisplayID: resumeID,
			Params: map[string]any{"gateway_session": resumeID},
		}
		ex.Callbacks.OnSession(*state.sessionUpdate)
		return resumeID, nil
	}
	// fresh：创建会话（persona 仅创建时给定；resume 一律不传，避免 persona 冲突判定）。
	// 工作目录只来自 Host resolver 的进程内可信产物（RFC §5.1.9）；
	// 无 Resolved（未注入 resolver 的测试装配）回退进程 cwd。
	payload := map[string]any{"cwd": ex.Resolved.CWD}
	if payload["cwd"] == "" {
		payload["cwd"] = "."
	}
	if persona != "" {
		payload["persona"] = truncate(persona, 8000)
	}
	var created struct {
		SessionID string `json:"sessionId"`
	}
	if err := client.call(ex.Ctx, "session.create", payload, &created); err != nil {
		return "", &runtime.ExecResult{Outcome: runtime.OutcomeFailed, Failure: wireFailure(err)}
	}
	sessionID := created.SessionID
	state.sessionUpdate = &runtime.SessionUpdate{
		Ref: "dsh://" + sessionID, DisplayID: sessionID,
		Params: map[string]any{"gateway_session": sessionID},
	}
	ex.Callbacks.OnSession(*state.sessionUpdate)
	g.selectModel(ex, client, sessionID)
	return sessionID, nil
}

// selectModel 尝试按编排快照路由模型；网关 provider 词表不匹配时容忍失败
// （网关缺省路由继续），仅记日志。
func (g *Gateway) selectModel(ex *runtime.ExecContext, client *wireClient, sessionID string) {
	spec := runtime.ModelSnapshotOf(ex.Run)
	model := spec.Model
	if model == "" {
		model = g.cfg.Model
	}
	if model == "" {
		return
	}
	payload := map[string]any{"sessionId": sessionID, "model": model}
	if p := dshProviderRoute(spec.Provider); p != "" {
		payload["provider"] = p
	}
	if err := client.call(ex.Ctx, "session.selectModel", payload, nil); err != nil {
		ex.Callbacks.OnLog("dsh", "selectModel 容忍失败: "+err.Error())
	}
}

func (g *Gateway) promptTurn(ctx context.Context, client *wireClient, sessionID, instruction string) *rpcWireError {
	return client.call(ctx, "session.prompt", map[string]any{
		"sessionId": sessionID,
		"mode":      "queue",
		"content":   []map[string]any{{"type": "text", "text": instruction}},
	}, nil)
}

// pump 消费 mux 流直到本轮 turn/end；同时服务审批与 steering 控制流。
func (g *Gateway) pump(ex *runtime.ExecContext, client *wireClient, sub *muxSub, sessionID string, state *turnRun, idle time.Duration) *runtime.ExecResult {
	cb := ex.Callbacks
	approvals := map[string]*approvalWire{}
	var approvalsMu sync.Mutex
	var cancelSent atomic.Bool

	requestCancel := func() {
		if cancelSent.CompareAndSwap(false, true) {
			if err := client.call(context.WithoutCancel(ex.Ctx), "session.cancel", map[string]any{"sessionId": sessionID}, nil); err != nil {
				log.Printf("dsh: run %s session.cancel: %v", ex.Run.ID, err)
			}
		}
	}

	// 控制流协程：steering / 审批决定 / 终态意图（cancel → session.cancel）。
	controlDone := make(chan struct{})
	defer func() { close(controlDone) }()
	go func() {
		for {
			select {
			case <-ex.Ctx.Done():
				requestCancel()
				return
			case <-controlDone:
				return
			case c, ok := <-ex.Controls:
				if !ok {
					return
				}
				switch c.Kind {
				case runtime.ControlInput:
					// steering：turn 进行中方有效；失败仅记日志（尽力而为）。
					if err := client.call(context.WithoutCancel(ex.Ctx), "session.prompt", map[string]any{
						"sessionId": sessionID, "mode": "steer",
						"content": []map[string]any{{"type": "text", "text": c.Instruction}},
					}, nil); err != nil {
						log.Printf("dsh: run %s steer: %v", ex.Run.ID, err)
					}
				case runtime.ControlApproval:
					approvalsMu.Lock()
					w := approvals[c.ApprovalID]
					delete(approvals, c.ApprovalID)
					approvalsMu.Unlock()
					if w == nil {
						continue
					}
					if w.kind == "question" {
						g.respondQuestion(ex.Ctx, client, w, c.Approved)
					} else {
						g.respondApproval(ex.Ctx, client, w, c.Approved)
					}
				}
			}
		}
	}()

	idleTimer := time.NewTimer(idle)
	defer idleTimer.Stop()
	for {
		select {
		case frame, ok := <-sub.frames:
			if !ok {
				// 下行流终止：网关崩溃/网络断。若终态意图已下达按其语义返回。
				if ex.Ctx.Err() != nil {
					return ptrResult(intentResult(ex, state))
				}
				return &runtime.ExecResult{Outcome: runtime.OutcomeFailed, Session: state.sessionUpdate,
					Failure: gwFailure(runtime.FamilyIO, "mux_stream_lost", "网关事件流中断", true)}
			}
			resetTimer(idleTimer, idle)
			if frame.SessionID != "" && frame.SessionID != sessionID {
				continue // 其他会话的广播帧
			}
			switch frame.Type {
			case "session/subscribed":
				// 订阅确认（lastSeq 基线）：无动作。
			case "session/event":
				if frame.Event == nil {
					continue
				}
				if done := g.handleSessionEvent(ex, frame.Event, state); done {
					return ptrResult(turnEndResult(ex, state))
				}
			case "session/queue", "session/projection":
				// 网关侧排队/投影通知（wire 词表含这两类）：暂无 canonical 事件
				// 映射，显式记 OnLog 保持可观测，不静默吞帧。
				ex.Callbacks.OnLog("dsh", frame.Type+" "+truncate(frame.raw, 400))
			case "approval/requested":
				w := &approvalWire{rpcID: frame.rpcID, sessionID: frame.SessionID,
					approvalID: frame.ApprovalID, kind: "tool"}
				summary := frame.ToolName
				if frame.Reason != "" {
					summary += ": " + frame.Reason
				}
				// risk 固定 "high"（与 kimiapp/codexapp 一致）：dsh 网关不提供
				// 风险分级，工具审批一律按 high 走人工确认。
				engineID := cb.RequestApproval("tool", "high", summary)
				if engineID == "" {
					// 发起失败：立即拒绝，防 harness 工具悬挂。
					_ = client.respond(context.WithoutCancel(ex.Ctx), frame.rpcID, map[string]any{
						"sessionId": frame.SessionID, "approvalId": frame.ApprovalID, "outcome": "rejected",
					})
					continue
				}
				approvalsMu.Lock()
				approvals[engineID] = w
				approvalsMu.Unlock()
			case "question/requested":
				g.handleQuestion(ex, client, frame, approvals, &approvalsMu)
			case "approval/resolved":
				// harness 侧终局（allowed-once/rejected/cancelled/unavailable）：
				// canonical 事件留给 engine 审批状态机表达，这里只做观测日志。
				log.Printf("dsh: run %s 审批终局 %s=%s", ex.Run.ID, frame.ApprovalID, frame.Outcome)
			case "stream/error":
				return &runtime.ExecResult{Outcome: runtime.OutcomeFailed, Session: state.sessionUpdate,
					Failure: gwFailure(runtime.FamilyIO, "stream_error", frame.Error.Error(), true)}
			}
		case <-idleTimer.C:
			// 长时间无帧：LLM 长思考之外的最可能原因是网关僵死。
			return &runtime.ExecResult{Outcome: runtime.OutcomeTimedOut, Session: state.sessionUpdate,
				Failure: gwFailure(runtime.FamilyTimeout, "idle_timeout", "事件流空闲超时", true)}
		case <-ex.Ctx.Done():
			requestCancel()
			// 等 turn/end(aborted) 或流终止；再等最多 5s 强制收尾。
			select {
			case frame, ok := <-sub.frames:
				if ok && frame.Type == "session/event" && frame.Event != nil &&
					frame.Event.Type == "turn/end" {
					return ptrResult(turnEndResult(ex, state))
				}
			case <-time.After(5 * time.Second):
				return ptrResult(intentResult(ex, state))
			}
		}
	}
}

// handleSessionEvent 投影 SessionEvent 到 canonical 事件；返回 true 表示
// 本轮 turn/end 到达。turn/start 只在 prompt 接受后首次到达时记录编号。
func (g *Gateway) handleSessionEvent(ex *runtime.ExecContext, ev *sessionEvent, state *turnRun) bool {
	var data map[string]any
	if len(ev.Data) > 0 {
		_ = json.Unmarshal(ev.Data, &data)
	}
	switch ev.Type {
	case "turn/start":
		if n, ok := data["turn"].(float64); ok {
			if !state.activeSeen {
				state.activeSeen = true
				state.activeTurn = int64(n)
			}
		}
	case "assistant/chunk":
		// raw.chunk 结构与前端 extractDeltaChunk 契约一致。
		ex.Callbacks.OnEvent(domain.EventMessageDelta, map[string]any{"raw": data})
		// usage 仅作兜底累计：同一 turn 的权威值来自 assistant/message.usage
		// （见 turnRun 注释；两类帧同时携带 usage 时若都累加会双计）。
		var chunk struct {
			Type  string         `json:"type"`
			Usage map[string]any `json:"usage"`
		}
		if json.Unmarshal(ev.Data, &chunk) == nil && chunk.Usage != nil {
			state.usageChunkLegacy.add(chunk.Usage)
			state.usageChunkBuckets.add(chunk.Usage)
			emitUsageProgress(ex, state)
		}
	case "assistant/message":
		ex.Callbacks.OnEvent(domain.EventMessageCompleted, map[string]any{
			"role": "assistant", "text": extractText(data),
		})
		if u, ok := data["usage"].(map[string]any); ok {
			state.usageMsgSeen = true
			state.usageMsgLegacy.add(u)
			state.usageMsgBuckets.add(u)
			emitUsageProgress(ex, state)
		}
	case "tool/call":
		// canonical 契约：{tool, call_id, args_summary?, args?}（notes:
		// tool-event-canonical-contract）。arguments 为模型原始 JSON 字符串。
		payload := map[string]any{"tool": data["name"], "call_id": data["callId"]}
		if s := dshArgsSummary(data["arguments"]); s != "" {
			payload["args_summary"] = s
		}
		if a := dshArgsJSON(data["arguments"]); a != "" {
			payload["args"] = a
		}
		ex.Callbacks.OnEvent(domain.EventToolStarted, payload)
	case "tool/result":
		// canonical 契约：{call_id, output?}。callId/isError/输出均在
		// message.content[0] 的 tool-result 块内（顶层无平铺字段）。
		callID, output, isError := dshToolResult(data)
		payload := map[string]any{"call_id": callID}
		if output != "" {
			payload["output"] = output
		}
		if isError {
			ex.Callbacks.OnEvent(domain.EventToolFailed, payload)
		} else {
			ex.Callbacks.OnEvent(domain.EventToolCompleted, payload)
		}
	case "turn/end":
		// 只对本轮收尾：resume 时在途旧 turn 的 turn/end 先于本轮 turn/start
		// 到达，activeSeen=false 期间一律忽略。
		if !state.activeSeen {
			return false
		}
		if n, ok := data["turn"].(float64); !ok || int64(n) != state.activeTurn {
			return false
		}
		reason, _ := data["reason"].(map[string]any)
		if reason != nil && reason["kind"] == "error" {
			errObj, _ := reason["error"].(map[string]any)
			code, _ := errObj["code"].(string)
			msg, _ := errObj["message"].(string)
			if code == "" {
				code = "turn_error"
			}
			state.failure = classifyTurnError(code, msg)
			// 不发 run.failed 事件：终态事件由 ModuleRunner/应用层按 Failure 权威
			// 发出，adapter 侧再发一次会双发。
		}
		return true
	}
	return false
}

// handleQuestion 把 harness 提问映射为 engine 审批；approve=选首项，
// reject=空选择（模型按未作答继续）。
func (g *Gateway) handleQuestion(ex *runtime.ExecContext, client *wireClient, frame muxFrame, approvals map[string]*approvalWire, mu *sync.Mutex) {
	if len(frame.Questions) == 0 {
		return
	}
	q := frame.Questions[0]
	w := &approvalWire{rpcID: frame.rpcID, sessionID: frame.SessionID, kind: "question", question: &q}
	summary := truncate(q.Question, 160)
	engineID := ex.Callbacks.RequestApproval("question", "ask_user", summary)
	if engineID == "" {
		g.respondQuestion(ex.Ctx, client, w, false)
		return
	}
	mu.Lock()
	approvals[engineID] = w
	mu.Unlock()
}

func (g *Gateway) respondApproval(ctx context.Context, client *wireClient, w *approvalWire, approved bool) {
	outcome := "rejected"
	if approved {
		outcome = "allowed-once"
	}
	if err := client.respond(context.WithoutCancel(ctx), w.rpcID, map[string]any{
		"sessionId": w.sessionID, "approvalId": w.approvalID, "outcome": outcome,
	}); err != nil {
		log.Printf("dsh: 审批回应失败: %v", err)
	}
}

func (g *Gateway) respondQuestion(ctx context.Context, client *wireClient, w *approvalWire, approved bool) {
	answer := map[string]any{
		"sessionId": w.sessionID,
		"answer":    map[string]any{"answers": []map[string]any{}},
	}
	if approved && w.question != nil && len(w.question.Options) > 0 {
		answer["answer"] = map[string]any{"answers": []map[string]any{{
			"id": w.question.ID, "selected": []string{w.question.Options[0].Label},
		}}}
	}
	if err := client.respond(context.WithoutCancel(ctx), w.rpcID, answer); err != nil {
		log.Printf("dsh: 提问回应失败: %v", err)
	}
}

// ptrResult 值结果取址（pump 返回 *ExecResult 以允许 nil 中途退出）。
func ptrResult(r runtime.ExecResult) *runtime.ExecResult { return &r }

// turnEndResult turn/end 后的统一收尾：usage（per_run 增量，message.usage
// 权威、chunk 兜底的去重口径）+ 会话句柄。
func turnEndResult(ex *runtime.ExecContext, state *turnRun) runtime.ExecResult {
	result := runtime.ExecResult{Session: state.sessionUpdate}
	in, out, cached := state.usageTotals()
	if _, _, seen := state.usageBuckets(); seen {
		usage := runtime.Usage{
			InputTokens: in, OutputTokens: out,
			CachedTokens: cached, Basis: runtime.UsagePerRun,
		}
		if buckets, source, ok := state.usageBuckets(); ok {
			if report, err := runtime.NewProviderUsageReport(ex, sessionRef(state.sessionUpdate),
				"dsh", "dsh-web-gateway", "1", source,
				"inputTokens/cacheReadTokens/outputTokens; input_total=sum(input+cache_read+cache_write) only when all three known",
				runtime.UsagePerRun, buckets.counters()); err == nil {
				usage = runtime.AttachProviderUsage(usage, report)
			}
		}
		result.Usage = &usage
	}
	switch {
	case ex.Ctx.Err() != nil:
		return intentResult(ex, state)
	case state.failure != nil:
		result.Outcome = runtime.OutcomeFailed
		result.Failure = state.failure
	default:
		result.Outcome = runtime.OutcomeSucceeded
	}
	return result
}

// intentResult Ctx 已取消且未见 turn/end：按终态意图（cancel/interrupt）返回。
func intentResult(ex *runtime.ExecContext, state *turnRun) runtime.ExecResult {
	outcome := runtime.OutcomeInterrupted
	if kind, ok := ex.TerminalIntent(); ok && kind == runtime.ControlCancel {
		outcome = runtime.OutcomeCancelled
	}
	result := runtime.ExecResult{Outcome: outcome, Session: state.sessionUpdate}
	in, out, cached := state.usageTotals()
	if buckets, source, ok := state.usageBuckets(); ok {
		usage := runtime.Usage{InputTokens: in, OutputTokens: out, CachedTokens: cached, Basis: runtime.UsagePerRun}
		if report, err := runtime.NewProviderUsageReport(ex, sessionRef(state.sessionUpdate),
			"dsh", "dsh-web-gateway", "1", source,
			"inputTokens/cacheReadTokens/outputTokens; input_total=sum(input+cache_read), cache_write=unknown",
			runtime.UsagePerRun, buckets.counters()); err == nil {
			usage = runtime.AttachProviderUsage(usage, report)
		}
		result.Usage = &usage
	}
	return result
}

// ── 错误分类 ──────────────────────────────────────────────────────────

// wireFailure 网关 RpcError → Failure；session 类错误可触发上层自愈。
func wireFailure(e *rpcWireError) *runtime.Failure {
	switch e.Code {
	case "session-not-found", "session-conflict", "subagent-not-resumable":
		return gwFailure(runtime.FamilySessionUnknown, e.Code, e.Message, false)
	case "model-unavailable", "agent-preset-not-found", "agent-preset-invalid", "settings-rejected", "credential-rejected":
		return gwFailure(runtime.FamilyConfig, e.Code, e.Message, false)
	case "agent-busy":
		return gwFailure(runtime.FamilyIO, e.Code, e.Message, true)
	case "carrier_unreachable", "carrier_bad_response", "carrier_bad_result":
		return gwFailure(runtime.FamilyIO, e.Code, e.Message, true)
	default:
		return gwFailure(runtime.FamilyInternal, e.Code, e.Message, false)
	}
}

// classifyTurnError turn/end(reason=error) 的 provider 侧错误分类。
func classifyTurnError(code, msg string) *runtime.Failure {
	low := strings.ToLower(code + " " + msg)
	family := runtime.FamilyTransientUpstream
	retryable := true
	switch {
	case strings.Contains(low, "quota") || strings.Contains(low, "429") || strings.Contains(low, "rate limit"):
		family = runtime.FamilyProviderQuota
	case strings.Contains(low, "401") || strings.Contains(low, "403") || strings.Contains(low, "auth") || strings.Contains(low, "api key"):
		family, retryable = runtime.FamilyConfig, false
	case strings.Contains(low, "insufficient") || strings.Contains(low, "balance"):
		family, retryable = runtime.FamilyProviderQuota, false
	}
	return gwFailure(family, code, msg, retryable)
}

func gwFailure(family runtime.ErrorFamily, code, message string, retryable bool) *runtime.Failure {
	return &runtime.Failure{Family: family, Code: code, Message: truncate(message, 200), Retryable: retryable}
}

// ── 杂项 ──────────────────────────────────────────────────────────────

// personaOf 编排快照 → session.create persona（创建期固化，resume 不重传）。
func personaOf(ex *runtime.ExecContext) string {
	persona := strings.TrimSpace(runtime.SystemPromptOf(ex.Run))
	policy := runtime.PolicySnapshotOf(ex.Run)
	if policy.Mode == "plan" {
		persona = strings.TrimSpace(persona + "\n\nPlan mode: analyze and produce a plan only; do not modify workspace files.")
	}
	return persona
}

// extractText assistant/message 的 message.content 文本块拼接（前端气泡用）。
func extractText(data map[string]any) string {
	msg, _ := data["message"].(map[string]any)
	if msg == nil {
		return ""
	}
	blocks, _ := msg["content"].([]any)
	var sb strings.Builder
	for _, b := range blocks {
		if m, ok := b.(map[string]any); ok && m["type"] == "text" {
			if t, ok := m["text"].(string); ok {
				sb.WriteString(t)
			}
		}
	}
	return sb.String()
}

// 工具事件载荷截断上限（canonical 契约，与 kimiapp/codexapp 一致）：
// run_events 持久化 + SSE 的消费面不允许无界透传原始工具 IO。
const (
	maxToolArgsSummary = 200
	maxToolArgs        = 2000
	maxToolOutput      = 2000
)

// dshArgsSummary 从 tool/call.arguments（模型原始 JSON 字符串）提取一行输入
// 摘要：命令/路径类参数优先，其次原串截断（≤maxToolArgsSummary）。
func dshArgsSummary(args any) string {
	s, _ := args.(string)
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var m map[string]any
	if json.Unmarshal([]byte(s), &m) == nil {
		for _, key := range []string{"command", "cmd", "path", "file_path", "query", "url"} {
			if v, _ := m[key].(string); strings.TrimSpace(v) != "" {
				return truncate(v, maxToolArgsSummary)
			}
		}
	}
	return truncate(s, maxToolArgsSummary)
}

// dshArgsJSON 完整入参的紧凑 JSON 文本（截断 ≤maxToolArgs），供前端工具行
// IN/OUT 展开卡；与 dshArgsSummary 同源（tool/call.arguments 的模型原始
// JSON 字符串），仅非空且为 JSON 对象/数组时携带，原文透传不重新 marshal；
// 截断发生在字符串层，不保证截断后仍是合法 JSON（前端只展示，不解析）。
func dshArgsJSON(args any) string {
	s, _ := args.(string)
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return ""
	}
	switch v.(type) {
	case map[string]any, []any:
		return truncate(s, maxToolArgs)
	}
	return ""
}

// dshToolResult 从 tool/result 帧提取折叠三元组：callId（tool-result 块的
// toolCallId，回退 message.source.callId）、isError（块标记）、输出文本
// （块 content 内 text 块拼接，截断 ≤maxToolOutput；无文本块则省略）。
func dshToolResult(data map[string]any) (callID, output string, isError bool) {
	msg, _ := data["message"].(map[string]any)
	if msg == nil {
		return "", "", false
	}
	blocks, _ := msg["content"].([]any)
	var sb strings.Builder
	for _, b := range blocks {
		block, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if callID == "" {
			if id, _ := block["toolCallId"].(string); id != "" {
				callID = id
			}
		}
		if e, _ := block["isError"].(bool); e {
			isError = true
		}
		parts, _ := block["content"].([]any)
		for _, p := range parts {
			if t, ok := p.(map[string]any); ok && t["type"] == "text" {
				if text, _ := t["text"].(string); text != "" {
					sb.WriteString(text)
				}
			}
		}
	}
	if callID == "" {
		src, _ := msg["source"].(map[string]any)
		callID, _ = src["callId"].(string)
	}
	return callID, truncate(sb.String(), maxToolOutput), isError
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = s[:n]
	}
	return s
}

func resetTimer(t *time.Timer, d time.Duration) {
	t.Stop()
	t.Reset(d)
}

// kimiapp.go — Kimi Code app-server Adapter（Adapter SPI v2 网关形态，镜像 dsh）。
//
// 形态：与长驻 kap-server（`kimi web`）交互——首 turn POST /sessions，后续
// turn 直接向同会话 POST /prompts（ISessionManager.resume 原生续会）；事件从
// /api/v1/ws 订阅流下行（帧 type=事件名）并映射 canonical；审批经
// /sessions/{sid}/approvals/{aid} 决议；steering 先 POST prompts 再
// prompts::steer 升级；取消走 REST abort（WS 侧不处理 abort 帧）。
// resume ref 携带但服务端会话缺失时返回 Failure{Family: session_unknown}
// （不静默降级 fresh：resume 轮 EffectiveInstruction 只含当轮输入，fresh 会话
// 收不到历史=失忆），由应用层自愈链路清锚点并以全量历史 fresh 重建。
package kimiapp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ybs/agent-team-workbench/internal/agentwork"
	"github.com/ybs/agent-team-workbench/internal/agentwork/kimiconfig"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/filechanges"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// refScheme 会话 ref 方案：kimiapp://<session_id>。
const refScheme = "kimiapp"

// Config 网关形态配置。BaseURL 非空时直连外部 kap-server（supervisor 只探活，
// Token 必填）；否则受管拉起 `kimi web`（Home=KIMI_CODE_HOME 必填，Port=0
// 时内核分配空闲口）。
type Config struct {
	BaseURL     string        // 直连已运行 kap-server，如 http://127.0.0.1:58627
	Token       string        // 直连模式 Bearer token
	Host        string        // 受管模式绑定 host（默认 127.0.0.1）
	Port        int           // 受管模式端口（0=动态空闲口；显式值用于复用探测）
	KimiBin     string        // kimi 可执行文件（默认 kimi）
	BinArgs     []string      // 覆盖启动参数（测试回放桩，对齐 dsh BinArgs）
	Home        string        // KIMI_CODE_HOME（受管模式必填；token 于 <home>/server.token）
	Model       string        // 缺省模型（prompt 级前向）
	IdleTimeout time.Duration // 事件流空闲保护（默认 10m）
}

// Module 实现 runtime.AdapterModule。
type Module struct {
	cfg Config
	sup *Supervisor
}

var _ runtime.AdapterModule = (*Module)(nil)

// New 创建 kimiapp 模块；进程拉起延迟到首次 Probe/Execute。
func New(cfg Config) *Module {
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 10 * time.Minute
	}
	return &Module{cfg: cfg, sup: newSupervisor(cfg)}
}

func (m *Module) Manifest(ctx context.Context) (runtime.AdapterManifest, error) {
	return runtime.AdapterManifest{
		AdapterID: "kimi-appserver", AdapterVersion: "1.0.0",
		Protocol: runtime.Protocol{Name: "kimi-kap-server", Version: "2"},
		Capabilities: map[string]runtime.CapabilityLevel{
			"streaming":                               runtime.CapSupported, // assistant.delta / thinking.delta
			runtime.CapabilityStructuredTransport:     runtime.CapSupported,
			runtime.CapabilitySchemaConstrainedOutput: runtime.CapUnavailable,
			runtime.CapabilityControlToolCall:         runtime.CapUnavailable,
			"multi_turn":                              runtime.CapSupported,
			"resume":                                  runtime.CapSupported, // 会话原生续会 + 40401 探测
			"steering":                                runtime.CapSupported, // prompts + prompts::steer
			"approval":                                runtime.CapSupported, // event.approval.requested + approvals REST
			"subagents":                               runtime.CapSupported, // Kimi AgentSwarm 与普通 Agent 子 Agent
			"swarm":                                   runtime.CapSupported, // session profile swarm_mode + AgentSwarm
			// REST abort（WS 无 abort 帧）：turn 级精确取消，非进程级。
			"interrupt":       runtime.CapSupported,
			"workspace_files": runtime.CapSupported,
			// kap 的 profile 负责权限与蜂巢模式；persona 仍由适配器注入
			// fresh 会话的首个 prompt。
			"system_prompt": runtime.CapAdapterTranslated,
			// prompt.plan_mode 服务端接受但不应用：plan 语义经 prompt 文本注入。
			"modes":             runtime.CapAdapterTranslated,
			"permissions":       runtime.CapAdapterTranslated, // prompt.permission_mode 三档映射
			"multi_vendor":      runtime.CapAdapterTranslated, // 服务端 provider 配置决定
			"structured_output": runtime.CapAdapterTranslated,
			"terminal":          runtime.CapUnavailable,
		},
		SchemaDigest: "sha256:kimi-kap-server-v2",
	}, nil
}

// Probe 校验 kimi CLI 可用；不在此阶段拉起 kap-server（避免就绪横幅 URL 触发 IDE 跳转）。
func (m *Module) Probe(ctx context.Context, req runtime.ProbeRequest) (runtime.ProbeResult, error) {
	mf, _ := m.Manifest(ctx)
	if m.cfg.BaseURL != "" {
		if _, err := m.sup.Ensure(ctx); err != nil {
			return runtime.ProbeResult{OK: false, Manifest: &mf, Error: err.Error()}, nil
		}
		return runtime.ProbeResult{OK: true, Manifest: &mf}, nil
	}
	bin := m.cfg.KimiBin
	if bin == "" {
		bin = "kimi"
	}
	if !agentwork.ExecutableOK(bin) {
		return runtime.ProbeResult{OK: false, Manifest: &mf, Error: "kimi CLI 不可用"}, nil
	}
	return runtime.ProbeResult{OK: true, Manifest: &mf}, nil
}

// Close 透传 supervisor 关停（宿主退出时回收 kap-server 进程组）。
func (m *Module) Close() { m.sup.Close() }

// turnState 一次 Execute 的可变状态（事件循环协程写，收尾读）。
type turnState struct {
	promptID             string // 本轮提交的 prompt_id（abort 目标）
	activeTurn           int64  // 本 prompt 触发的 turn 编号；0=尚未开始
	activeSeen           bool
	answer               strings.Builder // assistant.delta 累计（合成 message.completed）
	usageIn              int64           // legacy total（inputOther+cacheRead+cacheCreation）
	usageOut             int64
	usageCached          int64 // legacy cache-read projection
	usageUncached        int64
	usageCacheRead       int64
	usageCacheWrite      int64
	usageUncachedKnown   bool
	usageOutputKnown     bool
	usageCacheReadKnown  bool
	usageCacheWriteKnown bool
	usageSeen            bool
	endReason            string           // turn.ended.reason
	failure              *runtime.Failure // turn.ended.error 权威失败
	sessionUpdate        *runtime.SessionUpdate
	// pendingTools tracks only identified tool calls. KAP can emit turn.ended
	// before a result; those calls are closed synthetically at turn end.
	pendingTools map[string]string
	// toolResults makes tool.result terminal delivery idempotent. An empty ID
	// cannot be correlated safely and is intentionally not tracked here.
	toolResults   map[string]struct{}
	fileSnapshots map[string]fileSnapshot
	subagents     map[string]map[string]any
	subagentSeqs  map[int64]struct{}
	children      map[string]*childTurnState
}

type childTurnState struct {
	activeTurn  int64
	activeSeen  bool
	answer      strings.Builder
	usageIn     int64
	usageOut    int64
	usageCached int64
	pending     map[string]string
	results     map[string]struct{}
}

func tokenUsageValue(value *int64) (int64, bool) {
	if value == nil || *value < 0 {
		return 0, false
	}
	return *value, true
}

func addUsageValue(current, value int64) (int64, bool) {
	next, err := domain.CheckedAddNonNegative(current, value)
	return next, err == nil
}

func (s *turnState) addUsage(u *tokenUsage) {
	if u == nil {
		return
	}
	other, otherKnown := tokenUsageValue(u.InputOther)
	read, readKnown := tokenUsageValue(u.InputCacheRead)
	write, writeKnown := tokenUsageValue(u.InputCacheCreation)
	out, outputKnown := tokenUsageValue(u.Output)

	inputKnown := otherKnown && readKnown && writeKnown
	if inputKnown {
		input, ok := addUsageValue(other, read)
		if ok {
			input, ok = addUsageValue(input, write)
			if ok {
				if next, sumOK := addUsageValue(s.usageIn, input); sumOK {
					s.usageIn = next
				} else {
					inputKnown = false
				}
			} else {
				inputKnown = false
			}
		} else {
			inputKnown = false
		}
	} else {
		// Preserve legacy best-effort totals for fields that are present while
		// marking the canonical total unresolved until all dimensions are known.
		for _, value := range []struct {
			value int64
			known bool
			label string
		}{
			{other, otherKnown, "input_other"},
			{read, readKnown, "input_cache_read"},
			{write, writeKnown, "input_cache_creation"},
		} {
			if value.known {
				if next, ok := addUsageValue(s.usageIn, value.value); ok {
					s.usageIn = next
				}
			}
		}
	}
	if outputKnown {
		if next, ok := addUsageValue(s.usageOut, out); ok {
			s.usageOut = next
		} else {
			outputKnown = false
		}
	}
	if readKnown {
		if next, ok := addUsageValue(s.usageCached, read); ok {
			s.usageCached = next
		} else {
			readKnown = false
		}
	}
	if otherKnown {
		if next, ok := addUsageValue(s.usageUncached, other); !ok {
			otherKnown = false
		} else {
			s.usageUncached = next
		}
	}
	if readKnown {
		if next, ok := addUsageValue(s.usageCacheRead, read); !ok {
			readKnown = false
		} else {
			s.usageCacheRead = next
		}
	}
	if writeKnown {
		if next, ok := addUsageValue(s.usageCacheWrite, write); !ok {
			writeKnown = false
		} else {
			s.usageCacheWrite = next
		}
	}
	if !s.usageSeen {
		s.usageUncachedKnown = otherKnown
		s.usageOutputKnown = outputKnown
		s.usageCacheReadKnown = readKnown
		s.usageCacheWriteKnown = writeKnown
	} else {
		s.usageUncachedKnown = s.usageUncachedKnown && otherKnown
		s.usageOutputKnown = s.usageOutputKnown && outputKnown
		s.usageCacheReadKnown = s.usageCacheReadKnown && readKnown
		s.usageCacheWriteKnown = s.usageCacheWriteKnown && writeKnown
	}
	s.usageSeen = true
}

func (s *turnState) usageCounters() domain.UsageCountersV1 {
	counters := domain.UsageCountersV1{
		InputUncachedTokens: kimiUsageCounter(s.usageUncached, s.usageUncachedKnown),
		CacheReadTokens:     kimiUsageCounter(s.usageCacheRead, s.usageCacheReadKnown),
		CacheWriteTokens:    kimiUsageCounter(s.usageCacheWrite, s.usageCacheWriteKnown),
		OutputTokens:        kimiUsageCounter(s.usageOut, s.usageOutputKnown),
	}
	if counters.InputUncachedTokens != nil && counters.CacheReadTokens != nil && counters.CacheWriteTokens != nil {
		if total, ok := addUsageValue(*counters.InputUncachedTokens, *counters.CacheReadTokens); ok {
			if total, ok = addUsageValue(total, *counters.CacheWriteTokens); ok {
				counters.InputTokensTotal = &total
			}
		}
	}
	return counters
}

func kimiUsageCounter(value int64, known bool) *int64 {
	if !known {
		return nil
	}
	copy := value
	return &copy
}

func (s *turnState) usageSnapshot(ex *runtime.ExecContext) *runtime.Usage {
	if !s.usageSeen {
		return nil
	}
	usage := runtime.Usage{
		InputTokens: s.usageIn, OutputTokens: s.usageOut,
		CachedTokens: s.usageCached, Basis: runtime.UsagePerRun,
	}
	sessionRef := ""
	if s.sessionUpdate != nil {
		sessionRef = s.sessionUpdate.Ref
	}
	report, err := runtime.NewProviderUsageReport(ex, sessionRef,
		"kimi-appserver", "kimi-kap-server", "2", "turn.step.completed",
		"inputOther/inputCacheRead/inputCacheCreation/output; input_total=sum(input buckets)",
		runtime.UsagePerRun, s.usageCounters())
	if err == nil {
		usage = runtime.AttachProviderUsage(usage, report)
	}
	return &usage
}

type fileSnapshot struct {
	Path         string
	Root         string
	RelPath      string
	Before       string
	BeforeExists bool
	BeforeHash   string
}

// shouldApplyKimiModelSnapshot leaves an empty model to the signed-in Kimi
// runtime. The built-in kimi_local binding identifies the provider as "kimi"
// even when the user selected "follow runtime default"; forcing that
// incomplete snapshot through kimiconfig would block every new Task.
func shouldApplyKimiModelSnapshot(snap runtime.ModelSnapshot) bool {
	if strings.TrimSpace(snap.Model) != "" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(snap.Provider)) {
	case "", "kimi":
		return false
	default:
		return true
	}
}

// Execute 阻塞执行一轮：确保 kap-server → 解析/创建会话 → WS 订阅 → prompt →
// 事件泵推进到本 turn 的 turn.ended → 结构化返回。
func (m *Module) Execute(ex *runtime.ExecContext) runtime.ExecResult {
	if strings.TrimSpace(ex.Instruction) == "" {
		return runtime.ExecResult{Outcome: runtime.OutcomeFailed,
			Failure: modFailure(runtime.FamilyConfig, "instruction_required", "instruction required", false)}
	}
	snap := runtime.ModelSnapshotOf(ex.Run)
	if shouldApplyKimiModelSnapshot(snap) {
		changed, err := kimiconfig.ApplySnapshotIfChanged(m.cfg.Home, snap)
		if err != nil {
			return runtime.ExecResult{Outcome: runtime.OutcomeFailed,
				Failure: modFailure(runtime.FamilyConfig, "kimi_config", err.Error(), false)}
		}
		if changed {
			m.sup.recycle()
		}
	}
	state := &turnState{pendingTools: make(map[string]string), toolResults: make(map[string]struct{}), fileSnapshots: make(map[string]fileSnapshot), subagents: make(map[string]map[string]any), subagentSeqs: make(map[int64]struct{}), children: make(map[string]*childTurnState)}
	// Ctx 取消（cancel/interrupt）不是网关故障：任何阶段的取消按终态意图返回，
	// 不得误报成 gateway_unavailable/io 失败（否则终态会变成 failed）。
	ep, err := m.sup.Ensure(ex.Ctx)
	if err != nil {
		if ex.Ctx.Err() != nil {
			return intentResult(ex, state)
		}
		return runtime.ExecResult{Outcome: runtime.OutcomeFailed,
			Failure: modFailure(runtime.FamilyConfig, "gateway_unavailable", err.Error(), false)}
	}
	client := newRestClient(ep.BaseURL, ep.Token)

	sessionID, fresh, res := m.resolveSession(ex, client, state)
	if res != nil {
		if ex.Ctx.Err() != nil {
			return intentResult(ex, state)
		}
		return *res
	}

	// 订阅先于 prompt：会话事件按序送达，提交后的帧不会丢。冷启动/重启窗口
	// 内升级握手可能被断连：短暂重试（镜像 dsh 的 5×500ms）。
	stream, res := m.openStream(ex, ep, sessionID)
	if res != nil {
		if ex.Ctx.Err() != nil {
			return intentResult(ex, state)
		}
		return *res
	}
	if stream == nil { // 订阅失败源于 Ctx 取消
		return intentResult(ex, state)
	}
	defer stream.close()

	prompt, res := m.submitPrompt(ex, client, sessionID, fresh)
	if res != nil {
		if ex.Ctx.Err() != nil {
			// 取消可能落在 prompt 提交在途窗口：请求或已被服务端受理（无
			// prompt_id 可精确 abort），兜底会话级 abort 防 turn 悬挂后按意图返回。
			if kerr := client.abortSession(context.WithoutCancel(ex.Ctx), sessionID); kerr != nil {
				log.Printf("kimiapp: run %s 提交在途取消的会话兜底 abort: %v", ex.Run.ID, kerr)
			}
			return intentResult(ex, state)
		}
		return *res
	}
	state.promptID = prompt.PromptID // 先于泵启动写入，requestCancel 只读
	return m.pump(ex, client, ep, stream, sessionID, state)
}

// resolveSession 返回可用的 kap 会话 id 与是否 fresh 创建（fresh 决定
// persona 是否随首个 prompt 注入）。resume 铁律：ResumeID 非空时先
// GET /sessions/{id} 探测；40401 → Failure{Family: session_unknown,
// Retryable: false}，绝不静默降级 fresh；探测的传输层失败（网络错/5xx）保持
// transient/io 分类，不误报会话丢失。
func (m *Module) resolveSession(ex *runtime.ExecContext, client *restClient, state *turnState) (string, bool, *runtime.ExecResult) {
	if resumeID := runtime.SessionIDFromRef(ex.Session.Ref, refScheme); resumeID != "" {
		if _, kerr := client.getSession(ex.Ctx, resumeID); kerr != nil {
			f := kapFailure(kerr)
			if f.Family == runtime.FamilySessionUnknown {
				f.Code = "resume_" + f.Code
			}
			return "", false, &runtime.ExecResult{Outcome: runtime.OutcomeFailed, Failure: f}
		}
		if failure := applySessionDefaults(ex.Ctx, client, resumeID, defaultAgentConfig(ex)); failure != nil {
			return "", false, &runtime.ExecResult{Outcome: runtime.OutcomeFailed, Failure: failure}
		}
		// resume 命中：沿用原 ref 重报 SessionUpdate（runs_count 幂等语义：
		// 每个新 run 重报同 ref，是会话轮换计数增长的必要输入）。会话创建即报
		// （若后续 turn 崩溃，resume 锚点已固化，下轮仍可续）。
		state.sessionUpdate = &runtime.SessionUpdate{
			Ref: refScheme + "://" + resumeID, DisplayID: resumeID,
			Params: map[string]any{"kap_session": resumeID},
		}
		ex.Callbacks.OnSession(*state.sessionUpdate)
		return resumeID, false, nil
	}
	// fresh：create.agent_config 在 main 与 0.38 都是静默 no-op；这里只创建，
	// 随后统一走 profile + status 的可验证默认值路径。
	// 会话 metadata.cwd 只来自 Host resolver 的进程内可信产物（RFC §5.1.9）；
	// 无 Resolved（未注入 resolver 的测试装配）回退进程 cwd。
	sessionCWD := ex.Resolved.CWD
	if sessionCWD == "" {
		sessionCWD = "."
	}
	req := &createSessionRequest{Metadata: map[string]string{"cwd": sessionCWD}}
	created, kerr := client.createSession(ex.Ctx, req)
	if kerr != nil {
		return "", false, &runtime.ExecResult{Outcome: runtime.OutcomeFailed, Failure: kapFailure(kerr)}
	}
	sessionID := created.ID
	state.sessionUpdate = &runtime.SessionUpdate{
		Ref: refScheme + "://" + sessionID, DisplayID: sessionID,
		Params: map[string]any{"kap_session": sessionID},
	}
	ex.Callbacks.OnSession(*state.sessionUpdate)
	if failure := applySessionDefaults(ex.Ctx, client, sessionID, defaultAgentConfig(ex)); failure != nil {
		return "", false, &runtime.ExecResult{
			Outcome: runtime.OutcomeFailed, Failure: failure, Session: state.sessionUpdate,
		}
	}
	return sessionID, true, nil
}

// openStream 建立事件流并完成订阅；订阅 not_found（会话在探测后被删）按
// session_unknown 处理。
func (m *Module) openStream(ex *runtime.ExecContext, ep *endpoint, sessionID string) (*wsStream, *runtime.ExecResult) {
	var stream *wsStream
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		s, err := dialEvents(ex.Ctx, ep.BaseURL, ep.Token)
		if err == nil {
			ack, serr := s.subscribe(ex.Ctx, sessionID, nil)
			if serr == nil {
				if containsStr(ack.NotFound, sessionID) {
					s.close()
					return nil, &runtime.ExecResult{Outcome: runtime.OutcomeFailed, Failure: modFailure(
						runtime.FamilySessionUnknown, "session_not_found", "subscribe not_found", false)}
				}
				stream = s
				break
			}
			s.close()
			lastErr = serr
		} else {
			lastErr = err
		}
		if ex.Ctx.Err() != nil {
			break
		}
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ex.Ctx.Done():
		}
	}
	if stream == nil {
		if ex.Ctx.Err() != nil {
			return nil, nil // 调用方按终态意图处理
		}
		return nil, &runtime.ExecResult{Outcome: runtime.OutcomeFailed, Failure: modFailure(
			runtime.FamilyIO, "ws_subscribe_failed", lastErr.Error(), true)}
	}
	return stream, nil
}

// submitPrompt 提交本轮 prompt；model/permission_mode 为 prompt 级前向字段
// （resume 轮同样生效）。persona 只在 fresh 会话的首个 prompt 注入（kap 无
// system_prompt 应用通道；resume 轮会话上下文已含首轮注入）；plan 指令每个
// plan 模式 prompt 都带（prompt.plan_mode 服务端不应用，只能文本注入）。
func (m *Module) submitPrompt(ex *runtime.ExecContext, client *restClient, sessionID string, fresh bool) (*promptSubmitResult, *runtime.ExecResult) {
	text := ex.Instruction
	if fresh {
		if persona := personaOf(ex); persona != "" {
			text = "[本会话的角色与行为设定，请在本次及后续对话中始终遵循]\n" + persona + "\n\n" + text
		}
	}
	policy := runtime.PolicySnapshotOf(ex.Run)
	if policy.Mode == "plan" {
		text += "\n\n" + planDirective
	}
	req := &promptSubmitRequest{
		Content: []promptContentPart{{Type: "text", Text: text}},
	}
	if model := m.modelOf(ex); model != "" {
		req.Model = model
	}
	req.PermissionMode = permissionMode(policy)
	pr, kerr := client.submitPrompt(ex.Ctx, sessionID, req)
	if kerr != nil {
		return nil, &runtime.ExecResult{Outcome: runtime.OutcomeFailed, Failure: kapFailure(kerr)}
	}
	if pr.Status == "blocked" {
		return nil, &runtime.ExecResult{Outcome: runtime.OutcomeFailed, Failure: modFailure(
			runtime.FamilyConfig, "prompt_blocked", "prompt submission blocked", false)}
	}
	return pr, nil
}

// pump 消费事件流直到本轮 turn.ended；同时服务审批与 steering 控制流。
// 流中断时带 cursor（最近 durable seq）重连重订阅（≤3 次），volatile 帧
// 允许丢失（增量语义）。
func (m *Module) pump(ex *runtime.ExecContext, client *restClient, ep *endpoint, stream *wsStream, sessionID string, state *turnState) runtime.ExecResult {
	approvals := map[string]string{} // engine 审批 id → kap approval_id
	var approvalsMu sync.Mutex
	var cancelSent atomic.Bool

	// 取消只走 REST（WS 侧 switch 不处理 abort）：优先本 prompt 精确 abort，
	// 无 prompt_id 时兜底会话级 abort；40903/40402 幂等。
	requestCancel := func() {
		if !cancelSent.CompareAndSwap(false, true) {
			return
		}
		ctx := context.WithoutCancel(ex.Ctx)
		if state.promptID != "" {
			if kerr := client.abortPrompt(ctx, sessionID, state.promptID); kerr != nil {
				log.Printf("kimiapp: run %s prompts/%s:abort: %v", ex.Run.ID, state.promptID, kerr)
			}
			return
		}
		if kerr := client.abortSession(ctx, sessionID); kerr != nil {
			log.Printf("kimiapp: run %s session abort: %v", ex.Run.ID, kerr)
		}
	}

	// 控制流协程：steering（先提交新 prompt 再 ::steer 升级）/ 审批决定 /
	// 终态意图（cancel → REST abort）。
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
					wctx := context.WithoutCancel(ex.Ctx)
					policy := runtime.PolicySnapshotOf(ex.Run)
					pr, kerr := client.submitPrompt(wctx, sessionID, &promptSubmitRequest{
						Content:        []promptContentPart{{Type: "text", Text: c.Instruction}},
						PermissionMode: permissionMode(policy),
					})
					if kerr != nil {
						log.Printf("kimiapp: run %s steer submit: %v", ex.Run.ID, kerr)
						continue
					}
					if kerr := client.steer(wctx, sessionID, []string{pr.PromptID}); kerr != nil {
						log.Printf("kimiapp: run %s prompts::steer: %v", ex.Run.ID, kerr)
					}
				case runtime.ControlApproval:
					approvalsMu.Lock()
					wireID := approvals[c.ApprovalID]
					delete(approvals, c.ApprovalID)
					approvalsMu.Unlock()
					if wireID == "" {
						continue
					}
					decision := "rejected"
					if c.Approved {
						decision = "approved"
					}
					if kerr := client.resolveApproval(context.WithoutCancel(ex.Ctx), sessionID, wireID, decision, ""); kerr != nil {
						log.Printf("kimiapp: run %s approval resolve: %v", ex.Run.ID, kerr)
					}
				}
			}
		}
	}()

	p := &eventPump{m: m, ex: ex, client: client, sessionID: sessionID, state: state,
		approvals: approvals, approvalsMu: &approvalsMu}
	// 订阅握手期间缓存的先到帧（正常为空）先消费。
	if done := p.drain(stream); done {
		return turnEndResult(ex, state)
	}

	idle := m.cfg.IdleTimeout
	if idle <= 0 {
		idle = 10 * time.Minute
	}
	idleTimer := time.NewTimer(idle)
	defer idleTimer.Stop()
	var lastSeq int64
	var epoch string
	reattempts := 0
	for {
		select {
		case frame, ok := <-stream.frames:
			if !ok {
				// 下行流终止：kap-server 重启/网络断。终态意图优先；否则带
				// cursor 重连（≤3 次），仍失败按 io 可重试失败返回。
				if ex.Ctx.Err() != nil {
					return intentResult(ex, state)
				}
				if reattempts >= 3 {
					return runtime.ExecResult{Outcome: runtime.OutcomeFailed, Session: state.sessionUpdate,
						Failure: modFailure(runtime.FamilyIO, "ws_stream_lost", "事件流中断", true)}
				}
				reattempts++
				stream.close()
				select {
				case <-time.After(500 * time.Millisecond):
				case <-ex.Ctx.Done():
					return intentResult(ex, state)
				}
				s2, err := dialEvents(ex.Ctx, ep.BaseURL, ep.Token)
				if err != nil {
					continue
				}
				cursor := &sessionCursor{Seq: lastSeq, Epoch: epoch}
				ack, serr := s2.subscribe(ex.Ctx, sessionID, cursor)
				if serr != nil || containsStr(ack.NotFound, sessionID) {
					s2.close()
					continue
				}
				stream = s2
				if done := p.drain(stream); done {
					return turnEndResult(ex, state)
				}
				continue
			}
			resetTimer(idleTimer, idle)
			if !frame.Volatile && frame.Seq > 0 {
				lastSeq, epoch = frame.Seq, frame.Epoch
			}
			// 只消费本会话帧（跨会话/全局广播帧跳过）。
			if frame.SessionID != "" && frame.SessionID != sessionID {
				continue
			}
			if done := p.handle(frame); done {
				return turnEndResult(ex, state)
			}
		case <-idleTimer.C:
			// 长时间无帧：LLM 长思考之外的最可能原因是网关僵死。
			return runtime.ExecResult{Outcome: runtime.OutcomeTimedOut, Session: state.sessionUpdate,
				Failure: modFailure(runtime.FamilyTimeout, "idle_timeout", "事件流空闲超时", true)}
		case <-ex.Ctx.Done():
			requestCancel()
			// 等 turn.ended(cancelled) 或流终止；再等最多 5s 强制收尾。
			forceTimer := time.NewTimer(5 * time.Second)
			defer forceTimer.Stop()
			for {
				select {
				case frame, ok := <-stream.frames:
					if !ok {
						return intentResult(ex, state)
					}
					if done := p.handle(frame); done {
						return turnEndResult(ex, state)
					}
				case <-forceTimer.C:
					return intentResult(ex, state)
				}
			}
		}
	}
}

// eventPump 收敛事件帧处理所需的执行上下文。
type eventPump struct {
	m           *Module
	ex          *runtime.ExecContext
	client      *restClient
	sessionID   string
	state       *turnState
	approvals   map[string]string
	approvalsMu *sync.Mutex
}

// drain 消费 subscribe 期间缓存的先到帧。
func (p *eventPump) drain(stream *wsStream) bool {
	for _, frame := range stream.drainPending() {
		if done := p.handle(frame); done {
			return true
		}
	}
	return false
}

// handle 投影一帧到 canonical 事件/审批/状态；返回 true 表示本轮 turn.ended。
// 未映射帧（prompt.*/event.session.*/queue 类）显式 OnLog，不静默吞帧。
func (p *eventPump) handle(frame wsFrame) bool {
	if frame.Type == "" {
		return false
	}
	switch frame.Type {
	case "turn.started":
		var ev evTurnStarted
		_ = json.Unmarshal(frame.Payload, &ev)
		if !isMainAgent(ev.AgentID) {
			if ev.AgentID != "" {
				c := p.child(ev.AgentID)
				if c.activeTurn != ev.TurnID && (c.activeSeen || c.answer.Len() > 0 || len(c.pending) > 0 || len(c.results) > 0) {
					p.closeChildTools(ev.AgentID, c, "cancelled")
					c.answer.Reset()
					c.usageIn, c.usageOut, c.usageCached = 0, 0, 0
					c.pending = make(map[string]string)
					c.results = make(map[string]struct{})
				}
				c.activeTurn, c.activeSeen = ev.TurnID, true
			}
			return false
		}
		switch {
		case ev.PromptID != "" && ev.PromptID == p.state.promptID:
			p.state.activeTurn, p.state.activeSeen = ev.TurnID, true
		case ev.PromptID == "" && !p.state.activeSeen:
			// 回退：老服务端不回显 promptId 时，prompt 提交后的首个 turn 视为本轮。
			p.state.activeTurn, p.state.activeSeen = ev.TurnID, true
		}
	case "turn.ended":
		// 只对本轮收尾：resume 时在途旧 turn 的 turn.ended 可能先到，
		// activeSeen=false 或 turnId 不匹配一律忽略。
		var ev evTurnEnded
		_ = json.Unmarshal(frame.Payload, &ev)
		if !isMainAgent(ev.AgentID) {
			if c := p.childIfActive(ev.AgentID, ev.TurnID); c != nil {
				p.closeChildTools(ev.AgentID, c, ev.Reason)
				if ev.Reason == "completed" && c.answer.Len() > 0 {
					p.ex.Callbacks.OnEvent(domain.EventMessageCompleted, map[string]any{"role": "assistant", "text": c.answer.String(), "agent_id": ev.AgentID})
				}
				c.activeSeen = false
			}
			return false
		}
		if !p.myTurn(ev.TurnID, ev.AgentID) {
			return false
		}
		p.state.endReason = ev.Reason
		p.closePendingTools(ev.Reason)
		switch ev.Reason {
		case "failed", "blocked":
			p.state.failure = turnEndFailure(ev.Error)
			if p.state.failure == nil {
				if ev.Reason == "blocked" {
					p.state.failure = modFailure(runtime.FamilyConfig, "turn_blocked", "turn blocked", false)
				} else {
					p.state.failure = modFailure(runtime.FamilyTransientUpstream, "turn_failed", "turn failed", true)
				}
			}
		}
		// 合成 message.completed：kap 协议无 durable「助手消息完成」帧，
		// 由 assistant.delta 累计文本收口（与前端气泡契约一致）。
		if ev.Reason == "completed" && p.state.failure == nil && p.state.answer.Len() > 0 {
			p.ex.Callbacks.OnEvent(domain.EventMessageCompleted, map[string]any{
				"role": "assistant", "text": p.state.answer.String(),
			})
		}
		return true
	case "assistant.delta":
		var ev evDelta
		_ = json.Unmarshal(frame.Payload, &ev)
		if !isMainAgent(ev.AgentID) {
			if c := p.childIfActive(ev.AgentID, ev.TurnID); c != nil {
				p.ex.Callbacks.OnEvent(domain.EventMessageDelta, map[string]any{"agent_id": ev.AgentID, "raw": map[string]any{"chunk": map[string]any{"type": "text-delta", "text": ev.Delta}}})
				c.answer.WriteString(ev.Delta)
			}
			return false
		}
		if !p.myTurn(ev.TurnID, ev.AgentID) {
			return false
		}
		// raw.chunk 结构与前端 extractDeltaChunk 契约一致。
		p.ex.Callbacks.OnEvent(domain.EventMessageDelta, map[string]any{
			"raw": map[string]any{"chunk": map[string]any{"type": "text-delta", "text": ev.Delta}},
		})
		p.state.answer.WriteString(ev.Delta)
	case "thinking.delta":
		var ev evDelta
		_ = json.Unmarshal(frame.Payload, &ev)
		if !isMainAgent(ev.AgentID) {
			if c := p.childIfActive(ev.AgentID, ev.TurnID); c != nil {
				p.ex.Callbacks.OnEvent(domain.EventMessageDelta, map[string]any{"agent_id": ev.AgentID, "raw": map[string]any{"chunk": map[string]any{"type": "reasoning-delta", "text": ev.Delta}}})
			}
			return false
		}
		if !p.myTurn(ev.TurnID, ev.AgentID) {
			return false
		}
		p.ex.Callbacks.OnEvent(domain.EventMessageDelta, map[string]any{
			"raw": map[string]any{"chunk": map[string]any{"type": "reasoning-delta", "text": ev.Delta}},
		})
	case "turn.step.completed":
		// usage 权威来源：逐 step 累计（per_run）；input 计入 cacheRead/Creation。
		// 累计后即时 OnUsage 过程观测（覆盖语义）；终态结算仍走
		// turnEndResult 的 ExecResult.Usage。
		var ev evStepCompleted
		_ = json.Unmarshal(frame.Payload, &ev)
		if !isMainAgent(ev.AgentID) {
			if c := p.childIfActive(ev.AgentID, ev.TurnID); c != nil && ev.Usage != nil {
				other, _ := tokenUsageValue(ev.Usage.InputOther)
				read, _ := tokenUsageValue(ev.Usage.InputCacheRead)
				write, _ := tokenUsageValue(ev.Usage.InputCacheCreation)
				out, _ := tokenUsageValue(ev.Usage.Output)
				in := other + read + write
				c.usageIn += in
				c.usageOut += out
				c.usageCached += read
				p.ex.Callbacks.OnEvent(domain.EventUsageUpdated, map[string]any{"agent_id": ev.AgentID, "usage_in": c.usageIn, "usage_out": c.usageOut, "usage_cached": c.usageCached, "usage_basis": string(runtime.UsagePerRun)})
			}
			return false
		}
		if p.myTurn(ev.TurnID, ev.AgentID) && ev.Usage != nil {
			p.state.addUsage(ev.Usage)
			if usage := p.state.usageSnapshot(p.ex); usage != nil {
				p.ex.Callbacks.OnUsage(*usage)
			}
		}
	case "subagent.spawned", "subagent.started", "subagent.suspended", "subagent.completed", "subagent.failed":
		p.handleSubagent(frame)
	case "tool.call.started":
		var ev evToolCallStarted
		_ = json.Unmarshal(frame.Payload, &ev)
		if !isMainAgent(ev.AgentID) {
			if c := p.childIfActive(ev.AgentID, ev.TurnID); c != nil {
				if ev.ToolCallID != "" {
					if _, terminal := c.results[ev.ToolCallID]; terminal {
						return false
					}
					c.pending[ev.ToolCallID] = ev.Name
				}
				payload := map[string]any{"agent_id": ev.AgentID, "tool": ev.Name, "call_id": ev.ToolCallID}
				if s := toolArgsSummary(ev.Description, ev.Args); s != "" {
					payload["args_summary"] = s
				}
				if a := toolArgsJSON(ev.Args); a != "" {
					payload["args"] = a
				}
				p.ex.Callbacks.OnEvent(domain.EventToolStarted, payload)
			}
			return false
		}
		if !p.myTurn(ev.TurnID, ev.AgentID) {
			return false
		}
		if ev.ToolCallID != "" {
			if _, terminal := p.state.toolResults[ev.ToolCallID]; !terminal {
				p.state.pendingTools[ev.ToolCallID] = ev.Name
			}
		}
		payload := map[string]any{"tool": ev.Name, "call_id": ev.ToolCallID}
		if s := toolArgsSummary(ev.Description, ev.Args); s != "" {
			payload["args_summary"] = s
		}
		if a := toolArgsJSON(ev.Args); a != "" {
			payload["args"] = a
		}
		if ev.Name == "AgentSwarm" {
			if swarm := swarmMetadata(ev.ToolCallID, ev.Description, ev.Args); swarm != nil {
				payload["swarm"] = swarm
			}
		}
		if ev.ToolCallID != "" {
			if snap, ok := captureFileBefore(p.cwd(), ev.Name, ev.Args); ok {
				p.state.fileSnapshots[ev.ToolCallID] = snap
			}
		}
		p.ex.Callbacks.OnEvent(domain.EventToolStarted, payload)
	case "tool.result":
		var ev evToolResult
		_ = json.Unmarshal(frame.Payload, &ev)
		if !isMainAgent(ev.AgentID) {
			if c := p.childIfActive(ev.AgentID, ev.TurnID); c != nil {
				if ev.ToolCallID != "" {
					if _, seen := c.results[ev.ToolCallID]; seen {
						return false
					}
					c.results[ev.ToolCallID] = struct{}{}
				}
				tool := c.pending[ev.ToolCallID]
				delete(c.pending, ev.ToolCallID)
				payload := map[string]any{"agent_id": ev.AgentID, "call_id": ev.ToolCallID}
				if out := toolOutputText(ev.Output); out != "" {
					payload["output"] = out
					if stats := toolChangeStats(tool, out); stats != nil {
						payload["change_stats"] = stats
					}
				}
				if ev.IsError != nil && *ev.IsError {
					p.ex.Callbacks.OnEvent(domain.EventToolFailed, payload)
				} else {
					p.ex.Callbacks.OnEvent(domain.EventToolCompleted, payload)
				}
			}
			return false
		}
		if !p.myTurn(ev.TurnID, ev.AgentID) {
			return false
		}
		toolName := ""
		if ev.ToolCallID != "" {
			toolName = p.state.pendingTools[ev.ToolCallID]
			if _, seen := p.state.toolResults[ev.ToolCallID]; seen {
				return false
			}
			p.state.toolResults[ev.ToolCallID] = struct{}{}
			delete(p.state.pendingTools, ev.ToolCallID)
		}
		payload := map[string]any{"call_id": ev.ToolCallID}
		if out := toolOutputText(ev.Output); out != "" {
			payload["output"] = out
			if stats := toolChangeStats(toolName, out); stats != nil {
				payload["change_stats"] = stats
			}
		}
		if snap, ok := p.state.fileSnapshots[ev.ToolCallID]; ok && (ev.IsError == nil || !*ev.IsError) {
			if after, afterExists, ok := readSnapshotFile(snap.Path); ok && afterExists && (!snap.BeforeExists || after != snap.Before) {
				payload["file_change_snapshot"] = map[string]any{
					"path": snap.RelPath, "workspace_root": snap.Root, "before_content": snap.Before, "after_content": after,
					"before_exists": snap.BeforeExists, "after_exists": true,
					"write_count": 1, "before_hash": snap.BeforeHash, "after_hash": filechanges.Hash(after),
				}
			}
		}
		if ev.IsError != nil && *ev.IsError {
			p.ex.Callbacks.OnEvent(domain.EventToolFailed, payload)
		} else {
			p.ex.Callbacks.OnEvent(domain.EventToolCompleted, payload)
		}
	case "tool.progress":
		var ev evToolProgress
		_ = json.Unmarshal(frame.Payload, &ev)
		if !isMainAgent(ev.AgentID) {
			if c := p.childIfActive(ev.AgentID, ev.TurnID); c != nil {
				payload := map[string]any{"agent_id": ev.AgentID, "call_id": ev.ToolCallID, "text": ev.Update.Text}
				if ev.Update.Percent != nil {
					payload["percent"] = *ev.Update.Percent
				}
				p.ex.Callbacks.OnEvent(domain.EventToolProgress, payload)
			}
			return false
		}
		if !p.myTurn(ev.TurnID, ev.AgentID) {
			return false
		}
		payload := map[string]any{"call_id": ev.ToolCallID, "text": ev.Update.Text}
		if ev.Update.Percent != nil {
			payload["percent"] = *ev.Update.Percent
		}
		p.ex.Callbacks.OnEvent(domain.EventToolProgress, payload)
	case "event.approval.requested":
		p.handleApproval(frame)
	case "event.approval.resolved":
		// 服务端终局投影：canonical 事件留给 engine 审批状态机，这里只观测。
		log.Printf("kimiapp: run %s 审批终局 %s", p.ex.Run.ID, truncate(string(frame.Payload), 120))
	case "error":
		// 全局/会话错误帧：观测记录；turn 终局以 turn.ended 为权威。
		p.ex.Callbacks.OnLog("kimiapp", "error "+truncate(string(frame.Payload), 400))
	default:
		// prompt.submitted/steered/aborted/completed、event.session.*、
		// turn.step.* 等暂无 canonical 映射的帧：显式 OnLog 保持可观测。
		p.ex.Callbacks.OnLog("kimiapp", frame.Type+" "+truncate(string(frame.Payload), 400))
	}
	return false
}

func (p *eventPump) handleSubagent(frame wsFrame) {
	if p.state == nil {
		p.state = &turnState{}
	}
	if p.state.subagents == nil {
		p.state.subagents = make(map[string]map[string]any)
	}
	if p.state.subagentSeqs == nil {
		p.state.subagentSeqs = make(map[int64]struct{})
	}
	if frame.Seq > 0 {
		if _, seen := p.state.subagentSeqs[frame.Seq]; seen {
			return
		}
		p.state.subagentSeqs[frame.Seq] = struct{}{}
	}
	var raw map[string]any
	if json.Unmarshal(frame.Payload, &raw) != nil {
		return
	}
	get := func(names ...string) any {
		for _, n := range names {
			if v, ok := raw[n]; ok {
				return v
			}
		}
		return nil
	}
	str := func(names ...string) string { v, _ := get(names...).(string); return v }
	id := str("subagentId", "subagent_id", "id")
	if id == "" {
		return
	}
	snapshot := p.state.subagents[id]
	if snapshot == nil && frame.Type != "subagent.spawned" {
		return
	}
	if snapshot == nil {
		parentToolCallID := str("parentToolCallId", "parent_tool_call_id")
		if parentToolCallID == "" || p.state.pendingTools[parentToolCallID] == "" {
			return
		}
		snapshot = map[string]any{"runtime": "kimi", "subagent_id": id, "name": "", "parent_tool_call_id": "", "description": "", "run_in_background": false}
		p.state.subagents[id] = snapshot
	}
	if v := str("subagentName", "name", "agentName", "agent_name"); v != "" {
		snapshot["name"] = v
	}
	if v := str("parentToolCallId", "parent_tool_call_id"); v != "" {
		snapshot["parent_tool_call_id"] = v
	}
	if v := str("description", "prompt"); v != "" {
		snapshot["description"] = v
	}
	if v, ok := get("runInBackground", "run_in_background").(bool); ok {
		snapshot["run_in_background"] = v
	}
	if n, ok := numberValue(get("swarmIndex", "swarm_index")); ok && n >= 1 {
		snapshot["swarm_index"] = int(n)
	}
	if _, ok := snapshot["swarm_index"]; ok {
		snapshot["role"] = "member"
	} else {
		snapshot["role"] = "child"
	}
	snapshot["status"] = subagentStatus(frame.Type)
	// Each canonical update is a snapshot of the current lifecycle state.
	// Never carry a previous invocation's result, wait reason, or failure into
	// a resumed member with the same stable subagent id.
	delete(snapshot, "summary")
	delete(snapshot, "reason")
	delete(snapshot, "error")
	if summary := str("summary", "resultSummary", "result_summary"); summary != "" {
		snapshot["summary"] = summary
	}
	if reason := str("reason"); reason != "" {
		snapshot["reason"] = reason
	}
	if errText := errorText(get("error", "failure", "failure_reason")); errText != "" {
		snapshot["error"] = errText
	}
	if frame.Seq > 0 {
		snapshot["source_seq"] = frame.Seq
	}
	// Callback consumers may retain the map; emit a copy so later lifecycle
	// updates cannot rewrite already-persisted canonical events.
	emitted := make(map[string]any, len(snapshot))
	for k, v := range snapshot {
		emitted[k] = v
	}
	p.ex.Callbacks.OnEvent(domain.EventSubagentUpdated, emitted)
}

func subagentStatus(kind string) string {
	switch kind {
	case "subagent.spawned":
		return "queued"
	case "subagent.started":
		return "running"
	case "subagent.suspended":
		return "waiting"
	case "subagent.completed":
		return "completed"
	default:
		return "failed"
	}
}

func numberValue(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		if n >= 0 && n == float64(int64(n)) {
			return int64(n), true
		}
	case int:
		if n >= 0 {
			return int64(n), true
		}
	case json.Number:
		i, e := n.Int64()
		return i, e == nil && i >= 0
	}
	return 0, false
}
func errorText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case map[string]any:
		if s, ok := x["message"].(string); ok {
			return s
		}
		b, _ := json.Marshal(x)
		return string(b)
	}
	return ""
}

func swarmMetadata(callID, title string, args json.RawMessage) map[string]any {
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		m = map[string]any{}
	}
	if s, ok := m["description"].(string); ok && s != "" {
		title = s
	}
	if title == "" {
		title = "Kimi 蜂群"
	}
	items := []map[string]any{}
	resumeCount := 0
	if raw, ok := m["resume_agent_ids"].(map[string]any); ok {
		resumeCount = len(raw)
	}
	for i := 0; i < resumeCount; i++ {
		items = append(items, map[string]any{"index": i + 1, "description": "续接已有子 Agent"})
	}
	if raw, ok := m["items"].([]any); ok {
		for i, item := range raw {
			d := ""
			if s, ok := item.(string); ok {
				d = s
			}
			if d == "" {
				if x, ok := item.(map[string]any); ok {
					d, _ = x["description"].(string)
				}
			}
			items = append(items, map[string]any{"index": resumeCount + i + 1, "description": d})
		}
	}
	return map[string]any{"runtime": "kimi", "id": callID, "title": title, "total": len(items), "items": items}
}

// closePendingTools closes calls for which KAP ended the turn without a
// terminal tool.result. This is presentation evidence only: turnEndResult
// still derives the run outcome from turn.ended.reason.
func (p *eventPump) closePendingTools(reason string) {
	status := "failed"
	if reason == "completed" || reason == "cancelled" {
		status = "interrupted"
	}
	for callID, tool := range p.state.pendingTools {
		p.ex.Callbacks.OnEvent(domain.EventToolFailed, map[string]any{
			"call_id":        callID,
			"tool":           tool,
			"status":         status,
			"failure_reason": "turn_ended_before_tool_result",
		})
		delete(p.state.pendingTools, callID)
	}
}

func (p *eventPump) child(agentID string) *childTurnState {
	if p.state.children == nil {
		p.state.children = make(map[string]*childTurnState)
	}
	c := p.state.children[agentID]
	if c == nil {
		c = &childTurnState{pending: make(map[string]string), results: make(map[string]struct{})}
		p.state.children[agentID] = c
	}
	return c
}

func (p *eventPump) childIfActive(agentID string, turnID int64) *childTurnState {
	if agentID == "" || isMainAgent(agentID) || p.state.children == nil {
		return nil
	}
	c := p.state.children[agentID]
	if c == nil || !c.activeSeen || c.activeTurn != turnID {
		return nil
	}
	return c
}

func (p *eventPump) closeChildTools(agentID string, c *childTurnState, reason string) {
	status := "failed"
	if reason == "completed" || reason == "cancelled" {
		status = "interrupted"
	}
	for callID, tool := range c.pending {
		p.ex.Callbacks.OnEvent(domain.EventToolFailed, map[string]any{"agent_id": agentID, "call_id": callID, "tool": tool, "status": status, "failure_reason": "turn_ended_before_tool_result"})
		delete(c.pending, callID)
	}
}

// myTurn 只放行本轮 turn 的事件：prompt 排队/resume 期间，同会话旧 turn 的
// 在途事件（tool.*/approval 等）不归入本 run 时间线（与 delta 过滤同一条件）。
func (p *eventPump) myTurn(turnID int64, agentID string) bool {
	return p.state.activeSeen && turnID == p.state.activeTurn && isMainAgent(agentID)
}

// KAP session streams multiplex the parent and every child. Turn ids are only
// agent-local, so a missing identity is not safe to attribute to the parent.
func isMainAgent(agentID string) bool {
	return agentID == "main"
}

// handleApproval 把服务端审批请求映射为 engine 审批；发起失败立即拒绝，
// 防 harness 工具悬挂。
func (p *eventPump) handleApproval(frame wsFrame) {
	var ev evApprovalRequested
	_ = json.Unmarshal(frame.Payload, &ev)
	if ev.ApprovalID == "" {
		return
	}
	// 旧 turn 的在途审批不进本 run（turn_id 可缺省，缺省时无法判定、不过滤）；
	// 丢弃即不答——在途 turn 的归属方（原 run 的事件泵）会自行决议。
	if !p.state.activeSeen || (ev.TurnID != 0 && ev.TurnID != p.state.activeTurn) {
		return
	}
	summary := ev.ToolName
	if ev.Action != "" {
		summary += ": " + ev.Action
	}
	// risk 与 codexapp 对齐：kap 只在策略要求时才发起审批，一律按 high 登记
	//（kap 事件不携带风险分级；工具名属于 summary，不是 risk）。
	engineID := p.ex.Callbacks.RequestApproval("tool", "high", truncate(summary, 160))
	if engineID == "" {
		if kerr := p.client.resolveApproval(context.WithoutCancel(p.ex.Ctx), p.sessionID, ev.ApprovalID, "rejected", ""); kerr != nil {
			log.Printf("kimiapp: run %s 兜底拒绝审批失败: %v", p.ex.Run.ID, kerr)
		}
		return
	}
	p.approvalsMu.Lock()
	p.approvals[engineID] = ev.ApprovalID
	p.approvalsMu.Unlock()
}

// turnEndResult turn.ended 后的统一收尾：usage（per_run 增量）+ 会话句柄 +
// 终态（意图 > 失败 > 服务端 cancelled > 成功）。
func turnEndResult(ex *runtime.ExecContext, state *turnState) runtime.ExecResult {
	result := runtime.ExecResult{Session: state.sessionUpdate}
	if usage := state.usageSnapshot(ex); usage != nil {
		result.Usage = usage
	}
	switch {
	case ex.Ctx.Err() != nil:
		return intentResult(ex, state)
	case state.failure != nil:
		result.Outcome = runtime.OutcomeFailed
		result.Failure = state.failure
	case state.endReason == "cancelled":
		// 非 Ctx 取消的服务端侧中止（如其他客户端 abort）：按中断收尾。
		result.Outcome = runtime.OutcomeInterrupted
	default:
		result.Outcome = runtime.OutcomeSucceeded
	}
	return result
}

// intentResult Ctx 已取消且未见 turn.ended：按终态意图（cancel/interrupt）返回。
func intentResult(ex *runtime.ExecContext, state *turnState) runtime.ExecResult {
	outcome := runtime.OutcomeInterrupted
	if kind, ok := ex.TerminalIntent(); ok && kind == runtime.ControlCancel {
		outcome = runtime.OutcomeCancelled
	}
	result := runtime.ExecResult{Outcome: outcome, Session: state.sessionUpdate}
	result.Usage = state.usageSnapshot(ex)
	return result
}

// ── 配置投影 ──────────────────────────────────────────────────────────

// cwd 返回本 Run 的受信工作目录（Host resolver 产物；无 Resolved 回退进程 cwd）。
// file_change_snapshot 载荷的 workspace_root 以此为源，应用侧归一化/secureJoin
// 沿用该值——绝对路径只来自 resolver，绝不来自构造期配置。
func (p *eventPump) cwd() string {
	if p.ex != nil && p.ex.Resolved.CWD != "" {
		return p.ex.Resolved.CWD
	}
	return "."
}

// modelOf 编排快照模型别名优先，回落配置缺省模型。
func (m *Module) modelOf(ex *runtime.ExecContext) string {
	snap := runtime.ModelSnapshotOf(ex.Run)
	if model := kimiconfig.ModelAlias(snap); model != "" {
		return model
	}
	return m.cfg.Model
}

// personaOf 编排快照 → persona 文本。kap 无 system_prompt 应用通道，只能由
// 适配器文本注入（fresh 会话首个 prompt，见 submitPrompt）。
func personaOf(ex *runtime.ExecContext) string {
	return strings.TrimSpace(runtime.SystemPromptOf(ex.Run))
}

// planDirective plan 模式的注入指令（prompt.plan_mode 服务端接受但不应用）。
const planDirective = "Plan mode: analyze and produce a plan only; do not modify workspace files."

func defaultAgentConfig(ex *runtime.ExecContext) agentConfig {
	return agentConfig{
		PermissionMode: permissionMode(runtime.PolicySnapshotOf(ex.Run)),
		SwarmMode:      true,
	}
}

func applySessionDefaults(ctx context.Context, client *restClient, sessionID string, defaults agentConfig) *runtime.Failure {
	if kerr := client.updateProfile(ctx, sessionID, &sessionProfileRequest{AgentConfig: defaults}); kerr != nil {
		return kapFailure(kerr)
	}
	status, kerr := client.getSessionStatus(ctx, sessionID)
	if kerr != nil {
		return kapFailure(kerr)
	}
	if status.Permission != defaults.PermissionMode || status.SwarmMode != defaults.SwarmMode {
		return modFailure(
			runtime.FamilyConfig,
			"session_profile_not_applied",
			fmt.Sprintf("Kimi profile 未生效：permission=%s swarm=%t", status.Permission, status.SwarmMode),
			false,
		)
	}
	return nil
}

// permissionMode 统一审批策略 → kap permission_mode 三档：
// auto→yolo（免审批）、manual→manual（全审批）、其余策略默认 yolo。
func permissionMode(policy runtime.PolicySnapshot) string {
	switch policy.ApprovalPolicy {
	case "auto":
		return "yolo"
	case "manual":
		return "manual"
	default:
		return "yolo"
	}
}

// ── 工具事件载荷整形（与 codexapp 对齐的 canonical 契约）────────────────
//
// tool.started: {tool, call_id, args_summary?, args?}；tool.completed/failed:
// {call_id, output?, change_stats?}；tool.progress: {call_id, text, percent?}。输出统一截断，
// 防 run_events 膨胀（完整输出本就可达数 MB 级）。

const (
	maxToolArgsSummary = 200
	maxToolArgs        = 2000
	maxToolOutput      = 2000
)

const maxFileSnapshotBytes = 400_000

func captureFileBefore(root, tool string, args json.RawMessage) (fileSnapshot, bool) {
	name := strings.ToLower(strings.TrimSpace(tool))
	if name != "write" && name != "edit" && name != "apply_patch" {
		return fileSnapshot{}, false
	}
	var values map[string]any
	if json.Unmarshal(args, &values) != nil {
		return fileSnapshot{}, false
	}
	var path string
	for _, key := range []string{"path", "file_path", "filename", "target_file"} {
		if v, ok := values[key].(string); ok && strings.TrimSpace(v) != "" {
			path = v
			break
		}
	}
	if path == "" || root == "" {
		return fileSnapshot{}, false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return fileSnapshot{}, false
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return fileSnapshot{}, false
	}
	if path != base && !strings.HasPrefix(path, base+string(filepath.Separator)) {
		return fileSnapshot{}, false
	}
	for cur := path; ; cur = filepath.Dir(cur) {
		if info, err := os.Lstat(cur); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fileSnapshot{}, false
		}
		next := filepath.Dir(cur)
		if next == cur || cur == base {
			break
		}
	}
	before, exists, ok := readSnapshotFile(path)
	if !ok {
		return fileSnapshot{}, false
	}
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fileSnapshot{}, false
	}
	return fileSnapshot{Path: path, Root: base, RelPath: rel, Before: before, BeforeExists: exists, BeforeHash: filechanges.Hash(before)}, true
}

func readSnapshotFile(path string) (string, bool, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, true
		}
		return "", false, false
	}
	if len(b) > maxFileSnapshotBytes || bytesIndexZero(b) {
		return "", true, false
	}
	return string(b), true, true
}

func bytesIndexZero(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

var wroteBytesPattern = regexp.MustCompile(`(?i)^Wrote\s+([\d,]+)\s+bytes?\s+to\s+(.+)$`)

// toolChangeStats only promotes facts already present in the runtime result.
// Byte count and path are reliable; additions/deletions remain absent until
// the executor provides an old/new snapshot or a real diff.
func toolChangeStats(tool, output string) map[string]any {
	if !strings.EqualFold(strings.TrimSpace(tool), "write") {
		return nil
	}
	match := wroteBytesPattern.FindStringSubmatch(strings.TrimSpace(output))
	if len(match) != 3 {
		return nil
	}
	bytesWritten, err := strconv.ParseInt(strings.ReplaceAll(match[1], ",", ""), 10, 64)
	if err != nil || bytesWritten < 0 {
		return nil
	}
	path := strings.TrimSpace(match[2])
	if path == "" {
		return nil
	}
	return map[string]any{
		"operation": "write",
		"files":     1,
		"bytes":     bytesWritten,
		"path":      path,
	}
}

// toolArgsSummary 给 UI 的一行输入摘要：优先服务端 description，其次
// 命令/路径类参数，最后紧凑 JSON 截断。
func toolArgsSummary(description string, args json.RawMessage) string {
	if s := strings.TrimSpace(description); s != "" {
		return truncate(s, maxToolArgsSummary)
	}
	if len(args) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(args, &m) == nil {
		for _, key := range []string{"command", "cmd", "path", "file_path", "query", "url"} {
			if v, _ := m[key].(string); strings.TrimSpace(v) != "" {
				return truncate(v, maxToolArgsSummary)
			}
		}
	}
	return truncate(string(args), maxToolArgsSummary)
}

// toolArgsJSON 完整入参的紧凑 JSON 文本（截断 ≤maxToolArgs），供前端工具行
// IN/OUT 展开卡还原完整参数（args_summary 只是一行摘要）。仅参数非空且为
// JSON 对象/数组时携带；直接用 RawMessage 原文，不重新 marshal 以保键序；
// 截断发生在字符串层，不保证截断后仍是合法 JSON（前端只展示，不解析）。
func toolArgsJSON(args json.RawMessage) string {
	s := strings.TrimSpace(string(args))
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

// toolOutputText 提取 tool.result 输出文本：string 直出；ContentPart[]
// （{type:"text",text}）拼 text 段；其余紧凑 JSON 原样截断。
func toolOutputText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return truncate(s, maxToolOutput)
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil && len(parts) > 0 {
		var b strings.Builder
		for _, part := range parts {
			if part.Type == "text" {
				b.WriteString(part.Text)
			}
		}
		if b.Len() > 0 {
			return truncate(b.String(), maxToolOutput)
		}
	}
	return truncate(string(raw), maxToolOutput)
}

// ── 杂项 ──────────────────────────────────────────────────────────────

func modFailure(family runtime.ErrorFamily, code, message string, retryable bool) *runtime.Failure {
	return &runtime.Failure{Family: family, Code: code, Message: truncate(message, 200), Retryable: retryable}
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = s[:n]
	}
	return s
}

func containsStr(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func resetTimer(t *time.Timer, d time.Duration) {
	t.Stop()
	t.Reset(d)
}

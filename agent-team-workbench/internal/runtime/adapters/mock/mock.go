// Package mock 提供 Mock Adapter 模块（Adapter SPI v2）：无真实 Runtime 也能
// 完整演示一轮执行的 canonical 事件流、审批等待与取消/中断语义（协议文档 §12 M1 退出门）。
// 纯模块形态：不依赖应用层，执行只经 ExecContext.Callbacks / ExecResult 表达。
package mock

import (
	"context"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

const (
	// scheme 是 mock 会话 ref 的 scheme 前缀（mock://<id>）。
	scheme = "mock"
	// sessionPrefix 新会话 id 前缀（mock_atw_<ulid>）。
	sessionPrefix = "mock_atw_"
	// defaultStep 每个模拟阶段之间的停留时长。
	defaultStep = 1200 * time.Millisecond
)

// simSteps 对齐旧版 Mock 的消息与进度节奏；payload 逐字段一致，前端契约不变。
var simSteps = []struct {
	progress float64
	message  string
}{
	{0, "runtime 正在初始化"},
	{0.2, "开始分析任务要求"},
	{0.55, "正在生成实现方案"},
}

// Adapter 是 Mock Adapter 模块：Manifest/Probe 能力声明与旧版完全一致。
type Adapter struct {
	step time.Duration
}

var _ runtime.AdapterModule = (*Adapter)(nil)

// New 构造默认步长的 Mock Adapter 模块。
func New() *Adapter { return &Adapter{step: defaultStep} }

// NewWithStep 用自定义步长构造（测试用短步长加快流转）。
func NewWithStep(step time.Duration) *Adapter {
	if step <= 0 {
		step = defaultStep
	}
	return &Adapter{step: step}
}

func (a *Adapter) Manifest(ctx context.Context) (runtime.AdapterManifest, error) {
	return runtime.AdapterManifest{
		AdapterID: "mock", AdapterVersion: "1.0.0", ProviderVersion: "simulated",
		Protocol: runtime.Protocol{Name: "mock", Version: "1"},
		Capabilities: map[string]runtime.CapabilityLevel{
			"streaming": runtime.CapSupported, "resume": runtime.CapSupported,
			"interrupt": runtime.CapSupported, "approval": runtime.CapSupported,
			"workspace_files": runtime.CapSupported, "terminal": runtime.CapUnavailable,
			"structured_output": runtime.CapAdapterTranslated,
		},
		SchemaDigest: "sha256:mock",
	}, nil
}

func (a *Adapter) Probe(ctx context.Context, req runtime.ProbeRequest) (runtime.ProbeResult, error) {
	m, _ := a.Manifest(ctx)
	return runtime.ProbeResult{OK: true, Manifest: &m}, nil
}

// Execute 模拟一轮对话执行：上报会话句柄 → 逐条发出 delta/progress →
// （指令提及审批时）发起审批并等待决定 → message.completed + 产物事件 → 成功终态。
func (a *Adapter) Execute(ex *runtime.ExecContext) runtime.ExecResult {
	session := sessionOf(ex)
	// 崩溃也不丢 resume 时机：进 Execute 立刻上报会话句柄。
	ex.Callbacks.OnSession(session)

	for _, s := range simSteps {
		if !a.pause(ex) {
			return aborted(ex, session)
		}
		ex.Callbacks.OnEvent(domain.EventMessageDelta,
			map[string]any{"role": "assistant", "text": s.message})
		ex.Callbacks.OnProgress(s.progress)
	}

	// 指令中提到 approval / 审批 时模拟一次高风险审批。
	if strings.Contains(ex.Instruction, "approval") || strings.Contains(ex.Instruction, "审批") {
		approvalID := ex.Callbacks.RequestApproval("shell", "high", "准备执行模拟发布命令（Mock）")
		if approvalID == "" {
			return runtime.ExecResult{
				Outcome: runtime.OutcomeFailed,
				Failure: &runtime.Failure{
					Family: runtime.FamilyInternal, Code: "approval_unavailable",
					Message: "审批发起失败（Mock）",
				},
				Session: &session,
			}
		}
		approved, ok := a.awaitApproval(ex, approvalID)
		if !ok {
			return aborted(ex, session)
		}
		if !approved {
			// 审批被拒：对齐编排层语义（拒绝 → cancelling），由模块确认取消终态。
			return runtime.ExecResult{Outcome: runtime.OutcomeCancelled, Session: &session}
		}
	}

	if !a.pause(ex) {
		return aborted(ex, session)
	}
	ex.Callbacks.OnEvent(domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": "任务执行完成，产物已生成"})
	ex.Callbacks.OnEvent(domain.EventArtifactCreated, map[string]any{
		"logical_path": "output/result.md", "mime": "text/markdown",
		"size": 2048, "sha256": strings.Repeat("a", 64),
		"classification": "internal", "status": string(domain.ArtifactDraft),
	})
	ex.Callbacks.OnProgress(1.0)

	if !a.pause(ex) {
		return aborted(ex, session)
	}

	usage := runtime.Usage{
		InputTokens:  int64(128 + len(ex.Instruction)),
		OutputTokens: 256,
		Basis:        runtime.UsagePerRun,
	}
	return runtime.ExecResult{Outcome: runtime.OutcomeSucceeded, Usage: &usage, Session: &session}
}

// sessionOf 决定本轮会话句柄：ex.Session.Ref 可解析为 mock://<id> 时续用同一 id，
// 否则生成新会话 id（mock_atw_<ulid>）。
func sessionOf(ex *runtime.ExecContext) runtime.SessionUpdate {
	id := runtime.SessionIDFromRef(ex.Session.Ref, scheme)
	resumed := id != ""
	if !resumed {
		id = domain.NewID(sessionPrefix)
	}
	return runtime.SessionUpdate{
		Ref:    scheme + "://" + id,
		Params: map[string]any{"resumed": resumed},
	}
}

// pause 停留一个步长；期间 Ctx 取消（interrupt/cancel/关停）立即返回 false。
// mock 未声明 steering，收到控制命令时忽略并继续等待。
func (a *Adapter) pause(ex *runtime.ExecContext) bool {
	timer := time.NewTimer(a.step)
	defer timer.Stop()
	for {
		select {
		case <-ex.Ctx.Done():
			return false
		case <-timer.C:
			return true
		case _, ok := <-ex.Controls:
			if !ok {
				// 控制流已关闭：等满剩余时长后继续推进。
				<-timer.C
				return true
			}
		}
	}
}

// awaitApproval 阻塞等待与 approvalID 匹配的审批决定；
// Ctx 取消时返回 ok=false（interrupt/cancel 由终态意图区分）。
func (a *Adapter) awaitApproval(ex *runtime.ExecContext, approvalID string) (approved, ok bool) {
	for {
		select {
		case <-ex.Ctx.Done():
			return false, false
		case c, ok := <-ex.Controls:
			if !ok {
				return false, false
			}
			// 决定必须精确寻址：忽略其他审批或其他控制命令。
			if c.Kind == runtime.ControlApproval && c.ApprovalID == approvalID {
				return c.Approved, true
			}
		}
	}
}

// aborted 按 TerminalIntent 映射中断终态；无意图的取消（如服务关停）保守按 interrupted。
func aborted(ex *runtime.ExecContext, session runtime.SessionUpdate) runtime.ExecResult {
	outcome := runtime.OutcomeInterrupted
	if kind, ok := ex.TerminalIntent(); ok && kind == runtime.ControlCancel {
		outcome = runtime.OutcomeCancelled
	}
	return runtime.ExecResult{Outcome: outcome, Session: &session}
}

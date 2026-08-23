// Package scripted 是第二个 Adapter 模块形态：录制回放（fixture replay）。
// 真实 Provider（Codex/Kimi/DSH）接入时先脱敏录制 JSONL，再由本模块回放做
// schema drift 与 conformance 验证（协议文档 §9.2 / 附录 C）。
// 纯模块形态（Adapter SPI v2）：不依赖应用层，回放只经 ExecContext.Callbacks 表达。
package scripted

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

//go:embed fixture.jsonl
var defaultFixture []byte

const (
	// scheme 是 scripted 会话 ref 的 scheme 前缀（scripted://<id>）。
	scheme = "scripted"
	// sessionPrefix 新会话 id 前缀（scripted_atw_<ulid>）。
	sessionPrefix = "scripted_atw_"
)

// Step 是 fixture 中一行：回调/事件 + 延迟。Kind 语义：
//   - run.status_changed：兼容旧 fixture 行；新 SPI 下状态由 ModuleRunner 按
//     Outcome 权威推进（adapter 不得直写状态），回放为 no-op
//   - run.progress：Data.progress → OnProgress
//   - usage：Data.input_tokens / output_tokens / cached_tokens / basis → OnUsage
//   - session：Data.ref / params → OnSession
//   - log：Data.stream / line → OnLog
//   - 其余白名单事件名（message.delta / tool.started 等）→ OnEvent(kind, Data)
//
// 未知事件名按白名单校验直接忽略（与旧行为一致）。
type Step struct {
	Kind    string         `json:"kind"`
	Data    map[string]any `json:"data,omitempty"`
	DelayMS int            `json:"delay_ms,omitempty"`
}

// Adapter 按 fixture 顺序回放 canonical 事件流。
type Adapter struct {
	steps []Step
}

var _ runtime.AdapterModule = (*Adapter)(nil)

// New 构造回放内置默认 fixture 的模块。
func New() *Adapter { return &Adapter{steps: ParseFixture(defaultFixture)} }

// NewWithSteps 用自定义脚本构造（测试编排事件与行为）。
func NewWithSteps(steps ...Step) *Adapter { return &Adapter{steps: steps} }

// ParseFixture 解析 JSONL fixture（每行一个 Step）；无法解析的行跳过（与旧行为一致）。
func ParseFixture(raw []byte) []Step {
	var steps []Step
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var s Step
		if err := json.Unmarshal(sc.Bytes(), &s); err == nil && s.Kind != "" {
			steps = append(steps, s)
		}
	}
	return steps
}

// Steps 返回当前脚本副本（调试/断言用）。
func (a *Adapter) Steps() []Step { return append([]Step(nil), a.steps...) }

func (a *Adapter) Manifest(ctx context.Context) (runtime.AdapterManifest, error) {
	return runtime.AdapterManifest{
		AdapterID: "scripted", AdapterVersion: "1.0.0", ProviderVersion: "fixture-v1",
		Protocol: runtime.Protocol{Name: "fixture-replay", Version: "1"},
		Capabilities: map[string]runtime.CapabilityLevel{
			"streaming": runtime.CapSupported, "resume": runtime.CapSupported,
			"interrupt": runtime.CapSupported, "approval": runtime.CapUnavailable,
			"workspace_files": runtime.CapSupported, "terminal": runtime.CapUnavailable,
			"structured_output": runtime.CapSupported,
		},
		SchemaDigest: "sha256:scripted-fixture",
	}, nil
}

func (a *Adapter) Probe(ctx context.Context, req runtime.ProbeRequest) (runtime.ProbeResult, error) {
	m, _ := a.Manifest(ctx)
	if len(a.steps) == 0 {
		return runtime.ProbeResult{OK: false, Error: "fixture 为空"}, nil
	}
	return runtime.ProbeResult{OK: true, Manifest: &m}, nil
}

// Execute 回放脚本：先上报会话句柄，再逐步延迟 + 派发回调，最后成功返回。
func (a *Adapter) Execute(ex *runtime.ExecContext) runtime.ExecResult {
	session := sessionOf(ex)
	// 崩溃也不丢 resume 时机：进 Execute 立刻上报会话句柄。
	ex.Callbacks.OnSession(session)

	for _, s := range a.steps {
		if !delay(ex, s.DelayMS) {
			return aborted(ex, session)
		}
		a.apply(ex, s)
	}

	usage := runtime.Usage{InputTokens: 64, OutputTokens: 128, Basis: runtime.UsagePerRun}
	return runtime.ExecResult{Outcome: runtime.OutcomeSucceeded, Usage: &usage, Session: &session}
}

// apply 把一行脚本映射为 SPI 回调。
func (a *Adapter) apply(ex *runtime.ExecContext, s Step) {
	switch s.Kind {
	case "run.status_changed":
		// 新 SPI：adapter 不得直写状态；状态由 ModuleRunner 按 Outcome 权威推进。
	case "run.progress":
		if v, ok := num(s.Data["progress"]); ok {
			ex.Callbacks.OnProgress(v)
		}
	case "usage":
		u := runtime.Usage{Basis: runtime.UsagePerRun}
		if v, ok := num(s.Data["input_tokens"]); ok {
			u.InputTokens = int64(v)
		}
		if v, ok := num(s.Data["output_tokens"]); ok {
			u.OutputTokens = int64(v)
		}
		if v, ok := num(s.Data["cached_tokens"]); ok {
			u.CachedTokens = int64(v)
		}
		if b, ok := s.Data["basis"].(string); ok && b != "" {
			u.Basis = runtime.UsageBasis(b)
		}
		ex.Callbacks.OnUsage(u)
	case "session":
		up := runtime.SessionUpdate{}
		if ref, ok := s.Data["ref"].(string); ok {
			up.Ref = ref
		}
		if p, ok := s.Data["params"].(map[string]any); ok {
			up.Params = p
		}
		ex.Callbacks.OnSession(up)
	case "log":
		stream, _ := s.Data["stream"].(string)
		line, _ := s.Data["line"].(string)
		ex.Callbacks.OnLog(stream, line)
	default:
		if domain.IsKnownEventName(s.Kind) {
			ex.Callbacks.OnEvent(s.Kind, s.Data)
		}
	}
}

// sessionOf 决定本轮会话句柄：ex.Session.Ref 可解析为 scripted://<id> 时续用同一 id，
// 否则生成新会话 id（scripted_atw_<ulid>）。
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

// delay 停留 DelayMS；期间 Ctx 取消立即返回 false。scripted 未声明 steering/approval，
// 收到控制命令时忽略并继续回放。
func delay(ex *runtime.ExecContext, ms int) bool {
	if ms <= 0 {
		return ex.Ctx.Err() == nil
	}
	timer := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-ex.Ctx.Done():
			return false
		case <-timer.C:
			return true
		case _, ok := <-ex.Controls:
			if !ok {
				// 控制流已关闭：等满剩余时长后继续回放。
				<-timer.C
				return true
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

// num 兼容 JSON 反序列化出的 float64 与 Go 字面量构造的整数。
func num(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

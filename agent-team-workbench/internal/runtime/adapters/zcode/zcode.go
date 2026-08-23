// Package zcode 是 probe-only 的 Adapter SPI v2 桩（协议文档 §9 / §M5）。
// 结论先行：ZCode 公开文档仅确认 Hook JSON stdin/stdout 与 PermissionRequest，
// Hook 是扩展与策略通道，不等于 session start / stream / cancel API；
// 因此在官方可驱动协议被核验前，本包不提供执行面，只暴露 Probe 用于
// 版本/能力探测，所有能力显式声明 unavailable，禁止静默降级。
package zcode

import (
	"context"

	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// Module 实现 runtime.AdapterModule；执行面显式拒绝（probe-only）。
type Module struct{}

var _ runtime.AdapterModule = (*Module)(nil)

func New() *Module { return &Module{} }

func (m *Module) Manifest(ctx context.Context) (runtime.AdapterManifest, error) {
	return runtime.AdapterManifest{
		AdapterID: "zcode-probe", AdapterVersion: "0.0.1",
		Protocol: runtime.Protocol{Name: "zcode", Version: "0"},
		Capabilities: map[string]runtime.CapabilityLevel{
			"streaming": runtime.CapUnavailable, "resume": runtime.CapUnavailable,
			"interrupt": runtime.CapUnavailable, "approval": runtime.CapUnavailable,
			"workspace_files": runtime.CapUnavailable, "terminal": runtime.CapUnavailable,
			"structured_output": runtime.CapUnavailable,
		},
		SchemaDigest: "sha256:zcode-unverified",
	}, nil
}

// Probe 返回未核验状态；spike 任务是用真实安装核验官方可驱动面
// （start/stream/cancel/approval/resume 至少一套官方路径可稳定自动化）。
func (m *Module) Probe(ctx context.Context, req runtime.ProbeRequest) (runtime.ProbeResult, error) {
	mf, _ := m.Manifest(ctx)
	return runtime.ProbeResult{
		OK: false, Manifest: &mf,
		Error: "zcode 自动化协议未核验：公开面仅有 Hook JSON，不作为 Runtime Adapter",
	}, nil
}

// Execute 一律显式失败：执行面未接入前不得启动任何 Run（不静默降级）。
func (m *Module) Execute(ex *runtime.ExecContext) runtime.ExecResult {
	return runtime.ExecResult{
		Outcome: runtime.OutcomeFailed,
		Failure: &runtime.Failure{
			Family: runtime.FamilyConfig, Code: "start_unsupported",
			Message: "zcode 执行面未接入（probe-only）", Retryable: false,
		},
	}
}

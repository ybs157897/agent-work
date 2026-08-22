// Package zcode 是 M5 spike 的 ProtocolProbe 桩（协议文档 §9 / §M5）。
// 结论先行：ZCode 公开文档仅确认 Hook JSON stdin/stdout 与 PermissionRequest，
// Hook 是扩展与策略通道，不等于 session start / stream / cancel API；
// 因此在官方可驱动协议被核验前，本包不提供 RuntimeAdapter 执行面，
// 只暴露 Probe 用于版本/能力探测，所有能力显式声明 unavailable，禁止静默降级。
package zcode

import (
	"context"

	"github.com/ybs/agent-team-workbench/internal/runtime"
)

type Probe struct{}

var _ runtime.RuntimeAdapter = (*Probe)(nil)

func New() *Probe { return &Probe{} }

func (p *Probe) Manifest(ctx context.Context) (runtime.AdapterManifest, error) {
	return runtime.AdapterManifest{
		AdapterID: "zcode-probe", AdapterVersion: "0.0.1",
		Protocol: runtime.Protocol{Name: "unverified", Version: "0"},
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
func (p *Probe) Probe(ctx context.Context, req runtime.ProbeRequest) (runtime.ProbeResult, error) {
	m, _ := p.Manifest(ctx)
	return runtime.ProbeResult{
		OK: false, Manifest: &m,
		Error: "zcode 自动化协议未核验：公开面仅有 Hook JSON，不作为 Runtime Adapter",
	}, nil
}

// Start 显式拒绝：不支持的方法不得静默吞掉（协议文档 §8.1）。
func (p *Probe) Start(ctx context.Context, req runtime.StartRequest) (runtime.RuntimeHandle, error) {
	return nil, runtime.ErrStartUnsupported
}

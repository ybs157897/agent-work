// Package runtime 定义 Runtime Adapter SPI（协议文档 §8.1）。
// Provider 原始类型必须留在 adapters 内，不得跨模块暴露。
package runtime

import (
	"context"
	"errors"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// ErrStartUnsupported：Adapter 不提供执行面（如 probe-only 桩）；禁止静默降级。
var ErrStartUnsupported = errors.New("adapter does not provide an execution surface")

// CapabilityLevel 能力状态；禁止用单一 bool 掩盖限制。
type CapabilityLevel string

const (
	CapSupported         CapabilityLevel = "supported"
	CapExperimental      CapabilityLevel = "experimental"
	CapAdapterTranslated CapabilityLevel = "adapter_translated"
	CapUnavailable       CapabilityLevel = "unavailable"
)

type Protocol struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// AdapterManifest 必须通过 conformance tests，不能仅凭 provider 名称推断能力。
type AdapterManifest struct {
	AdapterID       string                     `json:"adapter_id"`
	AdapterVersion  string                     `json:"adapter_version"`
	ProviderVersion string                     `json:"provider_version,omitempty"`
	Protocol        Protocol                   `json:"protocol"`
	Capabilities    map[string]CapabilityLevel `json:"capabilities"`
	SchemaDigest    string                     `json:"schema_digest"`
}

type ProbeRequest struct {
	WorkspaceID string
}

type ProbeResult struct {
	OK       bool             `json:"ok"`
	Manifest *AdapterManifest `json:"manifest,omitempty"`
	Error    string           `json:"error,omitempty"`
}

type StartRequest struct {
	Run         *domain.ExecutionRun
	Instruction string
	WorkspaceID string
}

// RuntimeHandle 是一次 Run 的执行句柄；Recv 统一 backpressure 与关闭语义。
type RuntimeHandle interface {
	SessionRef() string
	Send(ctx context.Context, instruction string) error
	Interrupt(ctx context.Context) error
	Cancel(ctx context.Context) error
	ResolveApproval(ctx context.Context, approvalID string, approved bool) error
	Close(ctx context.Context) error
}

// RuntimeAdapter 稳定接口：不支持的方法不得静默吞掉，必须在 Manifest/Probe 声明。
type RuntimeAdapter interface {
	Manifest(ctx context.Context) (AdapterManifest, error)
	Probe(ctx context.Context, req ProbeRequest) (ProbeResult, error)
	Start(ctx context.Context, req StartRequest) (RuntimeHandle, error)
}

// InputForwarder 可选能力：把用户 steering 输入转发到活动 Run 的 Runtime 会话。
// 不支持的 adapter 不实现本接口即可（协议 §8.1：不静默降级）。
type InputForwarder interface {
	ForwardInput(ctx context.Context, runID, instruction string) error
}

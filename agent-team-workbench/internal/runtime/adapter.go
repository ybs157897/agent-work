// Package runtime 定义 Runtime Adapter SPI v2（协议文档 §8.1）。
// Provider 原始类型必须留在 adapters 内，不得跨模块暴露。
package runtime

import (
	"context"
)

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

// ManifestProber 是注册表条目的唯一形态：probe/binding 路径只需要能力
// 协商；执行面统一经 ModuleRunner.Dispatch（SPI v2），不存在第二条执行轨。
type ManifestProber interface {
	Manifest(ctx context.Context) (AdapterManifest, error)
	Probe(ctx context.Context, req ProbeRequest) (ProbeResult, error)
}

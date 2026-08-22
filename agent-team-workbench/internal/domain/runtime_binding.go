package domain

import "time"

// RuntimeBindingStatus 连接与能力状态投影。
type RuntimeBindingStatus string

const (
	BindingReady       RuntimeBindingStatus = "ready"
	BindingProbing     RuntimeBindingStatus = "probing"
	BindingDegraded    RuntimeBindingStatus = "degraded"
	BindingUnavailable RuntimeBindingStatus = "unavailable"
)

// RuntimeBinding 是设置页的 Runtime + 模型配置对象（协议文档 §2.1 / §5.3）。
// 模型配置 = provider + model + CredentialRef；凭据只存引用，
// 浏览器不持 Provider API Key，明文由 Runner 本地安全存储解析。
type RuntimeBinding struct {
	ID              string
	WorkspaceID     string
	RuntimeLabel    string // 调度用标签，如 dsh_local / mock_local
	AdapterID       string // 实现该 Runtime 的 Adapter
	AdapterVersion  string
	ProviderVersion string
	Provider        string // 模型提供方，如 deepseek / openai
	Model           string // 模型配置，如 deepseek-chat
	CredentialRef   string // CredentialRef，绝不存明文
	Capabilities    map[string]string
	Status          RuntimeBindingStatus
	Version         int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (b *RuntimeBinding) CheckVersion(expected int) error {
	if expected != 0 && expected != b.Version {
		return ErrVersionConflict
	}
	return nil
}

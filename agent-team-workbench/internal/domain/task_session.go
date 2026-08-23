package domain

import "time"

// TaskSession 是 (workspace, agent, adapter, task) 维度的长期会话锚点
// （对齐 Paperclip agent_task_sessions）：跨 Run 续接、指纹校验与轮换都以此为准。
// taskKey 当前取 work item ID；AgentProfileID 可为空（匿名运行）。
type TaskSession struct {
	ID          string
	WorkspaceID string
	// AgentProfileID + AdapterID + TaskKey 构成唯一键。
	AgentProfileID string
	AdapterID      string
	TaskKey        string
	// ParentAnchorID 父任务（同 agent+adapter）锚点 id：会话树镜像任务树，
	// 轮换谱系沿此链可查；父任务不存在或无锚点时为空。
	ParentAnchorID string
	// SessionParams adapter 私有参数；保留键：__ref（session_ref，如 claude://x）、
	// __fingerprint（创建该会话时的配置指纹，变更即视为配置漂移）。
	SessionParams map[string]any
	// DisplayID provider 侧人类可读会话 id（如 thread id、sessionId）。
	DisplayID string
	// RunsCount 自该会话创建以来使用它的 Run 数（轮换阈值输入）。
	RunsCount int
	// InputTokensCum 会话累计输入 token（per_run 用量累加而来；轮换阈值输入）。
	InputTokensCum int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// SessionRef 返回会话句柄（无则空串）。
func (t *TaskSession) SessionRef() string {
	if t == nil || t.SessionParams == nil {
		return ""
	}
	ref, _ := t.SessionParams["__ref"].(string)
	return ref
}

// Fingerprint 返回创建会话时的配置指纹。
func (t *TaskSession) Fingerprint() string {
	if t == nil || t.SessionParams == nil {
		return ""
	}
	fp, _ := t.SessionParams["__fingerprint"].(string)
	return fp
}

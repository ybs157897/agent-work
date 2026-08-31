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
	// SegmentSeq 参与线片段序号：同一 task_key 下第 N 段会话（轮换代际时 +1，
	// 缺省 1），片段边界显式化。
	SegmentSeq int
	// ContextSnapshotID / ContextGeneration 是该锚点当前挂靠的执行上下文身份
	//（RFC §4.8：context_generation 是兼容代际，不等于单个 Snapshot ID；
	// fingerprint 含 generation，变化必须 fresh/rotate）。
	ContextSnapshotID string
	ContextGeneration int
	// LastRunID / AnchorRunSequence 是 Run 创建事务内预先 claim 的锚点归属：
	// run.session 更新必须满足 generation 一致、incoming Run 是当前 anchor
	// owner、run sequence 不小于当前 anchor sequence（旧 Run 的墓碑不清新代际）。
	LastRunID         string
	AnchorRunSequence int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
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

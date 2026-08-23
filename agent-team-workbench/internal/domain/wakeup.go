package domain

import (
	"encoding/json"
	"errors"
	"time"
)

// PrefixWakeup — wakeup 请求 ID 前缀（协议文档 §5.1 opaque id 约定）。
const PrefixWakeup = "wkup_"

// ErrWakeupNotQueued CAS 失败：请求已离开 queued（被并发消费者占住或已迁移）。
// 调用方应视为「已被处理」，不得继续建 run。
var ErrWakeupNotQueued = errors.New("wakeup: no longer queued")

// WakeupSource 唤醒四源（M4，对齐 Paperclip wakeup 调度）：
// timer（心跳自主唤醒）/ assignment（工作项指派）/ on_demand（手动唤醒）/ automation（预留）。
type WakeupSource string

const (
	WakeupSourceTimer      WakeupSource = "timer"
	WakeupSourceAssignment WakeupSource = "assignment"
	WakeupSourceOnDemand   WakeupSource = "on_demand"
	WakeupSourceAutomation WakeupSource = "automation"
)

// ValidWakeupSource 报告 s 是否为合法唤醒源。
func ValidWakeupSource(s WakeupSource) bool {
	switch s {
	case WakeupSourceTimer, WakeupSourceAssignment, WakeupSourceOnDemand, WakeupSourceAutomation:
		return true
	}
	return false
}

// WakeupStatus 唤醒请求状态机：queued（待消费）→ coalesced（合并审计）| consumed（已创建 run）。
type WakeupStatus string

const (
	WakeupStatusQueued    WakeupStatus = "queued"
	WakeupStatusCoalesced WakeupStatus = "coalesced"
	WakeupStatusConsumed  WakeupStatus = "consumed"
)

// WakeupRequest 一次唤醒请求（agent_wakeup_requests 行）。
// Context 为唤醒附加上下文；保留键 instruction（显式指令，渲染时优先于模板）。
type WakeupRequest struct {
	ID             string
	WorkspaceID    string
	AgentProfileID string
	Source         WakeupSource
	// TaskKey 唤醒锚定的稳定 key：默认 work item id；timer 自主唤醒也必须有稳定 key。
	TaskKey string
	Context map[string]any
	Status  WakeupStatus
	WakeAt  time.Time
	// CreatedAt / UpdatedAt 由入队方与状态迁移方维护。
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ContextJSON 返回 context 列的 JSON 文本；nil map 视为 '{}'。
func (w *WakeupRequest) ContextJSON() string {
	if w == nil || w.Context == nil {
		return "{}"
	}
	b, err := json.Marshal(w.Context)
	if err != nil || len(b) == 0 {
		return "{}"
	}
	return string(b)
}

// Instruction 返回 context 中的显式 instruction（无则空串）。
func (w *WakeupRequest) Instruction() string {
	if w == nil {
		return ""
	}
	s, _ := w.Context["instruction"].(string)
	return s
}

const (
	// DefaultHeartbeatIntervalSec 全局缺省心跳间隔（30 分钟）；heartbeat_interval_sec=0 时生效。
	DefaultHeartbeatIntervalSec = 1800
	// DefaultPromptTemplate 缺省唤醒提示词模板；agent prompt_template 为空时生效。
	DefaultPromptTemplate = "{{agent.name}}，按你的职责（{{agent.role}}）检查任务「{{work_item.title}}」的当前状态，若有可推进的工作项请继续推进；没有则简短报告现状。"
)

// HeartbeatPolicy 从 AgentProfile 投影的心跳/唤醒策略（调度循环只读快照）。
type HeartbeatPolicy struct {
	Enabled          bool
	IntervalSec      int    // 0 = 用全局缺省 DefaultHeartbeatIntervalSec
	WakeOnAssignment bool   // 工作项指派时唤醒
	WakeOnDemand     bool   // 手动唤醒
	WakeOnAutomation bool   // 预留：自动化事件唤醒
	PromptTemplate   string // 空 = 用 DefaultPromptTemplate
}

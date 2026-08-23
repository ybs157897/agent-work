package domain

import "time"

// AgentAvailability 与 Presence 分离（协议文档 §2.2）：
// availability 是调度开关（enabled/disabled），presence 是运行投影（idle/busy/degraded/offline）。
type AgentAvailability string

const (
	AgentEnabled  AgentAvailability = "enabled"
	AgentDisabled AgentAvailability = "disabled"
)

type AgentPresence string

const (
	PresenceIdle     AgentPresence = "idle"
	PresenceBusy     AgentPresence = "busy"
	PresenceDegraded AgentPresence = "degraded"
	PresenceOffline  AgentPresence = "offline"
)

// RuntimePreference：每次 Run 可选 preferred/fallback Runtime，角色与 Runtime 解耦。
type RuntimePreference struct {
	Preferred   string   `json:"preferred,omitempty"`
	Fallbacks   []string `json:"fallbacks,omitempty"`
	Mode        string   `json:"mode,omitempty"`         // 统一执行模式：default | plan
	AgentPreset string   `json:"agent_preset,omitempty"` // DSH agent-presets id（standard/code/minimal/…）
}

// AgentPolicy 权限配置（协议 §8）：工具白名单 + 审批策略 + sandbox。
// Run 启动时固化快照；approval_policy: auto | approve_high_risk | manual。
type AgentPolicy struct {
	Tools            []string `json:"tools,omitempty"`
	ApprovalPolicy   string   `json:"approval_policy,omitempty"`
	Sandbox          string   `json:"sandbox,omitempty"`
	PermissionPreset string   `json:"permission_preset,omitempty"` // DSH permission preset id
}

// ApprovalPolicyManual 全量人工审批策略：plan dispatch 到此类 agent 的步骤
// 挂 ApprovalRequest（kind=plan_dispatch）等人放行（M4 审批护栏）。
const ApprovalPolicyManual = "manual"

// ModelRef 模型选择；Ref 引用 models/ 注册表条目，非空字段再覆盖条目的 provider/model。
type ModelRef struct {
	Ref      string `json:"ref,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// AgentProfile 持久角色配置（PM/Architect/UI/Developer/Reviewer），与 Runtime Session 解耦。
// 文件目录（agents/<slug>/）为配置真相源，DB 为运行时投影。
type AgentProfile struct {
	ID                string
	WorkspaceID       string
	Slug              string // 配置目录名；空表示尚未关联文件
	Name              string
	Role              string
	Skills            []string
	Instructions      string
	Avatar            string
	Availability      AgentAvailability
	Presence          AgentPresence
	RuntimePreference RuntimePreference
	ModelOverride     ModelRef
	Policy            AgentPolicy
	Version           int
	CreatedAt         time.Time
	UpdatedAt         time.Time

	// wakeup 调度策略（M4；列见 migrations/0005_wakeup.sql）。
	HeartbeatEnabled     bool
	HeartbeatIntervalSec int // 0 = 全局缺省 DefaultHeartbeatIntervalSec
	WakeOnAssignment     bool
	WakeOnDemand         bool
	WakeOnAutomation     bool
	PromptTemplate       string // 空 = 用 DefaultPromptTemplate
	LastHeartbeatAt      *time.Time
}

// Heartbeat 投影出 wakeup 调度所需的只读策略快照。
func (a *AgentProfile) Heartbeat() HeartbeatPolicy {
	return HeartbeatPolicy{
		Enabled:          a.HeartbeatEnabled,
		IntervalSec:      a.HeartbeatIntervalSec,
		WakeOnAssignment: a.WakeOnAssignment,
		WakeOnDemand:     a.WakeOnDemand,
		WakeOnAutomation: a.WakeOnAutomation,
		PromptTemplate:   a.PromptTemplate,
	}
}

// SetAvailability 切换调度开关；不等于立刻运行。
// 活动 Run 的处置策略（默认 interrupt）由应用层执行。
func (a *AgentProfile) SetAvailability(av AgentAvailability, now time.Time) {
	if a.Availability == av {
		return
	}
	a.Availability = av
	a.Version++
	a.UpdatedAt = now
}

// Workspace 多成员协作根对象。
type Workspace struct {
	ID        string
	Name      string
	Timezone  string
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// MemberRole RBAC 最小集（协议文档 §10.1）。
type MemberRole string

const (
	RoleOwner    MemberRole = "owner"
	RoleAdmin    MemberRole = "admin"
	RoleOperator MemberRole = "operator"
	RoleApprover MemberRole = "approver"
	RoleViewer   MemberRole = "viewer"
)

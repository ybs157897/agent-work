// Package security 承载 RBAC 最小集与审计约定（协议文档 §10.1）。
package security

import "github.com/ybs/agent-team-workbench/internal/domain"

// 权限点：与协议文档角色矩阵一一对应。
const (
	PermRead           = "read"             // 看板、活动、Run 与允许的 Artifact
	PermWorkItemWrite  = "work_item.write"  // 创建 / 指派 / 移动 / 阻塞任务
	PermRunControl     = "run.control"      // 启动、输入、中断、取消 Run
	PermApproval       = "approval.resolve" // 处理指定风险类型的审批
	PermAgentWrite     = "agent.write"      // AgentProfile 管理
	PermRuntimeManage  = "runtime.manage"   // RuntimeBinding / Runner 管理
	PermWorkspaceAdmin = "workspace.admin"  // Workspace 配置与成员管理
)

// rolePerms 角色 → 权限集。关键限制：
//   - Admin 不能读取凭据明文（任何角色都没有 credential.read 权限点）。
//   - Operator 不能修改凭据或扩大 Runtime scope。
//   - Approver 批准范围不得超过自身 policy scope（由审批策略层强制）。
//   - Viewer 不得查看敏感 raw payload 或执行命令。
var rolePerms = map[domain.MemberRole]map[string]bool{
	domain.RoleOwner: {
		PermRead: true, PermWorkItemWrite: true, PermRunControl: true,
		PermApproval: true, PermAgentWrite: true, PermRuntimeManage: true,
		PermWorkspaceAdmin: true,
	},
	domain.RoleAdmin: {
		PermRead: true, PermWorkItemWrite: true, PermRunControl: true,
		PermApproval: true, PermAgentWrite: true, PermRuntimeManage: true,
	},
	domain.RoleOperator: {
		PermRead: true, PermWorkItemWrite: true, PermRunControl: true,
	},
	domain.RoleApprover: {
		PermRead: true, PermApproval: true,
	},
	domain.RoleViewer: {
		PermRead: true,
	},
}

// Allow 校验角色是否持有权限；未知角色一律拒绝。
func Allow(role domain.MemberRole, perm string) bool {
	perms, ok := rolePerms[role]
	if !ok {
		return false
	}
	return perms[perm]
}

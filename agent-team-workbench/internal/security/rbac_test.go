package security

import (
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestRBACMatrix(t *testing.T) {
	cases := []struct {
		role domain.MemberRole
		perm string
		ok   bool
	}{
		// Operator：任务与运行控制可以，Runtime/审批/管理不行。
		{domain.RoleOperator, PermWorkItemWrite, true},
		{domain.RoleOperator, PermRunControl, true},
		{domain.RoleOperator, PermApproval, false},
		{domain.RoleOperator, PermRuntimeManage, false},
		// Approver：只能审批与只读。
		{domain.RoleApprover, PermApproval, true},
		{domain.RoleApprover, PermRunControl, false},
		// Viewer：只读。
		{domain.RoleViewer, PermRead, true},
		{domain.RoleViewer, PermWorkItemWrite, false},
		// Admin：全管理但无凭据明文权限（权限点不存在于任何角色）。
		{domain.RoleAdmin, PermWorkspaceAdmin, false},
		{domain.RoleAdmin, PermRuntimeManage, true},
		{domain.RoleOwner, PermWorkspaceAdmin, true},
		// 任何角色都没有 credential.read。
		{domain.RoleOwner, "credential.read", false},
		{domain.RoleAdmin, "credential.read", false},
		// 未知角色拒绝。
		{domain.MemberRole("intruder"), PermRead, false},
	}
	for _, c := range cases {
		if got := Allow(c.role, c.perm); got != c.ok {
			t.Errorf("Allow(%s, %s) = %v, want %v", c.role, c.perm, got, c.ok)
		}
	}
}

package core

import "testing"

func TestProjectRolePermissions(t *testing.T) {
	tests := []struct {
		role       string
		permission Permission
		allowed    bool
	}{
		{RoleProjectAdmin, PermissionProjectManage, true},
		{RoleProjectAdmin, PermissionDeploymentRun, true},
		{RoleProjectAdmin, PermissionAccessManage, false},
		{RoleOperator, PermissionProjectConfigure, true},
		{RoleOperator, PermissionStageApprove, true},
		{RoleOperator, PermissionProjectManage, false},
		{RoleDeployer, PermissionDeploymentRun, true},
		{RoleDeployer, PermissionDeploymentCancel, true},
		{RoleDeployer, PermissionProjectConfigure, false},
		{RoleViewer, PermissionProjectView, true},
		{RoleViewer, PermissionDeploymentRun, false},
	}
	for _, test := range tests {
		if got := RoleAllows(test.role, test.permission); got != test.allowed {
			t.Errorf("RoleAllows(%q, %q) = %v, want %v", test.role, test.permission, got, test.allowed)
		}
	}
}

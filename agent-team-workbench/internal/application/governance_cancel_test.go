package application

import (
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestCancelRunDecisionCoversEveryNonTerminalRunState(t *testing.T) {
	tests := []struct {
		status     domain.RunStatus
		target     domain.RunStatus
		action     string
		transition bool
	}{
		{domain.RunQueued, domain.RunCancelled, "cancel", true},
		{domain.RunStarting, domain.RunCancelled, "cancel", true},
		{domain.RunRunning, domain.RunCancelling, "cancel", true},
		{domain.RunWaitingApproval, domain.RunCancelling, "cancel", true},
		{domain.RunInterrupting, domain.RunInterrupting, "interrupt", false},
		{domain.RunCancelling, domain.RunCancelling, "cancel", false},
		{domain.RunReconnecting, domain.RunCancelled, "cancel", true},
		{domain.RunSucceeding, domain.RunCancelled, "cancel", true},
	}
	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			target, action, transition, err := cancelRunDecision(tc.status)
			if err != nil || target != tc.target || action != tc.action || transition != tc.transition {
				t.Fatalf("status=%s target=%s action=%s transition=%t err=%v; want target=%s action=%s transition=%t",
					tc.status, target, action, transition, err, tc.target, tc.action, tc.transition)
			}
		})
	}
}

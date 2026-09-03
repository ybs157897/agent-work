package application

import (
	"errors"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

func TestClassifyPlanSubmissionErrorUsesTypedFailureClass(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want domain.GovernanceErrorCode
	}{
		{name: "semantic", err: domain.ErrValidation, want: domain.GovernanceErrorPlanSemanticValidation},
		{name: "authority", err: markPlanSubmissionFailure(planSubmissionFailureAuthority, domain.ErrValidation), want: domain.GovernanceErrorPlanAuthorityDenied},
		{name: "quota", err: markPlanSubmissionFailure(planSubmissionFailureQuota, domain.ErrValidation), want: domain.GovernanceErrorPlanQuotaDenied},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyPlanSubmissionError(tc.err)
			if got.Code != tc.want {
				t.Fatalf("code=%s want=%s err=%v", got.Code, tc.want, got)
			}
			if !errors.Is(got, domain.ErrValidation) {
				t.Fatalf("classified error must preserve the public validation sentinel: %v", got)
			}
		})
	}
}

func TestPlanDecisionControlSnapshotSelectsOnlySupportedNativeMode(t *testing.T) {
	contextData := map[string]any{"role": coordinatorRole, "action": "repair_plan", "repair": map[string]any{"repair_attempt": 2}}
	tests := []struct {
		name string
		caps map[string]string
		want string
	}{
		{name: "text", caps: map[string]string{
			runtime.CapabilitySchemaConstrainedOutput: string(runtime.CapUnavailable),
			runtime.CapabilityControlToolCall:         string(runtime.CapUnavailable),
		}, want: planDecisionTransportText},
		{name: "experimental schema does not win", caps: map[string]string{
			runtime.CapabilitySchemaConstrainedOutput: string(runtime.CapExperimental),
			runtime.CapabilityControlToolCall:         string(runtime.CapSupported),
		}, want: planDecisionTransportTool},
		{name: "supported schema has priority", caps: map[string]string{
			runtime.CapabilitySchemaConstrainedOutput: string(runtime.CapSupported),
			runtime.CapabilityControlToolCall:         string(runtime.CapSupported),
		}, want: planDecisionTransportSchema},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := planDecisionControlSnapshot(&domain.RuntimeBinding{Capabilities: tc.caps}, contextData)
			if snapshot["transport_mode"] != tc.want || snapshot["repair_attempt"] != 2 {
				t.Fatalf("snapshot=%#v want mode=%s", snapshot, tc.want)
			}
		})
	}
}

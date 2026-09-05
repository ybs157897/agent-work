package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// TestWorkItemDTOPassesThroughLockFields 防回归（F1 执行锁）：workItemDTO 必须透传
// locked_by_run_id/locked_at（任务卡锁标记依赖），无锁时 omitted 不出现空键。
func TestWorkItemDTOPassesThroughLockFields(t *testing.T) {
	lockedAt := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	locked := toWorkItemDTO(&domain.WorkItem{ID: "wi_1", WorkspaceID: "ws_1", Title: "t",
		Status: domain.WorkItemInProgress, LockedByRunID: "run_9", LockedAt: &lockedAt, Version: 2})
	if locked.LockedByRunID != "run_9" || locked.LockedAt == nil || !locked.LockedAt.Equal(lockedAt) {
		t.Fatalf("锁字段应透传: %+v", locked)
	}
	body, err := json.Marshal(locked)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"locked_by_run_id":"run_9"`) || !strings.Contains(string(body), `"locked_at":"`) {
		t.Fatalf("锁字段应序列化: %s", body)
	}

	unlocked := toWorkItemDTO(&domain.WorkItem{ID: "wi_2", WorkspaceID: "ws_1", Title: "t",
		Status: domain.WorkItemTodo, Version: 1})
	if unlocked.LockedByRunID != "" || unlocked.LockedAt != nil {
		t.Fatalf("无锁 DTO 字段应为零值: %+v", unlocked)
	}
	body, err = json.Marshal(unlocked)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "locked_by_run_id") || strings.Contains(string(body), "locked_at") {
		t.Fatalf("无锁字段应 omitted: %s", body)
	}
}

func TestCreateRunRequestDecodesOutputContractWithoutTouchingInstruction(t *testing.T) {
	var req createRunRequest
	if err := json.Unmarshal([]byte(`{"agent_profile_id":"agent_1","output_contract":"languagegui/v1","input":{"instruction":"用户原文"}}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.OutputContract != "languagegui/v1" || req.Input.Instruction != "用户原文" {
		t.Fatalf("create run request decode mismatch: %+v", req)
	}
}

func TestDispatchRunRoleSeparatesWorkerCoordinatorAndEvaluation(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{name: "legacy worker", input: map[string]any{"instruction": "work"}, want: "worker"},
		{name: "governed worker", input: map[string]any{"task_coordinator": map[string]any{"role": "worker"}}, want: "worker"},
		{name: "coordinator", input: map[string]any{"task_coordinator": map[string]any{"role": "coordinator", "action": "wakeup"}}, want: "coordinator"},
		{name: "evaluation", input: map[string]any{"task_coordinator": map[string]any{"role": "coordinator", "action": "evaluation"}}, want: "evaluation"},
		{name: "evaluation action proves evaluation", input: map[string]any{"task_coordinator": map[string]any{"action": "evaluation"}}, want: "evaluation"},
		{name: "unknown protected role", input: map[string]any{"task_coordinator": map[string]any{"role": "future"}}, want: "coordinator"},
		{name: "malformed protected envelope", input: map[string]any{"task_coordinator": "not-an-envelope"}, want: "coordinator"},
		{name: "null protected envelope", input: map[string]any{"task_coordinator": nil}, want: "coordinator"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := dispatchRunRole(&domain.ExecutionRun{Input: test.input}); got != test.want {
				t.Fatalf("dispatchRunRole() = %q, want %q", got, test.want)
			}
		})
	}
}

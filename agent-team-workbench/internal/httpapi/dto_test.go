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

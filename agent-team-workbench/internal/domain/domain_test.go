package domain

import (
	"errors"
	"testing"
	"time"
)

var now = time.Date(2026, 8, 21, 6, 0, 0, 0, time.UTC)

func TestWorkItemStateMachine(t *testing.T) {
	cases := []struct {
		from, to WorkItemStatus
		ok       bool
	}{
		{WorkItemTodo, WorkItemInProgress, true},
		{WorkItemTodo, WorkItemCancelled, true},
		{WorkItemTodo, WorkItemBlocked, false},
		{WorkItemTodo, WorkItemCompleted, false},
		{WorkItemInProgress, WorkItemBlocked, true},
		{WorkItemInProgress, WorkItemCompleted, true},
		{WorkItemInProgress, WorkItemTodo, false},
		{WorkItemBlocked, WorkItemInProgress, true},
		{WorkItemBlocked, WorkItemCompleted, false},
		{WorkItemCompleted, WorkItemInProgress, false},
		{WorkItemCancelled, WorkItemTodo, false},
	}
	for _, c := range cases {
		w := &WorkItem{ID: NewID(PrefixWorkItem), Status: c.from, Version: 1}
		err := w.Transition(c.to, now)
		if c.ok && err != nil {
			t.Errorf("%s -> %s: expected ok, got %v", c.from, c.to, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s -> %s: expected error, got nil", c.from, c.to)
		}
		if !c.ok && err != nil && !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("%s -> %s: expected ErrIllegalTransition, got %v", c.from, c.to, err)
		}
	}
}

func TestWorkItemReviewGate(t *testing.T) {
	w := &WorkItem{Status: WorkItemTodo, Version: 1}
	// 未进入评审不能 completed
	if err := w.Accept(now); err == nil {
		t.Fatal("accept before review should fail")
	}
	if err := w.Transition(WorkItemInProgress, now); err != nil {
		t.Fatal(err)
	}
	if err := w.EnterReview(now); err != nil {
		t.Fatal(err)
	}
	if w.Phase != PhaseReview {
		t.Fatalf("expected phase review, got %q", w.Phase)
	}
	if err := w.Accept(now); err != nil {
		t.Fatal(err)
	}
	if w.Status != WorkItemCompleted {
		t.Fatalf("expected completed, got %s", w.Status)
	}
	// 终态不可逆
	if err := w.Transition(WorkItemInProgress, now); err == nil {
		t.Fatal("terminal state must be immutable")
	}
}

func TestWorkItemVersionConflict(t *testing.T) {
	w := &WorkItem{Version: 7}
	if err := w.CheckVersion(8); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("expected version conflict, got %v", err)
	}
	if err := w.CheckVersion(7); err != nil {
		t.Fatal(err)
	}
}

func TestRunStateMachine(t *testing.T) {
	cases := []struct {
		from, to RunStatus
		ok       bool
	}{
		{RunQueued, RunStarting, true},
		{RunQueued, RunRunning, false},
		{RunStarting, RunRunning, true},
		{RunRunning, RunWaitingApproval, true},
		{RunWaitingApproval, RunRunning, true},
		{RunRunning, RunInterrupting, true},
		{RunInterrupting, RunInterrupted, true},
		{RunRunning, RunCancelling, true},
		{RunCancelling, RunCancelled, true},
		{RunRunning, RunReconnecting, true},
		{RunReconnecting, RunLost, true},
		{RunReconnecting, RunRunning, true},
		{RunRunning, RunSucceeding, true},
		{RunSucceeding, RunSucceeded, true},
		{RunRunning, RunTodoBack(t), false},
	}
	for _, c := range cases {
		r := &ExecutionRun{Status: c.from, Version: 1}
		err := r.Transition(c.to, now)
		if c.ok != (err == nil) {
			t.Errorf("%s -> %s: ok=%v err=%v", c.from, c.to, c.ok, err)
		}
	}
}

func RunTodoBack(t *testing.T) RunStatus { t.Helper(); return RunQueued }

func TestRunTerminalImmutableAndFinishedAt(t *testing.T) {
	r := &ExecutionRun{Status: RunRunning, Version: 1}
	if err := r.Transition(RunSucceeding, now); err != nil {
		t.Fatal(err)
	}
	if err := r.Transition(RunSucceeded, now); err != nil {
		t.Fatal(err)
	}
	if r.FinishedAt == nil {
		t.Fatal("terminal run must have FinishedAt")
	}
	if err := r.Transition(RunRunning, now); err == nil {
		t.Fatal("terminal run must be immutable")
	}
	if err := r.MarkFailed(RunFailure{Code: "x"}, now); err == nil {
		t.Fatal("cannot fail a terminal run")
	}
}

func TestApprovalResolveIdempotent(t *testing.T) {
	a := &ApprovalRequest{Status: ApprovalPending}
	if err := a.Resolve(ApprovalApproved, "user_1", "ok", now); err != nil {
		t.Fatal(err)
	}
	// 幂等：重复相同决定返回成功
	if err := a.Resolve(ApprovalApproved, "user_1", "ok", now); err != nil {
		t.Fatalf("repeat same decision should be idempotent, got %v", err)
	}
	// 冲突决定报错
	if err := a.Resolve(ApprovalRejected, "user_2", "", now); err == nil {
		t.Fatal("conflicting decision should fail")
	}
}

func TestEventNameWhitelist(t *testing.T) {
	if _, err := NewCanonicalEvent("ws_1", "evil.event", AggregateWorkItem, "wi_1", 1, nil); err == nil {
		t.Fatal("unknown event name must be rejected")
	}
	ev, err := NewCanonicalEvent("ws_1", EventWorkItemMoved, AggregateWorkItem, "wi_1", 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ev.ContractVersion != "events/v1" || ev.Aggregate.Version != 2 {
		t.Fatalf("unexpected envelope: %+v", ev)
	}
}

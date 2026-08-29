package domain

import (
	"errors"
	"testing"
	"time"
)

// TestDispatchStateMachine 派发批次状态机防回归：running → collecting →
// completed/degraded；@直达批次（无 lead 汇总环节）允许 running 直达收口；
// 终态不可逆；未知迁移一律拒绝。
func TestDispatchStateMachine(t *testing.T) {
	cases := []struct {
		from, to DispatchStatus
		ok       bool
	}{
		{DispatchRunning, DispatchCollecting, true},
		{DispatchRunning, DispatchCompleted, true}, // @直达：成员全终态直接收口
		{DispatchRunning, DispatchDegraded, true},
		{DispatchRunning, DispatchCancelled, true},
		{DispatchRunning, DispatchRunning, false},
		{DispatchCollecting, DispatchCompleted, true},
		{DispatchCollecting, DispatchDegraded, true},
		{DispatchCollecting, DispatchCancelled, true},
		{DispatchCollecting, DispatchCollecting, false},
		{DispatchCollecting, DispatchRunning, false}, // 汇总即收口，不再回 running（防循环）
		{DispatchCompleted, DispatchCollecting, false},
		{DispatchDegraded, DispatchCompleted, false},
		{DispatchCancelled, DispatchRunning, false},
	}
	for _, c := range cases {
		d := &Dispatch{ID: NewID(PrefixDispatch), Status: c.from}
		err := d.Transition(c.to, now)
		if c.ok != (err == nil) {
			t.Errorf("%s -> %s: ok=%v err=%v", c.from, c.to, c.ok, err)
			continue
		}
		if !c.ok && err != nil && !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("%s -> %s: expected ErrIllegalTransition, got %v", c.from, c.to, err)
		}
	}
}

// TestDispatchTerminalImmutableAndClosedAt 终态封口：ClosedAt 落时刻且不可再迁移。
func TestDispatchTerminalImmutableAndClosedAt(t *testing.T) {
	d := &Dispatch{ID: NewID(PrefixDispatch), Status: DispatchRunning, CreatedAt: now}
	if d.ClosedAt != nil {
		t.Fatal("非终态批次不得有 closed_at")
	}
	if err := d.Transition(DispatchCollecting, now); err != nil {
		t.Fatal(err)
	}
	closed := now.Add(time.Minute)
	if err := d.Transition(DispatchCompleted, closed); err != nil {
		t.Fatal(err)
	}
	if d.ClosedAt == nil || !d.ClosedAt.Equal(closed) {
		t.Fatalf("closed_at 应为终态时刻: %v", d.ClosedAt)
	}
	if !d.Status.IsTerminal() {
		t.Fatal("completed 应为终态")
	}
	if err := d.Transition(DispatchDegraded, closed); err == nil {
		t.Fatal("终态批次不可再迁移")
	}
}

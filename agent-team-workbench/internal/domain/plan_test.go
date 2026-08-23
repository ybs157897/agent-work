package domain

import (
	"errors"
	"testing"
)

// TestPlanStateMachine 防回归：plan 状态机非法迁移必须拒绝（终态不可逆、
// 起点不在生命周期图上的迁移一律 ErrIllegalTransition）。
func TestPlanStateMachine(t *testing.T) {
	cases := []struct {
		from, to PlanStatus
		ok       bool
	}{
		{PlanActive, PlanWaiting, true},
		{PlanActive, PlanFinished, true},
		{PlanActive, PlanCancelled, true},
		{PlanActive, PlanFailed, true},
		{PlanWaiting, PlanFinished, true}, // supersede 路径
		{PlanWaiting, PlanCancelled, true},
		{PlanWaiting, PlanFailed, true},
		{PlanWaiting, PlanActive, false}, // defer 即批次终止，无游标回拨
		{PlanFinished, PlanActive, false},
		{PlanFinished, PlanWaiting, false},
		{PlanCancelled, PlanActive, false},
		{PlanFailed, PlanFinished, false},
		{PlanActive, PlanActive, false},
	}
	for _, c := range cases {
		p := &Plan{ID: NewID(PrefixPlan), Status: c.from, Version: 1}
		err := p.Transition(c.to, now)
		if c.ok != (err == nil) {
			t.Errorf("%s -> %s: ok=%v err=%v", c.from, c.to, c.ok, err)
		}
		if !c.ok && err != nil && !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("%s -> %s: expected ErrIllegalTransition, got %v", c.from, c.to, err)
		}
	}
}

func TestPlanFinishRecordsSupersede(t *testing.T) {
	p := &Plan{Status: PlanWaiting, Version: 3}
	if err := p.Finish(now, "plan_new"); err != nil {
		t.Fatal(err)
	}
	if p.Status != PlanFinished || p.SupersededBy != "plan_new" {
		t.Fatalf("supersede 未记录: %s -> %q", p.Status, p.SupersededBy)
	}
	if !p.Status.IsTerminal() {
		t.Fatal("finished 必须是终态")
	}
	// 终态不可再迁移
	if err := p.MarkWaiting(now); err == nil {
		t.Fatal("终态 plan 不可迁移")
	}
}

func TestPlanVerbWhitelist(t *testing.T) {
	for _, v := range []PlanVerb{PlanVerbDispatch, PlanVerbDefer, PlanVerbFinish, PlanVerbConsultKnowledge} {
		if !ValidPlanVerb(v) {
			t.Errorf("%s 应为合法 verb", v)
		}
	}
	for _, v := range []PlanVerb{"join", "use_session", ""} {
		if ValidPlanVerb(v) {
			t.Errorf("%q 不在词汇表内，应被拒绝", v)
		}
	}
}

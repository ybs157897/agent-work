package application

import (
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestGovernanceTurnDecisionKindCoversEveryControlOutcome(t *testing.T) {
	worker := "agent_decision_worker"
	decision := func(verb domain.PlanVerb) *domain.PlanDecisionV2 {
		out := &domain.PlanDecisionV2{SchemaVersion: "plan-decision/v2", Kind: "plan",
			Reason: "test", NextAction: "test", Steps: []domain.PlanDecisionStepV2{}}
		switch verb {
		case domain.PlanVerbDispatch:
			out.Steps = []domain.PlanDecisionStepV2{{Verb: verb, Dispatch: &domain.PlanDispatchStepV2{
				AgentID: worker, Title: "test", Instruction: "test", Acceptance: []string{"test"}}}}
		case domain.PlanVerbDefer:
			out.Steps = []domain.PlanDecisionStepV2{{Verb: verb, Defer: &domain.PlanDeferStepV2{}}}
		case domain.PlanVerbJoin:
			out.Steps = []domain.PlanDecisionStepV2{{Verb: verb, Join: &domain.PlanJoinStepV2{Children: domain.JoinChildren{All: true}}}}
		case domain.PlanVerbFinish:
			out.Steps = []domain.PlanDecisionStepV2{{Verb: verb, Finish: &domain.PlanFinishStepV2{}}}
		}
		return out
	}
	cases := []struct {
		name   string
		action string
		input  *domain.PlanDecisionV2
		want   domain.TurnDecisionKind
	}{
		{name: "repair", action: "repair_plan", input: decision(domain.PlanVerbDispatch), want: domain.TurnDecisionRepair},
		{name: "replan", action: "recover", input: decision(domain.PlanVerbDispatch), want: domain.TurnDecisionReplan},
		{name: "wait defer", action: "intake", input: decision(domain.PlanVerbDefer), want: domain.TurnDecisionWait},
		{name: "wait join", action: "intake", input: decision(domain.PlanVerbJoin), want: domain.TurnDecisionWait},
		{name: "finish", action: "intake", input: decision(domain.PlanVerbFinish), want: domain.TurnDecisionFinish},
		{name: "execute", action: "intake", input: decision(domain.PlanVerbDispatch), want: domain.TurnDecisionExecute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := &domain.ExecutionRun{Input: map[string]any{
				"task_coordinator": map[string]any{"action": tc.action},
			}}
			if got := governanceTurnDecisionKind(run, tc.input); got != tc.want {
				t.Fatalf("decision kind=%q want=%q", got, tc.want)
			}
		})
	}
}

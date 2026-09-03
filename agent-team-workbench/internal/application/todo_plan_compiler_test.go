package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	workbenchcontracts "github.com/ybs/agent-team-workbench/contracts"
	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

type todoPlanCompilerFixture struct {
	ctx        context.Context
	svc        *application.Service
	store      application.Store
	dispatcher *captureDispatcher
	workspace  string
	root       *domain.WorkItem
	goal       *domain.Goal
	todo       *domain.Todo
	header     *domain.TurnReceiptHeader
	source     *domain.ExecutionRun
	ownerID    string
	workerID   string
}

func newTodoPlanCompilerFixture(t *testing.T) *todoPlanCompilerFixture {
	return newTodoPlanCompilerFixtureWithDigest(t, "")
}

func newTodoPlanCompilerFixtureWithDigest(t *testing.T, digestOverride string) *todoPlanCompilerFixture {
	t.Helper()
	ctx, svc, store, dispatcher, workspaceID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, workspaceID, application.CreateWorkItemParams{
		Title: "Todo compiler root", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"root result is accepted"},
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatalf("get coordinator state: %v", err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatalf("get governance goal: %v", err)
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatalf("get todo: %v", err)
	}
	claimed, err := svc.ClaimTodo(ctx, todo.ID, state.CoordinatorAgentID, todo.Version,
		time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("claim todo: %v", err)
	}
	inputDigest, err := application.ComputeTodoPlanInputSnapshotDigest(goal, claimed, dispatcher.runs[0])
	if err != nil {
		t.Fatalf("compute input snapshot digest: %v", err)
	}
	if digestOverride != "" {
		inputDigest = digestOverride
	}
	header, err := svc.AdmitTurn(ctx, application.AdmitTurnParams{
		GoalID: goal.ID, TodoID: claimed.ID, OwnerAgentID: state.CoordinatorAgentID,
		ExpectedTodoVersion: claimed.Version, Attempt: 1, SchemaVersion: "plan-decision/v2",
		InputSnapshotDigest: inputDigest, AdmissionClientKey: "compiler-admit-1",
	})
	if err != nil {
		t.Fatalf("admit turn: %v", err)
	}
	source := dispatcher.runs[0]
	markCompilerSourceSucceeded(t, ctx, store, source.ID)
	source, err = store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &todoPlanCompilerFixture{
		ctx: ctx, svc: svc, store: store, dispatcher: dispatcher, workspace: workspaceID,
		root: root, goal: goal, todo: claimed, header: header, source: source,
		ownerID: state.CoordinatorAgentID, workerID: workerID,
	}
}

func markCompilerSourceSucceeded(t *testing.T, ctx context.Context, store application.Store, runID string) {
	t.Helper()
	run, err := store.Runs().Get(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, next := range []domain.RunStatus{
		domain.RunStarting, domain.RunRunning, domain.RunSucceeding, domain.RunSucceeded,
	} {
		expected := run.Version
		if err := run.Transition(next, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		if err := store.Runs().Update(ctx, run, expected); err != nil {
			t.Fatal(err)
		}
	}
}

func compilerTestDigest(ch byte) string {
	const prefix = "sha256:"
	return prefix + repeatCompilerByte(ch, 64)
}

func repeatCompilerByte(ch byte, count int) string {
	out := make([]byte, count)
	for i := range out {
		out[i] = ch
	}
	return string(out)
}

func compilerDecision(verb domain.PlanVerb, workerID string) *domain.PlanDecisionV2 {
	decision := &domain.PlanDecisionV2{
		SchemaVersion: "plan-decision/v2", Kind: "plan",
		Reason: "bounded compiler test", NextAction: "continue deterministically",
	}
	switch verb {
	case domain.PlanVerbDispatch:
		decision.Steps = []domain.PlanDecisionStepV2{
			{Verb: domain.PlanVerbDispatch, Dispatch: &domain.PlanDispatchStepV2{
				AgentID: workerID, Title: "bounded work", Instruction: "perform bounded work",
				Acceptance: []string{"work is complete"},
			}},
			{Verb: domain.PlanVerbJoin, Join: &domain.PlanJoinStepV2{Children: domain.JoinChildren{All: true}}},
		}
	case domain.PlanVerbConsultKnowledge:
		decision.Steps = []domain.PlanDecisionStepV2{{Verb: domain.PlanVerbConsultKnowledge,
			ConsultKnowledge: &domain.PlanConsultKnowledgeStepV2{Corpus: "docs", Terms: []string{"compiler"}}}}
	case domain.PlanVerbDefer:
		decision.Steps = []domain.PlanDecisionStepV2{{Verb: domain.PlanVerbDefer,
			Defer: &domain.PlanDeferStepV2{}}}
	case domain.PlanVerbJoin:
		decision.Steps = []domain.PlanDecisionStepV2{{Verb: domain.PlanVerbJoin,
			Join: &domain.PlanJoinStepV2{Children: domain.JoinChildren{All: true}}}}
	case domain.PlanVerbFinish:
		evaluation := false
		decision.Steps = []domain.PlanDecisionStepV2{{Verb: domain.PlanVerbFinish,
			Finish: &domain.PlanFinishStepV2{Evaluation: &evaluation}}}
	}
	return decision
}

func compileInput(f *todoPlanCompilerFixture, decision *domain.PlanDecisionV2) application.TodoToPlanCompileInput {
	return application.TodoToPlanCompileInput{
		TurnKey: f.header.TurnKey, OwnerAgentID: f.ownerID, SourceRunID: f.source.ID,
		Decision: decision, SchemaDigest: workbenchcontracts.PlanDecisionV2SchemaDigest(),
	}
}

func TestCompileTodoPlanFiveVerbsIsPureAndDeterministic(t *testing.T) {
	f := newTodoPlanCompilerFixture(t)
	stateBefore, err := f.store.TaskCoordinators().GetState(f.ctx, f.root.ID)
	if err != nil {
		t.Fatal(err)
	}
	runsBefore, err := f.store.Runs().ListByWorkItem(f.ctx, f.root.ID)
	if err != nil {
		t.Fatal(err)
	}
	phasesBefore, err := f.store.TurnReceipts().ListPhases(f.ctx, f.header.TurnKey)
	if err != nil {
		t.Fatal(err)
	}
	if plan, err := f.store.Plans().LatestByWorkItem(f.ctx, f.root.ID); err != nil || plan != nil {
		t.Fatalf("fixture must not start with a Plan: plan=%+v err=%v", plan, err)
	}
	dispatcherBefore := len(f.dispatcher.runs)

	cases := []struct {
		name      string
		verb      domain.PlanVerb
		stepCount int
		firstVerb domain.PlanVerb
	}{
		{name: "dispatch", verb: domain.PlanVerbDispatch, stepCount: 2, firstVerb: domain.PlanVerbDispatch},
		{name: "consult_knowledge", verb: domain.PlanVerbConsultKnowledge, stepCount: 1, firstVerb: domain.PlanVerbConsultKnowledge},
		{name: "defer", verb: domain.PlanVerbDefer, stepCount: 1, firstVerb: domain.PlanVerbDefer},
		{name: "join", verb: domain.PlanVerbJoin, stepCount: 1, firstVerb: domain.PlanVerbJoin},
		{name: "finish", verb: domain.PlanVerbFinish, stepCount: 1, firstVerb: domain.PlanVerbFinish},
	}
	var firstDigest string
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			compiled, err := f.svc.CompileTodoPlan(f.ctx, compileInput(f, compilerDecision(tc.verb, f.workerID)))
			if err != nil {
				t.Fatal(err)
			}
			if compiled.WorkItemID != f.root.ID || compiled.AgentProfileID != f.ownerID ||
				compiled.SourceRunID != f.source.ID || compiled.TurnKey != f.header.TurnKey {
				t.Fatalf("compiler identity mismatch: %+v", compiled)
			}
			if compiled.PlanClientKey != "governance:"+f.header.TurnKey.GoalID+":"+f.header.TurnKey.TodoID+":1" {
				t.Fatalf("unexpected governance Plan client key: %q", compiled.PlanClientKey)
			}
			if len(compiled.Steps) != tc.stepCount || compiled.Steps[0].Verb != string(tc.firstVerb) {
				t.Fatalf("unexpected compiled steps: %+v", compiled.Steps)
			}
			if compiled.Guardrails.MaxDispatch == nil || *compiled.Guardrails.MaxDispatch != f.todo.DecisionScope.MaxDispatch {
				t.Fatalf("compiler must carry Todo max_dispatch guardrail: %+v", compiled.Guardrails)
			}
			if compiled.Audit == nil || compiled.Audit.SchemaVersion != "plan-decision/v2" ||
				compiled.Audit.StepCount != tc.stepCount {
				t.Fatalf("unexpected decision audit: %+v", compiled.Audit)
			}
			if compiled.SchemaDigest != workbenchcontracts.PlanDecisionV2SchemaDigest() ||
				!domain.ValidCanonicalDigest(compiled.DecisionDigest) {
				t.Fatalf("invalid compiler digests: %+v", compiled)
			}
			if firstDigest == "" {
				firstDigest = compiled.DecisionDigest
			}
			again, err := f.svc.CompileTodoPlan(f.ctx, compileInput(f, compilerDecision(tc.verb, f.workerID)))
			if err != nil || again.DecisionDigest != compiled.DecisionDigest {
				t.Fatalf("same typed decision must compile deterministically: again=%+v err=%v", again, err)
			}
		})
	}

	stateAfter, err := f.store.TaskCoordinators().GetState(f.ctx, f.root.ID)
	if err != nil {
		t.Fatal(err)
	}
	runsAfter, err := f.store.Runs().ListByWorkItem(f.ctx, f.root.ID)
	if err != nil {
		t.Fatal(err)
	}
	phasesAfter, err := f.store.TurnReceipts().ListPhases(f.ctx, f.header.TurnKey)
	if err != nil {
		t.Fatal(err)
	}
	if stateAfter.Version != stateBefore.Version || len(runsAfter) != len(runsBefore) ||
		len(phasesAfter) != len(phasesBefore) || len(f.dispatcher.runs) != dispatcherBefore {
		t.Fatalf("pure compiler changed execution/governance state: before state=%+v runs=%d phases=%d dispatch=%d after state=%+v runs=%d phases=%d dispatch=%d",
			stateBefore, len(runsBefore), len(phasesBefore), dispatcherBefore,
			stateAfter, len(runsAfter), len(phasesAfter), len(f.dispatcher.runs))
	}
	if plan, err := f.store.Plans().LatestByWorkItem(f.ctx, f.root.ID); err != nil || plan != nil {
		t.Fatalf("pure compiler must not persist Plan: plan=%+v err=%v", plan, err)
	}
}

func TestCompileTodoPlanRejectsAuthorityAndSchemaDrift(t *testing.T) {
	t.Run("receipt input snapshot digest mismatch", func(t *testing.T) {
		f := newTodoPlanCompilerFixtureWithDigest(t, compilerTestDigest('f'))
		if _, err := f.svc.CompileTodoPlan(f.ctx, compileInput(f, compilerDecision(domain.PlanVerbFinish, f.workerID))); err == nil {
			t.Fatal("forged receipt input snapshot digest must fail closed")
		}
	})

	t.Run("schema digest mismatch has no side effect", func(t *testing.T) {
		f := newTodoPlanCompilerFixture(t)
		before, err := f.store.Runs().ListByWorkItem(f.ctx, f.root.ID)
		if err != nil {
			t.Fatal(err)
		}
		input := compileInput(f, compilerDecision(domain.PlanVerbDefer, f.workerID))
		input.SchemaDigest = compilerTestDigest('b')
		if _, err := f.svc.CompileTodoPlan(f.ctx, input); !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("schema digest drift must be validation error: %v", err)
		}
		after, err := f.store.Runs().ListByWorkItem(f.ctx, f.root.ID)
		if err != nil || len(after) != len(before) {
			t.Fatalf("schema rejection must not create Run: before=%d after=%d err=%v", len(before), len(after), err)
		}
	})

	t.Run("non-succeeded source is rejected", func(t *testing.T) {
		f := newTodoPlanCompilerFixture(t)
		source, err := f.store.Runs().Get(f.ctx, f.source.ID)
		if err != nil {
			t.Fatal(err)
		}
		source.Status = domain.RunFailed
		source.Failure = &domain.RunFailure{Code: "source_failed"}
		if err := f.store.Runs().Update(f.ctx, source, source.Version); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.CompileTodoPlan(f.ctx, compileInput(f, compilerDecision(domain.PlanVerbDefer, f.workerID))); !errors.Is(err, domain.ErrStateConflict) {
			t.Fatalf("non-succeeded source must be rejected as state conflict: %v", err)
		}
	})

	t.Run("wrong claim owner is rejected", func(t *testing.T) {
		f := newTodoPlanCompilerFixture(t)
		input := compileInput(f, compilerDecision(domain.PlanVerbDefer, f.workerID))
		input.OwnerAgentID = f.workerID
		if _, err := f.svc.CompileTodoPlan(f.ctx, input); !errors.Is(err, domain.ErrStateConflict) {
			t.Fatalf("wrong claim owner must be rejected: %v", err)
		}
	})

	t.Run("inactive Goal is rejected", func(t *testing.T) {
		f := newTodoPlanCompilerFixture(t)
		if _, err := f.svc.PauseGoal(f.ctx, f.goal.ID, f.goal.Version); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.CompileTodoPlan(f.ctx, compileInput(f, compilerDecision(domain.PlanVerbDefer, f.workerID))); !errors.Is(err, domain.ErrStateConflict) {
			t.Fatalf("inactive Goal must be rejected: %v", err)
		}
	})
}

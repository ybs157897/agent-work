package application

import (
	"errors"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestRejectedPlanDispatchOnlyBlocksTheCurrentWaitingTurn(t *testing.T) {
	key := domain.TurnKey{GoalID: "goal_current", TodoID: "todo_current", TurnSeq: 2}
	for _, tc := range []struct {
		name    string
		todo    *domain.Todo
		want    bool
		wantErr bool
	}{
		{name: "current waiting", todo: &domain.Todo{ID: key.TodoID, GoalID: key.GoalID, LastTurnSeq: 2, Status: domain.TodoWaiting}, want: true},
		{name: "newer turn owns control line", todo: &domain.Todo{ID: key.TodoID, GoalID: key.GoalID, LastTurnSeq: 3, Status: domain.TodoRunning}},
		{name: "same turn already moved", todo: &domain.Todo{ID: key.TodoID, GoalID: key.GoalID, LastTurnSeq: 2, Status: domain.TodoPending}},
		{name: "watermark behind", todo: &domain.Todo{ID: key.TodoID, GoalID: key.GoalID, LastTurnSeq: 1, Status: domain.TodoWaiting}, wantErr: true},
		{name: "wrong goal", todo: &domain.Todo{ID: key.TodoID, GoalID: "goal_other", LastTurnSeq: 2, Status: domain.TodoWaiting}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rejectedPlanDispatchTurnIsCurrent(tc.todo, key)
			if tc.wantErr {
				if !errors.Is(err, domain.ErrStateConflict) {
					t.Fatalf("identity mismatch must fail closed: %v", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("current=%v err=%v, want current=%v", got, err, tc.want)
			}
		})
	}
}

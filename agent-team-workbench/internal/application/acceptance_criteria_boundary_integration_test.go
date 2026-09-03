package application_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestAutoCoordinatorRequiresBoundedAcceptanceCriteriaAndPersistsNormalizedCopy(t *testing.T) {
	ctx, svc, store, _, workspaceID, _ := seedCoordinatorEnv(t)
	for name, criteria := range map[string][]string{
		"missing":          nil,
		"empty_after_trim": {" ", "\t"},
		"too_many": func() []string {
			values := make([]string, 65)
			for i := range values {
				values[i] = "criterion"
			}
			return values
		}(),
		"too_long": {strings.Repeat("x", 2001)},
	} {
		t.Run(name, func(t *testing.T) {
			before, _, err := store.WorkItems().List(ctx, workspaceID, application.WorkItemFilter{Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			_, err = svc.CreateWorkItem(ctx, workspaceID, application.CreateWorkItemParams{
				Title: "invalid coordinator root", RecordKind: domain.RecordKindTask,
				AutoCoordinate: true, AcceptanceCriteria: criteria,
			})
			if !errors.Is(err, domain.ErrValidation) {
				t.Fatalf("invalid criteria must fail closed: %v", err)
			}
			after, _, err := store.WorkItems().List(ctx, workspaceID, application.WorkItemFilter{Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			if len(after) != len(before) {
				t.Fatalf("invalid criteria must not create a WorkItem: before=%d after=%d", len(before), len(after))
			}
		})
	}

	root, err := svc.CreateWorkItem(ctx, workspaceID, application.CreateWorkItemParams{
		Title: "normalized coordinator root", RecordKind: domain.RecordKindTask,
		AutoCoordinate: true, AcceptanceCriteria: []string{"  first  ", "", "second"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := root.AcceptanceCriteria, []string{"first", "second"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("WorkItem must persist normalized criteria: got=%v want=%v", got, want)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	criteriaData, ok := state.Data["acceptance_criteria"].([]any)
	if ok {
		if len(criteriaData) != 2 || criteriaData[0] != "first" || criteriaData[1] != "second" {
			t.Fatalf("Coordinator state criteria drifted: %#v", state.Data["acceptance_criteria"])
		}
	} else {
		// In-process state retains the typed []string before the SQL JSON round trip.
		criteriaStrings, ok := state.Data["acceptance_criteria"].([]string)
		if !ok || len(criteriaStrings) != 2 || criteriaStrings[0] != "first" || criteriaStrings[1] != "second" {
			t.Fatalf("Coordinator state criteria drifted: %#v", state.Data["acceptance_criteria"])
		}
	}
}

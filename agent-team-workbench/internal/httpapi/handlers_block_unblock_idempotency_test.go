package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
	"github.com/ybs/agent-team-workbench/internal/sse"
)

type blockUnblockDispatcher struct {
	runs []*domain.ExecutionRun
}

func (d *blockUnblockDispatcher) Dispatch(_ context.Context, run *domain.ExecutionRun) error {
	d.runs = append(d.runs, run)
	return nil
}

func TestUnblockCommandIdempotencyReplaysWithoutGovernanceMutation(t *testing.T) {
	ctx := context.Background()
	store := openIdempotencyTestDB(t)
	dispatcher := &blockUnblockDispatcher{}
	svc := application.NewService(store, dispatcher, planTestNotifier{}, atwruntime.NewRegistry())
	server := NewServer(svc, store, sse.NewHub())
	workspaceID, _, _ := seedPlanHTTPEnv(t, server)
	root, err := svc.CreateWorkItem(ctx, workspaceID, application.CreateWorkItemParams{
		Title: "idempotent governance unblock", RecordKind: domain.RecordKindTask,
		AutoCoordinate: true, AcceptanceCriteria: []string{"unblock replays exactly once"},
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err = store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := svc.BlockWorkItem(ctx, root.ID, application.BlockParams{
		Code: "operator_pause", Message: "pause before replay", Source: "user",
	}, root.Version)
	if err != nil {
		t.Fatal(err)
	}

	path := "/api/v1/work-items/" + root.ID + "/commands/unblock"
	body := fmt.Sprintf(`{"expected_version":%d}`, blocked.Version)
	request := func(payload string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload))
		req.Header.Set("Idempotency-Key", "unblock-governance-1")
		return req
	}
	mux := server.Routes()
	first := httptest.NewRecorder()
	mux.ServeHTTP(first, request(body))
	if first.Code != http.StatusOK {
		t.Fatalf("first unblock status=%d body=%s", first.Code, first.Body.String())
	}

	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.Events().Since(ctx, workspaceID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	goalVersion, todoVersion, coordinatorVersion := goal.Version, todo.Version, state.Version

	replay := httptest.NewRecorder()
	mux.ServeHTTP(replay, request(body))
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("exact unblock replay must return the stored success: status=%d headers=%v body=%s",
			replay.Code, replay.Header(), replay.Body.String())
	}
	if strings.TrimSpace(replay.Body.String()) != strings.TrimSpace(first.Body.String()) {
		t.Fatalf("replayed unblock body differs: first=%s replay=%s", first.Body.String(), replay.Body.String())
	}

	goal, err = store.Goals().Get(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	todo, err = store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err = store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	eventsAfterReplay, err := store.Events().Since(ctx, workspaceID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if goal.Version != goalVersion || todo.Version != todoVersion || state.Version != coordinatorVersion ||
		len(eventsAfterReplay) != len(events) {
		t.Fatalf("unblock replay mutated governance: goal=%d/%d todo=%d/%d coordinator=%d/%d events=%d/%d",
			goal.Version, goalVersion, todo.Version, todoVersion, state.Version, coordinatorVersion,
			len(eventsAfterReplay), len(events))
	}

	conflict := httptest.NewRecorder()
	mux.ServeHTTP(conflict, request(`{"expected_version":999999}`))
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "idempotency_conflict") {
		t.Fatalf("same unblock key with a different command must conflict: status=%d body=%s",
			conflict.Code, conflict.Body.String())
	}
}

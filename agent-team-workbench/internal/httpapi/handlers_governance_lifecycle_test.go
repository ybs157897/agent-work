package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
	"github.com/ybs/agent-team-workbench/internal/sse"
)

func goalLifecycleRequest(path, body, key, ifMatch string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	return req
}

func goalLifecycleResponse(t *testing.T, mux http.Handler, req *http.Request) (int, map[string]any, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("response is not JSON: status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	return rec.Code, out, rec
}

func TestGoalLifecycleCommandsHTTPAreScopedVersionedAndIdempotent(t *testing.T) {
	ctx := context.Background()
	server := newPlanTestServer(t)
	workspaceID, leadID, _ := seedPlanHTTPEnv(t, server)
	root, err := server.svc.CreateWorkItem(ctx, workspaceID, application.CreateWorkItemParams{
		Title: "HTTP Goal lifecycle", RecordKind: domain.RecordKindTask, AgentProfileID: leadID,
		AcceptanceCriteria: []string{"pause, resume and cancel remain explicit commands"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := server.store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	basePath := "/api/v1/workspaces/" + workspaceID + "/goals/" + goal.ID + "/commands/"
	mux := server.Routes()

	missingKey := httptest.NewRecorder()
	mux.ServeHTTP(missingKey, goalLifecycleRequest(basePath+"pause", `{"expected_version":1}`, "", ""))
	if missingKey.Code != http.StatusBadRequest {
		t.Fatalf("missing Idempotency-Key must be rejected: status=%d body=%s", missingKey.Code, missingKey.Body.String())
	}
	unchanged, err := server.store.Goals().Get(ctx, goal.ID)
	if err != nil || unchanged.Version != goal.Version || unchanged.Status != goal.Status {
		t.Fatalf("missing key must not mutate Goal: before=%+v after=%+v err=%v", goal, unchanged, err)
	}

	wrongWorkspacePath := "/api/v1/workspaces/ws_other/goals/" + goal.ID + "/commands/pause"
	status, _, _ := goalLifecycleResponse(t, mux,
		goalLifecycleRequest(wrongWorkspacePath, `{"expected_version":1}`, "goal-wrong-workspace", `"1"`))
	if status != http.StatusNotFound {
		t.Fatalf("cross-workspace Goal command must be hidden: status=%d", status)
	}

	status, body, _ := goalLifecycleResponse(t, mux,
		goalLifecycleRequest(basePath+"pause", `{"expected_version":1}`, "goal-if-match-mismatch", `"2"`))
	if status != http.StatusConflict || body["code"] != "version_conflict" {
		t.Fatalf("If-Match mismatch must fail before mutation: status=%d body=%v", status, body)
	}

	pauseBody := `{"expected_version":2}`
	status, firstPause, firstPauseRec := goalLifecycleResponse(t, mux,
		goalLifecycleRequest(basePath+"pause", pauseBody, "goal-pause", `W/"2"`))
	if status != http.StatusOK || firstPause["status"] != string(domain.GoalWaiting) || firstPause["version"] != float64(3) {
		t.Fatalf("pause command did not return waiting Goal: status=%d body=%v", status, firstPause)
	}
	status, replayPause, replayPauseRec := goalLifecycleResponse(t, mux,
		goalLifecycleRequest(basePath+"pause", pauseBody, "goal-pause", `W/"2"`))
	if status != http.StatusOK || replayPauseRec.Header().Get("Idempotent-Replayed") != "true" ||
		strings.TrimSpace(replayPauseRec.Body.String()) != strings.TrimSpace(firstPauseRec.Body.String()) {
		t.Fatalf("pause replay must return the stored response: status=%d replay=%v body=%s first=%s",
			status, replayPause, replayPauseRec.Body.String(), firstPauseRec.Body.String())
	}

	status, body, _ = goalLifecycleResponse(t, mux,
		goalLifecycleRequest(basePath+"resume", `{"expected_version":3}`, "goal-resume", `"3"`))
	if status != http.StatusOK || body["status"] != string(domain.GoalActive) || body["version"] != float64(4) {
		t.Fatalf("resume command did not return active Goal: status=%d body=%v", status, body)
	}

	cancelBody := `{"expected_version":4}`
	status, firstCancel, firstCancelRec := goalLifecycleResponse(t, mux,
		goalLifecycleRequest(basePath+"cancel", cancelBody, "goal-cancel", `"4"`))
	if status != http.StatusOK || firstCancel["status"] != string(domain.GoalCancelled) || firstCancel["version"] != float64(5) {
		t.Fatalf("cancel command did not return cancelled Goal: status=%d body=%v", status, firstCancel)
	}
	status, replayCancel, replayCancelRec := goalLifecycleResponse(t, mux,
		goalLifecycleRequest(basePath+"cancel", cancelBody, "goal-cancel", `"4"`))
	if status != http.StatusOK || replayCancelRec.Header().Get("Idempotent-Replayed") != "true" ||
		strings.TrimSpace(replayCancelRec.Body.String()) != strings.TrimSpace(firstCancelRec.Body.String()) {
		t.Fatalf("cancel replay must return the stored response: status=%d replay=%v body=%s first=%s",
			status, replayCancel, replayCancelRec.Body.String(), firstCancelRec.Body.String())
	}
	status, body, _ = goalLifecycleResponse(t, mux,
		goalLifecycleRequest(basePath+"cancel", `{"expected_version":5}`, "goal-cancel", `"5"`))
	if status != http.StatusConflict || body["code"] != "idempotency_conflict" {
		t.Fatalf("same cancel key with a different body must conflict: status=%d body=%v", status, body)
	}
}

type failingCancelQuotaRepo struct {
	application.QuotaRepo
	targetKey domain.QuotaReservationKey
	fail      bool
}

func (r *failingCancelQuotaRepo) Release(ctx context.Context, reservation *domain.QuotaReservation, expectedVersion int) error {
	if reservation != nil && reservation.Key.Equal(r.targetKey) && r.fail {
		return fmt.Errorf("injected post-commit quota settlement failure")
	}
	return r.QuotaRepo.Release(ctx, reservation, expectedVersion)
}

type failingCancelStore struct {
	application.Store
	quota application.QuotaRepo
}

func (s *failingCancelStore) Quotas() application.QuotaRepo { return s.quota }

func TestCancelGoalHTTPReturnsCommitted202AndReplaysAfterPostCommitFailure(t *testing.T) {
	ctx := context.Background()
	base := openIdempotencyTestDB(t)
	failingQuota := &failingCancelQuotaRepo{QuotaRepo: base.Quotas(), fail: true}
	store := &failingCancelStore{Store: base, quota: failingQuota}
	svc := application.NewService(store, nil, planTestNotifier{}, atwruntime.NewRegistry())
	server := NewServer(svc, store, sse.NewHub())
	workspaceID, _, _ := seedPlanHTTPEnv(t, server)
	root, err := svc.CreateWorkItem(ctx, workspaceID, application.CreateWorkItemParams{
		Title: "cancel recovery response", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"a committed cancel is never retried as a fresh command"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := svc.ClaimTodo(ctx, todo.ID, state.CoordinatorAgentID, todo.Version, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	header, err := svc.AdmitTurn(ctx, application.AdmitTurnParams{
		GoalID: goal.ID, TodoID: claimed.ID, OwnerAgentID: state.CoordinatorAgentID,
		ExpectedTodoVersion: claimed.Version, Attempt: 1, SchemaVersion: "plan-decision/v2",
		InputSnapshotDigest: "sha256:" + strings.Repeat("0", 64), AdmissionClientKey: "cancel-recovery-turn",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AppendTurnReceiptPhase(ctx, &domain.TurnReceiptPhase{
		TurnKey: header.TurnKey, PhaseSeq: 1, Phase: domain.TurnReceiptPhaseDecisionDecode,
		Payload: map[string]any{"source_run_id": state.CurrentRunID},
	}); err != nil {
		t.Fatal(err)
	}
	reservation := &domain.QuotaReservation{
		Key:    domain.QuotaReservationKey{TurnKey: header.TurnKey, Kind: domain.QuotaOutputTokens},
		Status: domain.QuotaReservationReserved, ReservedAmount: 1,
		PolicyLimit: 1, PolicyEnforcement: domain.QuotaEnforcementAudit,
		PolicyDigest: "sha256:" + strings.Repeat("0", 64), Version: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if _, err := base.Quotas().Reserve(ctx, reservation); err != nil {
		t.Fatal(err)
	}
	failingQuota.targetKey = reservation.Key
	goal, err = store.Goals().Get(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}

	path := "/api/v1/workspaces/" + workspaceID + "/goals/" + goal.ID + "/commands/cancel"
	body := `{"expected_version":2}`
	// The Goal is active at version 2; its admitted Turn keeps a real quota
	// reservation open, so cancellation cleanup has a durable checkpoint.
	firstStatus, firstBody, firstRec := goalLifecycleResponse(t, server.Routes(),
		goalLifecycleRequest(path, body, "cancel-recovery", `"2"`))
	if firstStatus != http.StatusAccepted || firstBody["recovery_pending"] != true {
		t.Fatalf("committed post-commit failure must return 202 recovery response: status=%d body=%v", firstStatus, firstBody)
	}
	checkpoint, ok := firstBody["recovery_checkpoint"].(map[string]any)
	if !ok || checkpoint["kind"] != "goal_cancelled_cleanup" || checkpoint["root_work_item_id"] != root.ID {
		t.Fatalf("202 response must identify the durable recovery checkpoint: %v", firstBody["recovery_checkpoint"])
	}
	goalBody, ok := firstBody["goal"].(map[string]any)
	if !ok || goalBody["status"] != string(domain.GoalCancelled) {
		t.Fatalf("202 response must expose the committed cancelled Goal: %v", firstBody["goal"])
	}

	replayStatus, replayBody, replayRec := goalLifecycleResponse(t, server.Routes(),
		goalLifecycleRequest(path, body, "cancel-recovery", `"2"`))
	if replayStatus != http.StatusAccepted || replayRec.Header().Get("Idempotent-Replayed") != "true" ||
		strings.TrimSpace(replayRec.Body.String()) != strings.TrimSpace(firstRec.Body.String()) {
		t.Fatalf("same cancel key must replay committed 202 instead of re-running terminal command: status=%d body=%v raw=%s first=%s",
			replayStatus, replayBody, replayRec.Body.String(), firstRec.Body.String())
	}
	latestState, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latestState.Data["control_action"] != "settle_cancelled_goal" {
		t.Fatalf("202 must correspond to a durable cancellation recovery checkpoint: %+v", latestState)
	}
	failingQuota.fail = false
	if err := svc.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	settled, err := store.Quotas().Get(ctx, reservation.Key)
	if err != nil || settled.Status != domain.QuotaReservationReleased {
		t.Fatalf("recovery replay must settle the real quota reservation: reservation=%+v err=%v", settled, err)
	}
	latestState, err = store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil || latestState.Data["control_action"] != nil {
		t.Fatalf("successful recovery must clear the cancelled Goal checkpoint: state=%+v err=%v", latestState, err)
	}
}

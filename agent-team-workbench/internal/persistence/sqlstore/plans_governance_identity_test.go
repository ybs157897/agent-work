package sqlstore_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

func TestPlanGovernanceIdentityRoundTripAndClientLookup(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	seedWorkspace(t, db)
	insertWorkItem(t, db, "wi_plan_identity")
	planIdentityInsertParents(t, db)

	now := time.Now().UTC().Truncate(time.Microsecond)
	wantKey := &domain.TurnKey{GoalID: "goal_plan_identity", TodoID: "todo_plan_identity", TurnSeq: 7}
	want := &domain.Plan{
		ID:                    "plan_governance_roundtrip",
		WorkspaceID:           "ws_wk",
		WorkItemID:            "wi_plan_identity",
		AgentProfileID:        "agent_plan_identity",
		SourceRunID:           "run_plan_identity",
		ClientKey:             "governance:goal_plan_identity:todo_plan_identity:7",
		GovernanceTurnKey:     wantKey,
		DecisionSchemaVersion: "plan-decision/v2",
		DecisionSchemaDigest:  planIdentityDigest("a"),
		DecisionDigest:        planIdentityDigest("b"),
		ContextGeneration:     3,
		Status:                domain.PlanWaiting,
		Guardrails:            domain.PlanGuardrails{MaxDispatch: planIdentityIntPointer(4)},
		Version:               2,
		CreatedAt:             now,
		UpdatedAt:             now.Add(time.Minute),
		Steps: []domain.PlanStep{{
			PlanID: "plan_governance_roundtrip", Seq: 0, Verb: domain.PlanVerbDefer,
			Payload: map[string]any{"reason": "governance"}, Status: domain.PlanStepPending,
			CreatedAt: now,
		}},
	}
	if err := store.Plans().Create(ctx, want); err != nil {
		t.Fatal(err)
	}

	got, err := store.Plans().Get(ctx, want.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertPlanGovernanceIdentityEqual(t, got, want)
	if len(got.Steps) != 1 || got.Steps[0].Verb != domain.PlanVerbDefer {
		t.Fatalf("Plan steps 未完整往返: %+v", got.Steps)
	}

	byKey, err := store.Plans().GetByClientKey(ctx, "ws_wk", want.ClientKey)
	if err != nil {
		t.Fatal(err)
	}
	if byKey == nil || byKey.ID != want.ID {
		t.Fatalf("GetByClientKey 应查回同一 Plan: %+v", byKey)
	}
	if other, err := store.Plans().GetByClientKey(ctx, "ws_other", want.ClientKey); err != nil {
		t.Fatal(err)
	} else if other != nil {
		t.Fatalf("workspace 不同不得命中 Plan: %+v", other)
	}
	for _, query := range [][2]string{{"ws_wk", "missing"}, {"", want.ClientKey}, {"ws_wk", ""}} {
		if missing, err := store.Plans().GetByClientKey(ctx, query[0], query[1]); err != nil {
			t.Fatal(err)
		} else if missing != nil {
			t.Fatalf("不存在的 client key 应返回 nil: workspace=%q key=%q got=%+v", query[0], query[1], missing)
		}
	}
}

func TestPlanGovernanceIdentityLegacyAndDuplicateGuards(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	seedWorkspace(t, db)
	insertWorkItem(t, db, "wi_plan_identity")
	planIdentityInsertParents(t, db)
	now := time.Now().UTC().Truncate(time.Microsecond)

	legacy := &domain.Plan{
		ID: "plan_legacy_identity", WorkspaceID: "ws_wk", WorkItemID: "wi_plan_identity",
		AgentProfileID: "agent_legacy", Status: domain.PlanActive, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Plans().Create(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	gotLegacy, err := store.Plans().Get(ctx, legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotLegacy.ClientKey != "" || gotLegacy.GovernanceTurnKey != nil ||
		gotLegacy.DecisionSchemaVersion != "" || gotLegacy.DecisionSchemaDigest != "" || gotLegacy.DecisionDigest != "" {
		t.Fatalf("legacy Plan 治理字段必须保持零值: %+v", gotLegacy)
	}
	if got, err := store.Plans().GetByClientKey(ctx, "ws_wk", ""); err != nil {
		t.Fatal(err)
	} else if got != nil {
		t.Fatalf("legacy Plan 不应有 client key 命中: %+v", got)
	}

	base := &domain.Plan{
		ID:                    "plan_identity_base",
		WorkspaceID:           "ws_wk",
		WorkItemID:            "wi_plan_identity",
		AgentProfileID:        "agent_plan_identity",
		ClientKey:             "governance:goal_plan_identity:todo_plan_identity:1",
		GovernanceTurnKey:     &domain.TurnKey{GoalID: "goal_plan_identity", TodoID: "todo_plan_identity", TurnSeq: 1},
		DecisionSchemaVersion: "plan-decision/v2",
		DecisionSchemaDigest:  planIdentityDigest("c"),
		DecisionDigest:        planIdentityDigest("d"),
		Status:                domain.PlanActive,
		Version:               1,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if err := store.Plans().Create(ctx, base); err != nil {
		t.Fatal(err)
	}

	duplicateClient := *base
	duplicateClient.ID = "plan_identity_duplicate_client"
	duplicateClient.GovernanceTurnKey = &domain.TurnKey{GoalID: "goal_plan_identity", TodoID: "todo_plan_identity", TurnSeq: 1}
	duplicateClient.ClientKey = base.ClientKey
	if err := store.Plans().Create(ctx, &duplicateClient); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("重复 workspace/client key 应冲突，实际 %v", err)
	}

	duplicateTurn := *base
	duplicateTurn.ID = "plan_identity_duplicate_turn"
	duplicateTurn.ClientKey = "governance:goal_plan_identity:todo_plan_identity:1"
	duplicateTurn.DecisionDigest = planIdentityDigest("e")
	if err := store.Plans().Create(ctx, &duplicateTurn); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("重复 governance turn identity 应冲突，实际 %v", err)
	}
}

func planIdentityInsertParents(t *testing.T, db *sql.DB) {
	t.Helper()
	const now = "2026-09-01T00:00:00Z"
	if _, err := db.Exec(`INSERT INTO goals
		(id,workspace_id,root_work_item_id,objective,acceptance_contract,status,phase,current_todo_id,quota_policies,completion_evidence_summary,version,created_at,updated_at)
		VALUES ('goal_plan_identity','ws_wk','wi_plan_identity','plan identity','["accept"]','draft','planning',NULL,'[]','[]',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO goal_todos
		(id,goal_id,class,status,instruction,acceptance,resume_condition,priority,predecessors,successors,decision_scope,claim_owner_agent_id,claim_version,claim_claimed_at,claim_expires_at,last_turn_seq,version,created_at,updated_at)
		VALUES ('todo_plan_identity','goal_plan_identity','advancement','pending','compile','["done"]',NULL,'medium','[]','[]',?,NULL,0,NULL,NULL,0,1,?,?)`,
		`{"work_item_ids":["wi_plan_identity"],"agent_ids":["agent_plan_identity"],"runtime_capabilities":[],"write_scopes":[],"max_dispatch":4}`, now, now); err != nil {
		t.Fatal(err)
	}
}

func assertPlanGovernanceIdentityEqual(t *testing.T, got, want *domain.Plan) {
	t.Helper()
	if got.ClientKey != want.ClientKey || got.DecisionSchemaVersion != want.DecisionSchemaVersion ||
		got.DecisionSchemaDigest != want.DecisionSchemaDigest || got.DecisionDigest != want.DecisionDigest {
		t.Fatalf("Plan governance 文本字段不符: got=%+v want=%+v", got, want)
	}
	if got.GovernanceTurnKey == nil || !got.GovernanceTurnKey.Equal(*want.GovernanceTurnKey) {
		t.Fatalf("Plan governance turn key 不符: got=%+v want=%+v", got.GovernanceTurnKey, want.GovernanceTurnKey)
	}
}

func planIdentityDigest(seed string) string {
	if seed == "" {
		seed = "0"
	}
	return "sha256:" + strings.Repeat(seed[:1], 64)
}

func planIdentityIntPointer(value int) *int { return &value }

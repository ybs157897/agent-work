package main

import (
	"testing"
)

func TestCoordinatorPlanRepairMigrationUpgradesPromptAndGuardsCheckpoint(t *testing.T) {
	db := nativeGovernanceDB(t, "coordinator-plan-repair.db")
	applyNativeGovernanceMigrations(t, db, "0024_native_governance.sql")
	const now = "2026-09-01T00:00:00Z"
	if _, err := db.Exec(`INSERT INTO workspaces(id,name,timezone,version,created_at,updated_at)
		VALUES ('ws_repair','repair','UTC',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO agent_profiles
		(id,workspace_id,name,role,kind,prompt_version,instructions_editable,policy,created_at,updated_at)
		VALUES ('agent_repair','ws_repair','Coordinator','task_coordinator','task_coordinator',
		'task-coordinator.v1',0,'{"tools":[],"approval_policy":"manual","sandbox":"read-only"}',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO task_coordinator_configs
		(id,workspace_id,agent_profile_id,prompt_version,runtime_label,model_ref,fallback_model_ref,reasoning_effort,version,created_at,updated_at)
		VALUES ('coordcfg_repair','ws_repair','agent_repair','task-coordinator.v1','mock','{}','{}','medium',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}

	applyNativeGovernanceMigrations(t, db, "")
	var profileVersion, configVersion string
	if err := db.QueryRow(`SELECT prompt_version FROM agent_profiles WHERE id='agent_repair'`).Scan(&profileVersion); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT prompt_version FROM task_coordinator_configs WHERE id='coordcfg_repair'`).Scan(&configVersion); err != nil {
		t.Fatal(err)
	}
	if profileVersion != "task-coordinator.v2" || configVersion != "task-coordinator.v2" {
		t.Fatalf("prompt migration mismatch: profile=%q config=%q", profileVersion, configVersion)
	}
	assertNativeColumns(t, db, "task_coordinator_states",
		"repair_status", "repair_attempt", "repair_source_run_id", "repair_error_class",
		"repair_error_code", "repair_validation_errors")
	assertNativeExecFails(t, db, `UPDATE agent_profiles SET prompt_version='task-coordinator.v1' WHERE id='agent_repair'`)

	if _, err := db.Exec(`INSERT INTO work_items
		(id,workspace_id,title,record_kind,status,priority,version,created_at,updated_at)
		VALUES ('wi_repair','ws_repair','repair root','task','todo','medium',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO task_coordinator_states
		(id,workspace_id,root_work_item_id,coordinator_agent_id,status,version,created_at,updated_at)
		VALUES ('coordstate_repair','ws_repair','wi_repair','agent_repair','queued',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	assertNativeExecFails(t, db, `UPDATE task_coordinator_states
		SET repair_status='pending', repair_attempt=1, repair_error_class='syntax',
			repair_error_code='plan_json_syntax'
		WHERE id='coordstate_repair'`)
}

package main

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestRecordKindMigrationBackfill protects the historical boundary: rows with
// no authoritative task evidence stay Chat, while plan roots/outputs and
// non-fork parent-child records become Task.
func TestRecordKindMigrationBackfill(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "record-kind.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := ensureSchemaTable(db); err != nil {
		t.Fatal(err)
	}

	dir := repoMigrationsDir(t)
	files, err := discoverMigrations(dir)
	if err != nil {
		t.Fatal(err)
	}
	var before, recordKind []string
	for _, file := range files {
		if filepath.Base(file) == "0019_record_kind.sql" {
			recordKind = append(recordKind, file)
		} else {
			before = append(before, file)
		}
	}
	if len(recordKind) != 1 {
		t.Fatalf("应恰有一条 0019_record_kind 迁移，实际 %d", len(recordKind))
	}
	if err := applyMigrations(db, before); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO workspaces(id,name,timezone,version,created_at,updated_at)
		VALUES ('ws_kind','kind','UTC',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	insert := func(id, parentID, clientKey, description string) {
		t.Helper()
		_, err := db.Exec(`INSERT INTO work_items(id,workspace_id,parent_id,client_key,description,title,status,priority,version,created_at,updated_at)
			VALUES (?,?,?,?,?,'title','todo','medium',1,?,?)`, id, "ws_kind", nullable(parentID), nullable(clientKey), description, now, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	insert("wi_chat", "", "", "")
	insert("wi_plan", "", "", "")
	insert("wi_plan_child", "wi_plan", "", "")
	insert("wi_task_parent", "", "", "")
	insert("wi_task_child", "wi_task_parent", "", "")
	insert("wi_fork_parent", "", "", "")
	insert("wi_fork_child", "wi_fork_parent", "fork:wi_fork_parent:msg", "【分叉上下文】用户：旧上下文")

	if _, err := db.Exec(`INSERT INTO plans(id,workspace_id,work_item_id,agent_profile_id,created_at,updated_at)
		VALUES ('plan_kind','ws_kind','wi_plan','agent_kind',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO plan_steps(plan_id,seq,verb,payload,status,result_work_item_id,created_at)
		VALUES ('plan_kind',0,'dispatch','{}','executed','wi_plan_child',?)`, now); err != nil {
		t.Fatal(err)
	}

	if err := applyMigrations(db, recordKind); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"wi_chat":        "chat",
		"wi_plan":        "task",
		"wi_plan_child":  "task",
		"wi_task_parent": "task",
		"wi_task_child":  "task",
		"wi_fork_parent": "chat",
		"wi_fork_child":  "chat",
	}
	rows, err := db.Query(`SELECT id,record_kind FROM work_items ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, kind string
		if err := rows.Scan(&id, &kind); err != nil {
			t.Fatal(err)
		}
		if kind != want[id] {
			t.Errorf("%s record_kind=%q，期望 %q", id, kind, want[id])
		}
		delete(want, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(want) != 0 {
		t.Fatalf("迁移后缺少记录: %#v", want)
	}

	var notNull int
	var defaultValue sql.NullString
	if err := db.QueryRow(`SELECT "notnull",dflt_value FROM pragma_table_info('work_items') WHERE name='record_kind'`).Scan(&notNull, &defaultValue); err != nil {
		t.Fatal(err)
	}
	if notNull != 1 || !defaultValue.Valid || defaultValue.String != "'chat'" {
		t.Fatalf("record_kind 应 NOT NULL DEFAULT 'chat'，实际 notnull=%d default=%q", notNull, defaultValue.String)
	}

	if _, err := db.Exec(`INSERT INTO work_items(id,workspace_id,title,record_kind,created_at,updated_at)
		VALUES ('wi_invalid','ws_kind','invalid','other',?,?)`, now, now); err == nil {
		t.Fatal("非法 record_kind 应被拒绝")
	}
	if _, err := db.Exec(`UPDATE work_items SET record_kind='task' WHERE id='wi_chat'`); err == nil {
		t.Fatal("record_kind 变更应被拒绝")
	}
	if _, err := db.Exec(`INSERT INTO work_items(id,workspace_id,parent_id,title,record_kind,created_at,updated_at)
		VALUES ('wi_cross','ws_kind','wi_chat','cross','task',?,?)`, now, now); err == nil {
		t.Fatal("Chat 父项下插入 Task 子项应被拒绝")
	}
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

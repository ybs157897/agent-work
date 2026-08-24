package sqlstore_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

// openGrantsTestDB 临时文件 sqlite + 全量迁移（照 wakeups_test 的搭建方式）。
func openGrantsTestDB(t *testing.T) (*sql.DB, *sqlstore.Store) {
	t.Helper()
	db, err := sql.Open("sqlite",
		filepath.Join(t.TempDir(), "test.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, current, _, _ := runtime.Caller(0)
	migrationDir := filepath.Join(filepath.Dir(current), "..", "..", "..", "migrations", "sqlite")
	for _, name := range []string{
		"0001_init.sql",
		"0002_runtime_binding_model_config.sql",
		"0003_agent_config.sql",
		"0004_task_sessions.sql",
		"0005_wakeup.sql",
		"0006_plans.sql",
		"0007_task_sessions_parent.sql",
		"0008_plan_source_run_unique.sql",
		"0009_plan_consult_knowledge.sql",
		"0010_plan_join_guardrails.sql",
		"0011_activity_work_item.sql",
		"0012_approval_grants.sql", "0013_entity_client_keys.sql", "0014_task_execution_lock.sql",
	} {
		body, err := os.ReadFile(filepath.Join(migrationDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("migration %s: %v", name, err)
		}
	}
	return db, sqlstore.New(db, sqlstore.SQLiteDialect())
}

// seedGrantEnv 落 workspace/两个 agent/两个 work item（授权匹配的锚点行）。
func seedGrantEnv(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, stmt := range []string{
		`INSERT INTO workspaces(id,name,timezone,version,created_at,updated_at)
		 VALUES ('ws_g','g','UTC',1,'` + now + `','` + now + `')`,
		`INSERT INTO workspaces(id,name,timezone,version,created_at,updated_at)
		 VALUES ('ws_other','o','UTC',1,'` + now + `','` + now + `')`,
		`INSERT INTO agent_profiles(id,workspace_id,name,role,created_at,updated_at)
		 VALUES ('agent_a','ws_g','A','developer','` + now + `','` + now + `')`,
		`INSERT INTO agent_profiles(id,workspace_id,name,role,created_at,updated_at)
		 VALUES ('agent_b','ws_g','B','developer','` + now + `','` + now + `')`,
		`INSERT INTO work_items(id,workspace_id,title,status,priority,version,created_at,updated_at)
		 VALUES ('wi_1','ws_g','t1','todo','medium',1,'` + now + `','` + now + `')`,
		`INSERT INTO work_items(id,workspace_id,title,status,priority,version,created_at,updated_at)
		 VALUES ('wi_2','ws_g','t2','todo','medium',1,'` + now + `','` + now + `')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
}

func insertGrant(t *testing.T, s *sqlstore.Store, id, scope, kind, pattern, workItemID string, createdAt time.Time) {
	t.Helper()
	g := &domain.ApprovalGrant{
		ID: id, WorkspaceID: "ws_g", AgentProfileID: "agent_a", WorkItemID: workItemID,
		Scope: domain.ApprovalScope(scope), Kind: kind, Pattern: pattern, CreatedAt: createdAt,
	}
	if err := s.ApprovalGrants().Create(context.Background(), g); err != nil {
		t.Fatal(err)
	}
}

// TestApprovalGrantMatching 授权匹配语义：作用域（workspace 跨 work item / thread
// 锚定单 work item）+ kind 相等 + pattern 前缀（空=全部）+ workspace/agent 硬条件；
// 多命中取最新。
func TestApprovalGrantMatching(t *testing.T) {
	db, s := openGrantsTestDB(t)
	seedGrantEnv(t, db)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)

	insertGrant(t, s, "grant_ws", "workspace", "command", "Codex 请求执行命令：git push", "", base)
	insertGrant(t, s, "grant_thread", "thread", "command", "", "wi_1", base.Add(time.Minute))
	insertGrant(t, s, "grant_file", "workspace", "file_change", "Codex 请求应用文件变更", "", base)

	match := func(ws, agent, wi, kind, summary string) *domain.ApprovalGrant {
		t.Helper()
		g, err := s.ApprovalGrants().Matching(ctx, ws, agent, wi, kind, summary)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}

	if g := match("ws_g", "agent_a", "wi_2", "command", "Codex 请求执行命令：git push --force"); g == nil || g.ID != "grant_ws" {
		t.Fatalf("workspace 作用域应跨 work item 前缀命中 grant_ws，实际 %+v", g)
	}
	if g := match("ws_g", "agent_a", "wi_2", "command", "Codex 请求执行命令：gitx"); g != nil {
		t.Fatalf("pattern 非前缀不得命中，实际 %+v", g)
	}
	if g := match("ws_g", "agent_a", "wi_1", "command", "Codex 请求执行命令：rm -rf /"); g == nil || g.ID != "grant_thread" {
		t.Fatalf("thread 作用域空 pattern 应在 wi_1 命中 grant_thread，实际 %+v", g)
	}
	if g := match("ws_g", "agent_a", "wi_2", "command", "Codex 请求执行命令：rm -rf /"); g != nil {
		t.Fatalf("thread 授权不得跨 work item 生效，实际命中 %+v", g)
	}
	if g := match("ws_g", "agent_a", "wi_1", "permissions", "Codex 请求执行命令：git push"); g != nil {
		t.Fatalf("kind 不同不得命中，实际 %+v", g)
	}
	if g := match("ws_g", "agent_b", "wi_1", "command", "Codex 请求执行命令：git push --force"); g != nil {
		t.Fatalf("agent 不同不得命中，实际 %+v", g)
	}
	if g := match("ws_other", "agent_a", "wi_1", "command", "Codex 请求执行命令：git push"); g != nil {
		t.Fatalf("workspace 不同不得命中，实际 %+v", g)
	}
	if g := match("ws_g", "agent_a", "wi_1", "file_change", "Codex 请求应用文件变更 xxx"); g == nil || g.ID != "grant_file" {
		t.Fatalf("file_change kind 应命中 grant_file，实际 %+v", g)
	}
}

// TestApprovalGrantNewestWins 同一请求命中多条授权时取 created_at 最新（用户
// 最近一次「总是允许」的意图优先）。
func TestApprovalGrantNewestWins(t *testing.T) {
	db, s := openGrantsTestDB(t)
	seedGrantEnv(t, db)
	base := time.Now().UTC().Add(-time.Hour)
	insertGrant(t, s, "grant_old", "workspace", "command", "", "", base)
	insertGrant(t, s, "grant_new", "workspace", "command", "Codex 请求执行命令：git", "", base.Add(2*time.Minute))

	g, err := s.ApprovalGrants().Matching(context.Background(), "ws_g", "agent_a", "wi_1",
		"command", "Codex 请求执行命令：git push")
	if err != nil {
		t.Fatal(err)
	}
	if g == nil || g.ID != "grant_new" {
		t.Fatalf("应取最新授权 grant_new，实际 %+v", g)
	}
}

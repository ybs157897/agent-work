// migrate_test.go 确保唯一 SQLite 迁移历史可全量建库、幂等重放，且不会重命名
// 已写入 SQLite 数据库 schema_migrations 的历史版本。
package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	// SQLite driver 由 main.go 引入的 sqlstore 包注册，本文件无需重复。
)

// repoMigrationsDir 以本测试文件为锚点定位唯一迁移目录。
func repoMigrationsDir(t *testing.T) string {
	t.Helper()
	_, current, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(current), "..", "..", "migrations")
}

var historicalSQLiteMigrationVersions = []string{
	"0001_init",
	"0002_runtime_binding_model_config",
	"0003_agent_config",
	"0004_task_sessions",
	"0005_wakeup",
	"0006_plans",
	"0007_task_sessions_parent",
	"0008_plan_source_run_unique",
	"0009_plan_consult_knowledge",
	"0010_plan_join_guardrails",
	"0011_activity_work_item",
	"0012_approval_grants",
	"0013_entity_client_keys",
	"0014_task_execution_lock",
	"0015_run_event_agent_identity",
	"0016_dispatches",
	"0017_task_ledger",
	"0018_search_index",
	"0019_record_kind",
	"0020_task_coordinator",
	"0021_execution_context",
	"0022_task_comments",
	"0023_runner_event_dedup_v2",
}

func TestNoDialectMigrationDirectory(t *testing.T) {
	legacyDir := filepath.Join(repoMigrationsDir(t), "sqlite")
	if _, err := os.Stat(legacyDir); err == nil {
		t.Fatalf("SQLite 迁移已在 migrations/ 成为唯一真相源，不得恢复 %s", legacyDir)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// TestMigrationVersionsKeepExistingSQLiteDatabaseCompatible freezes the
// basename-based versions already stored by deployed SQLite databases. Moving
// the SQLite DDL to migrations/ must not make old databases replay history.
func TestMigrationVersionsKeepExistingSQLiteDatabaseCompatible(t *testing.T) {
	files, err := discoverMigrations(repoMigrationsDir(t))
	if err != nil {
		t.Fatal(err)
	}
	versions := make([]string, 0, len(files))
	for _, file := range files {
		versions = append(versions, strings.TrimSuffix(filepath.Base(file), ".sql"))
	}
	if len(versions) < len(historicalSQLiteMigrationVersions) ||
		!slices.Equal(versions[:len(historicalSQLiteMigrationVersions)], historicalSQLiteMigrationVersions) {
		t.Fatalf("SQLite 历史迁移版本改变: got %v, want prefix %v", versions, historicalSQLiteMigrationVersions)
	}
}

// TestMigrationsApplyEndToEnd 把唯一 migrations/ 全量应用到临时 SQLite 库，
// 走与生产相同的 ensureSchemaTable/discoverMigrations/applyMigrations 路径，
// 并验证 schema_migrations 的幂等性。
func TestMigrationsApplyEndToEnd(t *testing.T) {
	dir := repoMigrationsDir(t)
	files, err := discoverMigrations(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("未在 %s 发现迁移文件", dir)
	}

	db, err := sql.Open("sqlite",
		filepath.Join(t.TempDir(), "guard.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := ensureSchemaTable(db); err != nil {
		t.Fatalf("初始化 schema_migrations 失败: %v", err)
	}
	if err := applyMigrations(db, files); err != nil {
		t.Fatalf("全量应用 migrations 失败: %v", err)
	}

	var applied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != len(files) {
		t.Fatalf("schema_migrations 应记录 %d 条，实际 %d", len(files), applied)
	}

	// 幂等回归：重跑一遍不得重复记录、不得报错。
	if err := applyMigrations(db, files); err != nil {
		t.Fatalf("重复 apply 应幂等跳过: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != len(files) {
		t.Fatalf("重复 apply 后记录数应仍为 %d，实际 %d", len(files), applied)
	}
}

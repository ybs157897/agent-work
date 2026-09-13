// retire_removed_agent_bundles_migration_test.go 钉住 0066 的升级路径：已部署库里
// 残留的 nova/pixel/sentinel 普通 profile 被停用，其余普通 profile 与两个系统身份
// 完全不动。测试建到 0066 之前的库、插入真实字段形状的行，再只应用 0066。
package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestRetireRemovedAgentBundlesDisablesLegacyProfiles(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "retire-agents.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := ensureSchemaTable(db); err != nil {
		t.Fatal(err)
	}

	const target = "0066_retire_removed_agent_bundles.sql"
	dir := repoMigrationsDir(t)
	files, err := discoverMigrations(dir)
	if err != nil {
		t.Fatal(err)
	}
	var before, retirement []string
	for _, file := range files {
		if filepath.Base(file) == target {
			retirement = append(retirement, file)
		} else {
			before = append(before, file)
		}
	}
	if len(retirement) != 1 {
		t.Fatalf("应恰有一条 %s 迁移，实际 %d", target, len(retirement))
	}
	if err := applyMigrations(db, before); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO workspaces(id,name,timezone,version,created_at,updated_at)
		VALUES ('ws_roster','roster','UTC',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	// 系统身份的插入受触发器约束：这里按真实 schema 造行，才能证明迁移绕开它们。
	librarianPolicy := `{"approval_policy":"auto","sandbox":"workspace-write","tools":[]}`
	coordinatorPolicy := `{"approval_policy":"auto","sandbox":"read-only","tools":[]}`
	insert := func(id, slug, name, kind, availability, promptVersion string, editable int, policy string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO agent_profiles
			(id, workspace_id, slug, name, role, kind, skills, instructions, availability, presence,
			 runtime_preference, model_override, policy, prompt_version, instructions_editable,
			 heartbeat_enabled, heartbeat_interval_sec, wake_on_assignment, wake_on_demand,
			 wake_on_automation, prompt_template, version, created_at, updated_at)
			VALUES (?, 'ws_roster', ?, ?, 'developer', ?, '[]', '', ?, 'idle', '{}', '{}', ?, ?, ?,
			 0, 0, 1, 1, 0, '', 7, ?, ?)`,
			id, slug, name, kind, availability, policy, promptVersion, editable, now, now); err != nil {
			t.Fatalf("插入 %s: %v", id, err)
		}
	}
	insert("agent_nova", "nova", "Nova", "user", "enabled", "", 1, "{}")
	insert("agent_pixel", "pixel", "Pixel", "user", "enabled", "", 1, "{}")
	insert("agent_sentinel", "sentinel", "Sentinel", "user", "disabled", "", 1, "{}")
	insert("agent_atlas", "atlas", "产品智能体", "user", "enabled", "", 1, "{}")
	insert("agent_forge", "forge", "开发智能体", "user", "enabled", "", 1, "{}")
	insert("agent_librarian", "knowledge-librarian", "知识库管理员", "knowledge_librarian", "enabled",
		"knowledge-harness/v2", 0, librarianPolicy)
	insert("agent_coordinator", "", "Task Coordinator", "task_coordinator", "enabled",
		"task-coordinator.v2", 0, coordinatorPolicy)
	// 已停用的残留用可区分的时间戳，用来证明迁移的 availability 守卫不是空条件。
	const alreadyRetiredAt = "2026-01-01T00:00:00Z"
	if _, err := db.Exec(`UPDATE agent_profiles SET updated_at=? WHERE id='agent_sentinel'`, alreadyRetiredAt); err != nil {
		t.Fatal(err)
	}

	if err := applyMigrations(db, retirement); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"agent_nova":        "disabled",
		"agent_pixel":       "disabled",
		"agent_sentinel":    "disabled", // 已停用的残留保持停用（且不被再次写入）
		"agent_atlas":       "enabled",
		"agent_forge":       "enabled",
		"agent_librarian":   "enabled",
		"agent_coordinator": "enabled",
	}
	rows, err := db.Query(`SELECT id, availability, version FROM agent_profiles ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, availability string
		var version int
		if err := rows.Scan(&id, &availability, &version); err != nil {
			t.Fatal(err)
		}
		if availability != want[id] {
			t.Errorf("%s availability=%q，期望 %q", id, availability, want[id])
		}
		// 迁移只写运行态字段：version 保持不变，避免与 agent config sync intent 冲突。
		if version != 7 {
			t.Errorf("%s version=%d，迁移不应改动 version", id, version)
		}
		delete(want, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(want) != 0 {
		t.Fatalf("迁移后缺少 profile: %#v", want)
	}

	// updated_at 只对被停用的行写入，且沿用应用写入的 RFC3339-with-Z 形状。
	var raw string
	if err := db.QueryRow(`SELECT updated_at FROM agent_profiles WHERE id='agent_nova'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse(time.RFC3339Nano, raw); err != nil {
		t.Fatalf("updated_at 应为 RFC3339（应用写入形状），实际 %q: %v", raw, err)
	}
	var untouched string
	if err := db.QueryRow(`SELECT updated_at FROM agent_profiles WHERE id='agent_atlas'`).Scan(&untouched); err != nil {
		t.Fatal(err)
	}
	if untouched != now {
		t.Fatalf("未命中的 profile 不应被写入: %q", untouched)
	}
	if err := db.QueryRow(`SELECT updated_at FROM agent_profiles WHERE id='agent_sentinel'`).Scan(&untouched); err != nil {
		t.Fatal(err)
	}
	if untouched != alreadyRetiredAt {
		t.Fatalf("已停用的残留不应被再次写入: %q", untouched)
	}

	// 重入：直接再执行一次迁移 SQL 本身（绕开 schema_migrations 的版本跳过，
	// 单跑 SQL 才是对条件更新幂等性的真检验）。
	var retiredAt string
	if err := db.QueryRow(`SELECT updated_at FROM agent_profiles WHERE id='agent_nova'`).Scan(&retiredAt); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(retirement[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatalf("迁移 SQL 重放失败: %v", err)
	}
	var replayed string
	if err := db.QueryRow(`SELECT updated_at FROM agent_profiles WHERE id='agent_nova'`).Scan(&replayed); err != nil {
		t.Fatal(err)
	}
	if replayed != retiredAt {
		t.Fatalf("重放不得再次写入已停用行: %q → %q", retiredAt, replayed)
	}
	var disabled int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_profiles WHERE availability='disabled'`).Scan(&disabled); err != nil {
		t.Fatal(err)
	}
	if disabled != 3 {
		t.Fatalf("重放后停用行数应仍为 3，实际 %d", disabled)
	}
}

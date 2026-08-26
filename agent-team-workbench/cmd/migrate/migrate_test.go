// migrate_test.go 迁移双目录守卫：migrations/（PostgreSQL）与 migrations/sqlite/
// （SQLite）必须一一对应（NNNN_slug.sql 逐条对齐），且 SQLite 侧全量可应用
// （用临时库走 cmd/migrate 同一套发现与应用逻辑）。把"文件头注释约定"变成长期执行的断言。
package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"testing"
	// sqlite driver 由 main.go 的空导入注册，本文件无需重复。
)

// migrationFile 解析后的迁移文件名：NNNN_slug.sql。
type migrationFile struct {
	num  int    // 四位编号
	slug string // 编号后的语义 slug
	name string // 原始文件名
}

var migrationNameRe = regexp.MustCompile(`^([0-9]{4})_([a-z0-9_]+)\.sql$`)

// listMigrations 解析目录下全部迁移文件（按文件名排序），任何不合规命名都直接 fail。
func listMigrations(t *testing.T, dir string) []migrationFile {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	if len(names) == 0 {
		t.Fatalf("目录 %s 未发现任何迁移文件", dir)
	}
	files := make([]migrationFile, 0, len(names))
	for _, path := range names {
		name := filepath.Base(path)
		m := migrationNameRe.FindStringSubmatch(name)
		if m == nil {
			t.Fatalf("%s 不符合 NNNN_slug.sql 命名", filepath.ToSlash(filepath.Join(filepath.Base(dir), name)))
		}
		num, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatal(err)
		}
		for _, prev := range files {
			if prev.num == num {
				t.Fatalf("%s: 迁移编号 %04d 重复", name, num)
			}
		}
		files = append(files, migrationFile{num: num, slug: m[2], name: name})
	}
	return files
}

// repoMigrationsDir 以本测试文件为锚点定位仓库内迁移子目录。
func repoMigrationsDir(t *testing.T, sub ...string) string {
	t.Helper()
	_, current, _, _ := runtime.Caller(0)
	return filepath.Join(append([]string{filepath.Dir(current), "..", ".."}, sub...)...)
}

// TestMigrationDirsStayEquivalent PG 与 SQLite 双目录的迁移清单必须一一对应：
// 数量、编号、slug 逐条一致。漂移即 fail，并给出差异明细。
func TestMigrationDirsStayEquivalent(t *testing.T) {
	pg := listMigrations(t, repoMigrationsDir(t, "migrations"))
	sqlite := listMigrations(t, repoMigrationsDir(t, "migrations", "sqlite"))

	pgByID := make(map[int]migrationFile, len(pg))
	for _, f := range pg {
		pgByID[f.num] = f
	}
	sqliteByID := make(map[int]migrationFile, len(sqlite))
	for _, f := range sqlite {
		sqliteByID[f.num] = f
	}

	var missingInSQLite, missingInPG, slugMismatch []string
	for _, f := range pg {
		other, ok := sqliteByID[f.num]
		switch {
		case !ok:
			missingInSQLite = append(missingInSQLite, f.name)
		case other.slug != f.slug:
			slugMismatch = append(slugMismatch,
				fmt.Sprintf("%04d: PG %s / SQLite %s", f.num, f.slug, other.slug))
		}
	}
	for _, f := range sqlite {
		if _, ok := pgByID[f.num]; !ok {
			missingInPG = append(missingInPG, f.name)
		}
	}
	if len(missingInSQLite)+len(missingInPG)+len(slugMismatch) > 0 {
		t.Fatalf("migrations/ 与 migrations/sqlite/ 清单漂移:\n  仅 PG 有: %v\n  仅 SQLite 有: %v\n  slug 不一致: %v",
			missingInSQLite, missingInPG, slugMismatch)
	}
}

// TestSQLiteMigrationsApplyEndToEnd 把 migrations/sqlite/ 全量应用到临时 SQLite 库，
// 走与生产相同的 ensureSchemaTable/discoverMigrations/applyMigrations 路径，
// 断言 schema_migrations 记录数 == 文件数（PG 侧由 CI 的 go run ./cmd/migrate 覆盖）。
func TestSQLiteMigrationsApplyEndToEnd(t *testing.T) {
	dir := repoMigrationsDir(t, "migrations", "sqlite")
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
		t.Fatalf("全量应用 migrations/sqlite 失败: %v", err)
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

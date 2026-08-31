// Package migtest 供跨包测试共享的 SQLite 迁移夹具：动态派生 migrations/
// 全量清单并按序执行，新增迁移免同步各测试硬编码的文件名列表。非 _test
// 普通包以便跨包 import，不引 testing——由调用方对返回的 error 做 t.Fatal。
package migtest

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
)

// ApplyAll 对 db 按文件名序执行 migrations/ 下全部 *.sql 建库。
// 迁移目录相对本包源码定位（runtime.Caller 编译期固定），与调用方包层级无关；
// 空清单视为目录缺失/挪位，直接报错而非静默建出空库。
func ApplyAll(db *sql.DB) error {
	_, current, _, _ := runtime.Caller(0)
	migrationDir := filepath.Join(filepath.Dir(current), "..", "..", "migrations")
	names, err := filepath.Glob(filepath.Join(migrationDir, "*.sql"))
	if err != nil {
		return err
	}
	sort.Strings(names)
	if len(names) == 0 {
		return fmt.Errorf("migtest: 未在 %s 发现迁移文件", migrationDir)
	}
	for _, path := range names {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if _, err := db.Exec(string(body)); err != nil {
			return fmt.Errorf("migration %s: %w", filepath.Base(path), err)
		}
	}
	return nil
}

// cmd/migrate 顺序执行迁移 SQL，以 schema_migrations 记录已应用版本。
// 只支持 SQLite，固定从 migrations/ 读取唯一迁移历史。
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

func main() {
	defaultDSN := os.Getenv("DATABASE_URL")
	if defaultDSN == "" {
		defaultDSN = sqlstore.DefaultDSN
	}
	dsn := flag.String("dsn", defaultDSN, "SQLite 数据库 DSN")
	flag.Parse()

	db, err := sqlstore.Open(context.Background(), *dsn)
	if err != nil {
		log.Fatalf("打开 SQLite 数据库失败: %v", err)
	}
	defer db.Close()

	if err := ensureSchemaTable(db); err != nil {
		log.Fatalf("初始化 schema_migrations 失败: %v", err)
	}

	files, err := discoverMigrations("migrations")
	if err != nil {
		log.Fatal(err)
	}
	if err := applyMigrations(db, files); err != nil {
		log.Fatal(err)
	}
	log.Println("迁移完成")
}

// ensureSchemaTable 建幂等的版本记录表。
func ensureSchemaTable(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY, applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`)
	return err
}

// discoverMigrations 返回目录下按文件名排序的全部 *.sql 迁移路径。
func discoverMigrations(dir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(filepath.Clean(dir), "*.sql"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// applyMigrations 幂等应用全部未应用迁移：每个文件一个事务，SQL 与版本记录同事务提交。
func applyMigrations(db *sql.DB, files []string) error {
	for _, f := range files {
		version := strings.TrimSuffix(filepath.Base(f), ".sql")
		var applied bool
		if err := db.QueryRow(
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=?)`, version).Scan(&applied); err != nil {
			return fmt.Errorf("查询迁移状态失败: %w", err)
		}
		if applied {
			continue
		}
		body, err := os.ReadFile(filepath.Clean(f))
		if err != nil {
			return err
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("应用 %s 失败: %w", version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version) VALUES (?)`, version); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		fmt.Printf("已应用迁移 %s\n", version)
	}
	return nil
}

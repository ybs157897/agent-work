// cmd/atw-mcp 任务看板的 stdio MCP server（设计文档 F5）：
// 被 agent harness 的 MCP 配置拉起，把查询面 + claim/return 小写面
// 暴露为 MCP 工具。stdio 惯例：stdout 只走协议，日志一律 stderr。
package main

import (
	"database/sql"
	"flag"
	"log"
	"os"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/mark3labs/mcp-go/server"
	_ "modernc.org/sqlite"

	"github.com/ybs/agent-team-workbench/internal/mcpserver"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

func main() {
	dsn := flag.String("dsn", os.Getenv("DATABASE_URL"), "数据库 DSN（sqlite:// 前缀走 SQLite，否则 postgres）")
	flag.Parse()
	if *dsn == "" {
		log.Fatal("DATABASE_URL 或 -dsn 必须提供")
	}

	sqlDSN, driverName, dialect := *dsn, "pgx", sqlstore.PostgresDialect()
	if strings.HasPrefix(*dsn, "sqlite://") {
		driverName = "sqlite"
		sqlDSN = strings.TrimPrefix(*dsn, "sqlite://") + "?_pragma=foreign_keys(1)"
		dialect = sqlstore.SQLiteDialect()
	}
	db, err := sql.Open(driverName, sqlDSN)
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("数据库不可达: %v", err)
	}

	log.Printf("atw-mcp %s: stdio MCP server 启动（driver=%s）", mcpserver.Version, driverName)
	if err := server.ServeStdio(mcpserver.New(sqlstore.New(db, dialect))); err != nil {
		log.Fatalf("serve stdio 失败: %v", err)
	}
}

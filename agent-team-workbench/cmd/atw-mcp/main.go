// cmd/atw-mcp 任务看板的 stdio MCP server（设计文档 F5）：
// 被 agent harness 的 MCP 配置拉起，把查询面 + claim/return 小写面
// 暴露为 MCP 工具。stdio 惯例：stdout 只走协议，日志一律 stderr。
package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/mark3labs/mcp-go/server"

	"github.com/ybs/agent-team-workbench/internal/mcpserver"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

func main() {
	dsnDefault := os.Getenv("DATABASE_URL")
	if dsnDefault == "" {
		dsnDefault = sqlstore.DefaultDSN
	}
	dsn := flag.String("dsn", dsnDefault, "SQLite 数据库 DSN（sqlite://）")
	flag.Parse()
	db, err := sqlstore.Open(context.Background(), *dsn)
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()

	log.Printf("atw-mcp %s: SQLite stdio MCP server 启动", mcpserver.Version)
	if err := server.ServeStdio(mcpserver.New(sqlstore.New(db))); err != nil {
		log.Fatalf("serve stdio 失败: %v", err)
	}
}

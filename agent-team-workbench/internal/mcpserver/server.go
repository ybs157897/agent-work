// Package mcpserver 把任务看板暴露为 stdio MCP server（设计文档 F5）：
// 第一批只读查询面 + 第二批 claim/return 小写面，供 agent harness 经
// MCP 配置拉起本进程后自查看板。stdout 只走 MCP 协议，日志一律 stderr。
package mcpserver

import (
	"context"
	"log"

	"github.com/mark3labs/mcp-go/server"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// Version 对外上报的 server 版本（tools/list 握手可见）。
const Version = "0.1.0"

// New 装配 MCP server：写面 Service 用 stderr no-op dispatcher/notifier——
// MCP 进程不派活、不推 SSE，控制面主进程才负责调度。
func New(store application.Store) *server.MCPServer {
	svc := application.NewService(store, stderrDispatcher{}, stderrNotifier{}, atwruntime.NewRegistry())
	s := server.NewMCPServer("atw-mcp", Version)
	RegisterTools(s, &toolDeps{store: store, svc: svc})
	return s
}

// stderrDispatcher 理论上不会被触发（工具表不含派活命令），
// 兜底记 stderr 而非 panic，避免拖垮 stdio 协议进程。
type stderrDispatcher struct{}

func (stderrDispatcher) Dispatch(_ context.Context, run *domain.ExecutionRun) error {
	log.Printf("atw-mcp: unexpected dispatch for run %s (MCP 面不派活)", run.ID)
	return nil
}

type stderrNotifier struct{}

func (stderrNotifier) Notify(workspaceID string) {
	// MCP 进程无 SSE 订阅者，无需通知。
}

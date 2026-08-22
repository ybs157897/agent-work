// cmd/control-plane 启动 Go 控制平面：HTTP API + SSE + Mock Adapter（M1）。
// 存储：DATABASE_URL 支持 postgres:// 与 sqlite://（本地验证）。
package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/ybs/agent-team-workbench/internal/agentconfig"
	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/httpapi"
	"github.com/ybs/agent-team-workbench/internal/outbox"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	"github.com/ybs/agent-team-workbench/internal/runnergateway"
	"github.com/ybs/agent-team-workbench/internal/runtime"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/claudecode"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/codexapp"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/dsh"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/kimi"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/mock"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/scripted"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/zcode"
	"github.com/ybs/agent-team-workbench/internal/sse"
	_ "modernc.org/sqlite"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// lookOK 判断可执行文件可用（PATH 或文件路径）。
func lookOK(bin string) bool {
	if strings.Contains(bin, string(os.PathSeparator)) {
		_, err := os.Stat(bin)
		return err == nil
	}
	_, err := exec.LookPath(bin)
	return err == nil
}

func main() {
	dsn := env("DATABASE_URL", "sqlite://workbench.db")
	addr := env("LISTEN_ADDR", ":8080")

	ctx := context.Background()
	db, store, err := openStore(ctx, dsn)
	if err != nil {
		log.Fatalf("初始化存储失败: %v", err)
	}
	defer db.Close()
	hub := sse.NewHub()

	// 先构造 Service（无 dispatcher），Mock Adapter / Gateway 依赖 Service 作为引擎。
	registry := runtime.NewRegistry()
	svc := application.NewService(store, nil, hub, registry)
	adapter := mock.New(svc)
	registry.Register("mock", adapter)
	// M3 第二 Adapter：scripted 录制回放，验证 Adapter 边界与跨 Runtime 重试。
	registry.Register("scripted", scripted.New(svc))
	// M2 进程内 DSH Adapter：无 Runner 时直接驱动本机 DeepSeek Harness runtime。
	dshCfg := dsh.Config{
		BinPath:       env("ATW_DSH_BIN", "runtimes/dsh/node_modules/@deepseek-ai/dsh-sdk-jsonrpc-demo/lib/bin.js"),
		ConfigPath:    env("ATW_DSH_CONFIG", "runtimes/dsh/cordis.yml"),
		WorkspaceRoot: env("ATW_DSH_WORKDIR", "."),
		SessionRoot:   env("ATW_DATA_DIR", ".atw-data") + "/sessions",
		Model:         env("DSH_MODEL", "deepseek-v4-flash"),
		Provider:      "deepseek-official",
	}
	// 默认 cordis.yml 缺失时由 base + 工具片段生成（runtimes/dsh 工程需已 pnpm install）。
	if err := dsh.EnsureDefaultConfig(dshCfg.ConfigPath); err != nil {
		log.Printf("dsh: 默认配置未生成（probe 将报告不可用）: %v", err)
	}
	dshAdapter := dsh.New(dshCfg, svc)
	registry.Register("dsh", dshAdapter)
	// M3 真实 Adapter：OpenAI Codex app-server（本机 CLI 存在时启用）。
	if codexBin := env("ATW_CODEX_BIN", "codex"); lookOK(codexBin) {
		registry.Register("codex-appserver", codexapp.New(codexapp.Config{
			BinPath: codexBin, WorkspaceRoot: env("ATW_DSH_WORKDIR", "."),
			Model: env("ATW_CODEX_MODEL", ""),
		}, svc))
		log.Printf("codexapp: 已注册（bin=%s）", codexBin)
	}
	// M4 真实 Adapter：Claude Code CLI print mode（本机 CLI 存在时启用）。
	if claudeBin := env("ATW_CLAUDE_BIN", "claude"); lookOK(claudeBin) {
		registry.Register("claude-code", claudecode.New(claudecode.Config{
			BinPath: claudeBin, WorkspaceRoot: env("ATW_DSH_WORKDIR", "."),
			Model: env("ATW_CLAUDE_MODEL", ""),
		}, svc))
		log.Printf("claudecode: 已注册（bin=%s）", claudeBin)
	}
	// M3/M4 真实 Adapter：Kimi Code CLI print mode（本机 CLI 存在时启用）。
	if kimiBin := env("ATW_KIMI_BIN", "kimi"); lookOK(kimiBin) {
		registry.Register("kimi", kimi.New(kimi.Config{
			BinPath: kimiBin, WorkspaceRoot: env("ATW_DSH_WORKDIR", "."),
			Model: env("ATW_KIMI_MODEL", ""),
		}, svc))
		log.Printf("kimi: 已注册（bin=%s）", kimiBin)
	}
	// M5 spike：ZCode 仅注册 ProtocolProbe，不提供执行面。
	registry.Register("zcode-probe", zcode.New())

	// M2 Runner WSS Gateway：有在线 Runner 时分派到 Runner，否则回退内置 Mock。
	gateway := runnergateway.New(store, svc, hub)
	svc.SetDispatcher(&chainDispatcher{gw: gateway, fallback: adapter, store: store, registry: registry})
	svc.ApprovalForwarder = func(ctx context.Context, runID, approvalID string, approved bool) {
		gateway.ForwardApproval(ctx, runID, approvalID, approved)
		// 进程内 adapter（无 Runner）：把决定回写给阻塞中的服务端请求。
		run, err := store.Runs().Get(ctx, runID)
		if err != nil {
			return
		}
		adapterID := "mock"
		if b, err := store.Bindings().GetByLabel(ctx, run.WorkspaceID, run.RuntimeLabel); err == nil {
			adapterID = b.AdapterID
		}
		if a, ok := registry.Get(adapterID); ok {
			if r, ok := a.(interface{ ResolveApproval(string, bool) }); ok {
				r.ResolveApproval(runID, approved)
			}
		}
	}
	svc.ControlForwarder = func(ctx context.Context, runID, action string) {
		gateway.ForwardControl(ctx, runID, action)
		// 进程内 adapter（无 Runner）：cancel/interrupt → 进程组级终止（DSH process_scoped）。
		run, err := store.Runs().Get(ctx, runID)
		if err != nil {
			return
		}
		adapterID := "mock"
		if b, err := store.Bindings().GetByLabel(ctx, run.WorkspaceID, run.RuntimeLabel); err == nil {
			adapterID = b.AdapterID
		}
		a, ok := registry.Get(adapterID)
		if !ok {
			return
		}
		terminal := domain.RunCancelled
		if action == "interrupt" {
			terminal = domain.RunInterrupted
		}
		if c, ok := a.(interface {
			Control(string, domain.RunStatus)
		}); ok {
			c.Control(runID, terminal)
		}
	}
	// steering 输入转发：解析 run 的 adapter，支持 InputForwarder 的进程内 adapter 直接下发同 session prompt。
	svc.InputForwarder = func(ctx context.Context, runID, instruction string) {
		run, err := store.Runs().Get(ctx, runID)
		if err != nil {
			return
		}
		adapterID := "mock"
		if b, err := store.Bindings().GetByLabel(ctx, run.WorkspaceID, run.RuntimeLabel); err == nil {
			adapterID = b.AdapterID
		}
		a, ok := registry.Get(adapterID)
		if !ok {
			return
		}
		if f, ok := a.(runtime.InputForwarder); ok {
			if err := f.ForwardInput(ctx, runID, instruction); err != nil {
				log.Printf("转发 run 输入失败（%s → %s）: %v", runID, adapterID, err)
			}
		}
	}

	// Outbox publisher：事件先提交再发布（至少一次）。
	go outbox.NewPublisher(db).Run(ctx)

	if err := seed(ctx, svc, store); err != nil {
		log.Fatalf("seed 失败: %v", err)
	}

	// agents/ 目录为 Agent 配置真相源：启动导入 DB 投影（协议 §4.1）。
	agentCfg := agentconfig.NewImporter(env("AGENT_CONFIG_DIR", "agents"), store)
	if wsIDs, err := store.Workspaces().ListIDs(ctx); err == nil {
		for _, id := range wsIDs {
			res, err := agentCfg.Import(ctx, id)
			if err != nil {
				log.Fatalf("导入 agents/ 配置失败: %v", err)
			}
			if res.Created+res.Updated > 0 {
				log.Printf("agent 配置导入 workspace %s: %+v", id, res)
			}
		}
	}

	server := httpapi.NewServer(svc, store, hub)
	server.SetAgentConfigSync(agentCfg)
	root := http.NewServeMux()
	root.Handle("/runner/v1/connect", gateway)
	root.Handle("/", server.Routes())
	handler := http.Handler(root)
	// 若存在前端构建产物（web/dist），以 SPA fallback 静态托管，单二进制即可演示。
	if dist := env("WEB_DIST", "web/dist"); dirExists(dist) {
		handler = withStatic(handler, dist)
		log.Printf("静态托管前端产物 %s", dist)
	}
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("control-plane 监听 %s（Mock Adapter + Runner Gateway 已启用）", addr)
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// chainDispatcher：在线 Runner 可承接时走 WSS 分派；否则按 binding 的 adapter_id
// 路由到进程内已注册 Adapter（DSH 等），最后兜底内置 Mock（M1 路径）。
type chainDispatcher struct {
	gw       *runnergateway.Gateway
	fallback application.Dispatcher
	store    application.Store
	registry *runtime.Registry
}

func (c *chainDispatcher) Dispatch(ctx context.Context, run *domain.ExecutionRun) error {
	adapterID := "mock"
	if run.RuntimeLabel != "" {
		if b, err := c.store.Bindings().GetByLabel(ctx, run.WorkspaceID, run.RuntimeLabel); err == nil {
			adapterID = b.AdapterID
		}
	}
	if c.gw.Available(adapterID) {
		log.Printf("dispatch: run %s → 在线 Runner（adapter=%s）", run.ID, adapterID)
		return c.gw.Dispatch(ctx, run, adapterID)
	}
	if a, ok := c.registry.Get(adapterID); ok {
		if d, ok := a.(application.Dispatcher); ok {
			log.Printf("dispatch: run %s → 进程内 Adapter（%s）", run.ID, adapterID)
			return d.Dispatch(ctx, run)
		}
	}
	log.Printf("dispatch: run %s → Mock 兜底（adapter=%s 未注册执行面）", run.ID, adapterID)
	return c.fallback.Dispatch(ctx, run)
}

// openStore 按 DSN 前缀选择驱动：postgres:// → pgx stdlib；sqlite:// → 本地文件。
func openStore(ctx context.Context, dsn string) (*sql.DB, application.Store, error) {
	switch {
	case strings.HasPrefix(dsn, "sqlite://"):
		path := strings.TrimPrefix(dsn, "sqlite://")
		db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			return nil, nil, err
		}
		// 本地验证库：写串行避免锁竞争。
		db.SetMaxOpenConns(1)
		if err := db.PingContext(ctx); err != nil {
			return nil, nil, err
		}
		return db, sqlstore.New(db, sqlstore.SQLiteDialect()), nil
	default:
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			return nil, nil, err
		}
		if err := db.PingContext(ctx); err != nil {
			return nil, nil, err
		}
		return db, sqlstore.New(db, sqlstore.PostgresDialect()), nil
	}
}

// seed 在无数据时创建演示 Workspace、角色团队与看板任务。
func seed(ctx context.Context, svc *application.Service, store application.Store) error {
	ids, err := store.Workspaces().ListIDs(ctx)
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		return nil
	}
	ws := &domain.Workspace{
		ID: domain.NewID(domain.PrefixWorkspace), Name: "Agent Team Demo",
		Timezone: "UTC", Version: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		return err
	}
	log.Printf("seed: 创建演示 workspace %s", ws.ID)

	roles := []application.CreateAgentParams{
		{Name: "Nova", Role: "pm", Skills: []string{"需求分析", "优先级管理"}},
		{Name: "Atlas", Role: "architect", Skills: []string{"系统设计", "契约评审"}},
		{Name: "Pixel", Role: "ui", Skills: []string{"交互设计", "视觉规范"}},
		{Name: "Forge", Role: "developer", Skills: []string{"Go", "React", "测试"}},
		{Name: "Sentinel", Role: "reviewer", Skills: []string{"代码评审", "验收"}},
	}
	var devID string
	for _, p := range roles {
		a, err := svc.CreateAgent(ctx, ws.ID, p)
		if err != nil {
			return err
		}
		if p.Role == "developer" {
			devID = a.ID
		}
	}

	tasks := []application.CreateWorkItemParams{
		{Title: "实现任务卡详情与运行时间线", Priority: domain.PriorityHigh, AgentProfileID: devID},
		{Title: "SSE 断线补发验收", Priority: domain.PriorityMedium},
		{Title: "审批交互联调（提示词包含 approval）", Priority: domain.PriorityUrgent, AgentProfileID: devID},
	}
	for _, t := range tasks {
		if _, err := svc.CreateWorkItem(ctx, ws.ID, t); err != nil {
			return err
		}
	}

	// 默认 Runtime 与模型配置：Mock Adapter + 模拟 provider/model。
	if _, err := svc.CreateRuntimeBinding(ctx, ws.ID, application.CreateBindingParams{
		RuntimeLabel: "mock", AdapterID: "mock",
		Provider: "mock-provider", Model: "mock-model-v1",
		CredentialRef: "credref://runner-local/mock",
	}); err != nil {
		return err
	}
	// 第二 Runtime：scripted fixture 回放（跨 Runtime 重试验证用）。
	if _, err := svc.CreateRuntimeBinding(ctx, ws.ID, application.CreateBindingParams{
		RuntimeLabel: "scripted", AdapterID: "scripted",
		Provider: "fixture", Model: "fixture-v1",
		CredentialRef: "credref://runner-local/scripted",
	}); err != nil {
		return err
	}
	// DSH 本地 Runtime：真实 DeepSeek 模型（需 DEEPSEEK_API_KEY；Agent 配置里切换 preferred）。
	if _, err := svc.CreateRuntimeBinding(ctx, ws.ID, application.CreateBindingParams{
		RuntimeLabel: "dsh_local", AdapterID: "dsh",
		Provider: "deepseek", Model: "deepseek-v4-flash",
		CredentialRef: "env://DEEPSEEK_API_KEY",
	}); err != nil {
		return err
	}
	return nil
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// withStatic 把 /api/ 之外的请求交给前端产物；未命中文件回退 index.html（SPA 路由）。
func withStatic(api http.Handler, dist string) http.Handler {
	files := http.FileServer(http.Dir(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/runner/") {
			api.ServeHTTP(w, r)
			return
		}
		p := filepath.Join(dist, filepath.Clean(strings.TrimPrefix(r.URL.Path, "/")))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			files.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dist, "index.html"))
	})
}

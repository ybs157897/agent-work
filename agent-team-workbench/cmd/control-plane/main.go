// cmd/control-plane 启动 Go 控制平面：HTTP API + SSE + Mock Adapter（M1）。
// 存储：DATABASE_URL 支持 postgres:// 与 sqlite://（本地验证）。
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/ybs/agent-team-workbench/internal/agentconfig"
	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/httpapi"
	"github.com/ybs/agent-team-workbench/internal/knowledge"
	"github.com/ybs/agent-team-workbench/internal/modelconfig"
	"github.com/ybs/agent-team-workbench/internal/orchestrator"
	"github.com/ybs/agent-team-workbench/internal/outbox"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	"github.com/ybs/agent-team-workbench/internal/runnergateway"
	"github.com/ybs/agent-team-workbench/internal/runtime"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/claudecode"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/codexapp"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/dsh"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/kimi"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/kimiapp"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/mock"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/scripted"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/zcode"
	"github.com/ybs/agent-team-workbench/internal/scheduling"
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

// atoiEnv 读整型环境变量；缺失或非法时用 fallback。
func atoiEnv(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// resolveDshRepo 定位 deepseek-harness 仓库根（apps/cli/src/bin.ts 所在工程）：
// ATW_DSH_REPO 显式指定优先，其次探测常见相邻目录。
func resolveDshRepo(workbenchRoot string) string {
	if v := os.Getenv("ATW_DSH_REPO"); v != "" {
		return v
	}
	for _, cand := range []string{
		filepath.Join(workbenchRoot, "..", "deepseek-harness"),
		filepath.Join(workbenchRoot, "..", "..", "deepseek-harness"),
		filepath.Join(workbenchRoot, "runtimes", "dsh-harness"),
	} {
		if _, err := os.Stat(filepath.Join(cand, "apps", "cli", "src", "bin.ts")); err == nil {
			return cand
		}
	}
	return ""
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// run 承载全部启动与生命周期：资源清理走 defer，保证启动失败路径（返回 error）
// 与信号退出路径都先释放 dsh 网关进程、DB 连接再退出。
func run() error {
	dsn := env("DATABASE_URL", "sqlite://workbench.db")
	addr := env("LISTEN_ADDR", ":8080")

	// 优雅退出：SIGINT/SIGTERM 触发 ctx 取消，调度循环 / outbox publisher / HTTP 依次收尾。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, store, err := openStore(ctx, dsn)
	if err != nil {
		return fmt.Errorf("初始化存储失败: %w", err)
	}
	defer db.Close()
	hub := sse.NewHub()

	// 先构造 Service（无 dispatcher）；SPI v2：ModuleRunner 是唯一执行面，
	// registry 仅承载 Manifest/Probe 能力协商（probe/binding 路径）。
	registry := runtime.NewRegistry()
	svc := application.NewService(store, nil, hub, registry)
	modules := runtime.NewModuleRunner(svc)
	modules.RegisterTo(registry, "mock", mock.New())
	// M3 第二 Adapter：scripted 录制回放，验证 Adapter 边界与跨 Runtime 重试。
	modules.RegisterTo(registry, "scripted", scripted.New())
	// M2 进程内 DSH 网关 Adapter（boujoy 形态）：长驻 dsh web 网关 + supervisor，
	// 会话连续性由 harness 磁盘持久化原生保证（网关重启不丢上下文）。
	modelDir := env("MODEL_CONFIG_DIR", "models")
	credStore := modelconfig.NewCredentialsStore(modelDir)
	workbenchRoot, _ := os.Getwd()
	executionRoot := env("ATW_WORKSPACE_ROOT", env("ATW_DSH_WORKDIR", "."))
	dshGateway := dsh.NewGateway(dsh.GatewayConfig{
		BaseURL:       env("ATW_DSH_GATEWAY_URL", ""),
		Port:          atoiEnv("ATW_DSH_GATEWAY_PORT", 3090),
		RepoDir:       resolveDshRepo(workbenchRoot),
		WorkspaceRoot: executionRoot,
		Model:         env("DSH_MODEL", "deepseek-v4-flash"),
	})
	// 退出时回收网关进程（对齐 runnerd；supervisor Close 幂等，未拉起也可安全调用）。
	defer dshGateway.Close()
	modules.RegisterTo(registry, "dsh", dshGateway)
	// M3 真实 Adapter：OpenAI Codex app-server（本机 CLI 存在时启用）。
	if codexBin := env("ATW_CODEX_BIN", "codex"); lookOK(codexBin) {
		modules.RegisterTo(registry, "codex-appserver", codexapp.New(codexapp.Config{
			BinPath: codexBin, WorkspaceRoot: executionRoot,
			Model: env("ATW_CODEX_MODEL", ""),
		}))
		log.Printf("codexapp: 已注册（bin=%s）", codexBin)
	}
	// M4 真实 Adapter：Claude Code CLI print mode（本机 CLI 存在时启用）。
	if claudeBin := env("ATW_CLAUDE_BIN", "claude"); lookOK(claudeBin) {
		modules.RegisterTo(registry, "claude-code", claudecode.New(claudecode.Config{
			BinPath: claudeBin, WorkspaceRoot: executionRoot,
			Model: env("ATW_CLAUDE_MODEL", ""),
		}))
		log.Printf("claudecode: 已注册（bin=%s）", claudeBin)
	}
	// M3/M4 真实 Adapter：Kimi Code CLI print mode（本机 CLI 存在时启用）。
	if kimiBin := env("ATW_KIMI_BIN", "kimi"); lookOK(kimiBin) {
		modules.RegisterTo(registry, "kimi", kimi.New(kimi.Config{
			BinPath: kimiBin, WorkspaceRoot: executionRoot,
			Model: env("ATW_KIMI_MODEL", ""),
		}))
		log.Printf("kimi: 已注册（bin=%s）", kimiBin)
	}
	// 网关 Adapter：Kimi Code app-server（kap-server）。本机 kimi CLI 存在时
	// 受管拉起 `kimi web`（KIMI_CODE_HOME 缺省 .atw-data/kimi-home，端口动态
	// 空闲口）；ATW_KIMIAPP_URL 非空则直连外部实例（无需本地 CLI）。
	if kimiBin := env("ATW_KIMIAPP_BIN", "kimi"); lookOK(kimiBin) || env("ATW_KIMIAPP_URL", "") != "" {
		kimiModule := kimiapp.New(kimiapp.Config{
			BaseURL:       env("ATW_KIMIAPP_URL", ""),
			Token:         env("ATW_KIMIAPP_TOKEN", ""),
			Port:          atoiEnv("ATW_KIMIAPP_PORT", 0),
			KimiBin:       kimiBin,
			Home:          env("ATW_KIMIAPP_HOME", filepath.Join(".atw-data", "kimi-home")),
			WorkspaceRoot: executionRoot,
			Model:         env("ATW_KIMIAPP_MODEL", ""),
		})
		// 退出时回收 kap-server 进程组（supervisor Close 幂等，未拉起也可安全调用）。
		defer kimiModule.Close()
		modules.RegisterTo(registry, "kimi-appserver", kimiModule)
		log.Printf("kimiapp: 已注册（bin=%s home=%s）", kimiBin, env("ATW_KIMIAPP_HOME", filepath.Join(".atw-data", "kimi-home")))
	}
	// M5 spike：ZCode probe-only 模块（SPI v2，Execute 报 start_unsupported）。
	modules.RegisterTo(registry, "zcode-probe", zcode.New())

	// M2 Runner WSS Gateway：有在线 Runner 时分派到 Runner，否则按 binding 的
	// adapter_id 路由到进程内 ModuleRunner（唯一执行面）。
	gateway := runnergateway.New(store, svc, hub)
	svc.SetDispatcher(&chainDispatcher{gw: gateway, modules: modules, store: store})
	svc.ApprovalForwarder = func(ctx context.Context, runID, approvalID string, approved bool) {
		// 在线 Runner（ingress 已做 runner 侧审批 ID 翻译）→ 进程内模块 Controls。
		gateway.ForwardApproval(ctx, runID, approvalID, approved)
		_ = modules.ResolveApproval(runID, approvalID, approved)
	}
	svc.ControlForwarder = func(ctx context.Context, runID, action string) {
		gateway.ForwardControl(ctx, runID, action)
		terminal := domain.RunCancelled
		if action == "interrupt" {
			terminal = domain.RunInterrupted
		}
		modules.Control(runID, terminal)
	}
	// steering 输入转发：在线 Runner → 进程内模块（声明 steering 能力）。
	svc.InputForwarder = func(ctx context.Context, runID, instruction string) error {
		if gateway.ForwardInput(ctx, runID, instruction) {
			return nil
		}
		return modules.ForwardInput(ctx, runID, instruction)
	}

	// Outbox publisher：事件先提交再发布（至少一次）；挂可取消 ctx，随进程优雅退出。
	go outbox.NewPublisher(db).Run(ctx)

	if err := seed(ctx, svc, store); err != nil {
		return fmt.Errorf("seed 失败: %w", err)
	}
	if err := ensureBuiltinRuntimeBindings(ctx, svc, store, registry); err != nil {
		return fmt.Errorf("补齐内置 Runtime binding 失败: %w", err)
	}

	// agents/ 目录为 Agent 配置真相源：启动导入 DB 投影（协议 §4.1）。
	agentCfg := agentconfig.NewImporter(env("AGENT_CONFIG_DIR", "agents"), store)
	if wsIDs, err := store.Workspaces().ListIDs(ctx); err == nil {
		for _, id := range wsIDs {
			res, err := agentCfg.Import(ctx, id)
			if err != nil {
				return fmt.Errorf("导入 agents/ 配置失败: %w", err)
			}
			if res.Created+res.Updated > 0 {
				log.Printf("agent 配置导入 workspace %s: %+v", id, res)
			}
		}
	}

	server := httpapi.NewServer(svc, store, hub)
	server.SetAgentConfigSync(agentCfg)

	// models/ 目录为模型注册表真相源：Run 创建时按 agent 的 model.ref 解析快照。
	modelReg := modelconfig.NewRegistry(modelDir)
	if entries, err := modelReg.List(); err != nil {
		log.Printf("模型注册表加载失败（%s）: %v", modelReg.Dir(), err)
	} else {
		log.Printf("模型注册表 %s：%d 个条目", modelReg.Dir(), len(entries))
	}
	svc.ModelResolver = func(ref string) (orchestrator.ModelSpec, bool) {
		e, err := modelReg.Get(ref)
		if err != nil || e == nil {
			return orchestrator.ModelSpec{}, false
		}
		return orchestrator.ModelSpec{
			Ref: e.ID, ProviderID: e.ProviderID, ProviderLabel: e.Category, Provider: e.Provider, API: e.API, Model: e.Model,
			BaseURL: e.BaseURL, APIKeyEnv: e.APIKeyEnv,
			ContextWindow: e.ContextWindow, MaxTokens: e.MaxTokens,
		}, true
	}
	server.SetModelRegistry(modelReg)
	server.SetCredentialsStore(credStore)
	server.SetWorkbenchRoot(workbenchRoot)

	// M2 consult_knowledge：知识语料检索器。root 缺省 <workbenchRoot>/knowledge，
	// ATW_KNOWLEDGE_ROOT 覆盖；根/corpus 目录缺失不报错（语料层可空部署，
	// FileRetriever 对不存在的 corpus 返回空结果）。
	knowledgeRoot := env("ATW_KNOWLEDGE_ROOT", filepath.Join(workbenchRoot, "knowledge"))
	if retriever, err := knowledge.NewFileRetriever(knowledgeRoot); err != nil {
		log.Printf("knowledge: 检索器不可用（root %s）: %v", knowledgeRoot, err)
	} else {
		svc.Knowledge = retriever
		log.Printf("knowledge: 检索器已启用（root %s）", knowledgeRoot)
	}

	// 启动对账：清理上一进程遗留的「无 lease 且非终态」孤儿 run（进程内模块执行），
	// 防止该 (agent, task) 的后续 wakeup 被永久 coalesce 进死 run；runner 路径有
	// lease，由 runnergateway sweeper 负责不受影响。失败不阻断启动。
	if marked, err := svc.ReconcileOrphanRuns(ctx); err != nil {
		log.Printf("启动对账未完全成功: %v", err)
	} else if marked > 0 {
		log.Printf("启动对账：%d 个无租约孤儿 run 已收敛到终态（lost/failed）", marked)
	}

	// M4 wakeup 调度循环：每 tick 先生产到期 timer 唤醒（心跳自主唤醒），再消费全部
	// 到期唤醒（timer/assignment/on_demand）→ 源门控/心跳 claim → 活跃 run 合并
	//（instruction 经 InputForwarder 转发 steering）→ 模板渲染 → CreateRun。
	// 单 goroutine 顺序消费，避免并发双发 run。
	wakeupTick := time.Duration(atoiEnv("ATW_WAKEUP_TICK_SEC", 10)) * time.Second
	scheduler := &scheduling.Scheduler{
		Store:        store.Wakeups(),
		RunStarter:   svc,
		Interval:     wakeupTick,
		ForwardInput: svc.InputForwarder,
		Logger:       log.Default(),
	}
	go scheduler.Start(ctx)
	log.Printf("wakeup 调度循环已启动（tick %s，心跳缺省 %ds）", wakeupTick, domain.DefaultHeartbeatIntervalSec)

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
	// HTTP 服务异步监听；SIGINT/SIGTERM 或监听错误时进入优雅关闭
	//（先 Shutdown 停止接新请求，再经 defer 链回收 dsh 网关与 DB）。
	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
	case <-ctx.Done():
		log.Printf("收到退出信号，control-plane 开始优雅关闭")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("http 优雅关闭超时（强制退出）: %v", err)
	}
	return nil
}

// chainDispatcher：在线 Runner 可承接时走 WSS 分派；否则按 binding 的
// adapter_id 路由到进程内 ModuleRunner（唯一执行面）。
type chainDispatcher struct {
	gw      *runnergateway.Gateway
	modules *runtime.ModuleRunner
	store   application.Store
}

func (c *chainDispatcher) Dispatch(ctx context.Context, run *domain.ExecutionRun) error {
	adapterID := "mock"
	if run.RuntimeLabel != "" {
		if b, err := c.store.Bindings().GetByLabel(ctx, run.WorkspaceID, run.RuntimeLabel); err == nil {
			adapterID = b.AdapterID
		}
	}
	// 远程分派前校验 runner 上报的 manifest digest 与控制面本地真相一致；
	// 无本地模块时 digest 为空，校验退化为在线性判断。
	wantDigest := ""
	if m, ok := c.modules.Module(adapterID); ok {
		if mf, err := m.Manifest(ctx); err == nil {
			wantDigest = mf.SchemaDigest
		}
	}
	if c.gw.VerifyAdapterDigest(adapterID, wantDigest) {
		log.Printf("dispatch: run %s → 在线 Runner（adapter=%s）", run.ID, adapterID)
		return c.gw.Dispatch(ctx, run, adapterID)
	}
	if c.modules.Has(adapterID) {
		log.Printf("dispatch: run %s → 进程内模块（%s）", run.ID, adapterID)
		return c.modules.Dispatch(ctx, run)
	}
	return fmt.Errorf("%w: adapter %s 未注册执行面", domain.ErrCapabilityMissing, adapterID)
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

// ensureBuiltinRuntimeBindings 为已有 Workspace 补齐本机已注册的 Runtime。
// 不覆盖同名 binding，用户的 provider/model/版本与健康状态保持权威。
func ensureBuiltinRuntimeBindings(ctx context.Context, svc *application.Service, store application.Store, registry *runtime.Registry) error {
	workspaceIDs, err := store.Workspaces().ListIDs(ctx)
	if err != nil {
		return err
	}
	// codex_local：本机 codex-appserver 已注册时补齐。
	if _, ok := registry.Get("codex-appserver"); ok {
		for _, workspaceID := range workspaceIDs {
			if _, err := store.Bindings().GetByLabel(ctx, workspaceID, "codex_local"); err == nil {
				continue
			}
			if _, err := svc.CreateRuntimeBinding(ctx, workspaceID, application.CreateBindingParams{
				RuntimeLabel: "codex_local", AdapterID: "codex-appserver",
				Provider: "codex", Model: strings.TrimSpace(os.Getenv("ATW_CODEX_MODEL")),
				CredentialRef: "codex://local-account",
			}); err != nil {
				return err
			}
		}
	}
	// kimi_local：本机 kimi-appserver 已注册时补齐（模型由 kap-server 侧
	// 账号配置决定，binding 仅透传 ATW_KIMIAPP_MODEL 缺省）。
	if _, ok := registry.Get("kimi-appserver"); ok {
		for _, workspaceID := range workspaceIDs {
			if _, err := store.Bindings().GetByLabel(ctx, workspaceID, "kimi_local"); err == nil {
				continue
			}
			if _, err := svc.CreateRuntimeBinding(ctx, workspaceID, application.CreateBindingParams{
				RuntimeLabel: "kimi_local", AdapterID: "kimi-appserver",
				Provider: "kimi", Model: strings.TrimSpace(os.Getenv("ATW_KIMIAPP_MODEL")),
				CredentialRef: "kimi://local-account",
			}); err != nil {
				return err
			}
		}
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

// cmd/control-plane 启动 Go 控制平面：HTTP API + SSE + Mock Adapter（M1）。
// 存储：DATABASE_URL 只接受 sqlite:// DSN。
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ybs/agent-team-workbench/internal/agentconfig"
	"github.com/ybs/agent-team-workbench/internal/agentwork"
	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/hostregistry"
	"github.com/ybs/agent-team-workbench/internal/httpapi"
	"github.com/ybs/agent-team-workbench/internal/knowledge"
	"github.com/ybs/agent-team-workbench/internal/modelconfig"
	"github.com/ybs/agent-team-workbench/internal/observability"
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
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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
	// D4（Run Journal M2）：结构化日志默认 handler——text 输出 stderr，level
	// info。gateway 恢复路径与重启对账等「出了事才看得到」的关键环节经 slog
	// 输出，字段带 run_id/code；其余 log.Printf 保持现状（只换关键环节）。
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	dsn := env("DATABASE_URL", sqlstore.DefaultDSN)
	addr := env("LISTEN_ADDR", ":8080")

	// 优雅退出：SIGINT/SIGTERM 触发 ctx 取消，调度循环 / outbox publisher / HTTP 依次收尾。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := sqlstore.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("初始化存储失败: %w", err)
	}
	defer db.Close()
	store := sqlstore.New(db)
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
	workbenchRoot, _ := os.Getwd()
	projectSpace := agentwork.Resolve(workbenchRoot)
	if err := projectSpace.Ensure(); err != nil {
		return fmt.Errorf("初始化项目空间 %s 失败: %w", projectSpace.Root, err)
	}
	credStore := modelconfig.NewCredentialsStore(workbenchRoot)
	// 本机受信 registry（RFC §4.3）：root 只存在于本机 yaml；ATW_HOST_REGISTRY
	// 缺省 ./host-registry.yaml。加载失败（含文件不存在）时本机 mount 为空、
	// 只保证 host_local 存在——绝不从进程环境猜测执行根（RFC §6.1）。
	localRegistry := hostregistry.New()
	registryPath := env("ATW_HOST_REGISTRY", "host-registry.yaml")
	if reg, err := hostregistry.Load(registryPath); err == nil {
		localRegistry = reg
	} else {
		log.Printf("hostregistry: 加载 %s 失败（%v）；本机 mount 为空，仅保证 host_local 存在", registryPath, err)
	}
	dshGateway := dsh.NewGateway(dsh.GatewayConfig{
		BaseURL: env("ATW_DSH_GATEWAY_URL", ""),
		Port:    atoiEnv("ATW_DSH_GATEWAY_PORT", 3090),
		RepoDir: resolveDshRepo(workbenchRoot),
		Home:    projectSpace.DSHHome(),
		Model:   env("DSH_MODEL", "deepseek-v4-flash"),
	})
	// 退出时回收网关进程（对齐 runnerd；supervisor Close 幂等，未拉起也可安全调用）。
	defer dshGateway.Close()
	modules.RegisterTo(registry, "dsh", dshGateway)
	// M3 真实 Adapter：OpenAI Codex app-server（本机 CLI 存在时启用）。
	codexBin := agentwork.ResolveBundledBin(workbenchRoot, "ATW_CODEX_BIN", "codex", "codex")
	if agentwork.ExecutableOK(codexBin) {
		modules.RegisterTo(registry, "codex-appserver", codexapp.New(codexapp.Config{
			BinPath: codexBin, Home: projectSpace.CodexHome(),
			Model: env("ATW_CODEX_MODEL", ""),
		}))
		log.Printf("codexapp: 已注册（bin=%s home=%s）", codexBin, projectSpace.CodexHome())
	}
	// M4 真实 Adapter：Claude Code CLI print mode（本机 CLI 存在时启用）。
	if claudeBin := env("ATW_CLAUDE_BIN", "claude"); agentwork.ExecutableOK(claudeBin) {
		modules.RegisterTo(registry, "claude-code", claudecode.New(claudecode.Config{
			BinPath: claudeBin,
			Model:   env("ATW_CLAUDE_MODEL", ""),
		}))
		log.Printf("claudecode: 已注册（bin=%s）", claudeBin)
	}
	// M3/M4 真实 Adapter：Kimi Code CLI print mode（本机 CLI 存在时启用）。
	kimiBin := agentwork.ResolveBundledBin(workbenchRoot, "ATW_KIMI_BIN", "kimi", "kimi")
	if agentwork.ExecutableOK(kimiBin) {
		modules.RegisterTo(registry, "kimi", kimi.New(kimi.Config{
			BinPath: kimiBin, Home: projectSpace.KimiHome(),
			Model: env("ATW_KIMI_MODEL", ""),
		}))
		log.Printf("kimi: 已注册（bin=%s home=%s）", kimiBin, projectSpace.KimiHome())
	}
	// 网关 Adapter：Kimi Code app-server（kap-server）。本机 kimi CLI 存在时
	// 受管拉起 `kimi web`（KIMI_CODE_HOME 缺省 .atw-data/kimi-home，端口动态
	// 空闲口）；ATW_KIMIAPP_URL 非空则直连外部实例（无需本地 CLI）。
	kimiAppBin := agentwork.ResolveBundledBin(workbenchRoot, "ATW_KIMIAPP_BIN", "kimi", kimiBin)
	if agentwork.ExecutableOK(kimiAppBin) || env("ATW_KIMIAPP_URL", "") != "" {
		kimiModule := kimiapp.New(kimiapp.Config{
			BaseURL: env("ATW_KIMIAPP_URL", ""),
			Token:   env("ATW_KIMIAPP_TOKEN", ""),
			Port:    atoiEnv("ATW_KIMIAPP_PORT", 0),
			KimiBin: kimiAppBin,
			Home:    projectSpace.KimiHome(),
			Model:   env("ATW_KIMIAPP_MODEL", ""),
		})
		// 退出时回收 kap-server 进程组（supervisor Close 幂等，未拉起也可安全调用）。
		defer kimiModule.Close()
		modules.RegisterTo(registry, "kimi-appserver", kimiModule)
		log.Printf("kimiapp: 已注册（bin=%s home=%s）", kimiAppBin, projectSpace.KimiHome())
	}
	// M5 spike：ZCode probe-only 模块（SPI v2，Execute 报 start_unsupported）。
	modules.RegisterTo(registry, "zcode-probe", zcode.New())

	// 执行上下文解析器（RFC §4.6/§7.5）：从持久 snapshot + 本机受信 registry
	// 产出进程内可信 Resolved；本地执行只服务 host_local 路由（chainDispatcher）。
	modules.SetSnapshotResolver(func(ctx context.Context, runID string) (domain.ExecutionContextSnapshot, domain.ResolvedExecutionContext, error) {
		snapshot, err := store.ContextSnapshots().GetByRun(ctx, runID)
		if err != nil {
			return domain.ExecutionContextSnapshot{}, domain.ResolvedExecutionContext{}, err
		}
		resolved, err := localRegistry.Resolve(snapshot)
		if err != nil {
			return domain.ExecutionContextSnapshot{}, domain.ResolvedExecutionContext{}, err
		}
		return *snapshot, resolved, nil
	})

	// M2 Runner WSS Gateway（v2）：按 snapshot.execution_host_id 精确路由到
	// Runner，host_local 走进程内 ModuleRunner（唯一本地执行面）。
	gateway := runnergateway.New(store, svc, hub)
	svc.SetDispatcher(&chainDispatcher{gw: gateway, modules: modules, store: store, svc: svc,
		journal: observability.NewJournal(svc.RecordRunEvent)})
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

	// 本机执行上下文 bootstrap（RFC §6.1）：EnsureLocalHost + 本机 mount 广告落库；
	// 单 Workspace 且无 Location 才自动建默认 Location（多 Workspace 保持 unmapped）。
	if err := svc.EnsureLocalHostBootstrap(ctx, localRegistry.Advertise()); err != nil {
		return fmt.Errorf("本机 Host bootstrap 失败: %w", err)
	}
	if err := svc.EnsureSingleWorkspaceDefaultLocation(ctx); err != nil {
		return fmt.Errorf("默认 Location bootstrap 失败: %w", err)
	}

	// models/ 目录为模型注册表真相源：Run 创建时按 agent 的 model.ref 解析快照。
	modelReg := modelconfig.NewRegistry(modelDir)
	if entries, err := modelReg.List(); err != nil {
		log.Printf("模型注册表加载失败（%s）: %v", modelReg.Dir(), err)
	} else {
		log.Printf("模型注册表 %s：%d 个条目", modelReg.Dir(), len(entries))
	}
	if providers, err := modelReg.Providers(); err == nil {
		if n := credStore.HydrateEnv(providers); n > 0 {
			log.Printf("项目凭据已注入 %d 个 api_key_env（来源 %s）", n, credStore.Path())
		}
	}
	svc.ModelResolver = func(ref string) (orchestrator.ModelSpec, bool) {
		e, err := modelReg.Get(ref)
		if err != nil || e == nil {
			return orchestrator.ModelSpec{}, false
		}
		var price *domain.PriceSnapshotRef
		if e.Price != nil {
			copyPrice := *e.Price
			price = &copyPrice
		}
		return orchestrator.ModelSpec{
			Ref: e.ID, ProviderID: e.ProviderID, ProviderLabel: e.Category, Provider: e.Provider, API: e.API, Model: e.Model,
			BaseURL: e.BaseURL, APIKeyEnv: e.APIKeyEnv,
			ContextWindow: e.ContextWindow, MaxTokens: e.MaxTokens, PriceSnapshot: price,
		}, true
	}

	// agents/ 目录为 Agent 配置真相源：先重放 SQLite 中未完成的 durable
	// sync-intent，再允许文件导入覆盖 DB 投影。Runtime homes 与模型解析器
	// 只留在进程内，绝不写入 intent 快照。
	agentCfg := agentconfig.NewImporter(env("AGENT_CONFIG_DIR", "agents"), store)
	agentCfg.SetRuntimeConfig(agentconfig.RuntimeConfig{
		CodexHome: projectSpace.CodexHome(), KimiHome: projectSpace.KimiHome(),
		ResolveModel: svc.ModelResolver,
	})
	if reconciled, reconcileErr := agentCfg.Reconcile(ctx); reconcileErr != nil {
		return fmt.Errorf("对账 Agent 配置同步 intent 失败: %w", reconcileErr)
	} else if reconciled.Applied > 0 {
		log.Printf("agent 配置同步 intent 已对账: %+v", reconciled)
	}
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

	// Native governance state recovery runs after Agent import (Todo scopes need
	// durable owner identities) and before schedulers/recovery may advance work.
	// Consistency issues are observable but never silently overwritten.
	governanceWorkspaceIDs, err := store.Workspaces().ListIDs(ctx)
	if err != nil {
		return fmt.Errorf("列出治理 workspace 失败: %w", err)
	}
	for _, workspaceID := range governanceWorkspaceIDs {
		result, rebuildErr := svc.RebuildGovernanceState(ctx, workspaceID)
		if rebuildErr != nil {
			return fmt.Errorf("重建 workspace %s 治理状态失败: %w", workspaceID, rebuildErr)
		}
		if result.CreatedGoals+result.CreatedTodos > 0 {
			log.Printf("governance state: workspace %s created goals=%d todos=%d",
				workspaceID, result.CreatedGoals, result.CreatedTodos)
		}
		for _, issue := range result.Issues {
			log.Printf("governance consistency issue: workspace=%s root=%s goal=%s code=%s message=%s",
				workspaceID, issue.RootWorkItemID, issue.GoalID, issue.Code, issue.Message)
		}
	}

	server := httpapi.NewServer(svc, store, hub)
	server.SetAgentConfigSync(agentCfg)

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

	// 0021 前遗留的非终态无快照 Run：落 failed(execution_context_missing) 并触发
	// 既有 Coordinator 恢复（RFC §6.1；无快照的 Run 永不分派）。必须先于
	// 通用 orphan 对账，否则后者会先把它们改成 lost，导致 legacy 路径漏处理。
	if marked, err := svc.ReconcileLegacyContextRuns(ctx); err != nil {
		log.Printf("遗留上下文对账未完全成功: %v", err)
	} else if marked > 0 {
		log.Printf("启动对账：%d 个无快照遗留 run 已收敛到 failed(execution_context_missing)", marked)
	}
	// session_unknown 自愈的 Run 与锚点墓碑已同事务提交，但进程可能
	// 在 commit 后、Dispatcher 前退出。先按 deterministic client key 恢复这些
	// queued Run，再让通用 orphan 对账收敛其余无 lease 运行。
	if recovered, err := svc.RecoverPendingSelfHealRuns(ctx); err != nil {
		log.Printf("启动自愈 Run 恢复未完全成功: %v", err)
	} else if recovered > 0 {
		log.Printf("启动恢复：%d 个 queued session-heal run 已重新分派", recovered)
	}
	// 控制面重启对账 sweeper（Run Journal M2 §3.5）：进程死亡时 host_local 的
	// 在飞 run 无主（leaseSweeper 只管远程 lease），在通用 orphan 对账之前带
	// 证据合成收口——合成闭合未闭合相位 + recovery/decision 事件，失败码
	// control_plane_restart，lost 恒 retryable 交 coordinator due-state 循环
	// 重驱。queued 是合法待派发，本扫不动（留给下方 ReconcileOrphanRuns）。
	// 失败只 slog，不阻断启动。
	if swept, err := svc.ReconcileOrphanedLocalRuns(ctx); err != nil {
		slog.Warn("控制面重启对账未完全成功", "error", err)
	} else if swept > 0 {
		slog.Info("控制面重启对账：无租约在飞 run 已带证据收敛", "count", swept)
	}
	// 清理上一进程遗留的「无 lease 且非终态」普通孤儿 run（进程内模块执行），
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
		// F1 执行锁兜底回收：低频扫死属主（终态 run）残留锁。
		StaleLocks: store.WorkItems(),
		Logger:     log.Default(),
	}
	go scheduler.Start(ctx)
	log.Printf("wakeup 调度循环已启动（tick %s，心跳缺省 %ds）", wakeupTick, domain.DefaultHeartbeatIntervalSec)
	// Task Coordinator 的 queued/waiting_retry/running checkpoint 使用独立的
	// 轻量 due-scan；启动先扫一次，随后持续恢复连接中断或进程重启留下的控制线。
	go svc.RunCoordinatorRecoveryLoop(ctx, 2*time.Second)
	log.Printf("Task Coordinator 恢复循环已启动（tick 2s）")

	root := http.NewServeMux()
	root.Handle(runnergateway.ConnectPath, gateway)
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
	log.Printf("control-plane 监听 %s（Mock Adapter + Runner Gateway 已启用；项目空间 %s）", addr, projectSpace.Root)
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

// chainDispatcher host-aware 精确路由（RFC §7.5）：只读不可变 Snapshot 决定
// 执行面——host_local 走进程内 ModuleRunner，其余按 snapshot.execution_host_id
// 精确投递 Runner；禁止 remote→local 或跨 Host fallback。
type chainDispatcher struct {
	gw      *runnergateway.Gateway
	modules *runtime.ModuleRunner
	store   application.Store
	svc     *application.Service
	// journal 承载 Run Journal 的 dispatch 相位锚点（internal 事件，只落
	// run_events，不进 SSE/回放）。
	journal *observability.Journal
}

func (c *chainDispatcher) Dispatch(ctx context.Context, run *domain.ExecutionRun) error {
	adapterID := "mock"
	if run.RuntimeLabel != "" {
		if b, err := c.store.Bindings().GetByLabel(ctx, run.WorkspaceID, run.RuntimeLabel); err == nil {
			adapterID = b.AdapterID
		}
	}
	// Dispatcher 只读 Snapshot，不重新解析当前 WorkspaceLocation（RFC §5.1.5）；
	// 无快照的 Run 永不分派（RFC §5.1.3/§5.1.4）。
	snapshot, err := c.store.ContextSnapshots().GetByRun(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("run %s 无 context snapshot，拒绝分派: %w", run.ID, err)
	}
	// Run Journal dispatch 相位（入队/选 runner/lease 授予）：entered 在路由
	// 决策点发一次（两条路径必经此处，恰发一对中的一半），closed 由各执行面
	// 在交接成功/失败点补齐——host_local 在本文件，remote 在 runnergateway ingress。
	timer, err := c.journal.EnterPhase(ctx, run.ID, observability.PhaseDispatch, 1, map[string]any{
		"host_id": snapshot.ExecutionHostID, "runtime": run.RuntimeLabel, "adapter": adapterID,
	})
	if err != nil {
		log.Printf("run journal: run %s dispatch phase_entered 落库失败: %v", run.ID, err)
		timer = nil
	}
	if snapshot.ExecutionHostID == domain.LocalHostID {
		if c.modules.Has(adapterID) {
			log.Printf("dispatch: run %s → 进程内模块（%s，host_local）", run.ID, adapterID)
			err := c.modules.Dispatch(ctx, run)
			c.closeDispatchPhase(timer, run.ID, err)
			return err
		}
		err := fmt.Errorf("%w: adapter %s 未注册本地执行面", domain.ErrCapabilityMissing, adapterID)
		c.closeDispatchPhase(timer, run.ID, err)
		return err
	}
	log.Printf("dispatch: run %s → Runner（adapter=%s，host=%s）", run.ID, adapterID, snapshot.ExecutionHostID)
	if err := c.gw.Dispatch(ctx, run, snapshot, adapterID); err != nil {
		// dispatch 相位的 closed 由 ingress 的 lease/入队失败路径发出，这里不重复发；
		// 提交后目标 Host 无可用 Runner：Run 落 failed(retryable)，由 Coordinator
		// 既有有界 retry/replan 恢复；禁止跨 Host/本机 fallback（RFC §7.4）。
		_ = c.svc.RecordRunStatus(context.WithoutCancel(ctx), run.ID, domain.RunFailed,
			map[string]any{"code": "execution_host_unavailable", "retryable": true,
				"message": err.Error(), "family": "workspace"})
		return err
	}
	return nil
}

// closeDispatchPhase 收口 host_local 路径的 dispatch 相位（成对语义的另一半）。
// timer 为 nil 表示 entered 未落库——缺观测不补假事件；journal 失败只记日志，
// 绝不打断业务路径。failure 分类沿用既有 dispatch 终态语义（Service.dispatchRun
// 把 Dispatcher 错误统一落 failed(code=dispatch_failed, retryable)）。
func (c *chainDispatcher) closeDispatchPhase(timer *observability.PhaseTimer, runID string, dispatchErr error) {
	if timer == nil {
		return
	}
	var err error
	if dispatchErr != nil {
		err = timer.Close(observability.PhaseFailed, &observability.PhaseFailure{
			Code: "dispatch_failed", Message: dispatchErr.Error(), Retryable: true,
		}, map[string]any{"route": "host_local"})
	} else {
		err = timer.Close(observability.PhaseOK, nil, map[string]any{"route": "host_local"})
	}
	if err != nil {
		log.Printf("run journal: run %s dispatch phase_closed 落库失败: %v", runID, err)
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

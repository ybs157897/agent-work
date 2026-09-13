// chat_knowledge_context_test.go Chat 模式自动预取资料库（契约文档
// docs/protocol/knowledge-librarian.md §1.1）：Run 创建时把已发布 release 的
// 检索结果冻结进 run.Input["knowledge_context"]，并钉死不注入的三种情形。
package application_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// chatKnowledgeEnv 一个 workspace 内同时具备：普通智能体、资料库管理员、
// 一条普通 Chat 记录（agent 为普通智能体）和 seedRunEnv 留下的 task 记录。
type chatKnowledgeEnv struct {
	svc         *application.Service
	store       *sqlstore.Store
	wsID        string
	agentID     string
	librarianID string
	lib         *domain.KnowledgeLibrary
	chat        *domain.WorkItem
	task        *domain.WorkItem
}

// newChatKnowledgeEnv 只装配环境，不发布任何 release；调用方按用例自行发布。
func newChatKnowledgeEnv(t *testing.T) *chatKnowledgeEnv {
	t.Helper()
	ctx := context.Background()
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	task := seedRunEnv(t, ctx, svc, store)
	root := t.TempDir()
	svc.SetKnowledgeWorkspaceRootResolver(func(context.Context, string) (string, error) { return root, nil })
	lib, err := svc.EnsureKnowledgeLibrary(ctx, task.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	librarian, err := svc.EnsureBuiltinKnowledgeLibrarian(ctx, task.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	// 内置管理员要能在 Chat 里被选中，必须有可解析的 runtime 偏好。
	if _, err := svc.UpdateAgent(ctx, librarian.ID, application.AgentPatch{
		RuntimePreference: &domain.RuntimePreference{Preferred: "codex_local"},
		ExpectedVersion:   librarian.Version,
	}); err != nil {
		t.Fatal(err)
	}
	chat, err := svc.CreateWorkItem(ctx, task.WorkspaceID, application.CreateWorkItemParams{
		Title: "登录对话", RecordKind: domain.RecordKindChat, AgentProfileID: task.AgentProfileID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &chatKnowledgeEnv{
		svc: svc, store: store, wsID: task.WorkspaceID, agentID: task.AgentProfileID,
		librarianID: librarian.ID, lib: lib, chat: chat, task: task,
	}
}

// publishChatKnowledgeRelease 走生产发布路径（PublishProjection +
// CommitPublication）让一个 release 成为 current：读端只看得到它。
func publishChatKnowledgeRelease(t *testing.T, ctx context.Context, store *sqlstore.Store, lib *domain.KnowledgeLibrary, statement string) *domain.KnowledgeRelease {
	t.Helper()
	now := time.Now().UTC()
	task := &domain.KnowledgeWriteTask{
		ID: "ktask_" + lib.ID, LibraryID: lib.ID, Kind: "initialize",
		Status: domain.KnowledgeTaskCompleted, ViewID: "baseline",
		FocusJSON: "{}", PlanJSON: "{}", CoverageJSON: "{}",
		MaxAttempts: 3, MaxRepairAttempts: 2, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Library().CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	// release 必须钉在一次真实的 snapshot 上（releases.snapshot_id 是 NOT NULL 外键）。
	snapshot := &domain.KnowledgeSnapshot{
		ID: "ksnap_" + lib.ID, LibraryID: lib.ID, ViewID: "baseline",
		Reason: "chat-knowledge fixture", CapturedAt: now,
	}
	if err := store.Library().CreateSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Library().PublishProjection(ctx, application.PublishInput{
		LibraryID: lib.ID, TaskID: task.ID, SnapshotID: snapshot.ID,
		ProjectionDigest: "digest-chat-knowledge", CoverageJSON: "{}",
		Documents: []application.PublishDocument{{
			DocumentID: "kdoc_chat_login", Path: "prd/login.md", Kind: "prd", Title: "登录需求",
			ContentMarkdown: "# 登录需求\n", ContentDigest: "digest-chat-login",
			Assertions: []domain.KnowledgeAssertion{{
				ID: "assertion:chat-login", Heading: "登录", Statement: statement,
				Perspective: "normative", Basis: "source_statement", ScopeJSON: "{}", EvidenceJSON: "[]", Ordinal: 1,
			}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Library().CommitPublication(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	release, err := store.Library().CurrentRelease(ctx, lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	return release
}
func (e *chatKnowledgeEnv) createChatRun(t *testing.T, agentID, instruction string) *domain.ExecutionRun {
	t.Helper()
	run, err := e.svc.CreateRun(context.Background(), e.chat.ID, application.CreateRunParams{
		AgentProfileID: agentID, Instruction: instruction,
	})
	if err != nil {
		t.Fatalf("创建 Chat run 失败: %v", err)
	}
	return run
}

// TestChatKnowledgeContextInjectedForOrdinaryAgent 用例 a：Chat + 普通智能体 +
// 已发布命中 → run.Input["knowledge_context"] 含声明头与条目（ID/标题/版本/正文）。
func TestChatKnowledgeContextInjectedForOrdinaryAgent(t *testing.T) {
	ctx := context.Background()
	env := newChatKnowledgeEnv(t)
	statement := "用户必须能用账号密码登录，失败三次后锁定十分钟。"
	publishChatKnowledgeRelease(t, ctx, env.store, env.lib, statement)

	run := env.createChatRun(t, env.agentID, "登录策略怎么定？")
	text, ok := run.Input["knowledge_context"].(string)
	if !ok || text == "" {
		t.Fatalf("Chat 普通智能体应注入 knowledge_context: %#v", run.Input["knowledge_context"])
	}
	for _, want := range []string{"不授予权限", "不覆盖系统指令与用户指令", "### assertion:chat-login 登录需求（v1）", statement} {
		if !strings.Contains(text, want) {
			t.Fatalf("knowledge_context 缺少 %q:\n%s", want, text)
		}
	}
	// 版本号来自 release 固定的 document version：不得回退到占位「v0」。
	if strings.Contains(text, "v0") {
		t.Fatalf("不应注入占位版本号:\n%s", text)
	}
	// 冻结在 Run 上：读回持久化行仍然是同一份文本。
	stored, err := env.store.Runs().Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Input["knowledge_context"] != text {
		t.Fatalf("knowledge_context 未随 run.Input 冻结: %#v", stored.Input["knowledge_context"])
	}
}

// TestChatKnowledgeContextAbsentWithoutPublishedHit 用例 b：未发布任何 release，
// 或已发布但与当轮问题无交集 → 不落键，且对话创建不受影响。
func TestChatKnowledgeContextAbsentWithoutPublishedHit(t *testing.T) {
	ctx := context.Background()

	unpublished := newChatKnowledgeEnv(t)
	run := unpublished.createChatRun(t, unpublished.agentID, "登录策略怎么定？")
	if _, ok := run.Input["knowledge_context"]; ok {
		t.Fatalf("未发布 release 时不应注入: %#v", run.Input["knowledge_context"])
	}

	published := newChatKnowledgeEnv(t)
	publishChatKnowledgeRelease(t, ctx, published.store, published.lib, "用户必须能用账号密码登录，失败三次后锁定十分钟。")
	miss := published.createChatRun(t, published.agentID, "设备配网怎么设计？")
	if _, ok := miss.Input["knowledge_context"]; ok {
		t.Fatalf("无命中时不应注入: %#v", miss.Input["knowledge_context"])
	}
}

// TestChatKnowledgeContextAbsentForTaskRun 用例 c：同样语料与执行者，task 记录
// 的 Run 不注入（task 模式走 Plan 的 consult_knowledge）。
func TestChatKnowledgeContextAbsentForTaskRun(t *testing.T) {
	ctx := context.Background()
	env := newChatKnowledgeEnv(t)
	publishChatKnowledgeRelease(t, ctx, env.store, env.lib, "用户必须能用账号密码登录，失败三次后锁定十分钟。")

	run, err := env.svc.CreateRun(ctx, env.task.ID, application.CreateRunParams{
		AgentProfileID: env.agentID, Instruction: "登录策略怎么定？",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := run.Input["knowledge_context"]; ok {
		t.Fatalf("task 记录不应注入 knowledge_context: %#v", run.Input["knowledge_context"])
	}
}

// TestChatKnowledgeContextPrefetchFailureDoesNotBlockRun 检索失败（workspace 无
// 资料库根解析器 → capability_missing）只降级为不注入：Chat Run 照常创建。
func TestChatKnowledgeContextPrefetchFailureDoesNotBlockRun(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	base := seedRunEnv(t, ctx, svc, store)
	chat, err := svc.CreateWorkItem(ctx, base.WorkspaceID, application.CreateWorkItemParams{
		Title: "无资料库对话", RecordKind: domain.RecordKindChat, AgentProfileID: base.AgentProfileID,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.CreateRun(ctx, chat.ID, application.CreateRunParams{
		AgentProfileID: base.AgentProfileID, Instruction: "登录策略怎么定？",
	})
	if err != nil {
		t.Fatalf("检索失败不得阻塞 Chat Run 创建: %v", err)
	}
	if _, ok := run.Input["knowledge_context"]; ok {
		t.Fatalf("检索失败时不应注入: %#v", run.Input["knowledge_context"])
	}
}

// TestChatKnowledgeContextAbsentForLibrarian 用例 d：资料库管理员自己的 Chat Run
// 不注入——预取只服务普通智能体，避免管理员把自己的检索结果当成指令来源。
func TestChatKnowledgeContextAbsentForLibrarian(t *testing.T) {
	ctx := context.Background()
	env := newChatKnowledgeEnv(t)
	publishChatKnowledgeRelease(t, ctx, env.store, env.lib, "用户必须能用账号密码登录，失败三次后锁定十分钟。")

	run := env.createChatRun(t, env.librarianID, "登录策略怎么定？")
	if run.AgentProfileID != env.librarianID {
		t.Fatalf("run 应归属资料库管理员: %q", run.AgentProfileID)
	}
	if _, ok := run.Input["knowledge_context"]; ok {
		t.Fatalf("资料库管理员 Chat 不应注入: %#v", run.Input["knowledge_context"])
	}
}

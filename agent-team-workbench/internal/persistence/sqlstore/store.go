// Package sqlstore 以 SQLite/database/sql 实现 application.Store。
// 事务模型：InTx 把 *sql.Tx 放进 context，仓储方法自动复用同一事务，
// 保证 状态变更 + run_events + outbox + idempotency 同事务提交。
package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/scheduling"
	_ "modernc.org/sqlite"
)

type txKey struct{}

const (
	// DefaultDSN 是本地控制平面的默认持久化位置。
	DefaultDSN   = "sqlite://workbench.db"
	sqlitePrefix = "sqlite://"
)

// Open 打开本地 SQLite 数据库。仅接受 sqlite:// DSN，所有入口都统一启用
// 外键、写锁等待与 WAL，并把单个进程内的写入串行到一个连接。
func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	driverDSN, err := sqliteDriverDSN(dsn)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", driverDSN)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := verifySQLiteConnectionContract(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func verifySQLiteConnectionContract(ctx context.Context, db *sql.DB) error {
	var foreignKeys, busyTimeout int
	var journalMode string
	if err := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return fmt.Errorf("读取 SQLite foreign_keys 失败: %w", err)
	}
	if err := db.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		return fmt.Errorf("读取 SQLite busy_timeout 失败: %w", err)
	}
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		return fmt.Errorf("读取 SQLite journal_mode 失败: %w", err)
	}
	if foreignKeys != 1 || busyTimeout != 5000 || !strings.EqualFold(journalMode, "wal") {
		return fmt.Errorf("SQLite 连接契约未生效: foreign_keys=%d busy_timeout=%d journal_mode=%q",
			foreignKeys, busyTimeout, journalMode)
	}
	return nil
}

func sqliteDriverDSN(dsn string) (string, error) {
	if !strings.HasPrefix(dsn, sqlitePrefix) {
		return "", fmt.Errorf("SQLite DSN 必须以 %q 开头", sqlitePrefix)
	}
	pathAndQuery := strings.TrimPrefix(dsn, sqlitePrefix)
	path, rawQuery, hasQuery := strings.Cut(pathAndQuery, "?")
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("SQLite DSN 缺少数据库路径")
	}
	query := make(url.Values)
	if hasQuery {
		var err error
		query, err = url.ParseQuery(rawQuery)
		if err != nil {
			return "", fmt.Errorf("SQLite DSN query 非法: %w", err)
		}
	}
	// Pragma 是存储契约，不接受调用方覆盖。普通 SQLite URI 参数（如
	// cache=shared）保留；全部自定义 _pragma 清除后写入唯一可信集合。
	query.Del("_pragma")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "journal_mode(WAL)")
	return path + "?" + query.Encode(), nil
}

// executor 是 *sql.DB 与 *sql.Tx 的公共子集。
type executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type Store struct {
	db           *sql.DB
	workspaces   *WorkspaceRepo
	agents       *AgentRepo
	workItems    *WorkItemRepo
	plans        *PlanRepo
	runs         *RunRepo
	events       *EventRepo
	idem         *IdempotencyRepo
	bindings     *BindingRepo
	runners      *RunnerRepo
	audit        *AuditRepo
	caps         *CapsRepo
	tasks        *TaskSessionRepo
	wakeups      *WakeupRepo
	grants       *ApprovalGrantRepo
	dispatches   *DispatchRepo
	decisions    *DecisionRepo
	search       *SearchRepo
	coordinators *TaskCoordinatorRepo
	// Execution context 四仓储（任务控制面 RFC §4；实现在 execution_contexts.go）。
	execHosts    application.ExecutionHostRepo
	locations    application.WorkspaceLocationRepo
	wiContexts   application.WorkItemContextRepo
	ctxSnapshots application.ContextSnapshotRepo
	// TaskComment append-only 任务反馈流（任务控制面 RFC §4.9；实现在 task_comments.go）。
	taskComments application.TaskCommentRepo
}

var _ application.Store = (*Store)(nil)

// New 构造 SQLite Store；SQL 统一使用 SQLite 的 ? 占位符。
func New(db *sql.DB) *Store {
	s := &Store{db: db}
	s.workspaces = &WorkspaceRepo{store: s}
	s.agents = &AgentRepo{store: s}
	s.workItems = &WorkItemRepo{store: s}
	s.plans = &PlanRepo{store: s}
	s.runs = &RunRepo{store: s}
	s.events = &EventRepo{store: s}
	s.idem = &IdempotencyRepo{store: s}
	s.bindings = &BindingRepo{store: s}
	s.runners = &RunnerRepo{store: s}
	s.audit = &AuditRepo{store: s}
	s.caps = &CapsRepo{store: s}
	s.tasks = &TaskSessionRepo{store: s}
	s.wakeups = &WakeupRepo{store: s}
	s.grants = &ApprovalGrantRepo{store: s}
	s.dispatches = &DispatchRepo{store: s}
	s.decisions = &DecisionRepo{store: s}
	s.search = &SearchRepo{store: s}
	s.coordinators = &TaskCoordinatorRepo{store: s}
	s.execHosts = &ExecutionHostRepo{store: s}
	s.locations = &WorkspaceLocationRepo{store: s}
	s.wiContexts = &WorkItemContextRepo{store: s}
	s.ctxSnapshots = &ContextSnapshotRepo{store: s}
	s.taskComments = &TaskCommentRepo{store: s}
	return s
}

func (s *Store) Workspaces() application.WorkspaceRepo             { return s.workspaces }
func (s *Store) Agents() application.AgentRepo                     { return s.agents }
func (s *Store) WorkItems() application.WorkItemRepo               { return s.workItems }
func (s *Store) Plans() application.PlanRepo                       { return s.plans }
func (s *Store) Runs() application.RunRepo                         { return s.runs }
func (s *Store) Events() application.EventRepo                     { return s.events }
func (s *Store) Idempotency() application.IdempotencyRepo          { return s.idem }
func (s *Store) Bindings() application.RuntimeBindingRepo          { return s.bindings }
func (s *Store) Runners() application.RunnerRepo                   { return s.runners }
func (s *Store) Audit() application.AuditRepo                      { return s.audit }
func (s *Store) Caps() application.CapabilitySnapshotRepo          { return s.caps }
func (s *Store) TaskSessions() application.TaskSessionRepo         { return s.tasks }
func (s *Store) ApprovalGrants() application.ApprovalGrantRepo     { return s.grants }
func (s *Store) Dispatches() application.DispatchRepo              { return s.dispatches }
func (s *Store) DecisionEntries() application.DecisionRepo         { return s.decisions }
func (s *Store) Search() application.SearchRepo                    { return s.search }
func (s *Store) TaskCoordinators() application.TaskCoordinatorRepo { return s.coordinators }

// Execution context accessor（实现在 execution_contexts.go）。
func (s *Store) ExecutionHosts() application.ExecutionHostRepo         { return s.execHosts }
func (s *Store) WorkspaceLocations() application.WorkspaceLocationRepo { return s.locations }
func (s *Store) WorkItemContexts() application.WorkItemContextRepo     { return s.wiContexts }
func (s *Store) ContextSnapshots() application.ContextSnapshotRepo     { return s.ctxSnapshots }

// TaskComment accessor（实现在 task_comments.go）。
func (s *Store) TaskComments() application.TaskCommentRepo { return s.taskComments }

// Wakeups 返回满足 scheduling.Store 的唤醒仓储（application 端口复用同一接口定义）。
func (s *Store) Wakeups() scheduling.Store { return s.wakeups }

// InTx 在单个事务内执行 fn；fn 内所有仓储调用共享该事务。
func (s *Store) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if ctx.Value(txKey{}) != nil {
		return fn(ctx)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	tctx := context.WithValue(ctx, txKey{}, tx)
	if err := fn(tctx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// exec 返回当前事务或连接池。
func (s *Store) exec(ctx context.Context) executor {
	if tx, ok := ctx.Value(txKey{}).(*sql.Tx); ok {
		return tx
	}
	return s.db
}

// q 使用 SQLite ? 占位符执行查询。
func (s *Store) query(ctx context.Context, db executor, sqlText string, args ...any) (*sql.Rows, error) {
	return db.QueryContext(ctx, sqlText, args...)
}

func (s *Store) queryRow(ctx context.Context, db executor, sqlText string, args ...any) *sql.Row {
	return db.QueryRowContext(ctx, sqlText, args...)
}

func (s *Store) execStmt(ctx context.Context, db executor, sqlText string, args ...any) (sql.Result, error) {
	return db.ExecContext(ctx, sqlText, args...)
}

func (s *Store) mapErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	if sqliteUniqueViolation(err) {
		return domain.ErrIdempotencyConflict
	}
	return err
}

// scanTime 读取 SQLite DATETIME/RFC3339 文本；modernc 在不同列形状下可能提供
// time.Time、字节或字符串，因此三个形式都接受。
type scanTime struct {
	T     time.Time
	Valid bool
}

func (s *scanTime) Scan(v any) error {
	switch x := v.(type) {
	case nil:
		s.Valid = false
	case time.Time:
		s.T, s.Valid = x.UTC(), true
	case []byte:
		return s.parse(string(x))
	case string:
		return s.parse(x)
	}
	return nil
}

func (s *scanTime) parse(str string) error {
	if str == "" {
		s.Valid = false
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, str)
	if err != nil {
		return err
	}
	s.T, s.Valid = t.UTC(), true
	return nil
}

func mustTime(s scanTime) time.Time { return s.T }
func optTime(s scanTime) *time.Time {
	if !s.Valid {
		return nil
	}
	return &s.T
}

func timeParam(t time.Time) any {
	return t.UTC().Format(time.RFC3339Nano)
}

func nullTimeParam(t *time.Time) any {
	if t == nil {
		return nil
	}
	return timeParam(*t)
}

func sqliteUniqueViolation(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "UNIQUE constraint failed") ||
		strings.Contains(err.Error(), "constraint failed: PRIMARY KEY"))
}

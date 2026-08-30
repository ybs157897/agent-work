// Package sqlstore 以 database/sql 实现 application.Store，
// 通过 Dialect 抽象同时支持 PostgreSQL（生产）与 SQLite（本地验证）。
// 事务模型：InTx 把 *sql.Tx 放进 context，仓储方法自动复用同一事务，
// 保证 状态变更 + run_events + outbox + idempotency 同事务提交。
package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/scheduling"
)

type txKey struct{}

// executor 是 *sql.DB 与 *sql.Tx 的公共子集。
type executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Dialect 收敛两种数据库的差异。
type Dialect struct {
	Name      string
	TimeParam func(time.Time) any
	// AdvisoryLock 串行化序号分配；query 已由 Store.ph 翻译占位符。
	AdvisoryLock    func(ctx context.Context, db executor, query string, arg any) error
	UniqueViolation func(error) bool
}

func (d Dialect) NullTimeParam(t *time.Time) any {
	if t == nil {
		return nil
	}
	return d.TimeParam(*t)
}

// PostgresDialect：时间为原生 timestamptz；写并发用 advisory lock 串行化序号分配。
func PostgresDialect() Dialect {
	return Dialect{
		Name:      "postgres",
		TimeParam: func(t time.Time) any { return t.UTC() },
		AdvisoryLock: func(ctx context.Context, db executor, query string, arg any) error {
			_, err := db.ExecContext(ctx, query, arg)
			return err
		},
		UniqueViolation: func(err error) bool {
			var pq *pgconn.PgError
			return errors.As(err, &pq) && pq.Code == "23505"
		},
	}
}

// SQLiteDialect：时间存 RFC3339Nano 文本（UTC 定宽可字典序比较）；
// SQLite 写入天然串行，无需 advisory lock。
func SQLiteDialect() Dialect {
	return Dialect{
		Name: "sqlite",
		TimeParam: func(t time.Time) any {
			return t.UTC().Format(time.RFC3339Nano)
		},
		AdvisoryLock: func(ctx context.Context, db executor, query string, arg any) error {
			return nil
		},
		UniqueViolation: func(err error) bool {
			return strings.Contains(err.Error(), "UNIQUE constraint failed") ||
				strings.Contains(err.Error(), "constraint failed: PRIMARY KEY")
		},
	}
}

type Store struct {
	db         *sql.DB
	dialect    Dialect
	workspaces *WorkspaceRepo
	agents     *AgentRepo
	workItems  *WorkItemRepo
	plans      *PlanRepo
	runs       *RunRepo
	events     *EventRepo
	idem       *IdempotencyRepo
	bindings   *BindingRepo
	runners    *RunnerRepo
	audit      *AuditRepo
	caps       *CapsRepo
	tasks      *TaskSessionRepo
	wakeups    *WakeupRepo
	grants     *ApprovalGrantRepo
	dispatches *DispatchRepo
	decisions  *DecisionRepo
	search     *SearchRepo
}

var _ application.Store = (*Store)(nil)

// New 构造 Store；SQL 统一使用 ? 占位符，postgres 方言翻译为 $N。
func New(db *sql.DB, dialect Dialect) *Store {
	s := &Store{db: db, dialect: dialect}
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
	return s
}

func (s *Store) Workspaces() application.WorkspaceRepo         { return s.workspaces }
func (s *Store) Agents() application.AgentRepo                 { return s.agents }
func (s *Store) WorkItems() application.WorkItemRepo           { return s.workItems }
func (s *Store) Plans() application.PlanRepo                   { return s.plans }
func (s *Store) Runs() application.RunRepo                     { return s.runs }
func (s *Store) Events() application.EventRepo                 { return s.events }
func (s *Store) Idempotency() application.IdempotencyRepo      { return s.idem }
func (s *Store) Bindings() application.RuntimeBindingRepo      { return s.bindings }
func (s *Store) Runners() application.RunnerRepo               { return s.runners }
func (s *Store) Audit() application.AuditRepo                  { return s.audit }
func (s *Store) Caps() application.CapabilitySnapshotRepo      { return s.caps }
func (s *Store) TaskSessions() application.TaskSessionRepo     { return s.tasks }
func (s *Store) ApprovalGrants() application.ApprovalGrantRepo { return s.grants }
func (s *Store) Dispatches() application.DispatchRepo          { return s.dispatches }
func (s *Store) DecisionEntries() application.DecisionRepo     { return s.decisions }
func (s *Store) Search() application.SearchRepo                { return s.search }

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

// q 按方言翻译占位符后执行查询。
func (s *Store) query(ctx context.Context, db executor, sqlText string, args ...any) (*sql.Rows, error) {
	return db.QueryContext(ctx, s.ph(sqlText), args...)
}

func (s *Store) queryRow(ctx context.Context, db executor, sqlText string, args ...any) *sql.Row {
	return db.QueryRowContext(ctx, s.ph(sqlText), args...)
}

func (s *Store) execStmt(ctx context.Context, db executor, sqlText string, args ...any) (sql.Result, error) {
	return db.ExecContext(ctx, s.ph(sqlText), args...)
}

func (s *Store) ph(sqlText string) string {
	if s.dialect.Name != "postgres" {
		return sqlText
	}
	var b strings.Builder
	n := 0
	for _, ch := range sqlText {
		if ch == '?' {
			n++
			b.WriteString("$" + itoa(n))
		} else {
			b.WriteRune(ch)
		}
	}
	return b.String()
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

func (s *Store) mapErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil && s.dialect.UniqueViolation(err) {
		return domain.ErrIdempotencyConflict
	}
	return err
}

// scanTime 兼容 PostgreSQL（time.Time）与 SQLite（RFC3339 文本）的时间列。
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

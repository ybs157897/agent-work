package sqlstore

import (
	"context"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/application"
)

// SearchRepo 检索索引仓储（会话元模型 S4）：SQLite 走 FTS5 MATCH + snippet()，
// PG 走 plainto_tsquery + ts_headline + ts_rank；workspace 过滤靠 JOIN work_items。
type SearchRepo struct{ store *Store }

// IndexEntry 定点重写：先删 (kind, source_id) 再插入——三类条目各自
// (kind, source_id) 唯一，重写天然幂等（digest 刷新、投影重放不残留旧快照）。
// InTx 幂等复用调用方事务；独立调用时自成一个事务。
func (r *SearchRepo) IndexEntry(ctx context.Context, e *application.SearchEntry) error {
	return r.store.InTx(ctx, func(ctx context.Context) error {
		if _, err := r.store.execStmt(ctx, r.store.exec(ctx),
			`DELETE FROM search_index WHERE kind=? AND source_id=?`, e.Kind, e.SourceID); err != nil {
			return err
		}
		_, err := r.store.execStmt(ctx, r.store.exec(ctx),
			`INSERT INTO search_index(work_item_id, kind, source_id, title, body) VALUES (?,?,?,?,?)`,
			e.WorkItemID, e.Kind, e.SourceID, e.Title, e.Body)
		return r.store.mapErr(err)
	})
}

// Search 检索：query 为空或不含任何可索引 token 时返回空结果（FTS MATCH 语法
// 对裸特殊字符敏感，调用方不因此报错）。snippet 统一 [] 高亮标记、… 省略号。
func (r *SearchRepo) Search(ctx context.Context, workspaceID, query, workItemID, kind string, limit int) ([]*application.SearchResult, error) {
	tokens := indexedTokens(query)
	if len(tokens) == 0 {
		return []*application.SearchResult{}, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if r.store.dialect.Name == "postgres" {
		return r.searchPostgres(ctx, workspaceID, tokens, workItemID, kind, limit)
	}
	return r.searchSQLite(ctx, workspaceID, tokens, workItemID, kind, limit)
}

// searchSQLite FTS5：MATCH 短语逐词双引号包裹（规避查询语法注入），内置 rank 排序。
func (r *SearchRepo) searchSQLite(ctx context.Context, workspaceID string, tokens []string, workItemID, kind string, limit int) ([]*application.SearchResult, error) {
	q := `SELECT search_index.work_item_id, search_index.kind, search_index.source_id, search_index.title,
		snippet(search_index, 4, '[', ']', '…', 16)
		FROM search_index JOIN work_items ON work_items.id = search_index.work_item_id
		WHERE search_index MATCH ? AND work_items.workspace_id = ?`
	args := []any{strings.Join(quoteAll(tokens), " "), workspaceID}
	q, args = r.withFilters(q, args, workItemID, kind)
	q += ` ORDER BY rank LIMIT ?`
	args = append(args, limit)
	rows, err := r.store.query(ctx, r.store.exec(ctx), q, args...)
	return scanSearchRows(rows, err)
}

// searchPostgres plainto_tsquery + ts_headline + ts_rank。
func (r *SearchRepo) searchPostgres(ctx context.Context, workspaceID string, tokens []string, workItemID, kind string, limit int) ([]*application.SearchResult, error) {
	tsquery := strings.Join(tokens, " & ")
	q := `SELECT search_index.work_item_id, search_index.kind, search_index.source_id, search_index.title,
		ts_headline('simple', search_index.body, plainto_tsquery('simple', ?),
			'StartSel=[,StopSel=],MaxWords=16,MinWords=6,ShortWord=3,Ellipsis=…')
		FROM search_index JOIN work_items ON work_items.id = search_index.work_item_id
		WHERE search_index.tsv @@ plainto_tsquery('simple', ?) AND work_items.workspace_id = ?`
	args := []any{tsquery, tsquery, workspaceID}
	q, args = r.withFilters(q, args, workItemID, kind)
	q += ` ORDER BY ts_rank(search_index.tsv, plainto_tsquery('simple', ?)) DESC LIMIT ?`
	args = append(args, tsquery, limit)
	rows, err := r.store.query(ctx, r.store.exec(ctx), q, args...)
	return scanSearchRows(rows, err)
}

// withFilters 追加可选过滤（q 与 args 原位增长）；排序尾巴由各方言自拼
// （PG 的 rank 参数顺序不同）。返回的 args 顺序 = SQL 中 ? 出现顺序。
func (r *SearchRepo) withFilters(q string, args []any, workItemID, kind string) (string, []any) {
	if workItemID != "" {
		q += ` AND search_index.work_item_id = ?`
		args = append(args, workItemID)
	}
	if kind != "" {
		q += ` AND search_index.kind = ?`
		args = append(args, kind)
	}
	return q, args
}

func scanSearchRows(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}, err error) ([]*application.SearchResult, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*application.SearchResult{}
	for rows.Next() {
		item := &application.SearchResult{}
		if err := rows.Scan(&item.WorkItemID, &item.Kind, &item.SourceID, &item.Title, &item.Snippet); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// indexedTokens 拆词并剔除引号等 FTS 语法字符；全被剔除（纯符号 query）返回空。
func indexedTokens(query string) []string {
	var tokens []string
	for _, field := range strings.Fields(query) {
		if field = strings.TrimSpace(strings.ReplaceAll(field, `"`, "")); field != "" {
			tokens = append(tokens, field)
		}
	}
	return tokens
}

func quoteAll(tokens []string) []string {
	quoted := make([]string, len(tokens))
	for i, t := range tokens {
		quoted[i] = `"` + t + `"`
	}
	return quoted
}

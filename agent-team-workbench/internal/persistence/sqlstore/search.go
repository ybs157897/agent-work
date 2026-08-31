package sqlstore

import (
	"context"
	"strings"
	"unicode"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// SearchRepo 检索索引仓储（会话元模型 S4）：FTS5 MATCH + snippet()；workspace
// 过滤靠 JOIN work_items。
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
// 对裸特殊字符敏感，调用方不因此报错）。ASCII 等常规查询走 FTS；包含汉字时
// 再合并同一组 workspace/work-item/kind 过滤下的子串结果，补足 FTS 分词对 CJK
// 局部词的缺口。snippet 统一 [] 高亮标记、… 省略号。
func (r *SearchRepo) Search(ctx context.Context, workspaceID, query, workItemID, kind string, limit int) ([]*application.SearchResult, error) {
	tokens := indexedTokens(query)
	if len(tokens) == 0 {
		return []*application.SearchResult{}, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	hits, err := r.searchSQLite(ctx, workspaceID, tokens, workItemID, kind, limit)
	if err != nil {
		return nil, err
	}
	if !containsHan(tokens) || len(hits) >= limit {
		return hits, nil
	}

	// unicode61 可能把连续 CJK 串当作一个 token；显式子串检索补足局部词。
	// FTS 命中的条目仍排在前面，fallback 只补齐剩余结果并去重。
	fallback, err := r.searchSubstring(ctx, workspaceID, tokens, workItemID, kind, limit)
	if err != nil {
		return nil, err
	}
	return mergeSearchResults(hits, fallback, limit), nil
}

// searchSQLite FTS5：MATCH 短语逐词双引号包裹（规避查询语法注入），内置 rank 排序。
func (r *SearchRepo) searchSQLite(ctx context.Context, workspaceID string, tokens []string, workItemID, kind string, limit int) ([]*application.SearchResult, error) {
	q := `SELECT search_index.work_item_id, search_index.kind, search_index.source_id, search_index.title,
		snippet(search_index, 4, '[', ']', '…', 16)
		FROM search_index JOIN work_items ON work_items.id = search_index.work_item_id
		WHERE search_index MATCH ? AND work_items.workspace_id = ? AND work_items.record_kind = ?`
	args := []any{strings.Join(quoteAll(tokens), " "), workspaceID, domain.RecordKindTask}
	q, args = r.withFilters(q, args, workItemID, kind)
	q += ` ORDER BY rank LIMIT ?`
	args = append(args, limit)
	rows, err := r.store.query(ctx, r.store.exec(ctx), q, args...)
	return scanSearchRows(rows, err)
}

// searchSubstring 是 CJK 局部词的 SQLite FTS fallback。
func (r *SearchRepo) searchSubstring(ctx context.Context, workspaceID string, tokens []string, workItemID, kind string, limit int) ([]*application.SearchResult, error) {
	q := `SELECT search_index.work_item_id, search_index.kind, search_index.source_id,
		search_index.title, search_index.body
		FROM search_index JOIN work_items ON work_items.id = search_index.work_item_id
		WHERE work_items.workspace_id = ? AND work_items.record_kind = ?`
	args := []any{workspaceID, domain.RecordKindTask}
	for _, token := range tokens {
		q += ` AND LOWER(COALESCE(search_index.title,'') || ' ' || COALESCE(search_index.body,''))
			LIKE ? ESCAPE '!'`
		args = append(args, likePattern(token))
	}
	q, args = r.withFilters(q, args, workItemID, kind)
	q += ` ORDER BY search_index.source_id LIMIT ?`
	args = append(args, limit)
	rows, err := r.store.query(ctx, r.store.exec(ctx), q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make([]*application.SearchResult, 0)
	for rows.Next() {
		item := &application.SearchResult{}
		var body string
		if err := rows.Scan(&item.WorkItemID, &item.Kind, &item.SourceID, &item.Title, &body); err != nil {
			return nil, err
		}
		item.Snippet = substringSnippet(item.Title, body, tokens)
		results = append(results, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// mergeSearchResults 保留 FTS 的 rank 顺序，再按 kind/source_id 去重合并
// fallback；若 fallback 能补出 FTS 没有的 snippet，则填充空 snippet。
func mergeSearchResults(primary, fallback []*application.SearchResult, limit int) []*application.SearchResult {
	out := make([]*application.SearchResult, 0, limit)
	seen := make(map[string]int, len(primary)+len(fallback))
	for _, item := range append(primary, fallback...) {
		key := item.Kind + "\x00" + item.SourceID
		if i, ok := seen[key]; ok {
			if out[i].Snippet == "" && item.Snippet != "" {
				out[i].Snippet = item.Snippet
			}
			continue
		}
		if len(out) >= limit {
			break
		}
		seen[key] = len(out)
		out = append(out, item)
	}
	return out
}

func containsHan(tokens []string) bool {
	for _, token := range tokens {
		for _, r := range token {
			if unicode.Is(unicode.Han, r) {
				return true
			}
		}
	}
	return false
}

// likePattern 生成不解释用户 %, _ 和 ! 的 LIKE 模式。使用 ! 作为两种
// 数据库都支持的 ESCAPE 字符，避免查询文本改变匹配范围。
func likePattern(token string) string {
	escaped := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(strings.ToLower(token))
	return "%" + escaped + "%"
}

// substringSnippet 为 fallback 结果生成与 FTS 相同的 [] 高亮约定。正文优先，
// 产物等正文为空的条目回退到标题；长文本只保留命中点附近的 160 个 rune。
func substringSnippet(title, body string, tokens []string) string {
	for _, text := range []string{body, title} {
		if snippet, ok := highlightSubstring(text, tokens); ok {
			return snippet
		}
	}
	return ""
}

func highlightSubstring(text string, tokens []string) (string, bool) {
	all := []rune(text)
	for _, token := range tokens {
		needle := []rune(token)
		if len(needle) == 0 {
			continue
		}
		for matchStart := 0; matchStart+len(needle) <= len(all); matchStart++ {
			matched := true
			for i := range needle {
				if !strings.EqualFold(string(all[matchStart+i]), string(needle[i])) {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
			matchEnd := matchStart + len(needle)
			const maxRunes = 160
			if len(all) <= maxRunes {
				return string(all[:matchStart]) + "[" + string(all[matchStart:matchEnd]) + "]" + string(all[matchEnd:]), true
			}
			const contextRunes = 80
			from := matchStart - contextRunes
			if from < 0 {
				from = 0
			}
			to := matchEnd + contextRunes
			if to > len(all) {
				to = len(all)
			}
			prefix, suffix := "", ""
			if from > 0 {
				prefix = "…"
			}
			if to < len(all) {
				suffix = "…"
			}
			return prefix + string(all[from:matchStart]) + "[" + string(all[matchStart:matchEnd]) + "]" + string(all[matchEnd:to]) + suffix, true
		}
	}
	return "", false
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

package knowledge

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
)

const (
	defaultLimit    = 5
	maxLimit        = 20
	snippetMaxRunes = 200
)

// FileRetriever 直读 knowledge 根目录下的语料文件：无索引、无缓存，
// 每次 Retrieve 全量遍历解析。语料为人工 review 的法典条目（几十条
// 量级），重建成本为零换来零失效面。
type FileRetriever struct {
	root string
}

var _ Retriever = (*FileRetriever)(nil)

func NewFileRetriever(root string) (*FileRetriever, error) {
	if root == "" {
		return nil, fmt.Errorf("knowledge: root 不能为空")
	}
	return &FileRetriever{root: root}, nil
}

func (r *FileRetriever) Retrieve(ctx context.Context, q Query) ([]Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validCorpus(q.Corpus); err != nil {
		return nil, err
	}
	status := q.Status
	if status == "" {
		status = StatusEffective
	}

	entries, err := r.load(ctx, filepath.Join(r.root, q.Corpus), q.Corpus)
	if err != nil {
		return nil, err
	}
	// 状态过滤在前、版本去重在后：每个状态档内部各自取最高 version，
	// 默认查询下「v1 effective + v2 draft」返回 v1。
	entries = filterStatus(entries, status)
	entries = dedupLatestVersion(entries)

	terms := normalizeTerms(q.Terms)
	results := make([]Result, 0, len(entries))
	for _, e := range entries {
		s := score(e, terms)
		if s == 0 {
			continue
		}
		results = append(results, Result{Entry: e, Score: s, Snippet: snippet(e.Body, terms)})
	}
	slices.SortFunc(results, func(a, b Result) int {
		if a.Score != b.Score {
			return b.Score - a.Score
		}
		return strings.Compare(a.Entry.ID, b.Entry.ID)
	})
	if limit := clampLimit(q.Limit); len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// load 遍历 corpus 目录解析条目。目录不存在返回空（knowledge 层可选
// 部署）；_ 前缀与 README.md 为非条目文件，按约定跳过；嵌套子目录
// 递归收录，WalkDir 字典序保证结果确定。
func (r *FileRetriever) load(ctx context.Context, dir, corpus string) ([]Entry, error) {
	var entries []Entry
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == dir && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return fmt.Errorf("knowledge: 遍历 %s: %w", path, err)
		}
		if d.IsDir() {
			if path != dir && strings.HasPrefix(d.Name(), "_") {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, "_") || name == "README.md" || filepath.Ext(name) != ".md" {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		e, err := parseEntryFile(path, corpus)
		if err != nil {
			return err
		}
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// validCorpus：corpus 必须是单层目录名。它直接拼进文件路径，且
// Query 最终来自编排动词参数（不可信输入），禁止分隔符与目录跳转。
func validCorpus(c string) error {
	if c == "" || c == "." || c == ".." || strings.ContainsAny(c, `/\`) {
		return fmt.Errorf("knowledge: 非法 corpus %q", c)
	}
	return nil
}

func filterStatus(entries []Entry, status string) []Entry {
	if status == StatusAny {
		return entries
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.Status == status {
			out = append(out, e)
		}
	}
	return out
}

// dedupLatestVersion 同 ID 多版本只保留 version 最高者；WalkDir 字典序
// 保证同 version 平局时取先见者，结果确定。
func dedupLatestVersion(entries []Entry) []Entry {
	idx := make(map[string]int, len(entries))
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if i, ok := idx[e.ID]; ok {
			if e.Version > out[i].Version {
				out[i] = e
			}
			continue
		}
		idx[e.ID] = len(out)
		out = append(out, e)
	}
	return out
}

func normalizeTerms(terms []string) []string {
	out := make([]string, 0, len(terms))
	for _, t := range terms {
		if t = strings.TrimSpace(strings.ToLower(t)); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// score 每个 term 独立累计：与 ID 全等（大小写不敏感）+10，标题每次
// 出现 +3，正文每次出现 +1。零分条目不进结果。
func score(e Entry, terms []string) int {
	id := strings.ToLower(e.ID)
	title := strings.ToLower(e.Title)
	body := strings.ToLower(e.Body)
	n := 0
	for _, t := range terms {
		if t == id {
			n += 10
		}
		n += 3 * strings.Count(title, t)
		n += strings.Count(body, t)
	}
	return n
}

// snippet 取正文中首个含任一 term 的段落，截 200 rune（rune 口径切断，
// 不产生半个多字节字符）；正文无命中返回空串。
func snippet(body string, terms []string) string {
	for _, p := range paragraphs(body) {
		low := strings.ToLower(p)
		for _, t := range terms {
			if strings.Contains(low, t) {
				r := []rune(p)
				if len(r) > snippetMaxRunes {
					return string(r[:snippetMaxRunes])
				}
				return p
			}
		}
	}
	return ""
}

func clampLimit(n int) int {
	if n <= 0 {
		return defaultLimit
	}
	return min(n, maxLimit)
}

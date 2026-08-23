// Package knowledge 检索 agent 可读的持久化知识语料（法典）。
//
// 语料布局：knowledge 根目录下每个子目录是一个 corpus（如 prd/、arch/），
// 内放 markdown 条目（YAML frontmatter + 正文二级节），条目规范以
// agent-team-workbench-docs/product-agent-charter.md §3 为唯一依据。
package knowledge

import "context"

// 条目生命周期状态（charter §3.1）。检索按字符串精确匹配过滤，
// 语料侧的合法值校验由人工 review 兜底，本包不做 union 断言。
const (
	StatusDraft      = "draft"
	StatusEffective  = "effective"
	StatusSuperseded = "superseded"
	StatusRepealed   = "repealed"

	// StatusAny 跳过状态过滤（同 ID 多版本仍只取最高 version）。
	StatusAny = "any"
)

// Entry 是一条解析后的语料条目。Headings 为正文二级节（## 标题→节内容），
// 首个二级节之前的导语不在其中（经 Body 可达）。Path 与构造注入的
// root 同基准；Corpus 为检索时的 corpus 名。
type Entry struct {
	ID       string
	Title    string
	Version  int
	Status   string
	Corpus   string
	Path     string
	Body     string
	Headings map[string]string
}

// Query 是一次检索请求。Status 空串取默认 effective，可指定其他状态档
// 或 StatusAny；Limit <=0 取默认 5，超过上限截到 20；Terms 空或全空白
// 时无可命中条目，返回空结果。
type Query struct {
	Corpus string
	Terms  []string
	Status string
	Limit  int
}

// Result 是一条命中。Snippet 为正文（不含标题行）中首个含任一 term 的
// 段落，截 200 rune；正文无命中时为空串。
type Result struct {
	Entry   Entry
	Score   int
	Snippet string
}

// Retriever 按语料检索条目。corpus 目录不存在不是错误（knowledge 层
// 可选部署），返回空结果；语料文件损坏返回带路径的错误。
type Retriever interface {
	Retrieve(ctx context.Context, q Query) ([]Result, error)
}

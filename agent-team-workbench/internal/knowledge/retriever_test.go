package knowledge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func newCorpus(t *testing.T) (string, *FileRetriever) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "prd"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := NewFileRetriever(root)
	if err != nil {
		t.Fatal(err)
	}
	return root, r
}

// writeEntry 落一条条目文件并返回其路径；fm 为 frontmatter 文本。
func writeEntry(t *testing.T, root, name, fm, body string) string {
	t.Helper()
	path := filepath.Join(root, "prd", name)
	content := "---\n" + fm + "\n---\n" + body
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func fm(id, title string, version int, status string) string {
	return fmt.Sprintf("id: %s\ntitle: %s\nversion: %d\nstatus: %s", id, title, version, status)
}

func retrieve(t *testing.T, r Retriever, q Query) []Result {
	t.Helper()
	results, err := r.Retrieve(context.Background(), q)
	if err != nil {
		t.Fatalf("Retrieve(%+v): %v", q, err)
	}
	return results
}

func ids(results []Result) []string {
	out := make([]string, len(results))
	for i, res := range results {
		out[i] = res.Entry.ID
	}
	return out
}

func TestRetrieveParsesFrontmatterAndHeadings(t *testing.T) {
	root, r := newCorpus(t)
	path := writeEntry(t, root, "prd-014.md", fm("PRD-014", "会话历史压缩预算", 2, StatusEffective), `# PRD-014 会话历史压缩预算
（第 3 编 · 会话管理 / 第 2 章 · 上下文预算）

## 场景
多轮对话历史即将超出模型上下文窗口时，系统如何处置历史。

## 规则
1. 历史预算应当按模型上下文窗口的 35% 计算。
2. 超预算时应当触发会话轮换（新会话 + 摘要交接）。

## 关联
- 参见 ARCH-005（会话三层策略）
`)

	results := retrieve(t, r, Query{Corpus: "prd", Terms: []string{"压缩", "轮换"}})
	if len(results) != 1 {
		t.Fatalf("得到 %d 条结果，期望 1: %v", len(results), ids(results))
	}
	got := results[0]
	e := got.Entry
	if e.ID != "PRD-014" || e.Title != "会话历史压缩预算" || e.Version != 2 || e.Status != StatusEffective {
		t.Errorf("frontmatter 解析错误: %+v", e)
	}
	if e.Corpus != "prd" || e.Path != path {
		t.Errorf("Corpus/Path 错误: corpus=%q path=%q want=%q", e.Corpus, e.Path, path)
	}
	// 打分：压缩=标题1次×3 + 正文(H1行)1次×1；轮换=正文1次×1。
	if got.Score != 5 {
		t.Errorf("Score = %d, 期望 5", got.Score)
	}
	// Snippet 取首个命中文本块（空行分隔；charter 模板的规则条目相邻
	// 成行、同属一块）：导语块/场景块未命中，规则块命中。
	wantSnippet := "1. 历史预算应当按模型上下文窗口的 35% 计算。\n2. 超预算时应当触发会话轮换（新会话 + 摘要交接）。"
	if got.Snippet != wantSnippet {
		t.Errorf("Snippet = %q, 期望 %q", got.Snippet, wantSnippet)
	}
	// 二级节切分：导语不进 Headings，三级以下不存在于本条。
	wantHeadings := map[string]string{
		"场景": "多轮对话历史即将超出模型上下文窗口时，系统如何处置历史。",
		"规则": "1. 历史预算应当按模型上下文窗口的 35% 计算。\n2. 超预算时应当触发会话轮换（新会话 + 摘要交接）。",
		"关联": "- 参见 ARCH-005（会话三层策略）",
	}
	if len(e.Headings) != len(wantHeadings) {
		t.Errorf("Headings 数 = %d, 期望 %d: %v", len(e.Headings), len(wantHeadings), e.Headings)
	}
	for k, v := range wantHeadings {
		if e.Headings[k] != v {
			t.Errorf("Headings[%q] = %q, 期望 %q", k, e.Headings[k], v)
		}
	}
}

func TestRetrieveScoringOrderAndDeterminism(t *testing.T) {
	root, r := newCorpus(t)
	writeEntry(t, root, "prd-001.md", fm("PRD-001", "调度与唤醒", 1, StatusEffective),
		"调度器按心跳调度任务，调度失败重试。") // 调度：标题1×3 + 正文3×1 = 6
	writeEntry(t, root, "prd-002.md", fm("PRD-002", "调度", 1, StatusEffective),
		"唤醒。") // 调度：标题1×3 = 3
	writeEntry(t, root, "prd-003.md", fm("PRD-003", "其他", 1, StatusEffective),
		"无关内容。") // 零分，不进结果
	writeEntry(t, root, "prd-010.md", fm("PRD-010", "杂项", 1, StatusEffective),
		"杂项。") // 小写 term 与 ID 全等（大小写不敏感）：+10

	q := Query{Corpus: "prd", Terms: []string{"调度", "prd-010"}}
	first := retrieve(t, r, q)
	wantIDs := []string{"PRD-010", "PRD-001", "PRD-002"}
	if got := ids(first); !slices.Equal(got, wantIDs) {
		t.Fatalf("顺序 = %v, 期望 %v", got, wantIDs)
	}
	wantScores := []int{10, 6, 3}
	for i, s := range wantScores {
		if first[i].Score != s {
			t.Errorf("results[%d].Score = %d, 期望 %d", i, first[i].Score, s)
		}
	}
	// 同输入两次检索结果全等（确定性）。
	second := retrieve(t, r, q)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("两次检索结果不同:\n%+v\n%+v", first, second)
	}
}

func TestRetrieveStatusFilter(t *testing.T) {
	root, r := newCorpus(t)
	writeEntry(t, root, "prd-101.md", fm("PRD-101", "预算分配", 1, StatusEffective), "内容。")
	writeEntry(t, root, "prd-102.md", fm("PRD-102", "预算草案", 1, StatusDraft), "内容。")
	writeEntry(t, root, "prd-103.md", fm("PRD-103", "预算旧版", 1, StatusSuperseded), "内容。")

	cases := []struct {
		status string
		want   []string
	}{
		{"", []string{"PRD-101"}}, // 缺省 effective
		{StatusDraft, []string{"PRD-102"}},
		{StatusSuperseded, []string{"PRD-103"}},
		{StatusAny, []string{"PRD-101", "PRD-102", "PRD-103"}}, // 同分按 ID 升序
	}
	for _, tc := range cases {
		q := Query{Corpus: "prd", Terms: []string{"预算"}, Status: tc.status}
		if got := ids(retrieve(t, r, q)); !slices.Equal(got, tc.want) {
			t.Errorf("Status=%q: 得到 %v, 期望 %v", tc.status, got, tc.want)
		}
	}
}

func TestRetrieveVersionDedup(t *testing.T) {
	root, r := newCorpus(t)
	writeEntry(t, root, "prd-020-v1.md", fm("PRD-020", "目标条款", 1, StatusSuperseded), "旧文。")
	writeEntry(t, root, "prd-020-v2.md", fm("PRD-020", "目标条款", 2, StatusEffective), "新文。")
	writeEntry(t, root, "prd-021-v1.md", fm("PRD-021", "目标条款", 1, StatusEffective), "生效文。")
	writeEntry(t, root, "prd-021-v2.md", fm("PRD-021", "目标条款", 2, StatusDraft), "草稿文。")

	// any 全量下同 ID 只取最高 version（两个 ID 的 v1 均被去重）。
	got := retrieve(t, r, Query{Corpus: "prd", Terms: []string{"目标"}, Status: StatusAny})
	if len(got) != 2 {
		t.Fatalf("得到 %d 条, 期望 2: %v", len(got), ids(got))
	}
	if got[0].Entry.ID != "PRD-020" || got[0].Entry.Version != 2 || got[0].Entry.Body != "新文。" {
		t.Errorf("PRD-020 去重错误: %+v", got[0].Entry)
	}
	if got[1].Entry.ID != "PRD-021" || got[1].Entry.Version != 2 {
		t.Errorf("PRD-021 在 any 档应取 v2: %+v", got[1].Entry)
	}
	// 状态过滤在前、版本去重在后：默认档返回各 ID 仍在生效的版本
	//（PRD-020→v2，PRD-021→v1——v2 draft 被过滤，v1 effective 保留）。
	def := retrieve(t, r, Query{Corpus: "prd", Terms: []string{"目标"}})
	if got := ids(def); !slices.Equal(got, []string{"PRD-020", "PRD-021"}) {
		t.Errorf("默认档得到 %v, 期望 [PRD-020 PRD-021]", got)
	}
	if def[0].Entry.Version != 2 || def[1].Entry.Version != 1 {
		t.Errorf("默认档版本错误: PRD-020=%d(期望2) PRD-021=%d(期望1)",
			def[0].Entry.Version, def[1].Entry.Version)
	}
}

func TestRetrieveMissingCorpusDirReturnsEmpty(t *testing.T) {
	root, r := newCorpus(t)
	writeEntry(t, root, "prd-001.md", fm("PRD-001", "标题", 1, StatusEffective), "正文。")
	// knowledge 层可选部署：目录不存在的 corpus 返回空而非错误。
	if got := retrieve(t, r, Query{Corpus: "arch", Terms: []string{"标题"}}); len(got) != 0 {
		t.Errorf("缺失目录得到 %v, 期望空", ids(got))
	}
	if got := retrieve(t, r, Query{Corpus: "prd", Terms: []string{"标题"}}); len(got) != 1 {
		t.Errorf("正常 corpus 应命中 1 条, 得到 %v", ids(got))
	}
}

func TestRetrieveLimit(t *testing.T) {
	root, r := newCorpus(t)
	for i := 0; i < 25; i++ {
		name := fmt.Sprintf("prd-%03d.md", i)
		id := fmt.Sprintf("PRD-%03d", i)
		writeEntry(t, root, name, fm(id, "限额", 1, StatusEffective), "无关。")
	}
	cases := []struct {
		limit int
		want  int
	}{
		{30, 20}, // 超上限截到 20
		{0, 5},   // 缺省 5
		{-1, 5},
		{3, 3},
	}
	for _, tc := range cases {
		got := retrieve(t, r, Query{Corpus: "prd", Terms: []string{"限额"}, Limit: tc.limit})
		if len(got) != tc.want {
			t.Errorf("Limit=%d: 得到 %d 条, 期望 %d", tc.limit, len(got), tc.want)
		}
	}
	// 同分按 ID 升序截断：头部 PRD-000，截 20 条后尾部 PRD-019。
	got := retrieve(t, r, Query{Corpus: "prd", Terms: []string{"限额"}, Limit: 30})
	if got[0].Entry.ID != "PRD-000" || got[len(got)-1].Entry.ID != "PRD-019" {
		t.Errorf("截断边界错误: 首=%s 尾=%s", got[0].Entry.ID, got[len(got)-1].Entry.ID)
	}
}

func TestSnippetTruncationKeepsUTF8(t *testing.T) {
	root, r := newCorpus(t)
	longPara := strings.Repeat("会话轮换预算。", 40) // 280 rune，首个命中段落
	writeEntry(t, root, "prd-030.md", fm("PRD-030", "截断", 1, StatusEffective),
		strings.Repeat("前置说明。", 30)+"\n\n"+longPara)
	writeEntry(t, root, "prd-031.md", fm("PRD-031", "预算", 1, StatusEffective), "正文无词。")

	got := retrieve(t, r, Query{Corpus: "prd", Terms: []string{"预算"}})
	if len(got) != 2 {
		t.Fatalf("得到 %d 条, 期望 2: %v", len(got), ids(got))
	}
	var trunc, titleOnly Result
	for _, res := range got {
		if res.Entry.ID == "PRD-030" {
			trunc = res
		} else {
			titleOnly = res
		}
	}
	if n := len([]rune(trunc.Snippet)); n != snippetMaxRunes {
		t.Errorf("Snippet rune 数 = %d, 期望 %d", n, snippetMaxRunes)
	}
	if !utf8.ValidString(trunc.Snippet) {
		t.Errorf("Snippet 不是合法 UTF-8: %q", trunc.Snippet)
	}
	if want := string([]rune(longPara)[:snippetMaxRunes]); trunc.Snippet != want {
		t.Errorf("Snippet 截断内容错误")
	}
	// 命中仅在标题：Score 照算，Snippet 为空。
	if titleOnly.Entry.ID != "PRD-031" || titleOnly.Score != 3 || titleOnly.Snippet != "" {
		t.Errorf("标题命中条目异常: %+v", titleOnly)
	}
}

func TestRetrieveSkipsNonEntryFiles(t *testing.T) {
	root, r := newCorpus(t)
	writeEntry(t, root, "prd-001.md", fm("PRD-001", "正式条目", 1, StatusEffective), "正文。")
	writeEntry(t, root, "_draft.md", fm("PRD-002", "下划线草稿", 1, StatusEffective), "草稿。")
	writeEntry(t, root, "README.md", fm("PRD-003", "目录说明", 1, StatusEffective), "说明。")
	if err := os.WriteFile(filepath.Join(root, "prd", "notes.txt"), []byte("条目"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := retrieve(t, r, Query{Corpus: "prd", Terms: []string{"条目", "草稿", "说明"}, Status: StatusAny})
	if want := []string{"PRD-001"}; !slices.Equal(ids(got), want) {
		t.Errorf("非条目文件混入结果: %v, 期望 %v", ids(got), want)
	}
}

func TestRetrieveFailsLoudOnCorruptEntry(t *testing.T) {
	cases := []struct {
		name    string
		raw     string // 完整文件内容；空则由 fm/body 拼装
		fm      string
		body    string
		wantErr string
	}{
		{"missing_frontmatter.md", "没有 frontmatter 的正文。", "", "", "缺少 frontmatter"},
		{"unclosed.md", "---\nid: PRD-001\n正文。", "", "", "未闭合"},
		{"unknown_key.md", "", "id: PRD-001\ntitle: t\nversion: 1\nstatus: effective\nverison: 2\n", "正文。", "解析失败"},
		{"missing_fields.md", "", "title: 只有标题\n", "正文。", "缺少 id/title/status"},
		{"zero_version.md", "", "id: PRD-001\ntitle: t\nversion: 0\nstatus: effective\n", "正文。", "version 必须 >=1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, r := newCorpus(t)
			content := tc.raw
			if content == "" {
				content = "---\n" + tc.fm + "---\n" + tc.body
			}
			path := filepath.Join(root, "prd", tc.name)
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := r.Retrieve(context.Background(), Query{Corpus: "prd", Terms: []string{"正文"}})
			if err == nil {
				t.Fatalf("损坏条目未报错")
			}
			if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("错误未含路径(%s)或原因(%s): %v", path, tc.wantErr, err)
			}
		})
	}
}

func TestRetrieveRejectsInvalidCorpusAndEmptyRoot(t *testing.T) {
	_, r := newCorpus(t)
	for _, corpus := range []string{"", ".", "..", "a/b", `a\b`} {
		if _, err := r.Retrieve(context.Background(), Query{Corpus: corpus, Terms: []string{"x"}}); err == nil {
			t.Errorf("corpus %q 应被拒绝", corpus)
		}
	}
	if _, err := NewFileRetriever(""); err == nil {
		t.Error("空 root 应被拒绝")
	}
}

func TestRetrieveEmptyTermsReturnNothing(t *testing.T) {
	root, r := newCorpus(t)
	writeEntry(t, root, "prd-001.md", fm("PRD-001", "标题", 1, StatusEffective), "正文。")
	for _, terms := range [][]string{nil, {""}, {"   "}} {
		if got := retrieve(t, r, Query{Corpus: "prd", Terms: terms}); len(got) != 0 {
			t.Errorf("Terms %v 得到 %d 条, 期望 0", terms, len(got))
		}
	}
}

package knowledge

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// frontmatter 覆盖条目模板（charter §3.1）的全部键；KnownFields 严格
// 解析，未知键视为语料腐化报错——typo 字段落默认值混进检索结果比
// 一次显式失败更贵。
type frontmatter struct {
	ID      string `yaml:"id"`
	Title   string `yaml:"title"`
	Version int    `yaml:"version"`
	Status  string `yaml:"status"`

	EffectiveFrom string `yaml:"effective_from"`
	Supersedes    string `yaml:"supersedes"`
	Source        string `yaml:"source"`
}

// parseEntryFile 解析单个条目文件。前置条件：调用方已完成非条目文件
// （_ 前缀 / README.md / 非 .md）的跳过；走到这里的文件必须是合法条目，
// 否则返回带路径的错误，绝不静默跳过。
func parseEntryFile(path, corpus string) (Entry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Entry{}, fmt.Errorf("knowledge: 读取条目 %s: %w", path, err)
	}
	fm, body, err := splitFrontmatter(string(raw))
	if err != nil {
		return Entry{}, fmt.Errorf("knowledge: 条目 %s: %w", path, err)
	}
	if fm.ID == "" || fm.Title == "" || fm.Status == "" {
		return Entry{}, fmt.Errorf("knowledge: 条目 %s: frontmatter 缺少 id/title/status", path)
	}
	if fm.Version < 1 {
		return Entry{}, fmt.Errorf("knowledge: 条目 %s: version 必须 >=1，得到 %d", path, fm.Version)
	}
	return Entry{
		ID:       fm.ID,
		Title:    fm.Title,
		Version:  fm.Version,
		Status:   fm.Status,
		Corpus:   corpus,
		Path:     path,
		Body:     body,
		Headings: splitHeadings(body),
	}, nil
}

// splitFrontmatter 切出 `---` 包夹的 YAML 头与剩余正文；正文去除首尾
// 空白。CRLF 先归一为 LF，兼容 Windows 编辑器落盘的条目。
func splitFrontmatter(content string) (frontmatter, string, error) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], " \t") != "---" {
		return frontmatter{}, "", fmt.Errorf("缺少 frontmatter")
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") != "---" {
			continue
		}
		var fm frontmatter
		dec := yaml.NewDecoder(strings.NewReader(strings.Join(lines[1:i], "\n")))
		dec.KnownFields(true)
		if err := dec.Decode(&fm); err != nil {
			return frontmatter{}, "", fmt.Errorf("frontmatter 解析失败: %w", err)
		}
		return fm, strings.TrimSpace(strings.Join(lines[i+1:], "\n")), nil
	}
	return frontmatter{}, "", fmt.Errorf("frontmatter 未闭合")
}

// splitHeadings 按二级标题（## 行）切分正文；三级及以下标题归属所在
// 二级节的内容。首个二级节之前的导语不产出条目。
func splitHeadings(body string) map[string]string {
	out := map[string]string{}
	var name string
	var buf []string
	flush := func() {
		if name != "" {
			out[name] = strings.TrimSpace(strings.Join(buf, "\n"))
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if h, ok := heading2(line); ok {
			flush()
			name = h
			buf = buf[:0]
			continue
		}
		if name != "" {
			buf = append(buf, line)
		}
	}
	flush()
	return out
}

func heading2(line string) (string, bool) {
	if !strings.HasPrefix(line, "## ") {
		return "", false
	}
	if h := strings.TrimSpace(line[3:]); h != "" {
		return h, true
	}
	return "", false
}

// paragraphs 把正文切成非空文本块：空行分隔，标题行（# 前缀）独立成块
// 且自身不作为候选段落——Snippet 只取正文文本。
func paragraphs(body string) []string {
	var out []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			if s := strings.TrimSpace(strings.Join(cur, "\n")); s != "" {
				out = append(out, s)
			}
			cur = nil
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			flush()
			continue
		}
		cur = append(cur, line)
	}
	flush()
	return out
}

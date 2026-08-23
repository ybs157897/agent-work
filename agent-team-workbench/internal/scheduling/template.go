package scheduling

import (
	"strings"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// RenderPrompt 渲染唤醒提示词。规则：
//   - wakeContext 含非空 "instruction" 键时直接返回该 instruction（模板忽略）；
//   - template 为空用 domain.DefaultPromptTemplate；
//   - 变量替换：{{agent.name}} {{agent.role}} {{agent.slug}} {{work_item.title}}
//     与 {{context.KEY}}（取 wakeContext[KEY] 的 string 值，缺失/非 string 替换为空串）；
//   - 手写扫描而非 text/template：无效 {{ 语法（未知变量、缺闭合、空 context key）原样保留，
//     避免模板注入与严格语法报错。
func RenderPrompt(template string, agent *domain.AgentProfile, workItemTitle string, wakeContext map[string]any) string {
	if instruction, ok := wakeContext["instruction"].(string); ok && instruction != "" {
		return instruction
	}
	if template == "" {
		template = domain.DefaultPromptTemplate
	}
	var b strings.Builder
	rest := template
	for {
		open := strings.Index(rest, "{{")
		if open < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:open])
		end := strings.Index(rest[open:], "}}")
		if end < 0 {
			// 缺闭合：剩余部分原样保留。
			b.WriteString(rest[open:])
			return b.String()
		}
		inner := rest[open+2 : open+end]
		b.WriteString(renderVar(inner, agent, workItemTitle, wakeContext))
		rest = rest[open+end+2:]
	}
}

// renderVar 渲染单个 {{...}} 变量；未识别的语法原样返回（含花括号）。
func renderVar(inner string, agent *domain.AgentProfile, workItemTitle string, wakeContext map[string]any) string {
	var name, role, slug string
	if agent != nil {
		name, role, slug = agent.Name, agent.Role, agent.Slug
	}
	switch inner {
	case "agent.name":
		return name
	case "agent.role":
		return role
	case "agent.slug":
		return slug
	case "work_item.title":
		return workItemTitle
	}
	const contextPrefix = "context."
	if strings.HasPrefix(inner, contextPrefix) {
		key := inner[len(contextPrefix):]
		if key == "" {
			return "{{" + inner + "}}"
		}
		if v, ok := wakeContext[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}
	return "{{" + inner + "}}"
}

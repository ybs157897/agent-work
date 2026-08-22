// Package agentconfig 维护 agents/<slug>/ 文件目录（Agent 配置真相源）：
// agent.yaml 存角色/技能/Runtime 偏好/模型/权限，prompt.md 存系统提示词。
// DB 是运行时投影：启动与 reload 时文件 → DB，Web 编辑成功后再回写文件。
package agentconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"gopkg.in/yaml.v3"
)

// FileConfig 是 agents/<slug>/ 目录的内容。
type FileConfig struct {
	Slug    string   `yaml:"-"` // 目录名
	Name    string   `yaml:"name"`
	Role    string   `yaml:"role"`
	Skills  []string `yaml:"skills,omitempty"`
	Avatar  string   `yaml:"avatar,omitempty"`
	Runtime struct {
		Preferred string   `yaml:"preferred,omitempty"`
		Fallbacks []string `yaml:"fallbacks,omitempty"`
	} `yaml:"runtime,omitempty"`
	Model struct {
		Provider string `yaml:"provider,omitempty"`
		Model    string `yaml:"model,omitempty"`
	} `yaml:"model,omitempty"`
	Permissions struct {
		Tools          []string `yaml:"tools,omitempty"`
		ApprovalPolicy string   `yaml:"approval_policy,omitempty"` // auto | approve_high_risk | manual
		Sandbox        string   `yaml:"sandbox,omitempty"`
	} `yaml:"permissions,omitempty"`
	Prompt string `yaml:"-"` // prompt.md 内容
}

var validApprovalPolicy = map[string]bool{"": true, "auto": true, "approve_high_risk": true, "manual": true}

// ToProfile 把文件配置映射到领域对象的可配置字段（不含 availability/presence 等运行态）。
func (c *FileConfig) ToProfile(a *domain.AgentProfile) {
	a.Slug = c.Slug
	a.Name = c.Name
	a.Role = c.Role
	a.Skills = c.Skills
	a.Avatar = c.Avatar
	a.Instructions = c.Prompt
	a.RuntimePreference = domain.RuntimePreference{Preferred: c.Runtime.Preferred, Fallbacks: c.Runtime.Fallbacks}
	a.ModelOverride = domain.ModelRef{Provider: c.Model.Provider, Model: c.Model.Model}
	a.Policy = domain.AgentPolicy{
		Tools:          c.Permissions.Tools,
		ApprovalPolicy: c.Permissions.ApprovalPolicy,
		Sandbox:        c.Permissions.Sandbox,
	}
}

// FromProfile 反向生成文件配置（回写用）。
func FromProfile(a *domain.AgentProfile) *FileConfig {
	c := &FileConfig{
		Slug:   a.Slug,
		Name:   a.Name,
		Role:   a.Role,
		Skills: a.Skills,
		Avatar: a.Avatar,
		Prompt: a.Instructions,
	}
	c.Runtime.Preferred = a.RuntimePreference.Preferred
	c.Runtime.Fallbacks = a.RuntimePreference.Fallbacks
	c.Model.Provider = a.ModelOverride.Provider
	c.Model.Model = a.ModelOverride.Model
	c.Permissions.Tools = a.Policy.Tools
	c.Permissions.ApprovalPolicy = a.Policy.ApprovalPolicy
	c.Permissions.Sandbox = a.Policy.Sandbox
	return c
}

// LoadDir 扫描目录下所有 <slug>/agent.yaml + prompt.md。
func LoadDir(dir string) ([]*FileConfig, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*FileConfig
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		cfg, err := loadOne(filepath.Join(dir, e.Name()), e.Name())
		if err != nil {
			return nil, err
		}
		if cfg != nil {
			out = append(out, cfg)
		}
	}
	return out, nil
}

func loadOne(dir, slug string) (*FileConfig, error) {
	yamlPath := filepath.Join(dir, "agent.yaml")
	data, err := os.ReadFile(yamlPath)
	if os.IsNotExist(err) {
		return nil, nil // 无 agent.yaml 的目录忽略
	}
	if err != nil {
		return nil, err
	}
	cfg := &FileConfig{Slug: slug}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", yamlPath, err)
	}
	if cfg.Name == "" || cfg.Role == "" {
		return nil, fmt.Errorf("%w: %s: name 与 role 必填", domain.ErrValidation, yamlPath)
	}
	if !validApprovalPolicy[cfg.Permissions.ApprovalPolicy] {
		return nil, fmt.Errorf("%w: %s: approval_policy 必须是 auto|approve_high_risk|manual", domain.ErrValidation, yamlPath)
	}
	if prompt, err := os.ReadFile(filepath.Join(dir, "prompt.md")); err == nil {
		cfg.Prompt = string(prompt)
	}
	return cfg, nil
}

var slugSanitize = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify 由名称生成目录名；空结果回退 "agent"。
func Slugify(name string) string {
	s := slugSanitize.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "agent"
	}
	return s
}

// WriteBack 把 AgentProfile 写回 agents/<slug>/（tmp + rename 近似原子）；返回实际 slug。
// 目录已存在其他文件（如额外笔记）不清理。
func WriteBack(dir string, a *domain.AgentProfile, taken map[string]bool) (string, error) {
	slug := a.Slug
	if slug == "" {
		base := Slugify(a.Name)
		slug = base
		for i := 2; taken[slug]; i++ {
			slug = fmt.Sprintf("%s-%d", base, i)
		}
	}
	sub := filepath.Join(dir, slug)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		return "", err
	}
	cfg := FromProfile(a)
	cfg.Slug = slug
	yamlData, err := yaml.Marshal(cfg)
	if err != nil {
		return "", err
	}
	header := []byte("# Agent 配置文件（agents/<slug>/ 为真相源；Web 编辑会回写此文件）\n")
	if err := writeAtomic(filepath.Join(sub, "agent.yaml"), append(header, yamlData...)); err != nil {
		return "", err
	}
	if err := writeAtomic(filepath.Join(sub, "prompt.md"), []byte(cfg.Prompt)); err != nil {
		return "", err
	}
	return slug, nil
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

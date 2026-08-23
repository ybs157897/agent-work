// Package modelconfig 维护 models/registry.yaml（模型注册表，文件为真相源）。
// 按供应商分组，每组可含多个模型；Agent 以 model.ref 引用模型 id，
// 创建 Run 时编排层把条目解析为模型快照固化进 run.Input。
package modelconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"gopkg.in/yaml.v3"
)

const registryFileName = "registry.yaml"

// Entry 是扁平化的模型注册表条目（API / 编排层使用）。
type Entry struct {
	ID            string `yaml:"-" json:"id"`
	DisplayName   string `yaml:"display_name" json:"display_name"`
	Category      string `yaml:"category,omitempty" json:"category,omitempty"`
	ProviderID    string `yaml:"-" json:"provider_id"`
	Provider      string `yaml:"provider" json:"provider"`
	API           string `yaml:"api,omitempty" json:"api,omitempty"`
	Model         string `yaml:"model" json:"model"`
	APIKeyEnv     string `yaml:"api_key_env,omitempty" json:"api_key_env,omitempty"`
	BaseURL       string `yaml:"base_url,omitempty" json:"base_url,omitempty"`
	ContextWindow int    `yaml:"context_window,omitempty" json:"context_window,omitempty"`
	MaxTokens     int    `yaml:"max_tokens,omitempty" json:"max_tokens,omitempty"`
	Notes         string `yaml:"notes,omitempty" json:"notes,omitempty"`
}

// ModelDef 是 registry.yaml 中供应商下的单个模型。
type ModelDef struct {
	ID            string `yaml:"id"`
	DisplayName   string `yaml:"display_name"`
	Model         string `yaml:"model"`
	ContextWindow int    `yaml:"context_window,omitempty"`
	MaxTokens     int    `yaml:"max_tokens,omitempty"`
	Notes         string `yaml:"notes,omitempty"`
}

// ProviderDef 是 registry.yaml 中的供应商连接 + 模型列表。
type ProviderDef struct {
	ID        string     `yaml:"id"`
	Label     string     `yaml:"label,omitempty"`
	Provider  string     `yaml:"provider"`
	API       string     `yaml:"api,omitempty"`
	BaseURL   string     `yaml:"base_url,omitempty"`
	APIKeyEnv string     `yaml:"api_key_env,omitempty"`
	Models    []ModelDef `yaml:"models"`
}

// RegistryFile 是 models/registry.yaml 的顶层结构。
type RegistryFile struct {
	Providers []ProviderDef `yaml:"providers"`
}

var (
	idPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Validate 校验条目；id 可空（Upsert 时由 display_name 生成）。
func (e *Entry) Validate() error {
	if e.ID != "" && !idPattern.MatchString(e.ID) {
		return fmt.Errorf("%w: id 必须是小写 slug（字母/数字/连字符）", domain.ErrValidation)
	}
	if strings.TrimSpace(e.Provider) == "" || strings.TrimSpace(e.Model) == "" {
		return fmt.Errorf("%w: provider 与 model 必填", domain.ErrValidation)
	}
	if e.APIKeyEnv != "" && !envPattern.MatchString(e.APIKeyEnv) {
		return fmt.Errorf("%w: api_key_env 必须是合法环境变量名", domain.ErrValidation)
	}
	if e.BaseURL != "" && !strings.HasPrefix(e.BaseURL, "http://") && !strings.HasPrefix(e.BaseURL, "https://") {
		return fmt.Errorf("%w: base_url 必须是 http(s) URL", domain.ErrValidation)
	}
	if e.ContextWindow < 0 || e.MaxTokens < 0 {
		return fmt.Errorf("%w: context_window / max_tokens 必须为正数", domain.ErrValidation)
	}
	return nil
}

func (p *ProviderDef) connectionKey() string {
	return ProviderConnectionKey(p.Provider, p.BaseURL, p.API)
}

func (p *ProviderDef) toEntry(m ModelDef) *Entry {
	return &Entry{
		ID:            m.ID,
		DisplayName:   m.DisplayName,
		Category:      p.Label,
		ProviderID:    p.ID,
		Provider:      p.Provider,
		API:           p.API,
		Model:         m.Model,
		APIKeyEnv:     p.APIKeyEnv,
		BaseURL:       p.BaseURL,
		ContextWindow: m.ContextWindow,
		MaxTokens:     m.MaxTokens,
		Notes:         m.Notes,
	}
}

func (p *ProviderDef) fromEntry(e *Entry) ModelDef {
	return ModelDef{
		ID:            e.ID,
		DisplayName:   e.DisplayName,
		Model:         e.Model,
		ContextWindow: e.ContextWindow,
		MaxTokens:     e.MaxTokens,
		Notes:         e.Notes,
	}
}

func (rf *RegistryFile) flatten() []*Entry {
	out := make([]*Entry, 0)
	for i := range rf.Providers {
		p := &rf.Providers[i]
		for j := range p.Models {
			out = append(out, p.toEntry(p.Models[j]))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (rf *RegistryFile) validate() error {
	seenModel := map[string]bool{}
	seenProvider := map[string]bool{}
	for _, p := range rf.Providers {
		if strings.TrimSpace(p.ID) == "" {
			return fmt.Errorf("%w: 供应商 id 必填", domain.ErrValidation)
		}
		if !idPattern.MatchString(p.ID) {
			return fmt.Errorf("%w: 供应商 id 必须是小写 slug: %s", domain.ErrValidation, p.ID)
		}
		if seenProvider[p.ID] {
			return fmt.Errorf("%w: 重复的供应商 id: %s", domain.ErrValidation, p.ID)
		}
		seenProvider[p.ID] = true
		if strings.TrimSpace(p.Provider) == "" {
			return fmt.Errorf("%w: provider 路由名必填（供应商 %s）", domain.ErrValidation, p.ID)
		}
		if p.BaseURL != "" && strings.TrimSpace(p.API) == "" {
			return fmt.Errorf("%w: 供应商 %s 配置了 base_url 时必须指定 api", domain.ErrValidation, p.ID)
		}
		if strings.TrimSpace(p.APIKeyEnv) == "" {
			return fmt.Errorf("%w: 供应商 %s 必须配置 api_key_env", domain.ErrValidation, p.ID)
		}
		if !envPattern.MatchString(p.APIKeyEnv) {
			return fmt.Errorf("%w: 供应商 %s 的 api_key_env 非法", domain.ErrValidation, p.ID)
		}
		for _, m := range p.Models {
			if m.ID == "" {
				return fmt.Errorf("%w: 模型 id 必填", domain.ErrValidation)
			}
			if seenModel[m.ID] {
				return fmt.Errorf("%w: 重复的模型 id: %s", domain.ErrValidation, m.ID)
			}
			seenModel[m.ID] = true
			if err := p.toEntry(m).Validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

// GenerateProviderID 在名称与 provider 路由不一致或含非 ASCII 时生成稳定供应商 id。
func GenerateProviderID(label, provider string) string {
	label = strings.TrimSpace(label)
	provider = strings.TrimSpace(provider)
	if s := Slugify(provider); s != "" && s != "model" {
		return "prov-" + s
	}
	if s := Slugify(label); s != "" && s != "model" {
		return "prov-" + s
	}
	h := sha256.Sum256([]byte(label + "\x00" + provider))
	return "prov-" + hex.EncodeToString(h[:6])
}

// Registry 读写 models/registry.yaml。
type Registry struct {
	dir string
}

func NewRegistry(dir string) *Registry { return &Registry{dir: dir} }

func (r *Registry) Dir() string { return r.dir }

func (r *Registry) registryPath() string {
	return filepath.Join(r.dir, registryFileName)
}

// List 返回全部模型条目（扁平化）。
func (r *Registry) List() ([]*Entry, error) {
	if err := r.ensureRegistry(); err != nil {
		return nil, err
	}
	rf, err := r.load()
	if err != nil {
		return nil, err
	}
	if rf == nil {
		return nil, nil
	}
	return rf.flatten(), nil
}

// Get 按模型 id 读取；不存在返回 nil, nil。
func (r *Registry) Get(id string) (*Entry, error) {
	if !idPattern.MatchString(id) {
		return nil, nil
	}
	if err := r.ensureRegistry(); err != nil {
		return nil, err
	}
	rf, err := r.load()
	if err != nil {
		return nil, err
	}
	if rf == nil {
		return nil, nil
	}
	for i := range rf.Providers {
		p := &rf.Providers[i]
		for _, m := range p.Models {
			if m.ID == id {
				entry := p.toEntry(m)
				return entry, nil
			}
		}
	}
	return nil, nil
}

var slugSanitize = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify 由显示名生成 id；纯中文等非 ASCII 名称用稳定哈希。
func Slugify(name string) string {
	trimmed := strings.TrimSpace(name)
	s := slugSanitize.ReplaceAllString(strings.ToLower(trimmed), "-")
	s = strings.Trim(s, "-")
	if s != "" {
		return s
	}
	if trimmed == "" {
		return "model"
	}
	h := sha256.Sum256([]byte(trimmed))
	return "m-" + hex.EncodeToString(h[:6])
}

// Upsert 创建或更新模型条目。
func (r *Registry) Upsert(e *Entry) error {
	if e.ID == "" {
		e.ID = Slugify(e.DisplayName)
		for i := 2; ; i++ {
			existing, err := r.Get(e.ID)
			if err != nil {
				return err
			}
			if existing == nil {
				break
			}
			e.ID = fmt.Sprintf("%s-%d", Slugify(e.DisplayName), i)
		}
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if err := r.ensureRegistry(); err != nil {
		return err
	}
	rf, err := r.load()
	if err != nil {
		return err
	}
	if rf == nil {
		rf = &RegistryFile{}
	}

	rf.removeModel(e.ID)

	pi := -1
	if e.ProviderID != "" {
		for i := range rf.Providers {
			if rf.Providers[i].ID == e.ProviderID {
				pi = i
				break
			}
		}
	}
	if pi < 0 {
		providerID := strings.TrimSpace(e.ProviderID)
		if providerID == "" {
			providerID = GenerateProviderID(e.Category, e.Provider)
			for i := 2; ; i++ {
				candidate := providerID
				if i > 2 {
					candidate = fmt.Sprintf("%s-%d", providerID, i)
				}
				taken := false
				for _, p := range rf.Providers {
					if p.ID == candidate {
						taken = true
						break
					}
				}
				if !taken {
					providerID = candidate
					break
				}
			}
		}
		apiKeyEnv := e.APIKeyEnv
		if apiKeyEnv == "" {
			apiKeyEnv = SuggestAPIKeyEnv(e.Provider)
		}
		rf.Providers = append(rf.Providers, ProviderDef{
			ID:        providerID,
			Label:     e.Category,
			Provider:  e.Provider,
			API:       e.API,
			BaseURL:   e.BaseURL,
			APIKeyEnv: apiKeyEnv,
			Models:    []ModelDef{},
		})
		pi = len(rf.Providers) - 1
	} else {
		p := &rf.Providers[pi]
		if e.Category != "" {
			p.Label = e.Category
		}
		p.Provider = e.Provider
		p.API = e.API
		p.BaseURL = e.BaseURL
		if e.APIKeyEnv != "" {
			p.APIKeyEnv = e.APIKeyEnv
		}
	}

	model := rf.Providers[pi].fromEntry(e)
	rf.Providers[pi].Models = append(rf.Providers[pi].Models, model)
	sort.Slice(rf.Providers[pi].Models, func(i, j int) bool {
		return rf.Providers[pi].Models[i].ID < rf.Providers[pi].Models[j].ID
	})
	rf.pruneEmptyProviders()
	return r.save(rf)
}

func (rf *RegistryFile) removeModel(id string) {
	for pi := range rf.Providers {
		models := rf.Providers[pi].Models[:0]
		for _, m := range rf.Providers[pi].Models {
			if m.ID != id {
				models = append(models, m)
			}
		}
		rf.Providers[pi].Models = models
	}
	rf.pruneEmptyProviders()
}

func (rf *RegistryFile) pruneEmptyProviders() {
	next := rf.Providers[:0]
	for _, p := range rf.Providers {
		if len(p.Models) > 0 {
			next = append(next, p)
		}
	}
	rf.Providers = next
}

// Delete 删除模型条目；不存在视为成功。
func (r *Registry) Delete(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("%w: 非法模型 id", domain.ErrValidation)
	}
	rf, err := r.load()
	if err != nil {
		return err
	}
	if rf == nil {
		return nil
	}
	changed := false
	next := rf.Providers[:0]
	for _, p := range rf.Providers {
		models := p.Models[:0]
		for _, m := range p.Models {
			if m.ID == id {
				changed = true
				continue
			}
			models = append(models, m)
		}
		if len(models) == 0 {
			continue
		}
		p.Models = models
		next = append(next, p)
	}
	if !changed {
		return nil
	}
	rf.Providers = next
	rf.pruneEmptyProviders()
	return r.save(rf)
}

func (r *Registry) load() (*RegistryFile, error) {
	data, err := os.ReadFile(r.registryPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rf RegistryFile
	if err := yaml.Unmarshal(data, &rf); err != nil {
		return nil, fmt.Errorf("%s: %w", r.registryPath(), err)
	}
	if err := rf.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", r.registryPath(), err)
	}
	return &rf, nil
}

func (r *Registry) save(rf *RegistryFile) error {
	if err := rf.validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(rf)
	if err != nil {
		return err
	}
	header := []byte("# 模型注册表（models/registry.yaml 为真相源；Web 编辑会回写此文件）\n# API Key 存于 credentials.local.yaml，勿写密钥明文。\n")
	return writeAtomic(r.registryPath(), append(header, data...), 0o644)
}

// ensureRegistry 在 registry.yaml 缺失时从旧版单文件条目迁移。
func (r *Registry) ensureRegistry() error {
	if _, err := os.Stat(r.registryPath()); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	legacy, err := r.listLegacyFiles()
	if err != nil {
		return err
	}
	if len(legacy) == 0 {
		return nil
	}
	rf := &RegistryFile{}
	for _, e := range legacy {
		if err := r.upsertInto(rf, e); err != nil {
			return err
		}
	}
	return r.save(rf)
}

func (r *Registry) upsertInto(rf *RegistryFile, e *Entry) error {
	key := ProviderConnectionKey(e.Provider, e.BaseURL, e.API)
	pi := -1
	for i := range rf.Providers {
		if rf.Providers[i].connectionKey() == key {
			pi = i
			break
		}
	}
	if pi < 0 {
		apiKeyEnv := e.APIKeyEnv
		if apiKeyEnv == "" {
			apiKeyEnv = SuggestAPIKeyEnv(e.Provider)
		}
		rf.Providers = append(rf.Providers, ProviderDef{
			ID: GenerateProviderID(e.Category, e.Provider), Label: e.Category, Provider: e.Provider,
			API: e.API, BaseURL: e.BaseURL, APIKeyEnv: apiKeyEnv,
		})
		pi = len(rf.Providers) - 1
	}
	rf.Providers[pi].Models = append(rf.Providers[pi].Models, rf.Providers[pi].fromEntry(e))
	return nil
}

func (r *Registry) listLegacyFiles() ([]*Entry, error) {
	entries, err := os.ReadDir(r.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]*Entry, 0)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name == registryFileName || name == "credentials.local.yaml" ||
			!strings.HasSuffix(name, ".yaml") || strings.HasPrefix(name, ".") {
			continue
		}
		entry, err := r.loadLegacyEntry(filepath.Join(r.dir, name))
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r *Registry) loadLegacyEntry(path string) (*Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	entry := &Entry{ID: strings.TrimSuffix(filepath.Base(path), ".yaml")}
	if err := yaml.Unmarshal(data, entry); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := entry.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return entry, nil
}

// writeAtomic 临时文件 + rename 原子替换；mode 为目标文件权限位
// （registry.yaml 0644；credentials.local.yaml 必须 0600——含密钥明文）。
// WriteFile 受 umask 影响可能缺位，rename 后显式 chmod 修正（也顺带收紧历史遗留的过宽权限）。
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

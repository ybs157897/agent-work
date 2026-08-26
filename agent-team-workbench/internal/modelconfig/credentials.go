package modelconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/agentwork"
	"gopkg.in/yaml.v3"
)

type credentialItem struct {
	ProviderID string `yaml:"provider_id"`
	APIKey     string `yaml:"api_key"`
}

type credentialsFile struct {
	Items []credentialItem `yaml:"items,omitempty"`
}

// CredentialsStore 保存 .agent-work/credentials.local.yaml（gitignore），仅存本地 API Key。
type CredentialsStore struct {
	path       string
	legacyPath string
}

// NewCredentialsStore 创建凭据存储；主路径为项目空间 .agent-work/，兼容旧 models/ 路径。
func NewCredentialsStore(workbenchRoot string) *CredentialsStore {
	space := agentwork.Resolve(workbenchRoot)
	return &CredentialsStore{
		path:       space.CredentialsPath(),
		legacyPath: space.LegacyCredentialsPath(),
	}
}

func (s *CredentialsStore) Path() string { return s.path }

func (s *CredentialsStore) load() (*credentialsFile, error) {
	for _, p := range []string{s.path, s.legacyPath} {
		if p == "" {
			continue
		}
		data, err := os.ReadFile(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		if p == s.legacyPath {
			// 遗留路径曾按普通权限（0644）落盘且含明文 Key：读到即收紧为 owner-only。
			// 收紧失败不影响本次读取结果（如特殊文件系统），下轮读取会再次尝试。
			_ = os.Chmod(p, 0o600)
		}
		var f credentialsFile
		if err := yaml.Unmarshal(data, &f); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		return &f, nil
	}
	return &credentialsFile{}, nil
}

func (s *CredentialsStore) save(f *credentialsFile) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	out := credentialsFile{Items: f.Items}
	data, err := yaml.Marshal(out)
	if err != nil {
		return err
	}
	header := []byte("# 本地模型凭据（仅本机使用，勿提交到 Git）\n# 按 registry.yaml 中的 provider id 匹配。\n")
	// 含 API Key 明文，文件权限必须 0600（owner-only）。
	return writeAtomic(s.path, append(header, data...), 0o600)
}

// Get 按 provider_id 读凭据。ok=false 且 err=nil 表示确实未配置；
// err 非 nil 表示凭据文件存在但读取/解析失败——调用方不得当作"无凭据"处理。
func (s *CredentialsStore) Get(providerID string) (apiKey string, ok bool, err error) {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return "", false, nil
	}
	f, err := s.load()
	if err != nil {
		return "", false, err
	}
	for _, item := range f.Items {
		if item.ProviderID != providerID {
			continue
		}
		if strings.TrimSpace(item.APIKey) == "" {
			return "", false, nil
		}
		return item.APIKey, true, nil
	}
	return "", false, nil
}

// Set 保存或删除（apiKey 为空）供应商 API Key。
func (s *CredentialsStore) Set(providerID, apiKey string) error {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return fmt.Errorf("provider_id 必填")
	}
	f, err := s.load()
	if err != nil {
		return err
	}
	apiKey = strings.TrimSpace(apiKey)
	next := f.Items[:0]
	found := false
	for _, item := range f.Items {
		if item.ProviderID != providerID {
			next = append(next, item)
			continue
		}
		found = true
		if apiKey != "" {
			next = append(next, credentialItem{ProviderID: providerID, APIKey: apiKey})
		}
	}
	if !found && apiKey != "" {
		next = append(next, credentialItem{ProviderID: providerID, APIKey: apiKey})
	}
	f.Items = next
	return s.save(f)
}

// Delete 删除供应商凭据。
func (s *CredentialsStore) Delete(providerID string) error {
	return s.Set(providerID, "")
}

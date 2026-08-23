package modelconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type credentialItem struct {
	ProviderID string `yaml:"provider_id"`
	APIKey     string `yaml:"api_key"`
}

type credentialsFile struct {
	Items []credentialItem `yaml:"items,omitempty"`
}

// CredentialsStore 保存 models/credentials.local.yaml（gitignore），仅存本地 API Key。
type CredentialsStore struct {
	path string
}

func NewCredentialsStore(modelsDir string) *CredentialsStore {
	return &CredentialsStore{path: filepath.Join(modelsDir, "credentials.local.yaml")}
}

func (s *CredentialsStore) Path() string { return s.path }

func (s *CredentialsStore) load() (*credentialsFile, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return &credentialsFile{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f credentialsFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", s.path, err)
	}
	return &f, nil
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

// Get 按 provider_id 读取凭据；不存在或为空返回 false。
func (s *CredentialsStore) Get(providerID string) (apiKey string, ok bool) {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return "", false
	}
	f, err := s.load()
	if err != nil {
		return "", false
	}
	for _, item := range f.Items {
		if item.ProviderID != providerID {
			continue
		}
		if strings.TrimSpace(item.APIKey) == "" {
			return "", false
		}
		return item.APIKey, true
	}
	return "", false
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

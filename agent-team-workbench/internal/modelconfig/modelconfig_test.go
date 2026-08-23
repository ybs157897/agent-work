package modelconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func writeRegistry(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, registryFileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeLegacyEntry(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestListAndGet(t *testing.T) {
	dir := t.TempDir()
	writeRegistry(t, dir, `
providers:
  - id: prov-deepseek-official
    label: DeepSeek
    provider: deepseek-official
    api_key_env: DEEPSEEK_API_KEY
    models:
      - id: deepseek-v4-flash
        display_name: DeepSeek V4 Flash
        model: deepseek-v4-flash
  - id: prov-kimi
    label: Kimi
    provider: kimi
    api: openai-completions
    base_url: https://api.moonshot.cn/v1
    api_key_env: KIMI_API_KEY
    models:
      - id: kimi-k2
        display_name: Kimi K2
        model: kimi-k2-0905-preview
        notes: kimi CLI 走自己的 provider 配置
`)

	r := NewRegistry(dir)
	list, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "deepseek-v4-flash" || list[1].ID != "kimi-k2" {
		t.Fatalf("unexpected list: %+v", list)
	}
	got, err := r.Get("kimi-k2")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Category != "Kimi" || got.BaseURL != "https://api.moonshot.cn/v1" {
		t.Fatalf("unexpected entry: %+v", got)
	}
	if missing, _ := r.Get("nope"); missing != nil {
		t.Fatalf("missing id should be nil, got %+v", missing)
	}
	if bad, _ := r.Get("../escape"); bad != nil {
		t.Fatal("非法 id 必须返回 nil")
	}
}

func TestListMissingDir(t *testing.T) {
	list, err := NewRegistry(filepath.Join(t.TempDir(), "nope")).List()
	if err != nil || list != nil {
		t.Fatalf("missing dir should be empty: %v %+v", err, list)
	}
}

func TestUpsertCreateAndOverwrite(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)
	e := &Entry{ID: "deepseek-v4-flash", DisplayName: "DeepSeek V4 Flash", Category: "DeepSeek", Provider: "deepseek-official", Model: "deepseek-v4-flash", APIKeyEnv: "DEEPSEEK_API_KEY"}
	if err := r.Upsert(e); err != nil {
		t.Fatal(err)
	}
	got, _ := r.Get("deepseek-v4-flash")
	if got == nil || got.DisplayName != "DeepSeek V4 Flash" || got.Category != "DeepSeek" {
		t.Fatalf("upsert 未生效: %+v", got)
	}
	e.BaseURL = "https://gateway.example.com/v1"
	e.API = "openai-completions"
	if err := r.Upsert(e); err != nil {
		t.Fatal(err)
	}
	got, _ = r.Get("deepseek-v4-flash")
	if got.BaseURL != "https://gateway.example.com/v1" {
		t.Fatalf("覆盖未生效: %+v", got)
	}
}

func TestUpsertAutoID(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)
	e := &Entry{DisplayName: "Qwen3 Coder Plus", Provider: "openai-compatible", Model: "qwen3-coder-plus"}
	if err := r.Upsert(e); err != nil {
		t.Fatal(err)
	}
	if e.ID != "qwen3-coder-plus" {
		t.Fatalf("自动 id 错误: %s", e.ID)
	}
	e2 := &Entry{DisplayName: "Qwen3 Coder Plus", Provider: "openai-compatible", Model: "qwen3-coder-plus"}
	if err := r.Upsert(e2); err != nil {
		t.Fatal(err)
	}
	if e2.ID != "qwen3-coder-plus-2" {
		t.Fatalf("冲突避让失败: %s", e2.ID)
	}
}

func TestSlugifyChinese(t *testing.T) {
	id1 := Slugify("通义千问")
	id2 := Slugify("通义千问")
	if id1 != id2 || !strings.HasPrefix(id1, "m-") {
		t.Fatalf("unexpected chinese slug: %s", id1)
	}
	if Slugify("GLM-4 Flash") != "glm-4-flash" {
		t.Fatal("ascii slugify broken")
	}
}

func TestValidation(t *testing.T) {
	r := NewRegistry(t.TempDir())
	cases := []*Entry{
		{ID: "Bad_ID", Provider: "p", Model: "m"},
		{ID: "ok", Provider: "", Model: "m"},
		{ID: "ok", Provider: "p", Model: ""},
		{ID: "ok", Provider: "p", Model: "m", APIKeyEnv: "1BAD"},
		{ID: "ok", Provider: "p", Model: "m", BaseURL: "ftp://x"},
	}
	for i, e := range cases {
		if err := r.Upsert(e); !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("case %d 应拒绝: %v", i, err)
		}
	}
}

func TestDelete(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)
	if err := r.Upsert(&Entry{ID: "m1", Provider: "p", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete("m1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Get("m1"); got != nil {
		t.Fatal("删除后仍可读到")
	}
	if err := r.Delete("m1"); err != nil {
		t.Fatal("重复删除应幂等成功")
	}
	if err := r.Delete("../escape"); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("非法 id 删除应拒绝")
	}
}

func TestMigrateLegacyFiles(t *testing.T) {
	dir := t.TempDir()
	writeLegacyEntry(t, dir, "deepseek-v4-flash.yaml", `
display_name: DeepSeek V4 Flash
provider: deepseek-official
model: deepseek-v4-flash
`)
	writeLegacyEntry(t, dir, "kimi-k2.yaml", `
display_name: Kimi K2
category: Kimi
provider: kimi
api: openai-completions
model: kimi-k2
base_url: https://api.moonshot.cn/v1
`)

	r := NewRegistry(dir)
	list, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected migrated list, got %+v", list)
	}
	if _, err := os.Stat(filepath.Join(dir, registryFileName)); err != nil {
		t.Fatal("migration should create registry.yaml")
	}
}

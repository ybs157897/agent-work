package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/modelconfig"
)

// P0 防回归：GET provider-credentials 写后不可读——响应体绝不出现完整 api_key，
// 也不保留旧的 {"api_key": ...} 回显字段；只允许 configured/masked_hint/length。
func TestGetProviderCredentialNeverEchoesPlaintext(t *testing.T) {
	const fullKey = "sk-abc123XYZ7890wxyz"
	s := &Server{credentials: modelconfig.NewCredentialsStore(t.TempDir()), demoRole: domain.RoleOwner}
	if err := s.credentials.Set("prov-deepseek-official", fullKey); err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/models/provider-credentials?provider_id=prov-deepseek-official", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "sk-") || strings.Contains(body, fullKey) {
		t.Fatalf("响应泄露明文或 key 前缀: %s", body)
	}
	var resp struct {
		Configured bool   `json:"configured"`
		MaskedHint string `json:"masked_hint"`
		Length     int    `json:"length"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Configured {
		t.Fatal("configured = false, 期望 true")
	}
	if resp.MaskedHint != "wxyz" {
		t.Fatalf("masked_hint = %q, 期望末 4 位 wxyz", resp.MaskedHint)
	}
	if resp.Length != len(fullKey) {
		t.Fatalf("length = %d, 期望 %d", resp.Length, len(fullKey))
	}

	// PUT 行为不变：重新写入后 GET 仍只回脱敏提示。
	putReq := httptest.NewRequest(http.MethodPut, "/api/v1/models/provider-credentials",
		strings.NewReader(`{"provider_id":"prov-deepseek-official","api_key":"sk-brandnew-key-9999"}`))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, putReq)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/models/provider-credentials?provider_id=prov-deepseek-official", nil))
	body = rec.Body.String()
	if strings.Contains(body, "sk-brandnew-key-9999") || strings.Contains(body, `"api_key"`) {
		t.Fatalf("PUT 后 GET 泄露明文或出现 api_key 字段: %s", body)
	}
	if got, _, err := s.credentials.Get("prov-deepseek-official"); err != nil || got != "sk-brandnew-key-9999" {
		t.Fatalf("PUT 未生效: got=%q err=%v", got, err)
	}
}

// 无凭据时 configured=false 且不出现任何脱敏/明文字段。
func TestGetProviderCredentialNotConfigured(t *testing.T) {
	s := &Server{credentials: modelconfig.NewCredentialsStore(t.TempDir()), demoRole: domain.RoleOwner}
	mux := s.Routes()

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/models/provider-credentials?provider_id=prov-none", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["configured"] != false || len(resp) != 1 {
		t.Fatalf("无凭据应仅返回 {\"configured\":false}, got: %s", rec.Body.String())
	}

	// 缺 provider_id → 400。
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/models/provider-credentials", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺参 status = %d, 期望 400", rec.Code)
	}
}

// 凭据文件读取失败（≠未配置）必须向上报错，不得返回 configured=false 伪装成空。
func TestGetProviderCredentialIOErrorSurfacesAs500(t *testing.T) {
	dir := t.TempDir()
	s := &Server{credentials: modelconfig.NewCredentialsStore(dir), demoRole: domain.RoleOwner}
	// 主路径落成目录，制造读取失败（非 IsNotExist 的真实 IO 错误）。
	if err := os.MkdirAll(filepath.Join(dir, ".agent-work", "credentials.local.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/models/provider-credentials?provider_id=prov-x", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("IO 错误 status = %d, 期望 500; body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"configured"`) {
		t.Fatalf("IO 错误不得伪装成凭据状态: %s", rec.Body.String())
	}
}

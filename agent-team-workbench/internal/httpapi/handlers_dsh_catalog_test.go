// handlers_dsh_catalog_test.go catalog 端点的 JSON 形态契约：preset 为空时
// agent_presets/permission_presets 必须是 [] 而非 null（前端按数组消费，
// null 会在 .find 处崩溃——2026-08-24 agents 页白屏的根因）。
package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDSHCatalogEmptyPresetsSerializeAsArrays(t *testing.T) {
	s := newPlanTestServer(t)
	// 指向空 preset 根（env 优先于默认候选），稳定复现「零 preset」路径：
	// ForSDKAdapter 对空输入返回 nil 切片，序列化边界必须归一为 []。
	t.Setenv("ATW_DSH_PRESET_ROOTS", t.TempDir())
	mux := s.Routes()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtimes/dsh/catalog", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog 应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"agent_presets":[]`) {
		t.Fatalf("agent_presets 应为空数组而非 null，实际响应: %s", body)
	}
	if !strings.Contains(body, `"permission_presets":[`) {
		t.Fatalf("permission_presets 应为数组，实际响应: %s", body)
	}
}

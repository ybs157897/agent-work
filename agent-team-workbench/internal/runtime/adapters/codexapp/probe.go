package codexapp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"

	"github.com/ybs/agent-team-workbench/internal/agentwork/codexconfig"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// Probe 真实执行 initialize/initialized、account/read 与 model/list。
// 二进制存在但协议漂移、未登录或没有模型时必须报告 unavailable。
func (m *Module) Probe(ctx context.Context, req runtime.ProbeRequest) (runtime.ProbeResult, error) {
	manifest, _ := m.Manifest(ctx)
	if _, err := exec.LookPath(m.cfg.BinPath); err != nil {
		if _, statErr := os.Stat(m.cfg.BinPath); statErr != nil {
			return runtime.ProbeResult{OK: false, Manifest: &manifest, Error: "codex 不可用: " + m.cfg.BinPath}, nil
		}
	}

	sess, err := m.openAppServer(ctx)
	if err != nil {
		return runtime.ProbeResult{OK: false, Manifest: &manifest, Error: err.Error()}, nil
	}
	defer sess.close()

	providerVersion, err := sess.handshake()
	if err != nil {
		return runtime.ProbeResult{OK: false, Manifest: &manifest, Error: err.Error()}, nil
	}
	manifest.ProviderVersion = providerVersion
	m.setProviderVersion(providerVersion)

	accountFrame, err := sess.request(2, "account/read", map[string]any{"refreshToken": false})
	if err != nil {
		return runtime.ProbeResult{OK: false, Manifest: &manifest, Error: "account/read 失败: " + err.Error()}, nil
	}
	var accountResult struct {
		Account            any  `json:"account"`
		RequiresOpenAIAuth bool `json:"requiresOpenaiAuth"`
	}
	_ = json.Unmarshal(accountFrame.Result, &accountResult)
	if accountResult.RequiresOpenAIAuth && accountResult.Account == nil {
		if !codexconfig.CustomProviderReady(m.cfg.Home) {
			return runtime.ProbeResult{OK: false, Manifest: &manifest, Error: "Codex 尚未配置模型凭据，请在智能体页选择模型并保存"}, nil
		}
	}

	modelsFrame, err := sess.request(3, "model/list", map[string]any{"limit": 20, "includeHidden": false})
	if err != nil {
		return runtime.ProbeResult{OK: false, Manifest: &manifest, Error: "model/list 失败: " + err.Error()}, nil
	}
	var modelsResult struct {
		Data []json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(modelsFrame.Result, &modelsResult)
	if len(modelsResult.Data) == 0 {
		return runtime.ProbeResult{OK: false, Manifest: &manifest, Error: "Codex 未返回可用模型"}, nil
	}
	return runtime.ProbeResult{OK: true, Manifest: &manifest}, nil
}

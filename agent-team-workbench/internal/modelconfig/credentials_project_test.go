package modelconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/agentwork"
)

// TestCredentialsStoreLoadsProjectFile 验证仓库本地凭据文件能被 CredentialsStore 加载。
// 依赖 gitignore 的本地文件（.agent-work/credentials.local.yaml 主路径，旧 models/ 路径回退），
// fresh clone/worktree 本就不带该文件属正常环境：两个路径都不存在时 skip，存在时照常断言。
func TestCredentialsStoreLoadsProjectFile(t *testing.T) {
	root := filepath.Join("..", "..")
	space := agentwork.Resolve(root)
	_, errPrimary := os.Stat(space.CredentialsPath())
	_, errLegacy := os.Stat(space.LegacyCredentialsPath())
	if errPrimary != nil && errLegacy != nil {
		t.Skipf("本地凭据文件未配置（%s 与 %s 均不存在）", space.CredentialsPath(), space.LegacyCredentialsPath())
	}
	store := NewCredentialsStore(root)
	if _, ok, err := store.Get("prov-kimi"); err != nil || !ok {
		t.Fatalf("project credentials.local.yaml missing prov-kimi: ok=%v err=%v", ok, err)
	}
}

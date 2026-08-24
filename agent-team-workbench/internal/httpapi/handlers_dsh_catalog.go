package httpapi

import (
	"net/http"

	"github.com/ybs/agent-team-workbench/internal/dshcatalog"
)

func (s *Server) handleDSHCatalog(w http.ResponseWriter, r *http.Request) {
	roots := dshcatalog.ResolvePresetRoots(s.workbenchRoot)
	presets, err := dshcatalog.ListAgentPresets(roots)
	if err != nil {
		fail(w, r, err)
		return
	}
	presets = dshcatalog.ForSDKAdapter(presets)
	// JSON 序列化边界：nil 切片会落成 null，前端按数组消费（.find/.filter）会崩，
	// 这里统一归一为空数组。
	if presets == nil {
		presets = []dshcatalog.AgentPresetEntry{}
	}
	permissionPresets := dshcatalog.DefaultPermissionPresets()
	if permissionPresets == nil {
		permissionPresets = []dshcatalog.PermissionPresetEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"agent_presets":      presets,
		"permission_presets": permissionPresets,
	})
}

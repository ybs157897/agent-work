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
	writeJSON(w, http.StatusOK, map[string]any{
		"agent_presets":      presets,
		"permission_presets": dshcatalog.DefaultPermissionPresets(),
	})
}

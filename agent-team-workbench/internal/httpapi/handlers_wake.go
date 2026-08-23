package httpapi

import (
	"net/http"
)

// handleWakeAgent：on_demand 手动唤醒（M4 wakeup 调度）。
// 入队后由调度循环统一消费（≤一个 tick）；coalescing/心跳判定单点一致。
func (s *Server) handleWakeAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_profile_id")
	s.idempotent(w, r, agentID, func() (int, []byte) {
		var req wakeAgentRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		out, err := s.svc.RequestAgentWake(r.Context(), agentID, req.TaskKey, req.Instruction, req.Context)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusAccepted, out)
	})
}

type wakeAgentRequest struct {
	TaskKey     string         `json:"task_key"`
	Instruction string         `json:"instruction,omitempty"`
	Context     map[string]any `json:"context,omitempty"`
}

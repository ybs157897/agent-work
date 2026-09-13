package httpapi

import (
	"errors"
	"net/http"

	"github.com/ybs/agent-team-workbench/internal/application"
)

// handleRunFile 提供 Run 作用域的只读文件预览：客户端只给仓库相对路径，
// 服务端在 Run 执行上下文授权的仓库集合内解析。响应体不含宿主绝对路径。
func (s *Server) handleRunFile(w http.ResponseWriter, r *http.Request) {
	file, err := s.svc.ReadRunFile(r.Context(), r.PathValue("run_id"), r.URL.Query().Get("path"))
	if err != nil {
		switch {
		case errors.Is(err, application.ErrRunFileInvalidPath):
			writeProblem(w, r, Problem{Type: "https://workbench.example/problems/validation", Title: "Validation failed", Status: http.StatusBadRequest, Code: "invalid_file_path", Detail: err.Error()})
		case errors.Is(err, application.ErrRunFileNotFound):
			writeProblem(w, r, Problem{Type: "https://workbench.example/problems/not-found", Title: "File not found", Status: http.StatusNotFound, Code: "run_file_not_found", Detail: err.Error()})
		case errors.Is(err, application.ErrRunFileUnsupported):
			writeProblem(w, r, Problem{Type: "https://workbench.example/problems/unsupported", Title: "Preview unsupported", Status: http.StatusUnsupportedMediaType, Code: "run_file_unsupported", Detail: err.Error()})
		case errors.Is(err, application.ErrRunFileTooLarge):
			writeProblem(w, r, Problem{Type: "https://workbench.example/problems/too-large", Title: "File too large", Status: http.StatusRequestEntityTooLarge, Code: "run_file_too_large", Detail: err.Error()})
		case errors.Is(err, application.ErrRunFileUnavailable):
			writeProblem(w, r, Problem{Type: "https://workbench.example/problems/unavailable", Title: "Preview unavailable", Status: http.StatusConflict, Code: "run_file_unavailable", Detail: err.Error()})
		default:
			fail(w, r, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, file)
}

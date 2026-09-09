package httpapi

import (
	"io"
	"net/http"
	"path"
	"strings"
	"unicode"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/security"
)

const chatSourceMaxBytes = 10 << 20

func (s *Server) registerChatSourceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/work-items/{work_item_id}/sources", s.guard(security.PermWorkItemWrite, s.handleCreateChatSource))
	mux.HandleFunc("GET /api/v1/work-items/{work_item_id}/sources", s.guard(security.PermRead, s.handleListChatSources))
	mux.HandleFunc("GET /api/v1/work-items/{work_item_id}/sources/{source_id}", s.guard(security.PermRead, s.handleGetChatSource))
	mux.HandleFunc("GET /api/v1/work-items/{work_item_id}/sources/{source_id}/download", s.guard(security.PermRead, s.handleDownloadChatSource))
}

func (s *Server) handleCreateChatSource(w http.ResponseWriter, r *http.Request) {
	if s.chatSourceStore == nil {
		writeProblem(w, r, Problem{Type: "about:blank", Title: "Chat source unavailable", Status: http.StatusServiceUnavailable, Code: "chat_source_unavailable", Detail: "原件存储尚未配置"})
		return
	}
	clientKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if clientKey == "" {
		writeProblem(w, r, Problem{Type: "about:blank", Title: "Idempotency-Key required", Status: http.StatusBadRequest, Code: "missing_idempotency_key", Detail: "附件上传必须携带 Idempotency-Key"})
		return
	}
	// Multipart boundaries are random per request; source idempotency is based
	// on the logical client key plus the stored digest, never raw body bytes.
	r.Body = http.MaxBytesReader(w, r.Body, chatSourceMaxBytes+1<<20)
	if err := r.ParseMultipartForm(chatSourceMaxBytes + 1<<20); err != nil {
		writeProblem(w, r, Problem{Type: "about:blank", Title: "Invalid multipart source", Status: http.StatusBadRequest, Code: "chat_source_invalid", Detail: "附件上传格式无效或超过大小限制"})
		return
	}
	if value := strings.TrimSpace(r.FormValue("client_key")); value != "" {
		clientKey = value
	}
	files := r.MultipartForm.File["file"]
	if len(files) != 1 {
		writeProblem(w, r, Problem{Type: "about:blank", Title: "One file required", Status: http.StatusBadRequest, Code: "chat_source_invalid", Detail: "必须上传一个 file 附件"})
		return
	}
	header := files[0]
	if !validChatSourceFilename(header.Filename) {
		writeProblem(w, r, Problem{Type: "about:blank", Title: "Invalid filename", Status: http.StatusBadRequest, Code: "chat_source_invalid", Detail: "文件名无效"})
		return
	}
	file, err := header.Open()
	if err != nil {
		writeProblem(w, r, Problem{Type: "about:blank", Title: "Source read failed", Status: http.StatusBadRequest, Code: "chat_source_read_failed", Detail: "无法读取上传附件"})
		return
	}
	data, readErr := io.ReadAll(io.LimitReader(file, chatSourceMaxBytes+1))
	_ = file.Close()
	if readErr != nil || len(data) == 0 || len(data) > chatSourceMaxBytes {
		writeProblem(w, r, Problem{Type: "about:blank", Title: "Source too large", Status: http.StatusRequestEntityTooLarge, Code: "chat_source_too_large", Detail: "附件为空或超过 10 MiB 限制"})
		return
	}
	mimeType := strings.TrimSpace(header.Header.Get("Content-Type"))
	source, replayed, err := s.svc.CreateChatSource(r.Context(), application.ChatSourceUpload{
		ChatWorkItemID: r.PathValue("work_item_id"), Filename: header.Filename, MIME: mimeType,
		Data: data, ClientKey: clientKey,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		writeJSON(w, http.StatusOK, source)
		return
	}
	writeJSON(w, http.StatusCreated, source)
}

func (s *Server) handleListChatSources(w http.ResponseWriter, r *http.Request) {
	sources, err := s.svc.ChatSourcesFor(r.Context(), r.PathValue("work_item_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	if sources == nil {
		sources = []*domain.ChatSource{}
	}
	for i, source := range sources {
		view, viewErr := s.svc.ChatSourceView(r.Context(), source.WorkspaceID, source.ChatWorkItemID, source.ID)
		if viewErr != nil {
			fail(w, r, viewErr)
			return
		}
		sources[i] = view
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": sources})
}

func (s *Server) handleGetChatSource(w http.ResponseWriter, r *http.Request) {
	chat, err := s.store.WorkItems().Get(r.Context(), r.PathValue("work_item_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	source, err := s.svc.ChatSourceView(r.Context(), chat.WorkspaceID, chat.ID, r.PathValue("source_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, source)
}

func (s *Server) handleDownloadChatSource(w http.ResponseWriter, r *http.Request) {
	if s.chatSourceStore == nil {
		writeProblem(w, r, Problem{Type: "about:blank", Title: "Chat source unavailable", Status: http.StatusServiceUnavailable, Code: "chat_source_unavailable", Detail: "原件存储尚未配置"})
		return
	}
	chat, err := s.store.WorkItems().Get(r.Context(), r.PathValue("work_item_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	source, err := s.svc.ChatSource(r.Context(), chat.WorkspaceID, chat.ID, r.PathValue("source_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	file, err := s.svc.OpenChatSource(r.Context(), source)
	if err != nil {
		fail(w, r, err)
		return
	}
	defer file.Close()
	filename := path.Base(source.Filename)
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(filename, `"`, "")+`"`)
	if source.MIME != "" {
		w.Header().Set("Content-Type", source.MIME)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	http.ServeContent(w, r, filename, source.CreatedAt, file)
}

func validChatSourceFilename(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\\`) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

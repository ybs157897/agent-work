package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/agentwork"
)

const maxImageUploadBytes int64 = 10 << 20

var uploadImageExtensions = map[string]string{
	"image/gif":  ".gif",
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

type uploadedImageDTO struct {
	Name string `json:"name"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
	Path string `json:"path"`
}

func (s *Server) handleUploadImage(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.PathValue("workspace_id")
	if _, err := s.store.Workspaces().Get(r.Context(), workspaceID); err != nil {
		fail(w, r, err)
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeProblem(w, r, Problem{
			Type:  "https://workbench.example/problems/missing-idempotency-key",
			Title: "Idempotency-Key required", Status: http.StatusBadRequest,
			Code: "missing_idempotency_key", Detail: "上传图片必须携带 Idempotency-Key",
		})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxImageUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(maxImageUploadBytes); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeUploadProblem(w, r, http.StatusRequestEntityTooLarge, "image_too_large", "图片不能超过 10MB")
		} else {
			writeUploadProblem(w, r, http.StatusBadRequest, "invalid_multipart", "请使用 multipart/form-data 上传图片")
		}
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeUploadProblem(w, r, http.StatusBadRequest, "image_required", "请选择要上传的图片")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxImageUploadBytes+1))
	if err != nil {
		fail(w, r, err)
		return
	}
	if int64(len(data)) > maxImageUploadBytes {
		writeUploadProblem(w, r, http.StatusRequestEntityTooLarge, "image_too_large", "图片不能超过 10MB")
		return
	}
	mime := http.DetectContentType(data)
	ext, ok := uploadImageExtensions[mime]
	if !ok {
		writeUploadProblem(w, r, http.StatusUnsupportedMediaType, "unsupported_image", "仅支持 PNG、JPEG、WebP 或 GIF 图片")
		return
	}

	name := filepath.Base(strings.TrimSpace(header.Filename))
	if name == "." || name == "" {
		name = "image" + ext
	}
	digest := sha256.Sum256(data)
	contentHash := hex.EncodeToString(digest[:])
	requestDigest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s", name, mime, contentHash)))
	requestHash := hex.EncodeToString(requestDigest[:])

	s.idempotentHashed(w, r, workspaceID, key, requestHash, func() (int, []byte) {
		uploadRoot := filepath.Join(agentwork.Resolve(s.workbenchRoot).Root, "uploads", workspaceID)
		if err := os.MkdirAll(uploadRoot, 0o755); err != nil {
			return renderProblem(http.StatusInternalServerError, "upload_failed", "Upload failed", err.Error())
		}
		path := filepath.Join(uploadRoot, contentHash[:24]+ext)
		if err := writeFileOnce(path, data); err != nil {
			return renderProblem(http.StatusInternalServerError, "upload_failed", "Upload failed", err.Error())
		}
		return renderJSON(w, r, http.StatusCreated, uploadedImageDTO{
			Name: name,
			Mime: mime,
			Size: int64(len(data)),
			Path: path,
		})
	})
}

func writeFileOnce(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	return file.Close()
}

func writeUploadProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	writeProblem(w, r, Problem{
		Type:  "https://workbench.example/problems/" + code,
		Title: "Image upload rejected", Status: status,
		Code: code, Detail: detail,
	})
}

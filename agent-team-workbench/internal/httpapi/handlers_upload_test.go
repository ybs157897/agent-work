package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestUploadImageStoresValidatedContentAndReplays(t *testing.T) {
	t.Setenv("ATW_AGENT_WORK_DIR", "")
	server := newPlanTestServer(t)
	root := t.TempDir()
	server.SetWorkbenchRoot(root)
	workspaceID := "ws_upload"
	now := time.Now().UTC()
	if err := server.store.Workspaces().Create(context.Background(), &domain.Workspace{
		ID: workspaceID, Name: "upload", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	upload := func(key, filename string, data []byte) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+workspaceID+"/uploads/images", &body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, req)
		return rec
	}

	first := upload("upload-key", "screen.png", png)
	if first.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", first.Code, first.Body.String())
	}
	var got uploadedImageDTO
	if err := json.Unmarshal(first.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	expectedRoot := filepath.Join(root, ".agent-work", "uploads", workspaceID)
	if !strings.HasPrefix(got.Path, expectedRoot+string(filepath.Separator)) {
		t.Fatalf("path=%q 不在 %q 下", got.Path, expectedRoot)
	}
	stored, err := os.ReadFile(got.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, png) || got.Mime != "image/png" || got.Size != int64(len(png)) {
		t.Fatalf("upload dto/content mismatch: %#v", got)
	}

	replay := upload("upload-key", "screen.png", png)
	if replay.Code != http.StatusCreated || replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay status=%d header=%q body=%s", replay.Code, replay.Header().Get("Idempotent-Replayed"), replay.Body.String())
	}
	conflict := upload("upload-key", "screen.png", append(append([]byte(nil), png...), 0))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
}

func TestUploadImageRejectsNonImage(t *testing.T) {
	server := newPlanTestServer(t)
	workspaceID := "ws_upload_invalid"
	now := time.Now().UTC()
	if err := server.store.Workspaces().Create(context.Background(), &domain.Workspace{
		ID: workspaceID, Name: "upload", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("file", "notes.txt")
	_, _ = part.Write([]byte("not an image"))
	_ = writer.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+workspaceID+"/uploads/images", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Idempotency-Key", "invalid-image")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

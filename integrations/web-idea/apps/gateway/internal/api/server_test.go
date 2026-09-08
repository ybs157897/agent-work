package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ybs/web-idea/apps/gateway/internal/api"
	"github.com/ybs/web-idea/apps/gateway/internal/config"
	"github.com/ybs/web-idea/apps/gateway/internal/jdtls"
	"github.com/ybs/web-idea/apps/gateway/internal/workspace"
)

func TestWorkspaceFSRoundTrip(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Hello.java"), []byte("class Hello {}"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		Listen:       "127.0.0.1:0",
		AuthMode:     "dev",
		DevToken:     "test-token",
		MaxFileBytes: 1 << 20,
		CORSOrigins:  []string{"http://127.0.0.1:5173"},
	}
	h := api.New(cfg, workspace.NewStore(), nil).Handler()

	createBody, _ := json.Marshal(map[string]string{"root": root})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces", bytes.NewReader(createBody))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var ws struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&ws); err != nil || ws.ID == "" || ws.Status != "browsing" {
		t.Fatalf("decode: %+v %v", ws, err)
	}

	// unauthorized
	req = httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+ws.ID+"/fs/tree", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+ws.ID+"/fs/tree", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("tree: %d %s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+ws.ID+"/fs/file?path=Hello.java", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("file: %d %s", rr.Code, rr.Body.String())
	}
	body, _ := io.ReadAll(rr.Body)
	if string(body) != "class Hello {}" {
		t.Fatalf("body=%q", body)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+ws.ID+"/fs/files?q=hello", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("files: %d %s", rr.Code, rr.Body.String())
	}
	var files struct {
		Query string   `json:"query"`
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&files); err != nil {
		t.Fatal(err)
	}
	if files.Query != "hello" || len(files.Paths) != 1 || files.Paths[0] != "Hello.java" {
		t.Fatalf("files response=%+v", files)
	}

	// path escape
	req = httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+ws.ID+"/fs/file?path=../Hello.java", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("escape want 400, got %d %s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/workspaces/"+ws.ID, nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+ws.ID, nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("deleted workspace still visible: %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("healthz: %d", rr.Code)
	}
}

func TestCORSAllowsViteAlternatePort(t *testing.T) {
	cfg := config.Config{
		Listen:       "127.0.0.1:0",
		AuthMode:     "dev",
		DevToken:     "test-token",
		MaxFileBytes: 1 << 20,
		CORSOrigins:  []string{"http://127.0.0.1:5173"},
	}
	h := api.New(cfg, workspace.NewStore(), nil).Handler()

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/workspaces", nil)
	req.Header.Set("Origin", "http://127.0.0.1:5174")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Header().Get("Access-Control-Allow-Origin") != "http://127.0.0.1:5174" {
		t.Fatalf("cors origin=%q", rr.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestDeleteWorkspaceStopsJDTLSAndProxy(t *testing.T) {
	root := t.TempDir()
	launch := filepath.Join(root, "launch.sh")
	if err := os.WriteFile(launch, []byte("#!/bin/sh\nexec cat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Listen:       "127.0.0.1:0",
		AuthMode:     "dev",
		DevToken:     "test-token",
		MaxFileBytes: 1 << 20,
		CORSOrigins:  []string{"http://127.0.0.1:5173"},
	}
	mgr := jdtls.NewManager(jdtls.Config{
		LaunchScript: launch,
		DataRoot:     filepath.Join(root, "jdtls-data"),
	})
	server := httptest.NewServer(api.New(cfg, workspace.NewStore(), mgr).Handler())
	defer server.Close()

	createBody, _ := json.Marshal(map[string]string{"root": root})
	createReq, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/workspaces", bytes.NewReader(createBody))
	createReq.Header.Set("Authorization", "Bearer test-token")
	createReq.Header.Set("Content-Type", "application/json")
	createRes, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatal(err)
	}
	defer createRes.Body.Close()
	if createRes.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", createRes.StatusCode)
	}
	var ws struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(createRes.Body).Decode(&ws); err != nil {
		t.Fatal(err)
	}

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/workspaces/" + ws.ID + "/lsp"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Authorization": []string{"Bearer test-token"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	secondURL := wsURL
	secondConn, secondRes, err := websocket.DefaultDialer.Dial(secondURL, http.Header{"Authorization": []string{"Bearer test-token"}})
	if secondConn != nil {
		secondConn.Close()
	}
	if err == nil || secondRes == nil || secondRes.StatusCode != http.StatusConflict {
		if secondRes != nil {
			secondRes.Body.Close()
		}
		t.Fatalf("second lsp connection: response=%v err=%v", secondRes, err)
	}
	secondRes.Body.Close()

	deleteReq, _ := http.NewRequest(http.MethodDelete, server.URL+"/api/v1/workspaces/"+ws.ID, nil)
	deleteReq.Header.Set("Authorization", "Bearer test-token")
	deleteRes, err := http.DefaultClient.Do(deleteReq)
	if err != nil {
		t.Fatal(err)
	}
	deleteRes.Body.Close()
	if deleteRes.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", deleteRes.StatusCode)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("proxy websocket remained open after workspace delete")
	}
	if _, err := mgr.Session(ws.ID); err == nil {
		t.Fatal("jdtls session remained after workspace delete")
	}
	if _, err := os.Stat(filepath.Join(root, "jdtls-data", ws.ID)); !os.IsNotExist(err) {
		t.Fatalf("jdtls data remained after workspace delete: %v", err)
	}
}

func TestConfigRefusesPublicDevListen(t *testing.T) {
	t.Setenv("WEBIDEA_LISTEN", "0.0.0.0:8080")
	t.Setenv("WEBIDEA_AUTH_MODE", "dev")
	t.Setenv("WEBIDEA_DEV_TOKEN", "x")
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for 0.0.0.0 + dev")
	}
}

func TestStaticFilesStayInsideBuildDirectory(t *testing.T) {
	staticRoot := t.TempDir()
	outsideRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(staticRoot, "index.html"), []byte("<html>embedded</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(staticRoot, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staticRoot, "assets", "app.js"), []byte("console.log('ok')"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideRoot, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outsideRoot, "secret.txt"), filepath.Join(staticRoot, "secret-link.txt")); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		Listen:       "127.0.0.1:0",
		AuthMode:     "dev",
		DevToken:     "test-token",
		MaxFileBytes: 1 << 20,
		CORSOrigins:  []string{"http://127.0.0.1:5173"},
		StaticDir:    staticRoot,
	}
	h := api.New(cfg, workspace.NewStore(), nil).Handler()

	for _, tc := range []struct {
		path string
		want string
	}{
		{path: "/", want: "<html>embedded</html>"},
		{path: "/missing-route", want: "<html>embedded</html>"},
		{path: "/assets/app.js", want: "console.log('ok')"},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK || rr.Body.String() != tc.want {
			t.Fatalf("static %s: status=%d body=%q", tc.path, rr.Code, rr.Body.String())
		}
	}

	for _, path := range []string{"/%2e%2e/secret.txt", "/secret-link.txt"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("static escape %s: status=%d body=%q", path, rr.Code, rr.Body.String())
		}
	}
}

func TestReadOnlyWorkspaceRejectsWrites(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "Hello.java")
	if err := os.WriteFile(path, []byte("class Hello {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Listen:       "127.0.0.1:0",
		AuthMode:     "dev",
		DevToken:     "test-token",
		MaxFileBytes: 1 << 20,
		CORSOrigins:  []string{"http://127.0.0.1:5173"},
	}
	h := api.New(cfg, workspace.NewStore(), nil).Handler()
	body, _ := json.Marshal(map[string]any{"root": root, "read_only": true})
	create := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces", bytes.NewReader(body))
	create.Header.Set("Authorization", "Bearer test-token")
	create.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, create)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var ws struct {
		ID       string `json:"id"`
		ReadOnly bool   `json:"read_only"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&ws); err != nil || ws.ID == "" || !ws.ReadOnly {
		t.Fatalf("workspace=%+v err=%v", ws, err)
	}

	put := httptest.NewRequest(http.MethodPut, "/api/v1/workspaces/"+ws.ID+"/fs/file?path=Hello.java", strings.NewReader("class Changed {}"))
	put.Header.Set("Authorization", "Bearer test-token")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, put)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("read-only write: %d %s", rr.Code, rr.Body.String())
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(disk) != "class Hello {}" {
		t.Fatalf("read-only write changed disk: %q", disk)
	}
}

func TestReadOnlyLSPRejectsMutationRequest(t *testing.T) {
	root := t.TempDir()
	launch := filepath.Join(root, "launch.sh")
	if err := os.WriteFile(launch, []byte("#!/bin/sh\nexec cat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Listen:       "127.0.0.1:0",
		AuthMode:     "dev",
		DevToken:     "test-token",
		MaxFileBytes: 1 << 20,
		CORSOrigins:  []string{"http://127.0.0.1:5173"},
	}
	mgr := jdtls.NewManager(jdtls.Config{LaunchScript: launch, DataRoot: filepath.Join(root, "jdtls-data")})
	server := httptest.NewServer(api.New(cfg, workspace.NewStore(), mgr).Handler())
	defer server.Close()

	body, _ := json.Marshal(map[string]any{"root": root, "read_only": true})
	createReq, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/workspaces", bytes.NewReader(body))
	createReq.Header.Set("Authorization", "Bearer test-token")
	createReq.Header.Set("Content-Type", "application/json")
	createRes, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatal(err)
	}
	defer createRes.Body.Close()
	var ws struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(createRes.Body).Decode(&ws); err != nil || ws.ID == "" {
		t.Fatalf("workspace=%+v err=%v", ws, err)
	}

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/workspaces/" + ws.ID + "/lsp"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Authorization": []string{"Bearer test-token"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      7,
		"method":  "textDocument/rename",
	}); err != nil {
		t.Fatal(err)
	}
	_, response, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var forbidden struct {
		ID    int `json:"id"`
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response, &forbidden); err != nil {
		t.Fatal(err)
	}
	if forbidden.ID != 7 || forbidden.Error.Code != -32001 {
		t.Fatalf("forbidden response=%s", response)
	}
}

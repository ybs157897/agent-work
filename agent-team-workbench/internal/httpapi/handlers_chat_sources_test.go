package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/chatsources"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

func multipartSourceBody(t *testing.T, filename, clientKey string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("client_key", clientKey); err != nil {
		t.Fatal(err)
	}
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
	return &body, writer.FormDataContentType()
}

func TestChatSourceUploadReplayAndRunAdmission(t *testing.T) {
	server := newPlanTestServer(t)
	wsID, leadID, _ := seedPlanHTTPEnv(t, server)
	chat, err := server.svc.CreateWorkItem(context.Background(), wsID, application.CreateWorkItemParams{
		Title: "附件对话", RecordKind: domain.RecordKindChat, AgentProfileID: leadID,
	})
	if err != nil {
		t.Fatal(err)
	}
	server.SetChatSourceStore(chatsources.NewStore(t.TempDir()))
	data := []byte("原始附件内容，不由工作台预提取。")
	body, contentType := multipartSourceBody(t, "notes.md", "source-client-1", data)
	post := func(body *bytes.Buffer, contentType string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/work-items/"+chat.ID+"/sources", body)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Idempotency-Key", "source-idempotency-1")
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, req)
		return rec
	}
	first := post(body, contentType)
	if first.Code != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", first.Code, first.Body.String())
	}
	var source domain.ChatSource
	if err := json.Unmarshal(first.Body.Bytes(), &source); err != nil {
		t.Fatal(err)
	}
	if source.ID == "" || source.Status != domain.ChatSourceSaved || len(source.SHA256) != 64 || strings.Contains(first.Body.String(), "opaque_key") || strings.Contains(first.Body.String(), string(data)) {
		t.Fatalf("unsafe source response: %s", first.Body.String())
	}
	body2, contentType2 := multipartSourceBody(t, "notes.md", "source-client-1", data)
	second := post(body2, contentType2)
	if second.Code != http.StatusOK || second.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("multipart replay status=%d headers=%v body=%s", second.Code, second.Header(), second.Body.String())
	}
	var replay domain.ChatSource
	if err := json.Unmarshal(second.Body.Bytes(), &replay); err != nil || replay.ID != source.ID {
		t.Fatalf("replay source mismatch: %+v err=%v", replay, err)
	}
	list := httptest.NewRecorder()
	server.Routes().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/work-items/"+chat.ID+"/sources", nil))
	if list.Code != http.StatusOK || strings.Count(list.Body.String(), source.ID) != 1 {
		t.Fatalf("source list status=%d body=%s", list.Code, list.Body.String())
	}
	runBody := `{"agent_profile_id":"` + leadID + `","client_key":"run-source-1","input":{"instruction":"请读取附件","source_refs":[{"source_id":"` + source.ID + `","sha256":"` + source.SHA256 + `"}]}}`
	runReq := httptest.NewRequest(http.MethodPost, "/api/v1/work-items/"+chat.ID+"/runs", strings.NewReader(runBody))
	runReq.Header.Set("Content-Type", "application/json")
	runReq.Header.Set("Idempotency-Key", "run-source-1")
	runRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(runRec, runReq)
	if runRec.Code != http.StatusAccepted {
		t.Fatalf("Run source admission status=%d body=%s", runRec.Code, runRec.Body.String())
	}
	var runResponse struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(runRec.Body.Bytes(), &runResponse); err != nil || runResponse.RunID == "" {
		t.Fatalf("Run response=%s err=%v", runRec.Body.String(), err)
	}
	fresh, err := server.store.Runs().Get(context.Background(), runResponse.RunID)
	if err != nil {
		t.Fatal(err)
	}
	refs, ok := fresh.Input["source_refs"].([]any)
	if !ok || len(refs) != 1 || !strings.Contains(fresh.Input["instruction"].(string), "请读取附件") {
		t.Fatalf("Run source refs/instruction not frozen: %+v", fresh.Input)
	}
	handed, err := server.store.ChatSources().Get(context.Background(), wsID, chat.ID, source.ID)
	if err != nil || handed.Status != domain.ChatSourceHandedToAgent {
		t.Fatalf("source handoff status=%+v err=%v", handed, err)
	}
}

func TestChatSourceConcurrentLogicalReplayDoesNotConflict(t *testing.T) {
	server := newPlanTestServer(t)
	wsID, leadID, _ := seedPlanHTTPEnv(t, server)
	chat, err := server.svc.CreateWorkItem(context.Background(), wsID, application.CreateWorkItemParams{
		Title: "并发附件", RecordKind: domain.RecordKindChat, AgentProfileID: leadID,
	})
	if err != nil {
		t.Fatal(err)
	}
	server.SetChatSourceStore(chatsources.NewStore(t.TempDir()))
	data := []byte("same logical attachment")
	type result struct {
		code int
		body string
	}
	results := make(chan result, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, contentType := multipartSourceBody(t, "same.md", "concurrent-key", data)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/work-items/"+chat.ID+"/sources", body)
			req.Header.Set("Content-Type", contentType)
			req.Header.Set("Idempotency-Key", "concurrent-key")
			rec := httptest.NewRecorder()
			server.Routes().ServeHTTP(rec, req)
			results <- result{code: rec.Code, body: rec.Body.String()}
		}()
	}
	wg.Wait()
	close(results)
	created, replayed := 0, 0
	for item := range results {
		switch item.code {
		case http.StatusCreated:
			created++
		case http.StatusOK:
			replayed++
		default:
			t.Fatalf("concurrent upload status=%d body=%s", item.code, item.body)
		}
	}
	if created != 1 || replayed != 3 {
		t.Fatalf("concurrent logical replay counts: created=%d replayed=%d", created, replayed)
	}
}

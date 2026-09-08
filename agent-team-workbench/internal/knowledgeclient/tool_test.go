package knowledgeclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNewRunBoundValidatesDigestAndRunPath(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	token := "run-bound-secret"
	runID := "run_1"
	path := writeCapabilityFile(t, runCapabilityURL(server.URL, runID), token, 0o600)

	client, err := NewRunBound(path, tokenDigest(token), runID)
	if err != nil {
		t.Fatalf("valid capability rejected: %v", err)
	}
	if client.BaseURL != runCapabilityURL(server.URL, runID) {
		t.Fatalf("bound URL = %q", client.BaseURL)
	}

	for name, digest := range map[string]string{
		"wrong digest":     tokenDigest("other-secret"),
		"malformed digest": "not-a-sha256",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewRunBound(path, digest, runID)
			if err == nil || strings.Contains(err.Error(), token) {
				t.Fatalf("digest error leaked or was accepted: %v", err)
			}
		})
	}
	wrongPath := writeCapabilityFile(t, runCapabilityURL(server.URL, "run_2"), token, 0o600)
	if _, err := NewRunBound(wrongPath, tokenDigest(token), runID); err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("run path mismatch was accepted or leaked token: %v", err)
	}
}

func TestNewRunBoundRequiresStrictPrivateCapabilityFile(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	runID := "run_strict"
	token := "strict-secret"
	digest := tokenDigest(token)

	weak := writeCapabilityFile(t, runCapabilityURL(server.URL, runID), token, 0o644)
	if _, err := NewRunBound(weak, digest, runID); err == nil {
		t.Fatal("weak capability permissions were accepted")
	}

	target := writeCapabilityFile(t, runCapabilityURL(server.URL, runID), token, 0o600)
	link := filepath.Join(t.TempDir(), "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunBound(link, digest, runID); err == nil {
		t.Fatal("symlinked capability was accepted")
	}

	for name, raw := range map[string]string{
		"unknown field": `{"url":"` + runCapabilityURL(server.URL, runID) + `","token":"` + token + `","agent_id":"agent_other"}`,
		"trailing JSON": `{"url":"` + runCapabilityURL(server.URL, runID) + `","token":"` + token + `}{}`,
		"invalid JSON":  `{"url":"` + runCapabilityURL(server.URL, runID) + `","token":`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "capability.json")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewRunBound(path, digest, runID); err == nil || strings.Contains(err.Error(), token) {
				t.Fatalf("strict capability input was accepted or leaked token: %v", err)
			}
		})
	}
	oversized := filepath.Join(t.TempDir(), "oversized.json")
	if err := os.WriteFile(oversized, append([]byte(`{"url":"`+runCapabilityURL(server.URL, runID)+`","token":"`+token+`","padding":"`), []byte(strings.Repeat("x", maxRunBoundCapabilityBytes))...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunBound(oversized, digest, runID); err == nil {
		t.Fatal("oversized capability was accepted")
	}
}

func TestInvokeRecordRoutesStrictNaturalLanguageArguments(t *testing.T) {
	var calls atomic.Int32
	var gotBody map[string]any
	var gotAuth, gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotAuth, gotKey = r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key")
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/knowledge-agent/runs/run_record/records" {
			t.Errorf("unexpected record route: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected route", http.StatusInternalServerError)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode record body: %v", err)
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"submission":{"id":"kss_1"},"curation_status":"running"}`)
	}))
	defer server.Close()
	token := "record-secret"
	path := writeCapabilityFile(t, runCapabilityURL(server.URL, "run_record"), token, 0o600)
	client, err := NewRunBound(path, tokenDigest(token), "run_record")
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Invoke(context.Background(), json.RawMessage(`{"action":"record","content":"用户确认的自然语言规则","title":"退款约定","publish_intent":"confirmed_requirement"}`), "record-key")
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || gotAuth != "Bearer "+token || gotKey != "record-key" {
		t.Fatalf("record request auth/idempotency mismatch: calls=%d auth=%q key=%q", calls.Load(), gotAuth, gotKey)
	}
	if gotBody["content"] != "用户确认的自然语言规则" || gotBody["title"] != "退款约定" || gotBody["publish_intent"] != "confirmed_requirement" || gotBody["agent_id"] != nil || gotBody["workspace_id"] != nil {
		t.Fatalf("record body escaped its strict Run-bound contract: %+v", gotBody)
	}
	var response map[string]any
	if err := json.Unmarshal(result, &response); err != nil || response["submission"] == nil {
		t.Fatalf("record response = %s, err=%v", result, err)
	}
}

func TestInvokeReadRoutesPositiveVersionWithoutScopeSelectors(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/knowledge-agent/runs/run_read/items/kb_item/versions/3" {
			t.Errorf("unexpected read route: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected route", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"item":{"id":"kb_item"},"version":{"version":3}}`)
	}))
	defer server.Close()
	token := "read-secret"
	path := writeCapabilityFile(t, runCapabilityURL(server.URL, "run_read"), token, 0o600)
	client, err := NewRunBound(path, tokenDigest(token), "run_read")
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Invoke(context.Background(), json.RawMessage(`{"action":"read","item_id":"kb_item","version":3}`), "ignored-for-read")
	if err != nil || gotAuth != "Bearer "+token {
		t.Fatalf("read invocation failed: auth=%q err=%v", gotAuth, err)
	}
	if !json.Valid(result) {
		t.Fatalf("read returned invalid JSON: %s", result)
	}
}

func TestInvokeRejectsScopeForgeryAndIllegalOperationsBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()
	token := "strict-invoke-secret"
	path := writeCapabilityFile(t, runCapabilityURL(server.URL, "run_invoke"), token, 0o600)
	client, err := NewRunBound(path, tokenDigest(token), "run_invoke")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"unknown agent field":      `{"action":"record","content":"x","agent_id":"agent_other"}`,
		"unknown workspace field":  `{"action":"record","content":"x","workspace_id":"ws_other"}`,
		"unknown URL field":        `{"action":"record","content":"x","url":"http://other"}`,
		"unknown credential field": `{"action":"record","content":"x","token":"other"}`,
		"unsupported submit":       `{"action":"submit","content":"x"}`,
		"invalid record intent":    `{"action":"record","content":"x","publish_intent":"publish_anything"}`,
		"non-positive version":     `{"action":"read","item_id":"kb_item","version":0}`,
		"wrong ask fields":         `{"action":"ask","question":"x","item_id":"kb_item"}`,
		"wrong read field":         `{"action":"read","item_id":"kb_item","content":"x"}`,
		"wrong record field":       `{"action":"record","content":"x","version":1}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := client.Invoke(context.Background(), json.RawMessage(raw), "key"); err == nil || strings.Contains(err.Error(), token) {
				t.Fatalf("illegal invocation was accepted or leaked token: %v", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("strict invocation sent %d requests", calls.Load())
	}
}

func runCapabilityURL(base, runID string) string {
	return strings.TrimRight(base, "/") + "/api/v1/knowledge-agent/runs/" + url.PathEscape(runID)
}

func tokenDigest(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func writeCapabilityFile(t *testing.T, endpoint, token string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "capability.json")
	raw, err := json.Marshal(runBoundCapability{URL: endpoint, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

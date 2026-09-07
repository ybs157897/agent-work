package knowledgeclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestAskReturnsCompleteEvidenceAndBindsToken(t *testing.T) {
	var polls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer scoped-token" {
			t.Error("missing scoped token")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			if r.Header.Get("Idempotency-Key") != "request-1" {
				t.Error("missing stable key")
			}
			_, _ = w.Write([]byte(`{"id":"job-1","status":"running"}`))
			return
		}
		polls.Add(1)
		_, _ = w.Write([]byte(`{"id":"job-1","status":"completed","result":{"related":["feature-B"],"sources":["KB-A@1","KB-B@2"]}}`))
	}))
	defer s.Close()
	c, _ := New(s.URL, "scoped-token")
	c.PollInterval = time.Millisecond
	raw, err := c.Ask(context.Background(), map[string]string{"question": "feature A"}, "request-1")
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if polls.Load() != 1 || out["result"] == nil {
		t.Fatalf("lost evidence package: %s", raw)
	}
}

func TestAskCancellationCancelsDurableJob(t *testing.T) {
	var cancelled atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/jobs/job-1/cancel" {
			cancelled.Store(true)
			_, _ = w.Write([]byte(`{"id":"job-1","status":"cancelled"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"job-1","status":"running"}`))
	}))
	defer s.Close()
	c, _ := New(s.URL, "scoped-token")
	c.PollInterval = time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.Ask(ctx, map[string]string{"question": "A"}, "request-1"); err == nil {
		t.Fatal("cancellation ignored")
	}
	if !cancelled.Load() {
		t.Fatal("abandoned librarian Run after caller cancellation")
	}
}

func TestScopedTokenIsNotForwardedOnRedirect(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer target.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer s.Close()
	c, _ := New(s.URL, "secret")
	if _, err := c.Do(context.Background(), http.MethodGet, "/items/A", nil, ""); err == nil {
		t.Fatal("redirect accepted")
	}
	if leaked.Load() {
		t.Fatal("scoped token sent to redirected endpoint")
	}
}

package application

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestCodeWorkspaceCloseWaitsInflightCreateAndRejectsLaterCreates(t *testing.T) {
	svc := &CodeWorkspaceService{
		sessions:    make(map[string]*codeWorkspaceSession),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
		createsDone: closedSignal(),
	}
	go func() {
		<-svc.stop
		close(svc.done)
	}()
	if err := svc.beginCreate(); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		svc.Close(ctx)
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned while Create was still in flight")
	case <-time.After(25 * time.Millisecond):
	}
	svc.finishCreate()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after Create settled")
	}
	if err := svc.beginCreate(); err == nil {
		t.Fatal("Create admitted after service shutdown")
	}
}

func TestCodeWorkspaceSweepRetainsFailedReleaseForRetry(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	svc := &CodeWorkspaceService{
		client:   server.Client(),
		sessions: make(map[string]*codeWorkspaceSession),
	}
	session := &codeWorkspaceSession{CodeWorkspace: CodeWorkspace{
		ID: "cw_sweep", GatewayWorkspaceID: "upstream", ExpiresAt: time.Now().Add(-time.Minute),
	}, BaseURL: server.URL, Token: "secret"}
	svc.sessions[session.ID] = session
	svc.sweepExpired(context.Background())
	if _, ok := svc.sessions[session.ID]; !ok || !session.closing || attempts != 1 {
		t.Fatalf("failed release must remain retryable: present=%v closing=%v attempts=%d", ok, session.closing, attempts)
	}
	svc.sweepExpired(context.Background())
	if _, ok := svc.sessions[session.ID]; ok || attempts != 2 {
		t.Fatalf("successful retry must remove session: present=%v attempts=%d", ok, attempts)
	}
}

func TestCodeWorkspaceSweepDropsSessionsAfterGatewayGenerationChanges(t *testing.T) {
	svc := &CodeWorkspaceService{
		endpoint: func(context.Context) (string, string, error) { return "http://new-gateway", "new-token", nil },
		sessions: make(map[string]*codeWorkspaceSession),
	}
	active := &codeWorkspaceSession{CodeWorkspace: CodeWorkspace{ID: "cw_stale_active", GatewayWorkspaceID: "upstream"}, BaseURL: "http://old-gateway", Token: "old-token"}
	closing := &codeWorkspaceSession{CodeWorkspace: CodeWorkspace{ID: "cw_stale_closing", GatewayWorkspaceID: "upstream"}, BaseURL: "http://old-gateway", Token: "old-token", closing: true}
	svc.sessions[active.ID] = active
	svc.sessions[closing.ID] = closing
	svc.sweepExpired(context.Background())
	if len(svc.sessions) != 0 {
		t.Fatalf("generation change must discard stale active/closing handles: %+v", svc.sessions)
	}
}

func TestCodeWorkspaceSweepDoesNotStartGatewayWithoutSessions(t *testing.T) {
	var calls atomic.Int32
	svc := &CodeWorkspaceService{
		endpoint: func(context.Context) (string, string, error) {
			calls.Add(1)
			return "http://gateway", "token", nil
		},
		sessions: make(map[string]*codeWorkspaceSession),
	}
	svc.sweepExpired(context.Background())
	if got := calls.Load(); got != 0 {
		t.Fatalf("empty sweep called lazy Gateway endpoint %d times", got)
	}
}

func TestCodeWorkspaceConfirmedGenerationDropsStaleCapacityHandles(t *testing.T) {
	svc := &CodeWorkspaceService{sessions: make(map[string]*codeWorkspaceSession)}
	old := &codeWorkspaceSession{CodeWorkspace: CodeWorkspace{ID: "cw_old"}, BaseURL: "http://old", Token: "old"}
	current := &codeWorkspaceSession{CodeWorkspace: CodeWorkspace{ID: "cw_current"}, BaseURL: "http://new", Token: "new"}
	svc.sessions[old.ID] = old
	svc.sessions[current.ID] = current
	svc.dropStaleSessions("http://new", "new")
	if _, ok := svc.sessions[old.ID]; ok {
		t.Fatal("confirmed new Gateway generation retained stale session capacity handle")
	}
	if _, ok := svc.sessions[current.ID]; !ok {
		t.Fatal("current Gateway generation was removed")
	}
}

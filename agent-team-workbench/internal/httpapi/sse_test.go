package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/sse"
)

// replayBarrierEvents returns the first replay snapshot before pausing.  The
// test appends and notifies while the handler is between Since calls; this
// makes a subscribe-after-replay implementation deterministically lose the
// event without relying on timing sleeps.
type replayBarrierEvents struct {
	application.EventRepo
	snapshotReady chan struct{}
	release       chan struct{}
	sinceCalls    atomic.Int32
}

func (r *replayBarrierEvents) Since(ctx context.Context, workspaceID string, afterSeq int64, limit int) ([]*domain.CanonicalEvent, error) {
	events, err := r.EventRepo.Since(ctx, workspaceID, afterSeq, limit)
	if r.sinceCalls.Add(1) == 2 {
		close(r.snapshotReady)
		select {
		case <-r.release:
		case <-ctx.Done():
			return events, ctx.Err()
		}
	}
	return events, err
}

type replayBarrierStore struct {
	application.Store
	events application.EventRepo
}

func (s *replayBarrierStore) Events() application.EventRepo { return s.events }

type sseCaptureWriter struct {
	*httptest.ResponseRecorder
	marker string
	seen   chan struct{}
	once   sync.Once
}

func (w *sseCaptureWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), w.marker) {
		w.once.Do(func() { close(w.seen) })
	}
	return w.ResponseRecorder.Write(p)
}

func (w *sseCaptureWriter) Flush() { w.ResponseRecorder.Flush() }

func TestSSESubscribesBeforeReplayAndRechecksCursor(t *testing.T) {
	ctx := context.Background()
	base := openIdempotencyTestDB(t)
	now := time.Now().UTC()
	workspace := &domain.Workspace{
		ID: "ws_sse_race", Name: "sse", Timezone: "UTC", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := base.Workspaces().Create(ctx, workspace); err != nil {
		t.Fatal(err)
	}

	hub := sse.NewHub()
	barrier := &replayBarrierEvents{
		EventRepo:     base.Events(),
		snapshotReady: make(chan struct{}),
		release:       make(chan struct{}),
	}
	store := &replayBarrierStore{Store: base, events: barrier}
	server := &Server{store: store, hub: hub, demoRole: domain.RoleOwner}

	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+workspace.ID+"/events", nil).WithContext(requestCtx)
	rec := &sseCaptureWriter{
		ResponseRecorder: httptest.NewRecorder(),
		marker:           "sse-race-marker",
		seen:             make(chan struct{}),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.Routes().ServeHTTP(rec, req)
	}()

	select {
	case <-barrier.snapshotReady:
	case <-time.After(2 * time.Second):
		t.Fatal("SSE did not reach the replay barrier")
	}

	if err := base.InTx(ctx, func(txctx context.Context) error {
		ev, err := domain.NewCanonicalEvent(workspace.ID, domain.EventActivityCreated,
			domain.AggregateWorkspace, workspace.ID, 1,
			map[string]any{"marker": "sse-race-marker"})
		if err != nil {
			return err
		}
		_, err = base.Events().Append(txctx, ev, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// The notification is sent while the handler is still in the replay call.
	// A subscriber installed before replay retains this wakeup; a later
	// subscriber cannot observe it and must rely on the post-replay cursor read.
	hub.Notify(workspace.ID)
	close(barrier.release)

	select {
	case <-rec.seen:
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatalf("SSE replay missed an event committed during the replay window: %s", rec.Body.String())
	}
	cancel()
	<-done
	if !strings.Contains(rec.Body.String(), "sse-race-marker") {
		t.Fatalf("SSE 响应缺少竞态窗口内提交的事件: %s", rec.Body.String())
	}
}

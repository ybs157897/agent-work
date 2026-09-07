package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/modelconfig"
)

func TestConcurrentModelDeletesPreserveReadableRegistry(t *testing.T) {
	store := openIdempotencyTestDB(t)
	registry := modelconfig.NewRegistry(t.TempDir())
	const count = 12
	for i := 0; i < count; i++ {
		if err := registry.Upsert(&modelconfig.Entry{
			ID: fmt.Sprintf("bulk-delete-%02d", i), DisplayName: fmt.Sprintf("Model %d", i),
			ProviderID: "provider-bulk", Provider: "test", Model: "test-model",
			Notes: strings.Repeat("long model description ", i+1),
		}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{store: store, models: registry, demoRole: domain.RoleOwner}
	mux := s.Routes()
	start, stop := make(chan struct{}), make(chan struct{})
	errors := make(chan error, count+1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		<-start
		for {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/models", nil))
			if rec.Code != http.StatusOK {
				errors <- fmt.Errorf("model list during deletes: %d %s", rec.Code, rec.Body)
				return
			}
			select {
			case <-stop:
				return
			default:
			}
		}
	}()
	var workers sync.WaitGroup
	for i := 0; i < count; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			<-start
			id := fmt.Sprintf("bulk-delete-%02d", i)
			req := httptest.NewRequest(http.MethodDelete, "/api/v1/models/"+id, nil)
			req.Header.Set("Idempotency-Key", id)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusNoContent {
				errors <- fmt.Errorf("delete %s: %d %s", id, rec.Code, rec.Body)
			}
		}(i)
	}
	close(start)
	workers.Wait()
	close(stop)
	<-readerDone
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/models", nil))
	var result struct {
		Items []modelconfig.Entry `json:"items"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &result) != nil || len(result.Items) != 0 {
		t.Fatalf("successful deletes must all persist: status=%d body=%s", rec.Code, rec.Body)
	}
}

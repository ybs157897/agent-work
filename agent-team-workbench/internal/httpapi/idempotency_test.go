package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/migtest"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

// openIdempotencyTestDB 临时文件 sqlite + 全量迁移（migtest 动态发现
// migrations，新增迁移免同步清单）。MaxOpenConns(1) + busy_timeout
// 规避并发写下的 SQLITE_BUSY。
func openIdempotencyTestDB(t *testing.T) *sqlstore.Store {
	t.Helper()
	db, err := sql.Open("sqlite",
		filepath.Join(t.TempDir(), "test.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := migtest.ApplyAll(db); err != nil {
		t.Fatal(err)
	}
	return sqlstore.New(db)
}

func newIdempotentHandler(s *Server, scope string, exec func() (int, []byte)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.idempotent(w, r, scope, exec)
	}
}

func postWithKey(body string, key string) *http.Request {
	req := httptest.NewRequest("POST", "/api/v1/work-items", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", key)
	return req
}

// 并发同 key 同 body：exec 副作用只发生一次，其余请求为重放或 409 in-progress。
func TestIdempotentConcurrentSameKeyExecutesOnce(t *testing.T) {
	store := openIdempotencyTestDB(t)
	s := &Server{store: store, demoRole: domain.RoleOwner}

	var calls atomic.Int32
	handler := newIdempotentHandler(s, "ws_t", func() (int, []byte) {
		calls.Add(1)
		return renderJSON(nil, nil, http.StatusCreated, map[string]any{"ok": true})
	})

	const n = 8
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, postWithKey(`{"title":"x"}`, "key-1"))
			codes[i] = rec.Code
		}(i)
	}
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("exec 副作用次数 = %d, 期望 1", got)
	}
	created, conflict := 0, 0
	for i, code := range codes {
		switch code {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflict++ // 执行窗口内到达的并发重试
		default:
			t.Fatalf("resp[%d] status = %d, 期望 201 或 409", i, code)
		}
	}
	if created < 1 {
		t.Fatalf("至少一个请求应拿到执行结果: %v", codes)
	}
}

// 同 key 执行中（占位未完成）：后续同 hash 请求 409 in_progress；完成后重放原响应。
func TestIdempotentInProgressThenReplay(t *testing.T) {
	store := openIdempotencyTestDB(t)
	s := &Server{store: store, demoRole: domain.RoleOwner}

	entered := make(chan struct{})
	release := make(chan struct{})
	handler := newIdempotentHandler(s, "ws_t", func() (int, []byte) {
		close(entered)
		<-release
		return renderJSON(nil, nil, http.StatusCreated, map[string]any{"value": 42})
	})

	first := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(first, postWithKey(`{"a":1}`, "key-2"))
	}()
	<-entered

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, postWithKey(`{"a":1}`, "key-2"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("in-progress status = %d, 期望 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "idempotency_in_progress") {
		t.Fatalf("in-progress problem code 缺失: %s", rec.Body.String())
	}

	close(release)
	<-done
	if first.Code != http.StatusCreated {
		t.Fatalf("首个请求 status = %d, 期望 201", first.Code)
	}

	replay := httptest.NewRecorder()
	handler.ServeHTTP(replay, postWithKey(`{"a":1}`, "key-2"))
	if replay.Code != http.StatusCreated {
		t.Fatalf("重放 status = %d, 期望 201", replay.Code)
	}
	if replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatal("重放响应应带 Idempotent-Replayed: true")
	}
	if got := replay.Body.String(); got != strings.TrimSpace(first.Body.String()) {
		t.Fatalf("重放体 = %q, 期望 %q", got, first.Body.String())
	}
}

// 同 key 不同 body → 409 idempotency_conflict（既有语义保持不变）。
func TestIdempotentDifferentBodyConflicts(t *testing.T) {
	store := openIdempotencyTestDB(t)
	s := &Server{store: store, demoRole: domain.RoleOwner}

	var calls atomic.Int32
	handler := newIdempotentHandler(s, "ws_t", func() (int, []byte) {
		calls.Add(1)
		return renderJSON(nil, nil, http.StatusCreated, map[string]any{"ok": true})
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, postWithKey(`{"a":1}`, "key-3"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("首次 status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, postWithKey(`{"a":2}`, "key-3"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("不同 body status = %d, 期望 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "idempotency_conflict") {
		t.Fatalf("冲突 problem code 缺失: %s", rec.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("exec 次数 = %d, 期望 1", calls.Load())
	}
}

func TestIdempotentProblemContentTypeIsStableOnClaimReplay(t *testing.T) {
	store := openIdempotencyTestDB(t)
	s := &Server{store: store, demoRole: domain.RoleOwner}
	handler := newIdempotentHandler(s, "ws_problem", func() (int, []byte) {
		return renderProblem(http.StatusUnprocessableEntity, "validation_failed", "Validation failed", "bad input")
	})

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, postWithKey(`{"value":1}`, "problem-key"))
	if first.Code != http.StatusUnprocessableEntity || first.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("首次 4xx 必须标记 problem+json: status=%d headers=%v body=%s", first.Code, first.Header(), first.Body.String())
	}
	replay := httptest.NewRecorder()
	handler.ServeHTTP(replay, postWithKey(`{"value":1}`, "problem-key"))
	if replay.Code != first.Code || replay.Header().Get("Content-Type") != "application/problem+json" || replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("claim-first 重放必须保留 problem+json: status=%d headers=%v body=%s", replay.Code, replay.Header(), replay.Body.String())
	}
}

type fallbackIdempotencyRepo struct {
	mu   sync.Mutex
	rows map[string]application.IdempotencyRecord
}

func (r *fallbackIdempotencyRepo) Check(_ context.Context, workspaceID, key string) (*application.IdempotencyRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.rows[workspaceID+"\x00"+key]
	if !ok {
		return nil, nil
	}
	copy := rec
	return &copy, nil
}

func (r *fallbackIdempotencyRepo) Record(_ context.Context, workspaceID, key string, rec application.IdempotencyRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows[workspaceID+"\x00"+key] = rec
	return nil
}

type fallbackIdempotencyStore struct {
	application.Store
	repo application.IdempotencyRepo
}

func (s *fallbackIdempotencyStore) Idempotency() application.IdempotencyRepo { return s.repo }

func TestIdempotentRejectsNonDurableFallbackStore(t *testing.T) {
	base := openIdempotencyTestDB(t)
	store := &fallbackIdempotencyStore{
		Store: base,
		repo:  &fallbackIdempotencyRepo{rows: make(map[string]application.IdempotencyRecord)},
	}
	s := &Server{store: store, demoRole: domain.RoleOwner}
	handler := newIdempotentHandler(s, "fallback-problem", func() (int, []byte) {
		return renderProblem(http.StatusBadRequest, "bad_request", "Bad request", "bad body")
	})

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, postWithKey(`{"value":1}`, "fallback-problem-key"))
	if first.Code != http.StatusInternalServerError || first.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("non-durable fallback must fail closed: status=%d headers=%v", first.Code, first.Header())
	}
	replay := httptest.NewRecorder()
	handler.ServeHTTP(replay, postWithKey(`{"value":1}`, "fallback-problem-key"))
	if replay.Code != first.Code || replay.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("non-durable fallback must remain fail-closed: status=%d headers=%v body=%s", replay.Code, replay.Header(), replay.Body.String())
	}
}

type failingFinalizeIdempotencyRepo struct {
	completeErr error
	releaseErr  error
}

func (r failingFinalizeIdempotencyRepo) Check(context.Context, string, string) (*application.IdempotencyRecord, error) {
	return nil, nil
}

func (r failingFinalizeIdempotencyRepo) Record(context.Context, string, string, application.IdempotencyRecord) error {
	return nil
}

func (r failingFinalizeIdempotencyRepo) Claim(context.Context, string, string, string) (bool, *application.IdempotencyRecord, string, error) {
	return true, nil, "claim-test", nil
}

func (r failingFinalizeIdempotencyRepo) Complete(context.Context, string, string, string, string, int, string) error {
	return r.completeErr
}

func (r failingFinalizeIdempotencyRepo) Release(context.Context, string, string, string, string) error {
	return r.releaseErr
}

func (failingFinalizeIdempotencyRepo) Renew(context.Context, string, string, string, string) error {
	return nil
}

func TestIdempotentFinalizeErrorsAreReturned(t *testing.T) {
	for _, test := range []struct {
		name        string
		status      int
		completeErr error
		releaseErr  error
	}{
		{name: "complete", status: http.StatusCreated, completeErr: errors.New("complete failed")},
		{name: "release", status: http.StatusInternalServerError, releaseErr: errors.New("release failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &fallbackIdempotencyStore{Store: openIdempotencyTestDB(t), repo: failingFinalizeIdempotencyRepo{completeErr: test.completeErr, releaseErr: test.releaseErr}}
			s := &Server{store: store, demoRole: domain.RoleOwner}
			var calls atomic.Int32
			handler := newIdempotentHandler(s, "finalize", func() (int, []byte) {
				calls.Add(1)
				return test.status, []byte(`{"ok":true}`)
			})
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, postWithKey(`{"value":1}`, "finalize-key"))
			if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "idempotency_finalize_failed") {
				t.Fatalf("finalization failure must be visible: status=%d body=%s", rec.Code, rec.Body.String())
			}
			if calls.Load() != 1 {
				t.Fatalf("exec should run once before finalization failure, calls=%d", calls.Load())
			}
		})
	}
}

type nonRenewingIdempotencyRepo struct{}

func (nonRenewingIdempotencyRepo) Check(context.Context, string, string) (*application.IdempotencyRecord, error) {
	return nil, nil
}

func (nonRenewingIdempotencyRepo) Record(context.Context, string, string, application.IdempotencyRecord) error {
	return nil
}

func (nonRenewingIdempotencyRepo) Claim(context.Context, string, string, string) (bool, *application.IdempotencyRecord, string, error) {
	return true, nil, "non-renewing-token", nil
}

func (nonRenewingIdempotencyRepo) Complete(context.Context, string, string, string, string, int, string) error {
	return nil
}

func (nonRenewingIdempotencyRepo) Release(context.Context, string, string, string, string) error {
	return nil
}

func TestIdempotentRejectsClaimStoreWithoutRenewal(t *testing.T) {
	store := &fallbackIdempotencyStore{Store: openIdempotencyTestDB(t), repo: nonRenewingIdempotencyRepo{}}
	s := &Server{store: store, demoRole: domain.RoleOwner}
	var calls atomic.Int32
	handler := newIdempotentHandler(s, "non-renewing", func() (int, []byte) {
		calls.Add(1)
		return http.StatusCreated, []byte(`{"ok":true}`)
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, postWithKey(`{"value":1}`, "non-renewing-key"))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "idempotency_not_durable") {
		t.Fatalf("claim store without Renew must fail closed: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("non-renewing store must not execute the command: calls=%d", calls.Load())
	}
}

type cancellationAwareIdempotencyRepo struct {
	mu        sync.Mutex
	completed bool
	hash      string
}

func (r *cancellationAwareIdempotencyRepo) Check(context.Context, string, string) (*application.IdempotencyRecord, error) {
	return nil, nil
}

func (r *cancellationAwareIdempotencyRepo) Record(context.Context, string, string, application.IdempotencyRecord) error {
	return nil
}

func (r *cancellationAwareIdempotencyRepo) Claim(_ context.Context, _ string, _ string, requestHash string) (bool, *application.IdempotencyRecord, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.completed {
		return false, &application.IdempotencyRecord{RequestHash: r.hash, StatusCode: http.StatusCreated, ResultBody: `{"ok":true}`}, "", nil
	}
	r.hash = requestHash
	return true, nil, "cancel-aware-token", nil
}

func (r *cancellationAwareIdempotencyRepo) Complete(ctx context.Context, _ string, _ string, requestHash, _ string, statusCode int, resultBody string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hash, r.completed = requestHash, true
	if statusCode != http.StatusCreated || resultBody != `{"ok":true}` {
		return errors.New("unexpected completion payload")
	}
	return nil
}

func (*cancellationAwareIdempotencyRepo) Release(context.Context, string, string, string, string) error {
	return nil
}

func (*cancellationAwareIdempotencyRepo) Renew(context.Context, string, string, string, string) error {
	return nil
}

func TestIdempotencyFinalizationSurvivesClientCancellation(t *testing.T) {
	repo := &cancellationAwareIdempotencyRepo{}
	store := &fallbackIdempotencyStore{Store: openIdempotencyTestDB(t), repo: repo}
	s := &Server{store: store, demoRole: domain.RoleOwner}
	var calls atomic.Int32
	handler := newIdempotentHandler(s, "cancel-aware", func() (int, []byte) {
		calls.Add(1)
		return http.StatusCreated, []byte(`{"ok":true}`)
	})
	ctx, cancel := context.WithCancel(context.Background())
	firstReq := postWithKey(`{"value":1}`, "cancel-aware-key").WithContext(ctx)
	first := httptest.NewRecorder()
	// The command has completed its side effect, then the client disappears
	// before the handler finalizes the durable idempotency result.
	handlerWithCancellation := newIdempotentHandler(s, "cancel-aware", func() (int, []byte) {
		calls.Add(1)
		cancel()
		return http.StatusCreated, []byte(`{"ok":true}`)
	})
	handlerWithCancellation.ServeHTTP(first, firstReq)
	if first.Code != http.StatusCreated {
		t.Fatalf("client cancellation after exec must not fail finalization: status=%d body=%s", first.Code, first.Body.String())
	}
	replay := httptest.NewRecorder()
	handler.ServeHTTP(replay, postWithKey(`{"value":1}`, "cancel-aware-key"))
	if replay.Code != http.StatusCreated || replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("completed result must remain replayable after client cancellation: status=%d headers=%v body=%s", replay.Code, replay.Header(), replay.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("client cancellation caused duplicate execution: calls=%d", calls.Load())
	}
}

// exec 返回 5xx：占位行被释放，客户端可以同 key 重试（重试真正重新执行）。
func TestIdempotentReleasesPlaceholderOnServerError(t *testing.T) {
	store := openIdempotencyTestDB(t)
	s := &Server{store: store, demoRole: domain.RoleOwner}

	var calls atomic.Int32
	failOnce := func() (int, []byte) {
		if calls.Add(1) == 1 {
			return http.StatusInternalServerError, []byte(`{"error":"boom"}`)
		}
		return renderJSON(nil, nil, http.StatusCreated, map[string]any{"ok": true})
	}
	handler := newIdempotentHandler(s, "ws_t", failOnce)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, postWithKey(`{"a":1}`, "key-4"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("首次 status = %d, 期望 500", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, postWithKey(`{"a":1}`, "key-4"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("重试 status = %d, 期望 201（占位行应已释放）: %s", rec.Code, rec.Body.String())
	}
	if calls.Load() != 2 {
		t.Fatalf("exec 次数 = %d, 期望 2（5xx 后允许同 key 重试）", calls.Load())
	}
}

// 未知角色：统一 PermRead 的 GET 返回 403；公开端点 /health 不受影响且为嵌套 HealthStatus 投影。
func TestGetRoutesGuardedByPermRead(t *testing.T) {
	s := &Server{store: openIdempotencyTestDB(t), demoRole: domain.MemberRole("intruder")}
	mux := s.Routes()

	for _, path := range []string{
		"/api/v1/me",
		"/api/v1/workspaces",
		"/api/v1/workspaces/ws_1/dashboard",
		"/api/v1/models",
		"/api/v1/runs/run_1",
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("GET %s（未知角色）status = %d, 期望 403", path, rec.Code)
		}
	}

	// /health 为公开端点（openapi security: []）：不挂守卫，返回嵌套 HealthStatus。
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/health status = %d, 期望 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"health"`) ||
		!strings.Contains(rec.Body.String(), `"control_plane"`) {
		t.Fatalf("/health 应为 {health:{control_plane,runners}} 嵌套结构: %s", rec.Body.String())
	}

	// 凭据明文回显：GET 与 PUT 同级 PermRuntimeManage（intruder 同样 403）。
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/models/provider-credentials?provider_id=p1", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET provider-credentials（未知角色）status = %d, 期望 403", rec.Code)
	}
}

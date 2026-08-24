package httpapi

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

// openIdempotencyTestDB 临时文件 sqlite + 全量迁移（照 sqlstore 测试的搭建方式）。
// MaxOpenConns(1) + busy_timeout 规避并发写下的 SQLITE_BUSY。
func openIdempotencyTestDB(t *testing.T) *sqlstore.Store {
	t.Helper()
	db, err := sql.Open("sqlite",
		filepath.Join(t.TempDir(), "test.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, current, _, _ := runtime.Caller(0)
	migrationDir := filepath.Join(filepath.Dir(current), "..", "..", "migrations", "sqlite")
	for _, name := range []string{
		"0001_init.sql",
		"0002_runtime_binding_model_config.sql",
		"0003_agent_config.sql",
		"0004_task_sessions.sql",
		"0005_wakeup.sql",
		"0006_plans.sql",
		"0007_task_sessions_parent.sql",
		"0008_plan_source_run_unique.sql",
		"0009_plan_consult_knowledge.sql",
		"0010_plan_join_guardrails.sql",
		"0011_activity_work_item.sql",
		"0012_approval_grants.sql",
	} {
		body, err := os.ReadFile(filepath.Join(migrationDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("migration %s: %v", name, err)
		}
	}
	return sqlstore.New(db, sqlstore.SQLiteDialect())
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

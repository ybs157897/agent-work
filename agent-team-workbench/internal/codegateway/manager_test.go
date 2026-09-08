package codegateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestGatewayProcessHelper(t *testing.T) {
	if os.Getenv("ATW_TEST_CODE_GATEWAY") == "" {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	if os.Getenv("ATW_TEST_CODE_GATEWAY") == "wait" {
		<-ctx.Done()
		os.Exit(0)
	}
	listener, err := net.Listen("tcp", os.Getenv("WEBIDEA_LISTEN"))
	if err != nil {
		os.Exit(2)
	}
	defer listener.Close()
	fmt.Printf("WEBIDEA_READY=http://%s\n", listener.Addr())
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+os.Getenv("WEBIDEA_DEV_TOKEN") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = server.Serve(listener) }()
	<-ctx.Done()
	_ = server.Close()
	os.Exit(0)
}

func helperManager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html></html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	m := New(Config{Root: dir, Binary: binary, StaticDir: dir, DataDir: filepath.Join(dir, "data"),
		Args: []string{"-test.run=^TestGatewayProcessHelper$"}, StartupTimeout: 3 * time.Second})
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestConcurrentOpenSharesGatewayAndCloseReleasesListener(t *testing.T) {
	t.Setenv("ATW_TEST_CODE_GATEWAY", "serve")
	m := helperManager(t)
	if m.proc != nil {
		t.Fatal("gateway must be lazy")
	}
	var wg sync.WaitGroup
	addresses, tokens := make([]string, 8), make([]string, 8)
	for i := range addresses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			addresses[i], tokens[i], err = m.Endpoint(context.Background())
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for i := range addresses {
		if addresses[i] == "" || addresses[i] != addresses[0] || tokens[i] != tokens[0] || len(tokens[i]) < 40 {
			t.Fatal("concurrent opens did not share one private gateway")
		}
	}
	request, _ := http.NewRequest(http.MethodGet, addresses[0], nil)
	request.Header.Set("Authorization", "Bearer "+tokens[0])
	client := &http.Client{Timeout: time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("private token not applied to child")
	}
	pgid := m.proc.pgid
	dataDir := m.proc.dataDir
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(-pgid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("child process group still exists: %v", err)
	}
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("gateway index directory remained after shutdown: %v", err)
	}
	if _, _, err := m.Endpoint(context.Background()); err == nil {
		t.Fatal("closed manager restarted a process")
	}
}

func TestGatewayCrashRestartsWithNewCredential(t *testing.T) {
	t.Setenv("ATW_TEST_CODE_GATEWAY", "serve")
	m := helperManager(t)
	_, oldToken, err := m.Endpoint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	previous := m.proc
	_ = syscall.Kill(-previous.pgid, syscall.SIGKILL)
	<-previous.done
	if _, err := os.Stat(previous.dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("crashed gateway index directory remained: %v", err)
	}
	_, token, err := m.Endpoint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token == oldToken || m.proc.pgid == previous.pgid {
		t.Fatal("restart reused process identity or credential")
	}
}

func TestCanceledStartupDoesNotLeaveAChild(t *testing.T) {
	t.Setenv("ATW_TEST_CODE_GATEWAY", "wait")
	m := helperManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, _, err := m.Endpoint(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("startup error = %v", err)
	}
	if m.proc != nil {
		t.Fatal("canceled startup was published")
	}
}

func TestOnlyActualLoopbackEndpointsAreAccepted(t *testing.T) {
	for _, address := range []string{"http://example.com:42", "http://127.0.0.1:0", "https://127.0.0.1:80", "http://user@127.0.0.1:80", "http://127.0.0.1:80/?token=x", "http://127.0.0.1:99999"} {
		if validEndpoint(address) {
			t.Errorf("accepted %s", address)
		}
	}
	if !validEndpoint("http://127.0.0.1:12345") {
		t.Fatal("rejected actual loopback port")
	}
}

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ybs/web-idea/apps/gateway/internal/api"
	"github.com/ybs/web-idea/apps/gateway/internal/config"
	"github.com/ybs/web-idea/apps/gateway/internal/jdtls"
	"github.com/ybs/web-idea/apps/gateway/internal/workspace"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	var jdtlsMgr *jdtls.Manager
	if cfg.JdtlsLaunch != "" {
		dataRoot := cfg.JdtlsDataRoot
		if dataRoot == "" {
			dataRoot = filepath.Join(os.TempDir(), "web-idea-jdtls-data")
		}
		jdtlsMgr = jdtls.NewManager(jdtls.Config{
			LaunchScript: cfg.JdtlsLaunch,
			DataRoot:     dataRoot,
		})
		log.Printf("jdtls enabled via %s (data=%s)", cfg.JdtlsLaunch, dataRoot)
	} else {
		log.Printf("jdtls disabled (set WEBIDEA_JDTLS_LAUNCH to enable LSP)")
	}

	store := workspace.NewStore(workspace.Options{
		MaxWorkspaces:      cfg.WorkspaceMaxCount,
		DefaultReadOnlyTTL: cfg.WorkspaceDefaultTTL,
		MaxTTL:             cfg.WorkspaceMaxTTL,
	})
	srv := api.New(cfg, store, jdtlsMgr)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	srv.Start(ctx)
	defer srv.Close()
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		log.Fatalf("listen %s: %v", cfg.Listen, err)
	}
	defer listener.Close()
	fmt.Fprintf(os.Stdout, "WEBIDEA_READY=http://%s\n", listener.Addr().String())
	log.Printf("web-idea gateway listening on http://%s (auth=%s)", listener.Addr().String(), cfg.AuthMode)

	server := &http.Server{Handler: srv.Handler()}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("server shutdown: %v", err)
		}
		cancel()
		if jdtlsMgr != nil {
			jdtlsMgr.StopAll()
		}
		err := <-serveErr
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server stopped: %v", err)
		}
	case err := <-serveErr:
		if jdtlsMgr != nil {
			jdtlsMgr.StopAll()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server stopped: %v", err)
			os.Exit(1)
		}
	}
}

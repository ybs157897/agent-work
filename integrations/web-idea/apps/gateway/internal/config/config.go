package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ybs/web-idea/apps/gateway/internal/fsjail"
)

type Config struct {
	Listen       string
	AuthMode     string
	DevToken     string
	MaxFileBytes int64
	CORSOrigins  []string
	// Workspace lease policy is process-local and never persisted.
	WorkspaceMaxCount      int
	WorkspaceDefaultTTL    time.Duration
	WorkspaceMaxTTL        time.Duration
	WorkspaceSweepInterval time.Duration
	// JdtlsLaunch is path to launch.sh; empty disables LSP.
	JdtlsLaunch string
	// JdtlsDataRoot is parent dir for per-workspace jdtls -data.
	JdtlsDataRoot string
	// StaticDir serves the built web application when configured.
	StaticDir string
}

func Load() (Config, error) {
	cfg := Config{
		Listen:                 envOr("WEBIDEA_LISTEN", "127.0.0.1:8080"),
		AuthMode:               envOr("WEBIDEA_AUTH_MODE", "dev"),
		DevToken:               envOr("WEBIDEA_DEV_TOKEN", "dev-token-change-me"),
		MaxFileBytes:           fsjail.DefaultMaxFileBytes,
		CORSOrigins:            []string{"http://127.0.0.1:5173", "http://localhost:5173"},
		WorkspaceMaxCount:      32,
		WorkspaceDefaultTTL:    30 * time.Minute,
		WorkspaceMaxTTL:        24 * time.Hour,
		WorkspaceSweepInterval: time.Minute,
		JdtlsLaunch:            os.Getenv("WEBIDEA_JDTLS_LAUNCH"),
		JdtlsDataRoot:          envOr("WEBIDEA_JDTLS_DATA", ""),
		StaticDir:              envOr("WEBIDEA_STATIC_DIR", ""),
	}
	if v := os.Getenv("WEBIDEA_MAX_FILE_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return cfg, fmt.Errorf("invalid WEBIDEA_MAX_FILE_BYTES")
		}
		cfg.MaxFileBytes = n
	}
	if v := os.Getenv("WEBIDEA_CORS_ORIGINS"); v != "" {
		cfg.CORSOrigins = splitCSV(v)
	}
	if v := os.Getenv("WEBIDEA_WORKSPACE_MAX_COUNT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return cfg, fmt.Errorf("invalid WEBIDEA_WORKSPACE_MAX_COUNT")
		}
		cfg.WorkspaceMaxCount = n
	}
	if v := os.Getenv("WEBIDEA_WORKSPACE_DEFAULT_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return cfg, fmt.Errorf("invalid WEBIDEA_WORKSPACE_DEFAULT_TTL")
		}
		cfg.WorkspaceDefaultTTL = d
	}
	if v := os.Getenv("WEBIDEA_WORKSPACE_MAX_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return cfg, fmt.Errorf("invalid WEBIDEA_WORKSPACE_MAX_TTL")
		}
		cfg.WorkspaceMaxTTL = d
	}
	if v := os.Getenv("WEBIDEA_WORKSPACE_SWEEP_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return cfg, fmt.Errorf("invalid WEBIDEA_WORKSPACE_SWEEP_INTERVAL")
		}
		cfg.WorkspaceSweepInterval = d
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.AuthMode != "dev" {
		return fmt.Errorf("unsupported WEBIDEA_AUTH_MODE %q (P1 only supports dev)", c.AuthMode)
	}
	if c.DevToken == "" {
		return fmt.Errorf("WEBIDEA_DEV_TOKEN required")
	}
	if c.WorkspaceMaxCount <= 0 {
		return fmt.Errorf("workspace max count must be positive")
	}
	if c.WorkspaceMaxTTL <= 0 {
		return fmt.Errorf("workspace max ttl must be positive")
	}
	if c.WorkspaceDefaultTTL <= 0 || c.WorkspaceDefaultTTL > c.WorkspaceMaxTTL {
		return fmt.Errorf("workspace default ttl must be positive and no greater than max ttl")
	}
	if c.WorkspaceSweepInterval <= 0 {
		return fmt.Errorf("workspace sweep interval must be positive")
	}
	host := c.Listen
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	host = strings.Trim(host, "[]")
	if (host == "0.0.0.0" || host == "::" || host == "") && c.AuthMode == "dev" {
		return fmt.Errorf("refusing to listen on %s with AUTH_MODE=dev", c.Listen)
	}
	if c.StaticDir != "" {
		abs, err := filepath.Abs(c.StaticDir)
		if err != nil {
			return fmt.Errorf("invalid WEBIDEA_STATIC_DIR: %w", err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return fmt.Errorf("invalid WEBIDEA_STATIC_DIR: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("invalid WEBIDEA_STATIC_DIR: not a directory")
		}
	}
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

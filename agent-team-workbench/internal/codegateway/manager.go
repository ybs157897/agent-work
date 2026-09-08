// Package codegateway owns the local Java reading gateway process. Workspace
// authorization and reading sessions belong to the HTTP/application layers.
package codegateway

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Config struct {
	Root, Binary, StaticDir, DataDir, JDTLSLaunch string
	StartupTimeout                                time.Duration
	Stderr                                        io.Writer
	Args                                          []string
}

type process struct {
	cmd     *exec.Cmd
	pgid    int
	done    chan struct{}
	url     string
	token   string
	dataDir string
}

type Manager struct {
	mu     sync.Mutex
	cfg    Config
	proc   *process
	closed bool
}

// DefaultConfig only resolves the checked-in integration. It never discovers a
// neighboring mutable checkout or guesses a listening port.
func DefaultConfig(workbenchRoot, dataDir string) Config {
	root := os.Getenv("ATW_WEBIDEA_ROOT")
	if root == "" {
		root = filepath.Join(workbenchRoot, "..", "integrations", "web-idea")
	}
	root, _ = filepath.Abs(root)
	cfg := Config{Root: root, DataDir: dataDir, Stderr: os.Stderr,
		Binary:    filepath.Join(root, "apps", "gateway", "bin", "web-idea-gateway"),
		StaticDir: filepath.Join(root, "apps", "web", "dist")}
	if value := os.Getenv("ATW_WEBIDEA_BIN"); value != "" {
		cfg.Binary = value
	}
	if value := os.Getenv("ATW_WEBIDEA_WEB_DIST"); value != "" {
		cfg.StaticDir = value
	}
	if value := os.Getenv("ATW_WEBIDEA_JDTLS_LAUNCH"); value != "" {
		cfg.JDTLSLaunch = value
	} else {
		dist := os.Getenv("WEBIDEA_JDTLS_HOME")
		if dist == "" {
			dist = filepath.Join(root, "apps", "jdtls-sidecar", "dist")
		}
		launchers, _ := filepath.Glob(filepath.Join(dist, "plugins", "org.eclipse.equinox.launcher_*.jar"))
		if len(launchers) > 0 {
			cfg.JDTLSLaunch = filepath.Join(root, "apps", "jdtls-sidecar", "bin", "launch.sh")
		}
	}
	return cfg
}

func New(cfg Config) *Manager {
	if cfg.StartupTimeout <= 0 {
		cfg.StartupTimeout = 15 * time.Second
	}
	if cfg.Stderr == nil {
		cfg.Stderr = io.Discard
	}
	return &Manager{cfg: cfg}
}

// Endpoint lazily starts one gateway for all reading sessions. The token never
// leaves the server-side proxy; a restarted process receives a new token.
func (m *Manager) Endpoint(ctx context.Context) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if m.closed {
		return "", "", errors.New("代码服务已关闭")
	}
	if m.proc != nil {
		select {
		case <-m.proc.done:
			m.proc = nil
		default:
			return m.proc.url, m.proc.token, nil
		}
	}
	if info, err := os.Stat(m.cfg.Binary); err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return "", "", errors.New("代码服务尚未构建，请在工作台目录执行 make code-workspace-build")
	}
	if info, err := os.Stat(filepath.Join(m.cfg.StaticDir, "index.html")); err != nil || info.IsDir() {
		return "", "", errors.New("代码界面尚未构建，请在工作台目录执行 make code-workspace-build")
	}
	if err := os.MkdirAll(m.cfg.DataDir, 0o700); err != nil {
		return "", "", errors.New("无法创建代码索引数据目录")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", "", fmt.Errorf("生成代码服务凭据: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	dataDir, err := os.MkdirTemp(m.cfg.DataDir, "gateway-")
	if err != nil {
		return "", "", errors.New("无法创建代码服务独立索引目录")
	}
	cmd := exec.Command(m.cfg.Binary, m.cfg.Args...)
	cmd.Dir = m.cfg.Root
	cmd.Env = replaceEnv(os.Environ(), map[string]string{
		"WEBIDEA_LISTEN": "127.0.0.1:0", "WEBIDEA_AUTH_MODE": "dev",
		"WEBIDEA_DEV_TOKEN": token, "WEBIDEA_STATIC_DIR": m.cfg.StaticDir,
		"WEBIDEA_JDTLS_LAUNCH": m.cfg.JDTLSLaunch, "WEBIDEA_JDTLS_DATA": dataDir,
		"WEBIDEA_CORS_ORIGINS": "http://127.0.0.1",
	})
	cmd.Stderr = m.cfg.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = os.RemoveAll(dataDir)
		return "", "", err
	}
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		_ = os.RemoveAll(dataDir)
		return "", "", errors.New("启动代码服务失败，请检查服务可执行文件")
	}
	p := &process{cmd: cmd, pgid: cmd.Process.Pid, done: make(chan struct{}), token: token, dataDir: dataDir}
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if address, ok := strings.CutPrefix(scanner.Text(), "WEBIDEA_READY="); ok {
				select {
				case ready <- address:
				default:
				}
			}
		}
	}()
	go func() {
		_ = cmd.Wait()
		// This is the launch-time process group, reaped immediately with its
		// leader. A crashed Gateway must not retain Java children or index data.
		_ = syscall.Kill(-p.pgid, syscall.SIGKILL)
		_ = os.RemoveAll(p.dataDir)
		close(p.done)
	}()
	timer := time.NewTimer(m.cfg.StartupTimeout)
	defer timer.Stop()
	select {
	case address := <-ready:
		if !validEndpoint(address) {
			stopProcess(p)
			return "", "", errors.New("代码服务返回了无效的本机监听地址")
		}
		p.url = address
		m.proc = p
		return address, token, nil
	case <-p.done:
		return "", "", errors.New("代码服务在就绪前退出")
	case <-ctx.Done():
		stopProcess(p)
		return "", "", ctx.Err()
	case <-timer.C:
		stopProcess(p)
		return "", "", errors.New("代码服务启动超时")
	}
}

func validEndpoint(address string) bool {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	port, err := strconv.Atoi(u.Port())
	return ip != nil && ip.IsLoopback() && err == nil && port > 0 && port <= 65535
}

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	if m.proc != nil {
		stopProcess(m.proc)
		m.proc = nil
	}
	return nil
}

func stopProcess(p *process) {
	select {
	case <-p.done:
		return
	default:
	}
	_ = syscall.Kill(-p.pgid, syscall.SIGTERM)
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
	case <-timer.C:
		_ = syscall.Kill(-p.pgid, syscall.SIGKILL)
		<-p.done
	}
}

func replaceEnv(source []string, overrides map[string]string) []string {
	result := make([]string, 0, len(source)+len(overrides))
	for _, entry := range source {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[key]; !replaced {
			result = append(result, entry)
		}
	}
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	return result
}

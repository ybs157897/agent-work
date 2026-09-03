// supervisor.go — dsh web 网关进程守护（boujoy-harness 形态）。
//
// 两种形态：BaseURL 直连已运行网关（只做健康检查，不管理进程）；
// 或由本守护拉起长驻 `node --import tsx/esm <repo>/apps/cli/src/bin.ts
// web --host H --port P` 并在崩溃后按退避重启。会话连续性由 harness
// 原生保证（内存 miss → 磁盘 session.jsonl → agents.resume），网关重启
// 不丢上下文。control-plane 退出时 Close 回收进程组。
package dsh

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ybs/agent-team-workbench/internal/agentwork"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// Supervisor 管理一个网关进程（或只探活一个外部网关）。
type Supervisor struct {
	cfg GatewayConfig

	mu      sync.Mutex
	cmd     *exec.Cmd
	pgid    int // sampled immediately after start; the leader may be reaped before Close
	started bool
	stopped bool
	restart int
	cancel  context.CancelFunc
}

// NewSupervisor 按配置创建守护；Ensure 首次调用时才拉起进程。
func NewSupervisor(cfg GatewayConfig) *Supervisor {
	return &Supervisor{cfg: cfg}
}

// BaseURL 返回网关地址（Ensure 之前调用返回推导值）。
func (s *Supervisor) BaseURL() string {
	if s.cfg.BaseURL != "" {
		return s.cfg.BaseURL
	}
	host := s.cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%d", host, s.cfg.port())
}

// Ensure 网关可用则返回其地址：先探就绪（含他人启动的实例），不通且配置
// 允许拉起时 spawn 并等就绪。失败返回错误。
func (s *Supervisor) Ensure(ctx context.Context) (string, error) {
	base := s.BaseURL()
	client := newWireClient(base)
	// 已有就绪实例（外部启动或上次拉起未死）：直接复用。
	healthErr := client.ready(ctx)
	if healthErr == nil {
		return base, nil
	}
	if s.cfg.BaseURL != "" {
		return "", fmt.Errorf("dsh 网关不可达（%s）: %w", base, healthErr)
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return "", fmt.Errorf("dsh supervisor 已停止")
	}
	if !s.started {
		if err := s.spawnLocked(); err != nil {
			s.mu.Unlock()
			return "", err
		}
	}
	s.mu.Unlock()

	// 等就绪（冷启动含 cordis 插件树加载 + WS 面挂载，预留 45s）。
	waitCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := client.ready(waitCtx); err == nil {
			return base, nil
		}
		select {
		case <-waitCtx.Done():
			return "", fmt.Errorf("dsh 网关启动超时（%s）", base)
		case <-ticker.C:
		}
	}
}

// spawnLocked 启动网关进程并挂 monitor（调用方持锁）。端口被外部进程占用
// 时 Ensure 的探活分支已短路，这里只会因真正冲突失败。
func (s *Supervisor) spawnLocked() error {
	args := s.cfg.defaultArgs()
	if len(s.cfg.BinArgs) > 0 {
		args = s.cfg.BinArgs
	}
	cmd, err := runtime.TrustedCommand(s.cfg.nodeBin(), args...)
	if err != nil {
		return fmt.Errorf("拉起 dsh 网关失败: %w", err)
	}
	cmd.Dir = s.cfg.RepoDir
	cmd.Env = s.processEnv()
	cmd.Stdout = logWriter{prefix: "dsh-gateway"}
	cmd.Stderr = logWriter{prefix: "dsh-gateway:err"}
	setProcGroup(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("拉起 dsh 网关失败: %w", err)
	}
	s.cmd = cmd
	s.pgid = processGroupID(cmd)
	s.started = true
	monitorCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go s.monitor(monitorCtx, cmd)
	log.Printf("dsh supervisor: 网关已启动 pid=%d pgid=%d %s", cmd.Process.Pid, s.pgid, s.BaseURL())
	return nil
}

// monitor 进程退出后按退避重启（直到 Close）；外部 ctx 不参与生命周期。
func (s *Supervisor) monitor(ctx context.Context, cmd *exec.Cmd) {
	_ = cmd.Wait()
	select {
	case <-ctx.Done():
		return // 主动关停：不重启
	default:
	}
	s.mu.Lock()
	if s.stopped || s.cmd != cmd {
		s.mu.Unlock()
		return
	}
	s.restart++
	backoff := time.Duration(1<<min(s.restart, 5)) * time.Second // 2s..32s
	log.Printf("dsh supervisor: 网关退出，%v 后重启（第 %d 次）", backoff, s.restart)
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return
	case <-time.After(backoff):
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	if err := s.spawnLocked(); err != nil {
		log.Printf("dsh supervisor: 重启失败: %v", err)
	}
}

// Close 停止重启循环并回收进程组（SIGTERM→SIGKILL）。
func (s *Supervisor) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	if s.cancel != nil {
		s.cancel()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		signalGroup(s.cmd, s.pgid, sigTerm)
		done := make(chan struct{})
		go func() {
			_ = s.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			signalGroup(s.cmd, s.pgid, sigKill)
			<-done
		}
	}
}

// logWriter 把网关 stdout/stderr 限流转进标准日志。
type logWriter struct{ prefix string }

func (w logWriter) Write(p []byte) (int, error) {
	line := string(p)
	if len(line) > 512 {
		line = line[:512] + "…"
	}
	log.Printf("%s: %s", w.prefix, line)
	return len(p), nil
}

func (s *Supervisor) processEnv() []string {
	if strings.TrimSpace(s.cfg.Home) == "" {
		return os.Environ()
	}
	return agentwork.WithEnv(os.Environ(), "DSH_HOME", s.cfg.Home)
}

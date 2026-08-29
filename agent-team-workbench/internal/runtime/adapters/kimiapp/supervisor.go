package kimiapp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// endpoint 是 Ensure 产物：REST/WS 基址 + Bearer token。
type endpoint struct {
	BaseURL string
	Token   string
}

// Supervisor 守护 kap-server（`kimi web`）子进程：
//   - 直连模式（cfg.BaseURL 非空）：只做 healthz 探活，不拉进程；
//   - 受管模式：选端口（显式 Port 或内核分配空闲口）→ spawn（KIMI_CODE_HOME）
//     → healthz 轮询就绪 → 读 <home>/server.token → 崩溃退避重启（2s..32s）；
//   - Close 幂等：SIGTERM → 3s → SIGKILL，进程组级回收（pgid 启动时采样缓存）。
type Supervisor struct {
	cfg Config

	mu      sync.Mutex
	cmd     *exec.Cmd
	pgid    int // 启动后立即采样缓存（组长僵尸后 Getpgid 失败）
	port    int
	started bool
	stopped bool
	rests   int
}

func newSupervisor(cfg Config) *Supervisor {
	return &Supervisor{cfg: cfg}
}

// Ensure 返回可用 endpoint（幂等；已健康则复用，死亡则重启）。
func (s *Supervisor) Ensure(ctx context.Context) (*endpoint, error) {
	if s.cfg.BaseURL != "" {
		return s.ensureDirect(ctx)
	}
	return s.ensureSupervised(ctx)
}

func (s *Supervisor) ensureDirect(ctx context.Context) (*endpoint, error) {
	if strings.TrimSpace(s.cfg.Token) == "" {
		return nil, errors.New("kimiapp direct mode requires token")
	}
	client := newRestClient(s.cfg.BaseURL, strings.TrimSpace(s.cfg.Token))
	if kerr := client.healthz(ctx); kerr != nil {
		return nil, fmt.Errorf("kap-server healthz: %v", kerr)
	}
	return &endpoint{BaseURL: strings.TrimRight(s.cfg.BaseURL, "/"), Token: strings.TrimSpace(s.cfg.Token)}, nil
}

func (s *Supervisor) ensureSupervised(ctx context.Context) (*endpoint, error) {
	if strings.TrimSpace(s.cfg.Home) == "" {
		// 受管模式必填：避免误写用户的 ~/.kimi-code。
		return nil, errors.New("kimiapp supervised mode requires Home (KIMI_CODE_HOME)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return nil, errors.New("kimiapp supervisor closed")
	}
	host := s.cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	if s.port == 0 {
		if s.cfg.Port > 0 {
			s.port = s.cfg.Port
		} else {
			port, err := freePort(host)
			if err != nil {
				return nil, fmt.Errorf("pick free port: %w", err)
			}
			s.port = port
		}
	}
	base := fmt.Sprintf("http://%s:%d", host, s.port)
	// 复用：目标口已有健康实例（含用户手动起的 kimi web）。
	if client := newRestClient(base, ""); client.healthz(ctx) == nil {
		token, err := readTokenFile(s.cfg.Home)
		if err != nil {
			return nil, fmt.Errorf("kap-server healthy but token unavailable: %w", err)
		}
		return &endpoint{BaseURL: base, Token: token}, nil
	}
	if !s.started {
		if err := s.spawnLocked(ctx, host, base); err != nil {
			return nil, err
		}
	}
	token, err := s.waitReady(ctx, base)
	if err != nil {
		return nil, err
	}
	return &endpoint{BaseURL: base, Token: token}, nil
}

// spawnLocked 拉起 `kimi web`：BinArgs（测试回放桩）优先于默认参数；
// KIMI_CODE_HOME 指向受管目录；Setpgid 自立进程组并立即采样 pgid。
// 调用方持有 s.mu。
func (s *Supervisor) spawnLocked(ctx context.Context, host, base string) error {
	bin := s.cfg.KimiBin
	if bin == "" {
		bin = "kimi"
	}
	args := s.cfg.BinArgs
	if len(args) == 0 {
		args = []string{"web", "--host", host, "--port", strconv.Itoa(s.port), "--no-open"}
	}
	cmd, err := runtime.TrustedCommand(bin, args...)
	if err != nil {
		return fmt.Errorf("spawn %s: %w", bin, err)
	}
	cmd.Env = append(os.Environ(), "KIMI_CODE_HOME="+s.cfg.Home)
	cmd.Stdout = &logWriter{prefix: "kimiapp: kap-server:"}
	cmd.Stderr = &logWriter{prefix: "kimiapp: kap-server:"}
	setProcGroup(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn %s: %w", bin, err)
	}
	s.cmd = cmd
	s.pgid = processGroupID(cmd) // 启动时采样缓存；组长僵尸后不可再查
	s.started = true
	go s.monitor()
	log.Printf("kimiapp: kap-server 已拉起 pid=%d pgid=%d port=%d home=%s", cmd.Process.Pid, s.pgid, s.port, s.cfg.Home)
	return nil
}

// monitor 等待退出并按退避（2s<<rests，封顶 32s）重启；Close 置 stopped 后退出。
func (s *Supervisor) monitor() {
	for {
		err := s.waitProcess()
		s.mu.Lock()
		if s.stopped || s.cmd == nil {
			s.mu.Unlock()
			return
		}
		if err != nil {
			log.Printf("kimiapp: kap-server 异常退出: %v", err)
		}
		s.rests++
		backoff := 2 * time.Second << min(s.rests, 4)
		if backoff > 32*time.Second {
			backoff = 32 * time.Second
		}
		s.cmd = nil
		s.mu.Unlock()
		time.Sleep(backoff)
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			return
		}
		host := s.cfg.Host
		if host == "" {
			host = "127.0.0.1"
		}
		base := fmt.Sprintf("http://%s:%d", host, s.port)
		if err := s.spawnLocked(context.Background(), host, base); err != nil {
			log.Printf("kimiapp: kap-server 重启失败: %v", err)
			s.cmd = nil
		}
		s.mu.Unlock()
	}
}

// recycle 终止当前 kap-server 子进程；轮询 healthz 直至不可用再返回。
// 不在此调用 cmd.Wait()：monitor 协程负责回收，避免双重 wait 触发 "no child processes"。
func (s *Supervisor) recycle() {
	s.mu.Lock()
	if s.stopped || s.cmd == nil {
		s.started = false
		s.mu.Unlock()
		return
	}
	cmd, pgid := s.cmd, s.pgid
	host := s.cfg.Host
	port := s.port
	s.cmd = nil
	s.started = false
	s.mu.Unlock()
	signalGroup(cmd, pgid, sigTerm)
	if host == "" {
		host = "127.0.0.1"
	}
	base := fmt.Sprintf("http://%s:%d", host, port)
	probe := newRestClient(base, "")
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if probe.healthz(context.Background()) != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// waitProcess 等待当前子进程退出（快照后释放锁等待）。
func (s *Supervisor) waitProcess() error {
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd == nil {
		return nil
	}
	return cmd.Wait()
}

// waitReady 轮询 healthz（250ms 步进，45s 上限）→ 就绪后读 token 文件
// （token 由服务端首启生成；healthz 通过即应存在，仍留 5s 容忍窗口）。
func (s *Supervisor) waitReady(ctx context.Context, base string) (string, error) {
	probe := newRestClient(base, "")
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if probe.healthz(ctx) == nil {
			tokenDeadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(tokenDeadline) {
				if token, err := readTokenFile(s.cfg.Home); err == nil {
					return token, nil
				}
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(100 * time.Millisecond):
				}
			}
			return "", fmt.Errorf("token file not found under %s", s.cfg.Home)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return "", errors.New("kap-server not ready within 45s")
}

// Close 幂等回收：TERM → 3s → KILL（进程组）。
func (s *Supervisor) Close() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	cmd, pgid := s.cmd, s.pgid
	s.mu.Unlock()
	if cmd == nil {
		return
	}
	signalGroup(cmd, pgid, sigTerm)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		signalGroup(cmd, pgid, sigKill)
	}
}

func freePort(host string) (int, error) {
	if host == "" {
		host = "127.0.0.1"
	}
	l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// readTokenFile 读 <home>/server.token（43 字符 base64url，服务端首启生成）。
func readTokenFile(home string) (string, error) {
	b, err := os.ReadFile(home + string(os.PathSeparator) + "server.token")
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", errors.New("empty token file")
	}
	return token, nil
}

// logWriter 逐行转发子进程输出（单行截断 512 字节），防日志爆量。
// kap-server 就绪横幅会打印带 #token= 的 Local URL；脱敏后避免 IDE/终端自动打开浏览器。
type logWriter struct {
	prefix string
	buf    []byte
}

var kapLogURLPattern = regexp.MustCompile(`https?://[^\s]+`)

func sanitizeKapLogLine(line string) string {
	line = kapLogURLPattern.ReplaceAllString(line, "[kap-server]")
	if i := strings.Index(line, "Token:"); i >= 0 {
		return line[:i+len("Token:")] + " [redacted]"
	}
	return line
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		idx := bytes.IndexByte(w.buf, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimRight(string(w.buf[:idx]), "\r")
		w.buf = w.buf[idx+1:]
		if strings.TrimSpace(line) != "" {
			line = sanitizeKapLogLine(line)
			if len(line) > 512 {
				line = line[:512] + "…"
			}
			log.Printf("%s %s", w.prefix, line)
		}
	}
	if len(w.buf) > 8<<10 { // 无换行的超长行直接丢弃
		w.buf = w.buf[len(w.buf)-512:]
	}
	return len(p), nil
}

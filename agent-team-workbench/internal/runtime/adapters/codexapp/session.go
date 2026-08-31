package codexapp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/agentwork"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

const defaultSessionTimeout = 8 * time.Second

type appServerSession struct {
	ctx     context.Context
	stdin   io.WriteCloser
	frames  <-chan *rpcFrame
	readErr <-chan error
	close   func()
}

func (m *Module) openAppServer(ctx context.Context) (*appServerSession, error) {
	if _, err := exec.LookPath(m.cfg.BinPath); err != nil {
		if _, statErr := os.Stat(m.cfg.BinPath); statErr != nil {
			return nil, fmt.Errorf("codex 不可用: %s", m.cfg.BinPath)
		}
	}
	cancel := func() {}
	if _, ok := ctx.Deadline(); !ok {
		ctx, cancel = context.WithTimeout(ctx, defaultSessionTimeout)
	}

	cmd, err := runtime.TrustedCommand(m.cfg.BinPath, m.commandArgs(runtime.ModelSnapshot{})...)
	if err != nil {
		cancel()
		return nil, err
	}
	// probe 路径无 Run 上下文：app-server 进程 cwd 不承载执行语义，回退进程 cwd。
	cmd.Dir = ""
	cmd.Env = m.processEnv()
	setProcGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	pgid := processGroupID(cmd)
	go io.Copy(io.Discard, stderr)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	// 超时终止由看门狗承担：ctx 到点向整个进程组发 SIGKILL
	// （CommandContext 默认只杀单进程，会留下组内孤儿）。
	// 退出走独立 watchdog 通道：done 的唯一消费者是 closeFn。
	watchdog := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			signalGroup(cmd, pgid, sigKill)
		case <-watchdog:
		}
	}()

	reader := bufio.NewReaderSize(stdout, 64*1024)
	frames := make(chan *rpcFrame)
	readErr := make(chan error, 1)
	go func() {
		defer close(frames)
		for {
			frame, err := readFrame(reader, m.cfg.MaxFrameBytes)
			if err != nil {
				readErr <- err
				return
			}
			if frame != nil {
				frames <- frame
			}
		}
	}()

	closeFn := func() {
		close(watchdog)
		signalGroup(cmd, pgid, sigTerm)
		select {
		case <-done:
		case <-time.After(time.Second):
			signalGroup(cmd, pgid, sigKill)
			<-done
		}
		cancel()
	}

	return &appServerSession{
		ctx: ctx, stdin: stdin, frames: frames, readErr: readErr, close: closeFn,
	}, nil
}

func (s *appServerSession) request(id int64, method string, params map[string]any) (*rpcFrame, error) {
	frame := map[string]any{"id": id, "method": method}
	if params != nil {
		frame["params"] = params
	}
	if err := writeJSONFrame(s.stdin, frame); err != nil {
		return nil, err
	}
	return waitResponse(s.ctx, s.frames, s.readErr, id)
}

func (s *appServerSession) handshake() (providerVersion string, err error) {
	initFrame, err := s.request(1, "initialize", initializeParams())
	if err != nil {
		return "", fmt.Errorf("initialize 失败: %w", err)
	}
	var initResult struct {
		UserAgent string `json:"userAgent"`
	}
	_ = json.Unmarshal(initFrame.Result, &initResult)
	if err := writeJSONFrame(s.stdin, initializedNotification()); err != nil {
		return "", err
	}
	return initResult.UserAgent, nil
}

func waitResponse(ctx context.Context, frames <-chan *rpcFrame, readErr <-chan error, id int64) (*rpcFrame, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-readErr:
			return nil, err
		case frame, ok := <-frames:
			if !ok {
				return nil, io.ErrUnexpectedEOF
			}
			if frame.ID == nil || *frame.ID != id {
				continue
			}
			if frame.Error != nil {
				return nil, fmt.Errorf("rpc %d: %s", frame.Error.Code, frame.Error.Message)
			}
			return frame, nil
		}
	}
}

func writeJSONFrame(w io.Writer, frame map[string]any) error {
	b, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func (m *Module) commandArgs(model runtime.ModelSnapshot) []string {
	if len(m.cfg.Args) > 0 {
		return append([]string(nil), m.cfg.Args...)
	}
	args := []string{"app-server", "--stdio", "--enable", "multi_agent"}
	provider := strings.ToLower(strings.TrimSpace(model.Provider))
	if provider == "" || provider == "codex" || provider == "openai" {
		return append(args, "--enable", "multi_agent_v2")
	}
	// v2 子线程使用 OpenAI/Codex 的 agent_message Responses 扩展；第三方
	// Responses-compatible provider 只承诺标准输入项，使用稳定 multi_agent。
	return append(args, "--disable", "multi_agent_v2")
}

func (m *Module) processEnv() []string {
	if strings.TrimSpace(m.cfg.Home) == "" {
		return os.Environ()
	}
	return agentwork.WithEnv(os.Environ(), "CODEX_HOME", m.cfg.Home)
}

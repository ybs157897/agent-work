package codexapp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/ybs/agent-team-workbench/internal/runtime"
)

const defaultProbeTimeout = 8 * time.Second

// Probe 真实执行 initialize/initialized、account/read 与 model/list。
// 二进制存在但协议漂移、未登录或没有模型时必须报告 unavailable。
func (m *Module) Probe(ctx context.Context, req runtime.ProbeRequest) (runtime.ProbeResult, error) {
	manifest, _ := m.Manifest(ctx)
	if _, err := exec.LookPath(m.cfg.BinPath); err != nil {
		if _, statErr := os.Stat(m.cfg.BinPath); statErr != nil {
			return runtime.ProbeResult{OK: false, Manifest: &manifest, Error: "codex 不可用: " + m.cfg.BinPath}, nil
		}
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultProbeTimeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, m.cfg.BinPath, m.commandArgs()...)
	cmd.Dir = m.cfg.WorkspaceRoot
	cmd.Env = os.Environ()
	setProcGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return runtime.ProbeResult{OK: false, Manifest: &manifest, Error: err.Error()}, nil
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return runtime.ProbeResult{OK: false, Manifest: &manifest, Error: err.Error()}, nil
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return runtime.ProbeResult{OK: false, Manifest: &manifest, Error: err.Error()}, nil
	}
	if err := cmd.Start(); err != nil {
		return runtime.ProbeResult{OK: false, Manifest: &manifest, Error: err.Error()}, nil
	}
	pgid := processGroupID(cmd)
	go io.Copy(io.Discard, stderr)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		signalGroup(cmd, pgid, sigTerm)
		select {
		case <-done:
		case <-time.After(time.Second):
			signalGroup(cmd, pgid, sigKill)
			<-done
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

	if err := writeJSONFrame(stdin, map[string]any{"id": int64(1), "method": "initialize", "params": initializeParams()}); err != nil {
		return runtime.ProbeResult{OK: false, Manifest: &manifest, Error: err.Error()}, nil
	}
	initFrame, err := waitResponse(ctx, frames, readErr, 1)
	if err != nil {
		return runtime.ProbeResult{OK: false, Manifest: &manifest, Error: "initialize 失败: " + err.Error()}, nil
	}
	var initResult struct {
		UserAgent string `json:"userAgent"`
	}
	_ = json.Unmarshal(initFrame.Result, &initResult)
	manifest.ProviderVersion = initResult.UserAgent
	m.setProviderVersion(initResult.UserAgent)

	if err := writeJSONFrame(stdin, initializedNotification()); err != nil {
		return runtime.ProbeResult{OK: false, Manifest: &manifest, Error: err.Error()}, nil
	}
	if err := writeJSONFrame(stdin, map[string]any{"id": int64(2), "method": "account/read", "params": map[string]any{"refreshToken": false}}); err != nil {
		return runtime.ProbeResult{OK: false, Manifest: &manifest, Error: err.Error()}, nil
	}
	accountFrame, err := waitResponse(ctx, frames, readErr, 2)
	if err != nil {
		return runtime.ProbeResult{OK: false, Manifest: &manifest, Error: "account/read 失败: " + err.Error()}, nil
	}
	var accountResult struct {
		Account            any  `json:"account"`
		RequiresOpenAIAuth bool `json:"requiresOpenaiAuth"`
	}
	_ = json.Unmarshal(accountFrame.Result, &accountResult)
	if accountResult.RequiresOpenAIAuth && accountResult.Account == nil {
		return runtime.ProbeResult{OK: false, Manifest: &manifest, Error: "Codex 尚未登录，请先运行 codex login"}, nil
	}

	if err := writeJSONFrame(stdin, map[string]any{"id": int64(3), "method": "model/list", "params": map[string]any{"limit": 20, "includeHidden": false}}); err != nil {
		return runtime.ProbeResult{OK: false, Manifest: &manifest, Error: err.Error()}, nil
	}
	modelsFrame, err := waitResponse(ctx, frames, readErr, 3)
	if err != nil {
		return runtime.ProbeResult{OK: false, Manifest: &manifest, Error: "model/list 失败: " + err.Error()}, nil
	}
	var modelsResult struct {
		Data []json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(modelsFrame.Result, &modelsResult)
	if len(modelsResult.Data) == 0 {
		return runtime.ProbeResult{OK: false, Manifest: &manifest, Error: "Codex 未返回可用模型"}, nil
	}
	return runtime.ProbeResult{OK: true, Manifest: &manifest}, nil
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

func (m *Module) commandArgs() []string {
	if len(m.cfg.Args) > 0 {
		return append([]string(nil), m.cfg.Args...)
	}
	return []string{"app-server", "--stdio"}
}

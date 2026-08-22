// Package mock 提供 M1 的 Mock Adapter：无真实 Runtime 也能完整演示
// queued → starting → running → waiting_approval → succeeding → succeeded
// 以及 interrupt/cancel/retry 全状态流转（协议文档 §12 M1 退出门）。
package mock

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// Engine 是 Mock Adapter 依赖的应用层能力（避免循环依赖）。
type Engine interface {
	RecordRunStatus(ctx context.Context, runID string, to domain.RunStatus, data map[string]any) error
	RecordRunProgress(ctx context.Context, runID string, progress float64) error
	RecordRunEvent(ctx context.Context, runID, evType string, data map[string]any) error
	RequestApproval(ctx context.Context, runID, kind, risk, summary string) (*domain.ApprovalRequest, error)
	Approvals(ctx context.Context, runID string) ([]*domain.ApprovalRequest, error)
	RecordArtifact(ctx context.Context, runID string, art *domain.Artifact) error
	Run(ctx context.Context, id string) (*domain.ExecutionRun, error)
}

var _ Engine = (*application.Service)(nil)

// Adapter 同时实现 RuntimeAdapter SPI 与 application.Dispatcher。
type Adapter struct {
	engine Engine
	step   time.Duration

	mu      sync.Mutex
	cancels map[string]chan struct{}
}

var _ runtime.RuntimeAdapter = (*Adapter)(nil)
var _ application.Dispatcher = (*Adapter)(nil)

func New(engine Engine) *Adapter {
	return &Adapter{engine: engine, step: 1200 * time.Millisecond, cancels: make(map[string]chan struct{})}
}

func (a *Adapter) Manifest(ctx context.Context) (runtime.AdapterManifest, error) {
	return runtime.AdapterManifest{
		AdapterID: "mock", AdapterVersion: "1.0.0", ProviderVersion: "simulated",
		Protocol: runtime.Protocol{Name: "mock", Version: "1"},
		Capabilities: map[string]runtime.CapabilityLevel{
			"streaming": runtime.CapSupported, "resume": runtime.CapUnavailable,
			"interrupt": runtime.CapSupported, "approval": runtime.CapSupported,
			"workspace_files": runtime.CapSupported, "terminal": runtime.CapUnavailable,
			"structured_output": runtime.CapAdapterTranslated,
		},
		SchemaDigest: "sha256:mock",
	}, nil
}

func (a *Adapter) Probe(ctx context.Context, req runtime.ProbeRequest) (runtime.ProbeResult, error) {
	m, _ := a.Manifest(ctx)
	return runtime.ProbeResult{OK: true, Manifest: &m}, nil
}

func (a *Adapter) Start(ctx context.Context, req runtime.StartRequest) (runtime.RuntimeHandle, error) {
	a.Dispatch(ctx, req.Run)
	return &handle{runID: req.Run.ID, cancel: a.cancelCh(req.Run.ID)}, nil
}

// Dispatch 在权威写入成功后被调用；goroutine 模拟执行过程。
func (a *Adapter) Dispatch(ctx context.Context, run *domain.ExecutionRun) error {
	stop := make(chan struct{})
	a.mu.Lock()
	a.cancels[run.ID] = stop
	a.mu.Unlock()
	go a.simulate(run.ID, instructionOf(run))
	return nil
}

func (a *Adapter) cancelCh(runID string) chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ch, ok := a.cancels[runID]; ok {
		return ch
	}
	ch := make(chan struct{})
	a.cancels[runID] = ch
	return ch
}

func instructionOf(run *domain.ExecutionRun) string {
	if v, ok := run.Input["instruction"].(string); ok {
		return v
	}
	return ""
}

// simulate 按步骤推进 Run；每步前检查外部控制命令（cancelling/interrupting）。
func (a *Adapter) simulate(runID, instruction string) {
	ctx := context.Background()
	defer func() {
		a.mu.Lock()
		delete(a.cancels, runID)
		a.mu.Unlock()
	}()

	steps := []struct {
		status   domain.RunStatus // 空值表示不迁移状态，仅上报消息与进度
		progress float64
		message  string
	}{
		{domain.RunStarting, 0, "runtime 正在初始化"},
		{domain.RunRunning, 0.2, "开始分析任务要求"},
		{"", 0.55, "正在生成实现方案"},
	}
	for _, s := range steps {
		if !a.wait(runID) {
			return
		}
		if s.status != "" {
			if err := a.engine.RecordRunStatus(ctx, runID, s.status, nil); err != nil {
				return
			}
		}
		if err := a.engine.RecordRunEvent(ctx, runID, domain.EventMessageDelta,
			map[string]any{"role": "assistant", "text": s.message}); err != nil {
			return
		}
		if err := a.engine.RecordRunProgress(ctx, runID, s.progress); err != nil {
			return
		}
	}

	// 指令中提到 approval / 审批 时模拟一次高风险审批。
	if strings.Contains(instruction, "approval") || strings.Contains(instruction, "审批") {
		approval, err := a.engine.RequestApproval(ctx, runID, "shell", "high",
			"准备执行模拟发布命令（Mock）")
		if err != nil {
			return
		}
		if !a.waitForApproval(ctx, runID, approval.ID) {
			return
		}
	}

	if !a.wait(runID) {
		return
	}
	if err := a.engine.RecordRunEvent(ctx, runID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": "任务执行完成，产物已生成"}); err != nil {
		return
	}
	if err := a.engine.RecordArtifact(ctx, runID, &domain.Artifact{
		LogicalPath: "output/result.md", Mime: "text/markdown",
		Size: 2048, Sha256: strings.Repeat("a", 64), Classification: "internal",
	}); err != nil {
		return
	}
	if err := a.engine.RecordRunProgress(ctx, runID, 1.0); err != nil {
		return
	}
	if !a.wait(runID) {
		return
	}
	if err := a.engine.RecordRunStatus(ctx, runID, domain.RunSucceeding, nil); err != nil {
		return
	}
	a.wait(runID)
	_ = a.engine.RecordRunStatus(ctx, runID, domain.RunSucceeded, nil)
}

// wait 休眠一步；期间检测到 cancelling/interrupting 则确认终态并终止。
func (a *Adapter) wait(runID string) bool {
	ctx := context.Background()
	deadline := time.Now().Add(a.step)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		run, err := a.engine.Run(ctx, runID)
		if err != nil {
			return false
		}
		switch run.Status {
		case domain.RunCancelling:
			_ = a.engine.RecordRunStatus(ctx, runID, domain.RunCancelled, nil)
			return false
		case domain.RunInterrupting:
			_ = a.engine.RecordRunStatus(ctx, runID, domain.RunInterrupted, nil)
			return false
		case domain.RunCancelled, domain.RunInterrupted, domain.RunFailed, domain.RunLost:
			return false
		}
	}
	return true
}

// waitForApproval 轮询审批决定；拒绝时按策略进入 cancelling → cancelled。
func (a *Adapter) waitForApproval(ctx context.Context, runID, approvalID string) bool {
	for {
		time.Sleep(200 * time.Millisecond)
		run, err := a.engine.Run(ctx, runID)
		if err != nil {
			return false
		}
		switch run.Status {
		case domain.RunWaitingApproval:
			// 继续等待
		case domain.RunCancelling:
			_ = a.engine.RecordRunStatus(ctx, runID, domain.RunCancelled, nil)
			return false
		case domain.RunInterrupting:
			_ = a.engine.RecordRunStatus(ctx, runID, domain.RunInterrupted, nil)
			return false
		case domain.RunRunning:
			return true
		default:
			return false
		}
	}
}

type handle struct {
	runID  string
	cancel chan struct{}
}

func (h *handle) SessionRef() string                                 { return "mock://" + h.runID }
func (h *handle) Send(ctx context.Context, instruction string) error { return nil }
func (h *handle) Interrupt(ctx context.Context) error                { return nil }
func (h *handle) Cancel(ctx context.Context) error                   { return nil }
func (h *handle) ResolveApproval(ctx context.Context, approvalID string, approved bool) error {
	return nil
}
func (h *handle) Close(ctx context.Context) error { return nil }

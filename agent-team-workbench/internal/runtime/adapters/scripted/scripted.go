// Package scripted 是第二个 Adapter 形态：录制回放（fixture replay）。
// 真实 Provider（Codex/Kimi/DSH）接入时先脱敏录制 JSONL，再由本模式回放做
// schema drift 与 conformance 验证（协议文档 §9.2 / 附录 C）。
package scripted

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

//go:embed fixture.jsonl
var defaultFixture []byte

// Engine 与 mock.Adapter 相同的应用层能力面。
type Engine interface {
	RecordRunStatus(ctx context.Context, runID string, to domain.RunStatus, data map[string]any) error
	RecordRunProgress(ctx context.Context, runID string, progress float64) error
	RecordRunEvent(ctx context.Context, runID, evType string, data map[string]any) error
	RecordArtifact(ctx context.Context, runID string, art *domain.Artifact) error
	Run(ctx context.Context, id string) (*domain.ExecutionRun, error)
}

// Step 是 fixture 中一行：canonical 事件 + 延迟。
type Step struct {
	Kind    string         `json:"kind"`
	Data    map[string]any `json:"data,omitempty"`
	DelayMS int            `json:"delay_ms,omitempty"`
}

type Adapter struct {
	engine Engine
	steps  []Step
}

var _ runtime.RuntimeAdapter = (*Adapter)(nil)

func New(engine Engine) *Adapter {
	a := &Adapter{engine: engine}
	a.loadFixture(defaultFixture)
	return a
}

func (a *Adapter) loadFixture(raw []byte) {
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var s Step
		if err := json.Unmarshal(sc.Bytes(), &s); err == nil {
			a.steps = append(a.steps, s)
		}
	}
}

func (a *Adapter) Manifest(ctx context.Context) (runtime.AdapterManifest, error) {
	return runtime.AdapterManifest{
		AdapterID: "scripted", AdapterVersion: "1.0.0", ProviderVersion: "fixture-v1",
		Protocol: runtime.Protocol{Name: "fixture-replay", Version: "1"},
		Capabilities: map[string]runtime.CapabilityLevel{
			"streaming": runtime.CapSupported, "resume": runtime.CapUnavailable,
			"interrupt": runtime.CapSupported, "approval": runtime.CapUnavailable,
			"workspace_files": runtime.CapSupported, "terminal": runtime.CapUnavailable,
			"structured_output": runtime.CapSupported,
		},
		SchemaDigest: "sha256:scripted-fixture",
	}, nil
}

func (a *Adapter) Probe(ctx context.Context, req runtime.ProbeRequest) (runtime.ProbeResult, error) {
	m, _ := a.Manifest(ctx)
	if len(a.steps) == 0 {
		return runtime.ProbeResult{OK: false, Error: "fixture 为空"}, nil
	}
	return runtime.ProbeResult{OK: true, Manifest: &m}, nil
}

func (a *Adapter) Start(ctx context.Context, req runtime.StartRequest) (runtime.RuntimeHandle, error) {
	a.Dispatch(ctx, req.Run)
	return noopHandle{runID: req.Run.ID}, nil
}

// Dispatch 按 fixture 顺序回放 canonical 事件。
func (a *Adapter) Dispatch(ctx context.Context, run *domain.ExecutionRun) error {
	go a.replay(run.ID)
	return nil
}

func (a *Adapter) replay(runID string) {
	ctx := context.Background()
	for _, s := range a.steps {
		if s.DelayMS > 0 {
			time.Sleep(time.Duration(s.DelayMS) * time.Millisecond)
		}
		// 外部控制（cancelling/interrupting）优先于回放推进。
		if run, err := a.engine.Run(ctx, runID); err == nil {
			switch run.Status {
			case domain.RunCancelling:
				_ = a.engine.RecordRunStatus(ctx, runID, domain.RunCancelled, nil)
				return
			case domain.RunInterrupting:
				_ = a.engine.RecordRunStatus(ctx, runID, domain.RunInterrupted, nil)
				return
			case domain.RunCancelled, domain.RunInterrupted, domain.RunFailed, domain.RunLost:
				return
			}
		}
		switch s.Kind {
		case "run.status_changed":
			status := domain.RunStatus(strOf(s.Data, "status"))
			if status != "" {
				_ = a.engine.RecordRunStatus(ctx, runID, status, s.Data)
			}
		case "run.progress":
			if v, ok := s.Data["progress"].(float64); ok {
				_ = a.engine.RecordRunProgress(ctx, runID, v)
			}
		default:
			if domain.IsKnownEventName(s.Kind) {
				_ = a.engine.RecordRunEvent(ctx, runID, s.Kind, s.Data)
			}
		}
	}
}

func strOf(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

type noopHandle struct{ runID string }

func (h noopHandle) SessionRef() string                                 { return "scripted://" + h.runID }
func (h noopHandle) Send(ctx context.Context, instruction string) error { return nil }
func (h noopHandle) Interrupt(ctx context.Context) error                { return nil }
func (h noopHandle) Cancel(ctx context.Context) error                   { return nil }
func (h noopHandle) ResolveApproval(ctx context.Context, approvalID string, approved bool) error {
	return nil
}
func (h noopHandle) Close(ctx context.Context) error { return nil }

package zcode

import (
	"context"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

func TestManifestAllUnavailable(t *testing.T) {
	m := New()
	mf, err := m.Manifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mf.AdapterID != "zcode-probe" || mf.Protocol.Name != "zcode" {
		t.Fatalf("manifest 标识错误: %+v", mf)
	}
	for _, cap := range []string{
		"streaming", "resume", "interrupt", "approval",
		"workspace_files", "terminal", "structured_output",
	} {
		if got := mf.Capabilities[cap]; got != atwruntime.CapUnavailable {
			t.Errorf("能力 %s = %s，probe-only 桩必须 unavailable", cap, got)
		}
	}
}

func TestProbeReportsUnverified(t *testing.T) {
	m := New()
	res, err := m.Probe(context.Background(), atwruntime.ProbeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || res.Error == "" || res.Manifest == nil {
		t.Fatalf("probe 应报告未核验: %+v", res)
	}
}

// Execute 必须显式失败（config/start_unsupported），不产生任何副作用。
func TestExecuteAlwaysFailsClosed(t *testing.T) {
	m := New()
	run := &domain.ExecutionRun{
		ID: domain.NewID(domain.PrefixRun), Status: domain.RunQueued, Version: 1,
		AdapterID: "zcode-probe", Input: map[string]any{"instruction": "x"},
	}
	ex := &atwruntime.ExecContext{
		Ctx: context.Background(), Run: run, Instruction: "x",
		Callbacks: &noopCallbacks{}, // 不会被调用
		Controls:  make(chan atwruntime.Control, 1),
	}
	res := m.Execute(ex)
	if res.Outcome != atwruntime.OutcomeFailed || res.Failure == nil {
		t.Fatalf("Execute 应显式失败: %+v", res)
	}
	if res.Failure.Family != atwruntime.FamilyConfig || res.Failure.Code != "start_unsupported" ||
		res.Failure.Retryable {
		t.Fatalf("失败形态错误: %+v", res.Failure)
	}
}

type noopCallbacks struct{}

func (c *noopCallbacks) OnEvent(eventType string, data map[string]any) {}
func (c *noopCallbacks) OnProgress(progress float64)                   {}
func (c *noopCallbacks) OnLog(stream, line string)                     {}
func (c *noopCallbacks) OnSpawn(pid, processGroupID int)               {}
func (c *noopCallbacks) OnUsage(u atwruntime.Usage)                    {}
func (c *noopCallbacks) OnSession(update atwruntime.SessionUpdate)     {}
func (c *noopCallbacks) RequestApproval(kind, risk, summary string) string {
	return ""
}

package runnergateway

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// ── 分派：run.offer ──────────────────────────────────────────────────

// Available 判断是否有在线 Runner 能承接该 adapter 的 Run。
func (g *Gateway) Available(adapterID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, rc := range g.conns {
		if rc.hasAdapter(adapterID) {
			return true
		}
	}
	return false
}

func (rc *runnerConn) hasAdapter(adapterID string) bool {
	for _, a := range rc.adapters {
		if a == adapterID {
			return true
		}
	}
	return false
}

// Dispatch 创建租约并下发 run.offer；权威状态已在前置事务写入（无幽灵任务）。
func (g *Gateway) Dispatch(ctx context.Context, run *domain.ExecutionRun, adapterID string) error {
	g.mu.Lock()
	var target *runnerConn
	for _, rc := range g.conns {
		if rc.hasAdapter(adapterID) {
			target = rc
			break
		}
	}
	g.mu.Unlock()
	if target == nil {
		return domain.ErrNotFound
	}

	lease := &application.RunLease{
		LeaseID: domain.NewID(domain.PrefixLease), RunID: run.ID,
		RunnerID: target.runnerID, RenewedUntil: time.Now().UTC().Add(leaseTTL),
	}
	if err := g.store.Runners().CreateLease(ctx, lease); err != nil {
		return err
	}

	spec, _ := json.Marshal(map[string]any{
		"run_id":          run.ID,
		"adapter_id":      adapterID,
		"workspace_alias": "default", // Runner 本地解析为授权根目录；禁止宿主绝对路径
		"input":           run.Input,
		"policy": map[string]any{
			"sandbox": "workspace-scoped", "network_policy": "egress-allowed",
			"approval_policy": "required_for_high_risk",
		},
	})
	payload, _ := json.Marshal(map[string]any{
		"lease_id": lease.LeaseID, "fencing_token": lease.FencingToken, "run_spec": json.RawMessage(spec),
	})
	target.sendEnvelope(Envelope{
		V: 1, MessageID: domain.NewID("msg_"), Kind: "request", Method: "run.offer",
		RunnerID: target.runnerID, RunID: run.ID, SentAt: time.Now().UTC(), Payload: payload,
	})

	g.mu.Lock()
	target.activeRuns[run.ID] = lease.LeaseID
	g.mu.Unlock()
	return nil
}

// ForwardApproval 把审批决定作为 run.command 下发（协议文档 §7.1）。
func (g *Gateway) ForwardApproval(ctx context.Context, runID, approvalID string, approved bool) {
	g.forwardCommand(runID, "approval.resolve", map[string]any{
		"approval_id": approvalID, "approved": approved,
	})
}

// ForwardControl 转发 interrupt / cancel 命令；终态由 Runner 确认后回报。
func (g *Gateway) ForwardControl(ctx context.Context, runID, action string) {
	g.forwardCommand(runID, action, nil)
}

func (g *Gateway) forwardCommand(runID, command string, body map[string]any) {
	g.mu.Lock()
	var target *runnerConn
	for _, rc := range g.conns {
		if _, ok := rc.activeRuns[runID]; ok {
			target = rc
			break
		}
	}
	g.mu.Unlock()
	if target == nil {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"command_id": domain.NewID("cmd_"), "command": command, "body": body,
	})
	target.sendEnvelope(Envelope{
		V: 1, MessageID: domain.NewID("msg_"), Kind: "request", Method: "run.command",
		RunnerID: target.runnerID, RunID: runID, SentAt: time.Now().UTC(), Payload: payload,
	})
}

// ── 事件入口：run.accept / run.event / artifact.manifest ─────────────

func (g *Gateway) handleMessage(rc *runnerConn, env Envelope) {
	ctx := context.Background()
	switch env.Method {
	case "heartbeat", "ack":
		_ = g.store.Runners().SetStatus(ctx, rc.runnerID, "connected", time.Now().UTC())
	case "run.accept":
		// 接受后才准备 Workspace；状态迁移由后续 run.event 驱动。
		log.Printf("runnergateway: %s 接受 run %s", rc.runnerID, env.RunID)
	case "run.reject":
		g.handleReject(ctx, rc, env)
	case "run.event":
		g.handleRunEvent(ctx, rc, env)
	default:
		log.Printf("runnergateway: 忽略未知方法 %s", env.Method)
	}
}

func (g *Gateway) handleReject(ctx context.Context, rc *runnerConn, env Envelope) {
	g.mu.Lock()
	delete(rc.activeRuns, env.RunID)
	g.mu.Unlock()
	// 容量/能力校验失败：回到 queued 等待重新调度。
	if err := g.engine.RecordRunStatus(ctx, env.RunID, domain.RunFailed, nil); err != nil {
		log.Printf("runnergateway: run %s reject 处理失败: %v", env.RunID, err)
	}
}

// handleRunEvent：runner_seq 去重 → fencing 校验 → 映射到引擎。
func (g *Gateway) handleRunEvent(ctx context.Context, rc *runnerConn, env Envelope) {
	var p struct {
		RunnerSeq int64 `json:"runner_seq"`
		Event     struct {
			Kind string         `json:"kind"`
			Data map[string]any `json:"data"`
		} `json:"event"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return
	}
	runID := env.RunID

	// 去重：重连重发的事件不重复执行（至少一次投递 → 幂等消费）。
	if err := g.store.Runners().RunnerEventDedup(ctx, runID, rc.runnerID, p.RunnerSeq); err == domain.ErrIdempotencyConflict {
		g.ack(rc, runID, p.RunnerSeq)
		return
	}

	// Fencing：活跃 lease 必须属于该 Runner，旧连接恢复不能继续写入。
	if lease, err := g.store.Runners().ActiveLease(ctx, runID); err == nil {
		if lease.Released || lease.RunnerID != rc.runnerID {
			log.Printf("runnergateway: run %s 拒绝过期 runner %s 的事件（fencing）", runID, rc.runnerID)
			return
		}
	}

	g.applyEvent(ctx, runID, p.Event.Kind, p.Event.Data)
	g.ack(rc, runID, p.RunnerSeq)
}

// applyEvent 把 canonical candidate 事件映射到应用引擎。
func (g *Gateway) applyEvent(ctx context.Context, runID, kind string, data map[string]any) {
	switch kind {
	case "run.status_changed":
		status := domain.RunStatus(str(data, "status"))
		if status == "" {
			return
		}
		if err := g.engine.RecordRunStatus(ctx, runID, status, data); err != nil {
			log.Printf("runnergateway: run %s 状态 %s 记录失败: %v", runID, status, err)
		}
		g.finalizeIfTerminal(runID)
	case "run.progress":
		if v, ok := data["progress"].(float64); ok {
			_ = g.engine.RecordRunProgress(ctx, runID, v)
		}
	case "approval.requested":
		a, err := g.engine.RequestApproval(ctx, runID, str(data, "kind"), str(data, "risk"), str(data, "summary"))
		if err != nil {
			log.Printf("runnergateway: run %s 审批请求失败: %v", runID, err)
		} else {
			g.mu.Lock()
			g.serverApprovals[runID] = a.ID
			g.mu.Unlock()
		}
	case "artifact.manifest":
		art := &domain.Artifact{
			LogicalPath: str(data, "logical_path"), Mime: str(data, "mime"),
			Size: int64(num(data, "size")), Sha256: str(data, "sha256"),
			Classification: "internal",
		}
		if err := g.engine.RecordArtifact(ctx, runID, art); err != nil {
			log.Printf("runnergateway: run %s 产物记录失败: %v", runID, err)
		}
	default:
		// message.* / tool.*：按白名单投影。
		if domain.IsKnownEventName(kind) {
			_ = g.engine.RecordRunEvent(ctx, runID, kind, data)
		}
	}
}

// finalizeIfTerminal：Run 终态后释放租约并摘除活动记录。
func (g *Gateway) finalizeIfTerminal(runID string) {
	ctx := context.Background()
	run, err := g.engine.Run(ctx, runID)
	if err != nil || !run.Status.IsTerminal() {
		return
	}
	if lease, err := g.store.Runners().ActiveLease(ctx, runID); err == nil {
		_ = g.store.Runners().ReleaseLease(ctx, lease.LeaseID, time.Now().UTC())
	}
	g.mu.Lock()
	for _, rc := range g.conns {
		delete(rc.activeRuns, runID)
	}
	delete(g.serverApprovals, runID)
	g.mu.Unlock()
}

func (g *Gateway) ack(rc *runnerConn, runID string, seq int64) {
	payload, _ := json.Marshal(map[string]any{"contiguous_seq": seq, "backpressure": "none"})
	rc.sendEnvelope(Envelope{
		V: 1, MessageID: domain.NewID("msg_"), Kind: "ack", Method: "ack",
		RunnerID: rc.runnerID, RunID: runID, SentAt: time.Now().UTC(), Payload: payload,
	})
}

func str(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func num(m map[string]any, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}

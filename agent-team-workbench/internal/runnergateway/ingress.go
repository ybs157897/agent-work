package runnergateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// ── 分派：host-aware run.offer（RFC §7.5）────────────────────────────

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

// Dispatch 创建租约并下发 run.offer。选择条件精确匹配（RFC §7.5）：
//
//	runner.execution_host_id == snapshot.ExecutionHostID
//	AND runner.status == connected（conns 只含当前连接）
//	AND runner advertises adapter
//	AND capacity available（slots - 活跃 run 数）
//
// 无匹配返回错误——禁止跨 Host/本机回退；同 Host 重试由 Coordinator durable
// retry 创建新 Run。
func (g *Gateway) Dispatch(ctx context.Context, run *domain.ExecutionRun, snapshot *domain.ExecutionContextSnapshot, adapterID string) error {
	if snapshot == nil {
		return errors.New("dispatch 需要 context snapshot（无快照不分派）")
	}
	g.mu.Lock()
	var target *runnerConn
	for _, rc := range g.conns {
		if rc.hostID == snapshot.ExecutionHostID && rc.hasAdapter(adapterID) && rc.capacity() {
			target = rc
			break
		}
	}
	g.mu.Unlock()
	if target == nil {
		return fmt.Errorf("%w: host %s 无在线 %s runner 且有容量（不跨 Host/本机回退）",
			domain.ErrNotFound, snapshot.ExecutionHostID, adapterID)
	}
	if !g.connectionCredentialCurrent(ctx, target) {
		g.handleDisconnect(target)
		return fmt.Errorf("%w: host %s 的 runner enrollment credential 已撤销", domain.ErrNotFound, snapshot.ExecutionHostID)
	}

	lease := &application.RunLease{
		LeaseID: domain.NewID(domain.PrefixLease), RunID: run.ID,
		RunnerID: target.runnerID, RenewedUntil: time.Now().UTC().Add(leaseTTL),
	}
	if err := g.store.InTx(ctx, func(txCtx context.Context) error {
		return g.store.Runners().CreateLease(txCtx, lease)
	}); err != nil {
		return err
	}
	// 先把 lease 放入当前连接的内存 fence，再 enqueue offer：并发 disconnect
	// 可以看到它并将 Run 收敛到 reconnecting；若 enqueue 失败，下方会同时
	// 清内存与 DB lease，让 Dispatcher 按失败路径重试，绝不制造悬置 offer。
	g.mu.Lock()
	if g.conns[target.runnerID] != target {
		g.mu.Unlock()
		_ = g.store.Runners().ReleaseLease(ctx, lease.LeaseID, time.Now().UTC())
		return fmt.Errorf("%w: runner %s 在 lease 创建后已换代", domain.ErrNotFound, target.runnerID)
	}
	target.mu.Lock()
	target.activeRuns[run.ID] = &activeRun{LeaseID: lease.LeaseID, FencingToken: lease.FencingToken}
	target.mu.Unlock()
	g.mu.Unlock()

	policy, _ := run.Input["policy"].(map[string]any)
	if policy == nil {
		policy = map[string]any{
			"sandbox": "workspace-write", "approval_policy": "approve_high_risk",
		}
	}
	// offer 只带 opaque 身份（契约 §8.2）：宿主绝对路径永不进下行帧。
	payload := marshalPayload(offerPayload{
		LeaseID: lease.LeaseID, FencingToken: lease.FencingToken,
		RunSpec: offerRunSpec{
			RunID: run.ID, AdapterID: adapterID,
			ContextSnapshot: wireSnapshot(snapshot),
			Input:           run.Input,
			Policy:          policy,
		},
	})
	if target.sendEnvelope(Envelope{
		V: ProtocolVersion, MessageID: domain.NewID("msg_"), Kind: "request", Method: "run.offer",
		RunnerID: target.runnerID, RunID: run.ID, SentAt: time.Now().UTC(), Payload: payload,
	}) {
		return nil
	}
	g.forgetLease(run.ID, lease.LeaseID)
	// sendEnvelope 已关闭不可写 transport；清掉连接投影，避免后续 Dispatch 再次
	// 选中同一死连接。forgetLease 在前，确保这次未送达的 offer 不会被标为
	// reconnecting 后悬置。
	g.handleDisconnect(target)
	if err := g.store.Runners().ReleaseLease(ctx, lease.LeaseID, time.Now().UTC()); err != nil {
		return fmt.Errorf("offer enqueue failed and lease release failed: %w", err)
	}
	return fmt.Errorf("%w: runner %s 无法接收 run.offer", domain.ErrNotFound, target.runnerID)
}

// ForwardApproval 把审批决定作为 run.command 下发（协议 §7.1）。
// 服务端审批 ID 与 runner 模块的审批 ID 不同源：按 (run, server approval)
// 双键翻译成 runner 的 ID——同 run 多个并发审批互不串扰。映射必须来自持久
// ApprovalRequest；绝不把 server approval ID 降级透传给 Runner。
func (g *Gateway) ForwardApproval(ctx context.Context, runID, approvalID string, approved bool) {
	g.restoreApprovalMappings(ctx, runID)
	g.mu.Lock()
	runnerApprovalID := g.runnerApprovals[runID][approvalID]
	g.mu.Unlock()
	if runnerApprovalID == "" {
		log.Printf("runnergateway: approval %s/%s 缺持久 runner approval_id，拒绝错误透传", runID, approvalID)
		return
	}
	_ = g.forwardCommand(runID, "approval.resolve", map[string]any{
		"approval_id": runnerApprovalID, "approved": approved,
	})
}

// ForwardControl 转发 interrupt / cancel 命令；终态由 Runner 确认后回报。
func (g *Gateway) ForwardControl(ctx context.Context, runID, action string) {
	_ = g.forwardCommand(runID, action, nil)
}

// ForwardInput 向承接该 Run 的在线 Runner 转发 steering；返回是否命中活动租约。
func (g *Gateway) ForwardInput(ctx context.Context, runID, instruction string) bool {
	return g.forwardCommand(runID, "input", map[string]any{"instruction": instruction})
}

// forwardCommand 按活动租约补全 lease/fencing/epoch framing 后下发 run.command。
func (g *Gateway) forwardCommand(runID, command string, body map[string]any) bool {
	g.mu.Lock()
	var target *runnerConn
	var ar *activeRun
	for _, rc := range g.conns {
		rc.mu.Lock()
		if lease, ok := rc.activeRuns[runID]; ok {
			target, ar = rc, lease
		}
		rc.mu.Unlock()
		if target != nil {
			break
		}
	}
	g.mu.Unlock()
	if target == nil {
		return false
	}
	if !g.isCurrent(target) || !g.connectionCredentialCurrent(context.Background(), target) {
		g.handleDisconnect(target)
		return false
	}
	payload := marshalPayload(commandPayload{
		RunID: runID, LeaseID: ar.LeaseID, RunnerID: target.runnerID,
		ConnectionEpoch: target.epoch, FencingToken: ar.FencingToken,
		CommandID: domain.NewID("cmd_"), Command: command, Body: body,
	})
	return target.sendEnvelope(Envelope{
		V: ProtocolVersion, MessageID: domain.NewID("msg_"), Kind: "request", Method: "run.command",
		RunnerID: target.runnerID, RunID: runID, SentAt: time.Now().UTC(), Payload: payload,
	})
}

// ── 帧入口：heartbeat / run.accept / run.reject / run.event / ack ────
//
// 全部只做 transport 校验（当前连接、epoch、lease/fencing 匹配查证），
// 应用一律走 application.ApplyRunner* 原子命令；网关不 dedup、不映射事件、
// 不自改 Run 状态。

func (g *Gateway) handleMessage(rc *runnerConn, env Envelope) {
	if !g.connectionCredentialCurrent(context.Background(), rc) {
		log.Printf("runnergateway: runner %s 的 enrollment credential 已撤销，关闭连接", rc.runnerID)
		g.handleDisconnect(rc)
		return
	}
	// envelope 身份必须与连接态一致（其余 runner 的帧是传输错误）。
	if env.RunnerID != "" && env.RunnerID != rc.runnerID {
		log.Printf("runnergateway: 忽略身份不符帧（conn=%s frame=%s method=%s）", rc.runnerID, env.RunnerID, env.Method)
		return
	}
	switch env.Method {
	case "heartbeat":
		g.handleHeartbeat(rc, env)
	case "run.accept":
		g.handleAccept(rc, env)
	case "run.reject":
		g.handleReject(rc, env)
	case "run.event":
		g.handleRunEvent(rc, env)
	case "ack":
		// runner→server 的 keepalive ack（如对 run.command 的回执）：只刷新
		// 状态，续租节奏由 heartbeat 承担。
		g.touchRunner(rc)
	default:
		log.Printf("runnergateway: 忽略未知方法 %s", env.Method)
	}
}

// connectionCredentialCurrent 对已握手的 remote connection 执行轻量 recheck。
// hello 时的 digest 是连接建立时的认证快照；Host admin 轮换 credential 后，旧
// connection 在下一次 heartbeat/event/dispatch 前即被撤销。手工构造的单元测试
// 连接没有 credentialDigest，保留其纯 transport 测试语义。
func (g *Gateway) connectionCredentialCurrent(ctx context.Context, rc *runnerConn) bool {
	if rc == nil || rc.enrollmentDigest == "" {
		return true
	}
	host, err := g.store.ExecutionHosts().Get(ctx, rc.hostID)
	return err == nil && host != nil && host.Kind == domain.HostKindRemote &&
		host.EnrollmentRef != "" && host.EnrollmentRef == rc.enrollmentDigest
}

// transportLease 校验帧的 run/lease/fencing 与该连接的活动租约完全匹配
// （RFC §8.3 active authority：run_id + lease_id + runner_id + fencing_token）。
func transportLease(rc *runnerConn, runID, leaseID string, fencing int64) (*activeRun, bool) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	ar, ok := rc.activeRuns[runID]
	if !ok || ar.LeaseID != leaseID || ar.FencingToken != fencing {
		return nil, false
	}
	return ar, true
}

// handleHeartbeat：当前连接 + 当前 epoch 才续租（RenewLeasesByRunnerIfEpoch
// 的兑现方）；旧 epoch 心跳 0 行生效。
func (g *Gateway) handleHeartbeat(rc *runnerConn, env Envelope) {
	if env.ConnectionEpoch != rc.epoch || !g.isCurrent(rc) || rc.superseded {
		return
	}
	g.touchRunner(rc)
	if _, err := g.store.Runners().RenewLeasesByRunnerIfEpoch(context.Background(), rc.runnerID, rc.epoch, rc.bootID, time.Now().UTC().Add(leaseTTL)); err != nil {
		log.Printf("runnergateway: %s 续租失败: %v", rc.runnerID, err)
	}
}

func (g *Gateway) touchRunner(rc *runnerConn) {
	_ = g.store.Runners().SetStatus(context.Background(), rc.runnerID, "connected", time.Now().UTC())
}

// handleAccept：transport 校验通过后登记 Runner 侧接单；状态迁移由后续
// run.event 驱动（accept 不 ACK——它是 response 帧，不是待确认事件）。
func (g *Gateway) handleAccept(rc *runnerConn, env Envelope) {
	var p acceptPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return
	}
	if !g.eventTransportValid(rc, p.RunID, p.LeaseID, p.RunnerID, p.ConnectionEpoch, p.FencingToken) {
		log.Printf("runnergateway: 丢弃过期 run.accept（run=%s conn=%s）", p.RunID, rc.runnerID)
		return
	}
	err := g.engine.ApplyRunnerAccept(context.Background(), application.RunnerAcceptInput{
		RunID: p.RunID, LeaseID: p.LeaseID, RunnerID: p.RunnerID,
		ConnectionEpoch: p.ConnectionEpoch, FencingToken: p.FencingToken,
		SnapshotDigest: p.SnapshotDigest,
	})
	if err != nil {
		log.Printf("runnergateway: run %s accept 应用失败: %v", p.RunID, err)
		return
	}
	log.Printf("runnergateway: %s 接受 run %s（digest=%s）", rc.runnerID, p.RunID, p.SnapshotDigest)
}

// handleReject：应用层单事务完成 lease 释放 + Run 落 failed(retryable, family)
// + canonical event/outbox；网关只清内存 activeRuns，不改投另一 Runner。
func (g *Gateway) handleReject(rc *runnerConn, env Envelope) {
	var p rejectPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return
	}
	if !g.eventTransportValid(rc, p.RunID, p.LeaseID, p.RunnerID, p.ConnectionEpoch, p.FencingToken) {
		log.Printf("runnergateway: 丢弃过期 run.reject（run=%s conn=%s）", p.RunID, rc.runnerID)
		return
	}
	if err := g.engine.ApplyRunnerReject(context.Background(), application.RunnerRejectInput{
		RunID: p.RunID, LeaseID: p.LeaseID, RunnerID: p.RunnerID,
		ConnectionEpoch: p.ConnectionEpoch, FencingToken: p.FencingToken,
		ReasonCode: p.Reason, ReasonFamily: p.Reason, ReasonMessage: p.Detail,
	}); err != nil {
		// 瞬态失败：内存 activeRuns 保留，租约过期由 sweeper 收口。
		log.Printf("runnergateway: run %s reject 应用失败: %v", p.RunID, err)
		return
	}
	g.mu.Lock()
	rc.mu.Lock()
	delete(rc.activeRuns, p.RunID)
	rc.mu.Unlock()
	delete(g.runnerApprovals, p.RunID)
	g.mu.Unlock()
	log.Printf("runnergateway: run %s 被 %s 拒绝（reason=%s detail=%s）", p.RunID, rc.runnerID, p.Reason, p.Detail)
}

// eventTransportValid 是 run.accept/reject/event 共用的 transport 校验：
// 当前连接（未被顶替）+ 帧 epoch 与连接一致 + 活动 lease/fencing 完全匹配。
func (g *Gateway) eventTransportValid(rc *runnerConn, runID, leaseID, runnerID, epoch string, fencing int64) bool {
	if runnerID != rc.runnerID || epoch != rc.epoch || rc.superseded || !g.isCurrent(rc) {
		return false
	}
	_, ok := transportLease(rc, runID, leaseID, fencing)
	return ok
}

// handleRunEvent：transport 校验 → application.ApplyRunnerEvent 原子应用 →
// commit 后 ACK。瞬态应用失败不 ACK（Runner 保留 pending 重试）；
// duplicate/stale 由 Ack.Outcome 表达、照常 ACK；与活动租约不匹配的旧帧
// 直接回 stale ACK 不应用（避免 Runner 永久重放）。
func (g *Gateway) handleRunEvent(rc *runnerConn, env Envelope) {
	var p eventPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		log.Printf("runnergateway: 丢弃无法解析的 run.event（conn=%s）", rc.runnerID)
		return
	}
	if p.EventID == "" || p.ProducerSeq < 1 {
		// 毒帧：无法构造合法 ACK（契约要求 event_id/producer_seq），直接丢弃。
		log.Printf("runnergateway: 丢弃缺事件身份的 run.event（run=%s conn=%s）", p.RunID, rc.runnerID)
		return
	}
	// 与活动租约不匹配（旧 epoch / 旧 lease / 未知 run）：ACK 但不应用。
	if !g.eventTransportValid(rc, p.RunID, p.LeaseID, p.RunnerID, p.ConnectionEpoch, p.FencingToken) {
		log.Printf("runnergateway: 旧帧 ACK 不应用（run=%s lease=%s conn=%s）", p.RunID, p.LeaseID, rc.runnerID)
		g.ackStale(rc, p)
		return
	}

	ack, err := g.engine.ApplyRunnerEvent(context.Background(), application.RunnerEventInput{
		RunID: p.RunID, LeaseID: p.LeaseID, RunnerID: p.RunnerID,
		ConnectionEpoch: p.ConnectionEpoch, FencingToken: p.FencingToken,
		EventID: p.EventID, ProducerSeq: p.ProducerSeq,
		Kind: p.Event.Kind, Data: p.Event.Data,
	})
	if err != nil {
		// 瞬态失败整体回滚：不 ACK，Runner 重试（RFC §8.3.1）。
		log.Printf("runnergateway: run %s 事件 %s 应用失败（不 ACK，待重发）: %v", p.RunID, p.Event.Kind, err)
		return
	}
	g.ackEvent(rc, ack)
	// applied 与 duplicate 都要从持久行恢复 runner approval mapping：前者
	// 覆盖 ACK 丢失后的首次落库，后者覆盖 Gateway restart 后的重放。
	if p.Event.Kind == "approval.requested" {
		g.restoreApprovalMappings(context.Background(), p.RunID)
	}
	if ack.Outcome != application.RunnerEventApplied {
		return
	}
	switch p.Event.Kind {
	case "run.status_changed":
		if status := domain.RunStatus(str(p.Event.Data, "status")); status.IsTerminal() {
			g.releaseRun(p.RunID)
		}
	}
}

const runnerApprovalIDKey = "runner_approval_id"

// restoreApprovalMappings 从 ApprovalRequest.RequestedBy 重建 run 内全部
// (server approval → runner approval) 映射。它不依赖 pending 状态，已决审批在
// Gateway 重启/Runner 重连后仍需要 runner-local ID 才能重放 command。
func (g *Gateway) restoreApprovalMappings(ctx context.Context, runID string) {
	if g.store == nil {
		return
	}
	approvals, err := g.store.Runs().ListApprovals(ctx, runID)
	if err != nil {
		return
	}
	mappings := make(map[string]string)
	for _, a := range approvals {
		if a == nil || a.RequestedBy == nil {
			continue
		}
		if runnerID, _ := a.RequestedBy[runnerApprovalIDKey].(string); runnerID != "" {
			mappings[a.ID] = runnerID
		}
	}
	if len(mappings) == 0 {
		return
	}
	g.mu.Lock()
	if g.runnerApprovals[runID] == nil {
		g.runnerApprovals[runID] = make(map[string]string)
	}
	for approvalID, runnerID := range mappings {
		g.runnerApprovals[runID][approvalID] = runnerID
	}
	g.mu.Unlock()
}

// restoreAndReplayApprovals 在同 boot 重连后恢复所有 active Run 的映射，并把
// 已落库的 approved/rejected 决定补发给 Runner。welcome 已先入 send queue，
// 因而 command 必然在协商后到达；Runner command channel 对重复决议幂等。
func (g *Gateway) restoreAndReplayApprovals(rc *runnerConn) {
	if rc == nil || g.store == nil {
		return
	}
	rc.mu.Lock()
	runIDs := make([]string, 0, len(rc.activeRuns))
	for runID := range rc.activeRuns {
		runIDs = append(runIDs, runID)
	}
	rc.mu.Unlock()
	for _, runID := range runIDs {
		g.restoreApprovalMappings(context.Background(), runID)
		approvals, err := g.store.Runs().ListApprovals(context.Background(), runID)
		if err != nil {
			continue
		}
		for _, approval := range approvals {
			if approval == nil || approval.RequestedBy == nil {
				continue
			}
			runnerApprovalID, _ := approval.RequestedBy[runnerApprovalIDKey].(string)
			if runnerApprovalID == "" {
				continue
			}
			approved := approval.Status == domain.ApprovalApproved
			if approval.Status != domain.ApprovalApproved && approval.Status != domain.ApprovalRejected {
				continue
			}
			if !g.forwardCommand(runID, "approval.resolve", map[string]any{
				"approval_id": runnerApprovalID, "approved": approved,
			}) {
				log.Printf("runnergateway: 重放审批决定失败（run=%s approval=%s）", runID, approval.ID)
			}
		}
	}
}

// releaseRun：Run 终态后摘除网关内存活动记录（lease 释放由应用层在
// ApplyRunnerEvent 事务内完成，网关不重复释放）。
func (g *Gateway) releaseRun(runID string) {
	g.mu.Lock()
	for _, rc := range g.conns {
		rc.mu.Lock()
		delete(rc.activeRuns, runID)
		rc.mu.Unlock()
	}
	delete(g.runnerApprovals, runID)
	g.mu.Unlock()
}

// forgetLease 删除特定 offer lease 的内存镜像。enqueue 失败或创建后换代时不得
// 误删同 Run 的更新 fencing lease。
func (g *Gateway) forgetLease(runID, leaseID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, rc := range g.conns {
		rc.mu.Lock()
		if active := rc.activeRuns[runID]; active != nil && active.LeaseID == leaseID {
			delete(rc.activeRuns, runID)
		}
		rc.mu.Unlock()
	}
}

// ackEvent 回 ACK：只在 ApplyRunnerEvent 成功（事务 commit）后发送。
// payload 带 run_id/lease_id/runner_id/fencing_token/acked_producer_seq/event_id。
func (g *Gateway) ackEvent(rc *runnerConn, ack application.RunnerEventAck) {
	payload := marshalPayload(ackPayload{
		RunID: ack.RunID, LeaseID: ack.LeaseID, RunnerID: ack.RunnerID,
		FencingToken: ack.FencingToken, AckedProducerSeq: ack.AckedProducerSeq,
		EventID: ack.EventID, Backpressure: BackpressureNone,
	})
	rc.sendEnvelope(Envelope{
		V: ProtocolVersion, MessageID: domain.NewID("msg_"), Kind: "ack", Method: "ack",
		RunnerID: rc.runnerID, RunID: ack.RunID, SentAt: time.Now().UTC(), Payload: payload,
	})
}

// ackStale 对与活动租约不匹配的帧回显 ACK（避免 Runner 永久重放），
// 不进入 Application、event store 或 Session anchor。
func (g *Gateway) ackStale(rc *runnerConn, p eventPayload) {
	payload := marshalPayload(ackPayload{
		RunID: p.RunID, LeaseID: p.LeaseID, RunnerID: p.RunnerID,
		FencingToken: p.FencingToken, AckedProducerSeq: p.ProducerSeq,
		EventID: p.EventID, Backpressure: BackpressureNone,
	})
	rc.sendEnvelope(Envelope{
		V: ProtocolVersion, MessageID: domain.NewID("msg_"), Kind: "ack", Method: "ack",
		RunnerID: rc.runnerID, RunID: p.RunID, SentAt: time.Now().UTC(), Payload: payload,
	})
}

func str(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

package runnergateway

// v2 线协议形态（权威：contracts/runner/v2/schema.json，RFC §8）。
// 九种 envelope：runner.hello / server.welcome / run.offer / run.accept /
// run.reject / run.command / run.event / ack / heartbeat，全部 v=2；
// v1 帧在握手与读循环两处拒绝，不留兼容分支。
//
// ContextSnapshot 的 branch_name/base_revision/location_version 与本实现
// 同名字段保持一致，Runner 可以按完整身份闭集重算 snapshot_digest；契约
// 守卫测试禁止 wire struct 与 schema 再次漂移。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// ConnectPath 是 v2 网关的唯一挂载路由（v1 /runner/v1/connect 已删除）。
const ConnectPath = "/runner/v2/connect"

// ProtocolVersion 是唯一支持的协议版本。
const ProtocolVersion = 2

// Envelope 对应 contracts/runner/v2 信封。ConnectionEpoch 仅为 heartbeat 的
// envelope 级字段（契约 Heartbeat required；其余 envelope additionalProperties:
// false，靠 omitempty 保证不出现）。
type Envelope struct {
	V               int             `json:"v"`
	MessageID       string          `json:"message_id"`
	Kind            string          `json:"kind"` // request | response | event | ack
	Method          string          `json:"method"`
	RunnerID        string          `json:"runner_id,omitempty"`
	RunID           string          `json:"run_id,omitempty"`
	ReplyTo         string          `json:"reply_to,omitempty"`
	ConnectionEpoch string          `json:"connection_epoch,omitempty"`
	SentAt          time.Time       `json:"sent_at"`
	Payload         json.RawMessage `json:"payload,omitempty"`
}

// adapterInfo 对应契约 AdapterManifestRef：分派前能力校验的 runner 侧真相。
type adapterInfo struct {
	ID           string `json:"adapter_id"`
	Version      string `json:"adapter_version"`
	SchemaDigest string `json:"schema_digest"`
}

// helloPayload 对应 RunnerHelloPayload。
type helloPayload struct {
	HostID           string             `json:"host_id"`
	RunnerID         string             `json:"runner_id"`
	BootID           string             `json:"boot_id"`
	ConnectionEpoch  string             `json:"connection_epoch"`
	ProtocolVersions []int              `json:"protocol_versions"`
	RunnerVersion    string             `json:"runner_version"`
	OS               string             `json:"os,omitempty"`
	Arch             string             `json:"arch,omitempty"`
	Slots            int                `json:"slots"`
	Adapters         []adapterInfo      `json:"adapters"`
	Mounts           []domain.HostMount `json:"mounts"`
}

// supportsV2 报告 hello 是否声明 v2 兼容（protocol_versions 必须含 2）。
func (h *helloPayload) supportsV2() bool {
	for _, v := range h.ProtocolVersions {
		if v == ProtocolVersion {
			return true
		}
	}
	return false
}

// leasePolicy 对应 ServerWelcomePayload.lease_policy。
type leasePolicy struct {
	TTLSeconds           int `json:"ttl_seconds"`
	RenewIntervalSeconds int `json:"renew_interval_seconds"`
}

// welcomePayload 对应 ServerWelcomePayload。
type welcomePayload struct {
	SelectedVersion          int         `json:"selected_version"`
	HeartbeatIntervalSeconds int         `json:"heartbeat_interval_seconds"`
	LeasePolicy              leasePolicy `json:"lease_policy"`
	MaxFrameBytes            int         `json:"max_frame_bytes,omitempty"`
}

// wireContextSnapshot 是 ExecutionContextSnapshot 的 wire 子集
// （契约 ContextSnapshot + 三个体补身份字段，见文件头偏差说明）。
// checkout_ref/worktree_ref 按契约为可空：root 为 null；branch 无 worktree_ref。
type wireContextSnapshot struct {
	ID                  string         `json:"id"`
	SchemaVersion       string         `json:"schema_version"`
	ExecutionHostID     string         `json:"execution_host_id"`
	WorkspaceLocationID string         `json:"workspace_location_id"`
	WorkspaceAlias      string         `json:"workspace_alias"`
	MountGeneration     string         `json:"mount_generation"`
	RepositoryIdentity  string         `json:"repository_identity"`
	RefKind             domain.RefKind `json:"ref_kind"`
	BranchName          string         `json:"branch_name,omitempty"`
	CheckoutRef         *string        `json:"checkout_ref"`
	WorktreeRef         *string        `json:"worktree_ref"`
	BaseRevision        string         `json:"base_revision,omitempty"`
	LocationVersion     int            `json:"location_version"`
	ContextGeneration   int            `json:"context_generation"`
	SnapshotDigest      string         `json:"snapshot_digest"`
}

// wireSnapshot 把领域快照编码为线形态。WorkspaceAlias 对应领域 MountAlias
// （契约字段名）；空 checkout/worktree ref 序列化为 null。
func wireSnapshot(s *domain.ExecutionContextSnapshot) wireContextSnapshot {
	w := wireContextSnapshot{
		ID:                  s.ID,
		SchemaVersion:       s.SchemaVersion,
		ExecutionHostID:     s.ExecutionHostID,
		WorkspaceLocationID: s.WorkspaceLocationID,
		WorkspaceAlias:      s.MountAlias,
		MountGeneration:     s.MountGeneration,
		RepositoryIdentity:  s.RepositoryIdentity,
		RefKind:             s.RefKind,
		BranchName:          s.BranchName,
		BaseRevision:        s.BaseRevision,
		LocationVersion:     s.LocationVersion,
		ContextGeneration:   s.ContextGeneration,
		SnapshotDigest:      s.SnapshotDigest,
	}
	if s.CheckoutRef != "" {
		w.CheckoutRef = &s.CheckoutRef
	}
	if s.WorktreeRef != "" {
		w.WorktreeRef = &s.WorktreeRef
	}
	return w
}

// toDomain 还原为领域快照（RunID 由 run_spec 补齐）。schema 非 v1 拒绝——
// Runner 只执行 execution-context/v1。
func (w wireContextSnapshot) toDomain(runID string) (domain.ExecutionContextSnapshot, error) {
	if w.SchemaVersion != domain.SnapshotSchemaV1 {
		return domain.ExecutionContextSnapshot{}, fmt.Errorf("context_snapshot.schema_version %q 必须为 %q", w.SchemaVersion, domain.SnapshotSchemaV1)
	}
	s := domain.ExecutionContextSnapshot{
		ID:                  w.ID,
		RunID:               runID,
		SchemaVersion:       w.SchemaVersion,
		WorkspaceLocationID: w.WorkspaceLocationID,
		LocationVersion:     w.LocationVersion,
		MountGeneration:     w.MountGeneration,
		ExecutionHostID:     w.ExecutionHostID,
		MountAlias:          w.WorkspaceAlias,
		RepositoryIdentity:  w.RepositoryIdentity,
		RefKind:             w.RefKind,
		BranchName:          w.BranchName,
		BaseRevision:        w.BaseRevision,
		ContextGeneration:   w.ContextGeneration,
		SnapshotDigest:      w.SnapshotDigest,
	}
	if w.CheckoutRef != nil {
		s.CheckoutRef = *w.CheckoutRef
	}
	if w.WorktreeRef != nil {
		s.WorktreeRef = *w.WorktreeRef
	}
	return s, nil
}

// offerPayload 对应 RunOfferPayload（下行：Gateway → Runner）。
type offerPayload struct {
	LeaseID      string       `json:"lease_id"`
	FencingToken int64        `json:"fencing_token"`
	RunSpec      offerRunSpec `json:"run_spec"`
}

// offerRunSpec 对应 RunOfferPayload.run_spec。AgentProfileID 是 adapter 侧
// 绑定 provider usage report provenance.agent_id 的身份来源（缺失则远程
// adapter 造不出 sealed report）。
type offerRunSpec struct {
	RunID           string              `json:"run_id"`
	AdapterID       string              `json:"adapter_id"`
	AgentProfileID  string              `json:"agent_profile_id"`
	ContextSnapshot wireContextSnapshot `json:"context_snapshot"`
	Input           map[string]any      `json:"input"`
	Policy          map[string]any      `json:"policy"`
}

// acceptPayload 对应 RunAcceptPayload（snapshot_digest 为 Runner 用本地事实
// 重算后的值；与 offer 不一致必须走 reject 而非 accept）。
type acceptPayload struct {
	RunID           string `json:"run_id"`
	LeaseID         string `json:"lease_id"`
	RunnerID        string `json:"runner_id"`
	ConnectionEpoch string `json:"connection_epoch"`
	FencingToken    int64  `json:"fencing_token"`
	SnapshotDigest  string `json:"snapshot_digest"`
}

// rejectReason 是 run.reject 的 reason 闭集（契约 enum）。
const (
	RejectWorkspace = "workspace"
	RejectCapacity  = "capacity"
)

// rejectPayload 对应 RunRejectPayload。固定行为（应用层单事务）：CAS 释放
// lease、清 activeRuns、Run 落 failed(retryable=true, family=reason)、
// canonical event/outbox；重复 reject 幂等；Gateway 不改投另一 Runner。
type rejectPayload struct {
	RunID           string `json:"run_id"`
	LeaseID         string `json:"lease_id"`
	RunnerID        string `json:"runner_id"`
	ConnectionEpoch string `json:"connection_epoch"`
	FencingToken    int64  `json:"fencing_token"`
	Reason          string `json:"reason"`
	Detail          string `json:"detail,omitempty"`
}

// commandPayload 对应 RunCommandPayload（下行控制：input/interrupt/cancel/
// approval.resolve）。
type commandPayload struct {
	RunID           string         `json:"run_id"`
	LeaseID         string         `json:"lease_id"`
	RunnerID        string         `json:"runner_id"`
	ConnectionEpoch string         `json:"connection_epoch"`
	FencingToken    int64          `json:"fencing_token"`
	CommandID       string         `json:"command_id"`
	Command         string         `json:"command"`
	Body            map[string]any `json:"body,omitempty"`
}

// runnerEvent 是 canonical candidate 事件载荷（message.*/tool.*/run.* 白名单，
// 校验在应用层 ApplyRunnerEvent）。
type runnerEvent struct {
	Kind string         `json:"kind"`
	Data map[string]any `json:"data,omitempty"`
}

// eventPayload 对应 RunEventPayload。事件身份 = (run_id, lease_id, runner_id,
// producer_seq)；event_id/producer_seq 由 Runner 分配、重连原样重发；
// connection_epoch 只识别 transport connection，不参与 dedup。
type eventPayload struct {
	RunID           string      `json:"run_id"`
	LeaseID         string      `json:"lease_id"`
	RunnerID        string      `json:"runner_id"`
	ConnectionEpoch string      `json:"connection_epoch"`
	FencingToken    int64       `json:"fencing_token"`
	EventID         string      `json:"event_id"`
	ProducerSeq     int64       `json:"producer_seq"`
	ProviderCursor  *string     `json:"provider_cursor,omitempty"`
	Event           runnerEvent `json:"event"`
	RawRef          *string     `json:"raw_ref,omitempty"`
}

// ackBackpressure 是 RunAckPayload.backpressure 闭集。
const (
	BackpressureNone  = "none"
	BackpressureSlow  = "slow"
	BackpressurePause = "pause"
)

// ackPayload 对应 RunAckPayload：按 Run 维度确认，不沿用 v1 全局
// contiguous_seq；对旧 epoch/旧 fencing 帧也回 ACK 但不应用。
type ackPayload struct {
	RunID             string     `json:"run_id"`
	LeaseID           string     `json:"lease_id"`
	RunnerID          string     `json:"runner_id"`
	FencingToken      int64      `json:"fencing_token"`
	AckedProducerSeq  int64      `json:"acked_producer_seq"`
	EventID           string     `json:"event_id"`
	LeaseRenewedUntil *time.Time `json:"lease_renewed_until,omitempty"`
	Backpressure      string     `json:"backpressure,omitempty"`
}

// heartbeatPayload 对应 HeartbeatPayload：允许为空对象，不携带 run/lease 维度
// 字段（run 级确认走 ack）。
type heartbeatPayload struct{}

// ParseHostCredential 解析 enrollment 凭据 `atw_host_<host_id>_<secret>`
// （格式由主智能体冻结：host_id 段不含 '_'，secret 为其后全部内容）。
// Gateway 用它做 WSS 升级 + hello 双重校验；runnerd 用它取 hello 上报的
// host_id。返回 ok=false 表示格式非法。
func ParseHostCredential(token string) (hostID, secret string, ok bool) {
	const prefix = "atw_host_"
	if !strings.HasPrefix(token, prefix) {
		return "", "", false
	}
	rest := token[len(prefix):]
	const hostPrefix = "host_"
	if !strings.HasPrefix(rest, hostPrefix) {
		return "", "", false
	}
	rest = rest[len(hostPrefix):]
	i := strings.Index(rest, "_")
	if i <= 0 || i == len(rest)-1 {
		return "", "", false
	}
	return hostPrefix + rest[:i], rest[i+1:], true
}

// enrollmentDigest 是 enrollment_ref 的口径：sha256(secret) hex。
func enrollmentDigest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// marshalPayload 序列化 payload；编不出来是编程错误（类型受 schema 约束）。
func marshalPayload(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("runnergateway: payload 序列化失败: " + err.Error())
	}
	return b
}

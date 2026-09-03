package domain

import (
	"fmt"
	"time"
)

const ProviderUsageAnchorSchemaVersionV1 = "provider-usage-anchor/v1"

type ProviderUsageAnchorState string

const (
	ProviderUsageAnchorReady       ProviderUsageAnchorState = "ready"
	ProviderUsageAnchorInvalidated ProviderUsageAnchorState = "invalidated"
)

// ProviderUsageAnchorV1 is the durable provider-cumulative baseline used to
// derive per-run usage.  An invalidated marker deliberately carries unknown
// counters so a new session cannot inherit the prior provider's baseline.
type ProviderUsageAnchorV1 struct {
	SchemaVersion      string                   `json:"schema_version"`
	State              ProviderUsageAnchorState `json:"state"`
	AdapterID          string                   `json:"adapter_id"`
	SessionRef         string                   `json:"session_ref,omitempty"`
	ContextGeneration  int                      `json:"context_generation"`
	SegmentSeq         int                      `json:"segment_seq"`
	Counters           UsageCountersV1          `json:"counters"`
	SourceRunID        string                   `json:"source_run_id"`
	ObservedAt         time.Time                `json:"observed_at"`
	InvalidationReason string                   `json:"invalidation_reason,omitempty"`
}

func (a *ProviderUsageAnchorV1) Validate() error {
	if a == nil {
		return fmt.Errorf("%w: nil provider usage anchor", ErrValidation)
	}
	if a.SchemaVersion != ProviderUsageAnchorSchemaVersionV1 {
		return fmt.Errorf("%w: provider usage anchor schema_version %q", ErrValidation, a.SchemaVersion)
	}
	if a.State != ProviderUsageAnchorReady && a.State != ProviderUsageAnchorInvalidated {
		return fmt.Errorf("%w: provider usage anchor state %q", ErrValidation, a.State)
	}
	if err := validateText("provider usage anchor.adapter_id", a.AdapterID, 512); err != nil {
		return err
	}
	if a.SessionRef != "" {
		if err := validateText("provider usage anchor.session_ref", a.SessionRef, 1024); err != nil {
			return err
		}
	}
	if a.ContextGeneration < 0 {
		return fmt.Errorf("%w: provider usage anchor.context_generation must be >= 0", ErrValidation)
	}
	if a.SegmentSeq < 1 {
		return fmt.Errorf("%w: provider usage anchor.segment_seq must be >= 1", ErrValidation)
	}
	// An anchor is a per-kind watermark merge, not a copy of one provider
	// observation. Its input dimensions may therefore come from different
	// observations while a provider counter is recovering from a reset. Keep
	// the strict decomposition rule on ProviderUsageReport and CanonicalUsage;
	// anchor validation only needs the nullable non-negative counter shape.
	if err := validateProviderUsageAnchorCounters(a.Counters); err != nil {
		return fmt.Errorf("%w: provider usage anchor.counters: %v", ErrValidation, err)
	}
	if err := validateTypedID("provider usage anchor.source_run_id", a.SourceRunID, PrefixRun); err != nil {
		return err
	}
	if a.ObservedAt.IsZero() {
		return fmt.Errorf("%w: provider usage anchor.observed_at is required", ErrValidation)
	}
	knownCounter := a.Counters.InputTokensTotal != nil || a.Counters.InputUncachedTokens != nil ||
		a.Counters.CacheReadTokens != nil || a.Counters.CacheWriteTokens != nil || a.Counters.OutputTokens != nil
	if a.State == ProviderUsageAnchorReady {
		if a.SessionRef == "" {
			return fmt.Errorf("%w: ready provider usage anchor requires session_ref", ErrValidation)
		}
		if !knownCounter {
			return fmt.Errorf("%w: ready provider usage anchor requires a known counter", ErrValidation)
		}
		if a.InvalidationReason != "" {
			return fmt.Errorf("%w: ready provider usage anchor cannot carry invalidation_reason", ErrValidation)
		}
	} else {
		if knownCounter {
			return fmt.Errorf("%w: invalidated provider usage anchor counters must be unknown", ErrValidation)
		}
		if err := validateText("provider usage anchor.invalidation_reason", a.InvalidationReason, 2000); err != nil {
			return err
		}
	}
	return nil
}

func validateProviderUsageAnchorCounters(c UsageCountersV1) error {
	for name, value := range map[string]*int64{
		"input_tokens_total":    c.InputTokensTotal,
		"input_uncached_tokens": c.InputUncachedTokens,
		"cache_read_tokens":     c.CacheReadTokens,
		"cache_write_tokens":    c.CacheWriteTokens,
		"output_tokens":         c.OutputTokens,
	} {
		if value != nil && *value < 0 {
			return fmt.Errorf("%w: usage counter %s must be >= 0", ErrValidation, name)
		}
	}
	return nil
}

// TaskSession 是 (workspace, agent, adapter, task) 维度的长期会话锚点
// （对齐 Paperclip agent_task_sessions）：跨 Run 续接、指纹校验与轮换都以此为准。
// taskKey 当前取 work item ID；AgentProfileID 可为空（匿名运行）。
type TaskSession struct {
	ID          string
	WorkspaceID string
	// AgentProfileID + AdapterID + TaskKey 构成唯一键。
	AgentProfileID string
	AdapterID      string
	TaskKey        string
	// ParentAnchorID 父任务（同 agent+adapter）锚点 id：会话树镜像任务树，
	// 轮换谱系沿此链可查；父任务不存在或无锚点时为空。
	ParentAnchorID string
	// SessionParams adapter 私有参数；保留键：__ref（session_ref，如 claude://x）、
	// __fingerprint（创建该会话时的配置指纹，变更即视为配置漂移）。
	SessionParams map[string]any
	// DisplayID provider 侧人类可读会话 id（如 thread id、sessionId）。
	DisplayID string
	// RunsCount 自该会话创建以来使用它的 Run 数（轮换阈值输入）。
	RunsCount int
	// InputTokensCum 会话累计输入 token（per_run 用量累加而来；轮换阈值输入）。
	InputTokensCum int64
	// SegmentSeq 参与线片段序号：同一 task_key 下第 N 段会话（轮换代际时 +1，
	// 缺省 1），片段边界显式化。
	SegmentSeq int
	// ContextSnapshotID / ContextGeneration 是该锚点当前挂靠的执行上下文身份
	//（RFC §4.8：context_generation 是兼容代际，不等于单个 Snapshot ID；
	// fingerprint 含 generation，变化必须 fresh/rotate）。
	ContextSnapshotID string
	ContextGeneration int
	// LastRunID / AnchorRunSequence 是 Run 创建事务内预先 claim 的锚点归属：
	// run.session 更新必须满足 generation 一致、incoming Run 是当前 anchor
	// owner、run sequence 不小于当前 anchor sequence（旧 Run 的墓碑不清新代际）。
	LastRunID         string
	AnchorRunSequence int64
	// ProviderUsageAnchor is independent from InputTokensCum: it stores the
	// provider's cumulative baseline and advances through its own sequence.
	ProviderUsageAnchor    *ProviderUsageAnchorV1
	ProviderUsageAnchorSeq int64
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// SessionRef 返回会话句柄（无则空串）。
func (t *TaskSession) SessionRef() string {
	if t == nil || t.SessionParams == nil {
		return ""
	}
	ref, _ := t.SessionParams["__ref"].(string)
	return ref
}

// Fingerprint 返回创建会话时的配置指纹。
func (t *TaskSession) Fingerprint() string {
	if t == nil || t.SessionParams == nil {
		return ""
	}
	fp, _ := t.SessionParams["__fingerprint"].(string)
	return fp
}

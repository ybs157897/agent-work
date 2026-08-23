// Package domain 定义工作台领域模型与状态机（协议文档 §4）。
//
// 关键边界：WorkItem 是看板业务真相；ExecutionRun 是一次不可覆盖的执行尝试；
// Provider Session 只是 Adapter 私有句柄，三者不能永久一对一绑定。
package domain

import (
	"crypto/rand"
	"time"

	"github.com/oklog/ulid/v2"
)

// 资源 ID 类型前缀（协议文档 §5.1：opaque、带类型前缀）。
const (
	PrefixWorkspace = "ws_"
	PrefixAgent     = "agent_"
	PrefixWorkItem  = "wi_"
	PrefixPlan      = "plan_"
	PrefixRun       = "run_"
	PrefixApproval  = "approval_"
	PrefixArtifact  = "artifact_"
	PrefixEvent     = "evt_"
	PrefixRunner    = "runner_"
	PrefixLease     = "lease_"
	PrefixBinding   = "rb_"
	PrefixCaps      = "caps_"
	PrefixTaskSess  = "ts_"
)

// NewID 生成带类型前缀的 ULID。
func NewID(prefix string) string {
	return prefix + ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String()
}

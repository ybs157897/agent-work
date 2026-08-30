package domain

import "time"

// DecisionEntry 决策台账行（会话元模型 S2）：任务级共享记忆的用户原话引文。
// quote 必须存用户原话（引文 + 链回片段内位置），LLM 转述只允许进
// work_items.rolling_digest——转述会漂移，「当时怎么定的」必须保真。
type DecisionEntry struct {
	ID string
	// WorkItemID 台账归属任务；决策不属于任何会话（D4：横向协作只走台账）。
	WorkItemID string
	Quote      string
	// SourceRunID 可选：钉出来源轮次；SourceRef 链回片段内位置（消息序号等）。
	SourceRunID string
	SourceRef   string
	CreatedAt   time.Time
}

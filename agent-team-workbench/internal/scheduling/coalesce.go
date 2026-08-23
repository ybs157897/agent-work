package scheduling

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// Enqueuer 是入队所需的最小能力（scheduling.Store 与 application 层端口均满足）。
type Enqueuer interface {
	EnqueueWakeup(ctx context.Context, r *domain.WakeupRequest) error
}

// EnqueueWakeup 写入一条唤醒请求（on_demand / assignment 入口的统一入队 helper，
// merge 阶段由 httpapi / 指派事件调用）。校验 source 与 taskKey，生成 wkup_ 前缀 ID；
// wakeAt 为零值视为立即到期。返回已落库的请求。
func EnqueueWakeup(ctx context.Context, store Enqueuer, source domain.WakeupSource,
	workspaceID, agentProfileID, taskKey string, wakeContext map[string]any, wakeAt time.Time) (*domain.WakeupRequest, error) {
	if !domain.ValidWakeupSource(source) {
		return nil, fmt.Errorf("scheduling: 非法唤醒源 %q", string(source))
	}
	if taskKey == "" {
		return nil, errors.New("scheduling: taskKey 不能为空（timer 自主唤醒也需要稳定 key）")
	}
	now := time.Now().UTC()
	if wakeAt.IsZero() {
		wakeAt = now
	}
	w := &domain.WakeupRequest{
		ID:             domain.NewID(domain.PrefixWakeup),
		WorkspaceID:    workspaceID,
		AgentProfileID: agentProfileID,
		Source:         source,
		TaskKey:        taskKey,
		Context:        wakeContext,
		Status:         domain.WakeupStatusQueued,
		WakeAt:         wakeAt,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := store.EnqueueWakeup(ctx, w); err != nil {
		return nil, err
	}
	return w, nil
}

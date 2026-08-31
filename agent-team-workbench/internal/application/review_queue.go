// review_queue.go Review Queue 服务端 read model（任务控制面 RFC §4.10/§9.5）。
//
// 唯一派生条件：record_kind=task ∧ status=in_progress ∧ phase IN (review,
// acceptance)。固定排序 pending_since ASC, priority DESC, id ASC；cursor 是
// 不透明 token 且携带排序键全列；total_count 独立于分页（badge 权威值）。
// 本文件只做投影聚合：不新增任何状态、不发事件、不写任何表。
//
// 存储现状说明：WorkItemRepo.List 支持 record_kind/status/priority 过滤与
// created_at 游标，但不支持 phase 过滤与 pending_since 排序。这里用既有方法
// 组合——同一 read transaction 内按 status=priority 过滤分页拉全量候选，
// phase 过滤、固定排序、total_count、排序键游标全部在应用层收口。候选集是
// in_progress 的 Task（串行执行模型下规模有界），拉全量后内存投影。
package application

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// reviewQueuePhases 队列的 phase 闭包；phase 过滤参数只能取其中之一。
var reviewQueuePhases = map[domain.WorkItemPhase]bool{
	domain.PhaseReview:     true,
	domain.PhaseAcceptance: true,
}

// priorityRank 固定排序的 priority 列（DESC）：值越高越靠前；未知值垫底。
func priorityRank(p domain.Priority) int {
	switch p {
	case domain.PriorityUrgent:
		return 4
	case domain.PriorityHigh:
		return 3
	case domain.PriorityMedium:
		return 2
	case domain.PriorityLow:
		return 1
	default:
		return 0
	}
}

// ReviewQueueQuery GET /workspaces/{id}/review-queue 的查询参数。
// Phase 为空表示不过滤（review+acceptance 都要）；Priority 同理。
type ReviewQueueQuery struct {
	WorkspaceID string
	Cursor      string
	Limit       int
	Priority    domain.Priority
	Phase       domain.WorkItemPhase
}

// ReviewQueueCoordinator 队列行的 Coordinator 投影（非 Coordinator Task 为 nil）。
type ReviewQueueCoordinator struct {
	Status    string
	Stage     string
	UpdatedAt time.Time
	Version   int
}

// ReviewQueueWatermark 各来源版本水位（同一 read snapshot 内读取；RFC §9.5）。
type ReviewQueueWatermark struct {
	// AsOfEventSeq 同一 snapshot 内 Workspace MAX(stream_seq)。
	AsOfEventSeq    int64
	WorkItemVersion int
	// CoordinatorVersion 非 Coordinator Task 为 0（投影整体为 null）。
	CoordinatorVersion int
	// LatestRunVersion 最新 Run 的乐观锁版本；无 Run 为 0。
	LatestRunVersion int
	// CommentRevision 根评论 cursor 水位；无评论流（历史 Task）为 0。
	CommentRevision int64
}

// ReviewQueueItem 队列行。
type ReviewQueueItem struct {
	WorkItem     *domain.WorkItem
	PendingSince time.Time
	Coordinator  *ReviewQueueCoordinator
	LatestRunID  string
	// RunCount 该 Task 的 Run 总数（runs_count 投影）。
	RunCount  int
	Watermark ReviewQueueWatermark
}

// ReviewQueuePage 分页响应；TotalCount 是过滤条件下的全量匹配数。
type ReviewQueuePage struct {
	Items       []ReviewQueueItem
	TotalCount  int
	NextCursor  string
	GeneratedAt time.Time
}

// reviewQueueCursor 排序键全列：pending_since ASC, priority DESC, id ASC。
type reviewQueueCursor struct {
	Since      time.Time
	Priority   domain.Priority
	WorkItemID string
}

func encodeReviewQueueCursor(c reviewQueueCursor) string {
	raw := c.Since.UTC().Format(time.RFC3339Nano) + "|" + string(c.Priority) + "|" + c.WorkItemID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeReviewQueueCursor(token string) (reviewQueueCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return reviewQueueCursor{}, fmt.Errorf("%w: 非法 cursor", domain.ErrValidation)
	}
	parts := strings.SplitN(string(raw), "|", 3)
	if len(parts) != 3 {
		return reviewQueueCursor{}, fmt.Errorf("%w: 非法 cursor", domain.ErrValidation)
	}
	since, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return reviewQueueCursor{}, fmt.Errorf("%w: 非法 cursor", domain.ErrValidation)
	}
	return reviewQueueCursor{Since: since, Priority: domain.Priority(parts[1]), WorkItemID: parts[2]}, nil
}

// afterCursor 报告排序键是否严格落在 cursor 之后（固定排序的下一页起点）。
func (i ReviewQueueItem) afterCursor(c reviewQueueCursor) bool {
	since := i.pendingSince()
	if since.After(c.Since) {
		return true
	}
	if since.Before(c.Since) {
		return false
	}
	rank, cRank := priorityRank(i.WorkItem.Priority), priorityRank(c.Priority)
	if rank != cRank {
		return rank < cRank // priority DESC：cursor 之后 = rank 更小
	}
	return i.WorkItem.ID > c.WorkItemID
}

// pendingSince = phase_entered_at；历史行（0022 前）缺时间时回退 updated_at，
// 保证排序键恒有值（迁移已回填，这里只是投影层的防御）。
func (i ReviewQueueItem) pendingSince() time.Time {
	if i.WorkItem.PhaseEnteredAt != nil {
		return *i.WorkItem.PhaseEnteredAt
	}
	return i.WorkItem.UpdatedAt
}

// ReviewQueue 聚合队列页。整个取数（候选、水位、事件序号）在同一 read
// transaction 内完成；generated_at 是服务端时间。
func (s *Service) ReviewQueue(ctx context.Context, q ReviewQueueQuery) (*ReviewQueuePage, error) {
	if q.WorkspaceID == "" {
		return nil, fmt.Errorf("%w: workspace required", domain.ErrValidation)
	}
	if q.Phase != "" && !reviewQueuePhases[q.Phase] {
		return nil, fmt.Errorf("%w: phase 只接受 review|acceptance", domain.ErrValidation)
	}
	if q.Priority != "" {
		switch q.Priority {
		case domain.PriorityLow, domain.PriorityMedium, domain.PriorityHigh, domain.PriorityUrgent:
		default:
			return nil, fmt.Errorf("%w: 非法 priority %q", domain.ErrValidation, q.Priority)
		}
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	var cursor reviewQueueCursor
	if q.Cursor != "" {
		c, err := decodeReviewQueueCursor(q.Cursor)
		if err != nil {
			return nil, err
		}
		cursor = c
	}

	page := &ReviewQueuePage{GeneratedAt: time.Now().UTC()}
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		// ① 候选集：record_kind=task ∧ status=in_progress（∧ priority）。底层
		// List 以 created_at DESC 游标分页，这里逐页拉全量（in_progress 有界）。
		var candidates []*domain.WorkItem
		repoCursor := ""
		for {
			items, next, err := s.store.WorkItems().List(ctx, q.WorkspaceID, WorkItemFilter{
				RecordKind: domain.RecordKindTask,
				Status:     domain.WorkItemInProgress,
				Priority:   q.Priority,
				Cursor:     repoCursor,
				Limit:      200,
			})
			if err != nil {
				return err
			}
			candidates = append(candidates, items...)
			if next == "" {
				break
			}
			repoCursor = next
		}
		// ② phase 派生条件 + phase/priority 过滤参数（priority 已内含于底层
		// List，这里统一兜一层保证 total_count 与条件严格一致）。
		items := make([]ReviewQueueItem, 0, len(candidates))
		for _, wi := range candidates {
			if !reviewQueuePhases[wi.Phase] {
				continue
			}
			if q.Phase != "" && wi.Phase != q.Phase {
				continue
			}
			if q.Priority != "" && wi.Priority != q.Priority {
				continue
			}
			item := ReviewQueueItem{WorkItem: wi}
			item.PendingSince = item.pendingSince()
			items = append(items, item)
		}
		// ③ 固定排序：pending_since ASC, priority DESC, id ASC。
		sort.SliceStable(items, func(a, b int) bool {
			ia, ib := items[a], items[b]
			sa, sb := ia.pendingSince(), ib.pendingSince()
			if !sa.Equal(sb) {
				return sa.Before(sb)
			}
			ra, rb := priorityRank(ia.WorkItem.Priority), priorityRank(ib.WorkItem.Priority)
			if ra != rb {
				return ra > rb
			}
			return ia.WorkItem.ID < ib.WorkItem.ID
		})
		page.TotalCount = len(items)
		// ④ 游标切片（排序键全列定位，不依赖底层 List 的 created_at 游标）。
		start := 0
		if cursor.WorkItemID != "" {
			for start < len(items) && !items[start].afterCursor(cursor) {
				start++
			}
		}
		end := start + limit
		if end > len(items) {
			end = len(items)
		}
		window := items[start:end]
		if end < len(items) && len(window) > 0 {
			last := window[len(window)-1]
			page.NextCursor = encodeReviewQueueCursor(reviewQueueCursor{
				Since: last.pendingSince(), Priority: last.WorkItem.Priority, WorkItemID: last.WorkItem.ID,
			})
		}
		// ⑤ 水位投影（仅本页行）：coordinator / latest run / comment revision。
		for i := range window {
			if err := s.hydrateReviewQueueItem(ctx, &window[i]); err != nil {
				return err
			}
		}
		page.Items = window
		// ⑥ as_of_event_seq：事务末尾读同一 snapshot 的 Workspace MAX(stream_seq)。
		seq, err := s.store.Events().LatestSeq(ctx, q.WorkspaceID)
		if err != nil {
			return err
		}
		for i := range page.Items {
			page.Items[i].Watermark.AsOfEventSeq = seq
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return page, nil
}

// hydrateReviewQueueItem 在同一事务内补齐行级投影：Coordinator 摘要、最新
// Run id/版本、评论 revision 水位。非 Coordinator Task 的 Coordinator 投影为
// nil（不视为错误）。
func (s *Service) hydrateReviewQueueItem(ctx context.Context, item *ReviewQueueItem) error {
	wi := item.WorkItem
	item.Watermark.WorkItemVersion = wi.Version
	var coordinatorState *domain.TaskCoordinatorState
	if state, err := s.store.TaskCoordinators().GetStateForWorkItem(ctx, wi.ID); err == nil {
		coordinatorState = state
		item.Coordinator = &ReviewQueueCoordinator{
			Status:    string(state.Status),
			Stage:     state.Phase,
			UpdatedAt: state.UpdatedAt,
			Version:   state.Version,
		}
		item.Watermark.CoordinatorVersion = state.Version
	} else if !isNotFoundErr(err) {
		return err
	}
	latestRunID, runCount, err := s.store.WorkItems().LatestRunID(ctx, wi.ID)
	if err != nil {
		return err
	}
	item.LatestRunID = latestRunID
	item.RunCount = runCount
	if latestRunID != "" {
		if run, runErr := s.store.Runs().Get(ctx, latestRunID); runErr == nil {
			item.Watermark.LatestRunVersion = run.Version
		} else if !isNotFoundErr(runErr) {
			return runErr
		}
	}
	// 评论水位只属于有评论流（Coordinator cursor 行）的 Task 树。
	if coordinatorState != nil {
		if rev, revErr := s.store.TaskComments().LatestRevision(ctx, coordinatorState.RootWorkItemID); revErr == nil {
			item.Watermark.CommentRevision = rev
		} else if !isNotFoundErr(revErr) {
			return revErr
		}
	}
	return nil
}

// isNotFoundErr 统一 ErrNotFound 判定（应用层投影把「无该来源」视为合法空）。
func isNotFoundErr(err error) bool {
	return errors.Is(err, domain.ErrNotFound)
}

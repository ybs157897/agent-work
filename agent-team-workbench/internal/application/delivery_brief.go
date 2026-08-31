// delivery_brief.go Delivery Brief 确定性服务端聚合（任务控制面 RFC §4.11/§9.6）。
//
// Brief 是 read model：不调 LLM、不持久化新摘要、不复用 rolling_digest 代替
// 完整证据。全部来源在同一 read transaction/snapshot 内聚合；事务末尾读取同一
// snapshot 的 Workspace MAX(stream_seq) 作为 as_of_event_seq。
//
// 来源与排序（RFC §4.11）：
//   - attempts：Run + TaskCoordinatorEvent 关联，按 attempt number（并列按
//     created_at+id）；runs：created_at+id；files：path；artifacts：
//     logical_path+id；comments：root revision。
//   - evidence 只收白名单结构化事实（coordinator_event/run_status/
//     evaluation_verdict/tool_result/artifact/change_set）；普通 Markdown 不是
//     证据（正文继续只由 AgentOutput 渲染，§5.3）。
//   - trust 三态：控制面落库事实=control_plane；runner/adapter 结构化上报=
//     runtime_reported；模型文本产物（verdict）=model_reported。
//   - 上限：attempts 50 / runs 100 / evidence 200 / files 200 / artifacts 100 /
//     comments 200 / risks 100；超限 truncation 对应位 true，完整总数保留在本
//     结构（*Total 字段；冻结 openapi 暂只暴露 change set 的 total_* 与 truncation 位）。
//   - 任一非权限来源读取失败 → 可用部分 + missing_sources + state=partial；
//     Chat 边界整体 fail closed（返回错误，绝不返回 partial brief）。
package application

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/filechanges"
)

// Brief 各段上限（RFC §4.11）。
const (
	briefAttemptLimit  = 50
	briefRunLimit      = 100
	briefEvidenceLimit = 200
	briefFileLimit     = 200
	briefArtifactLimit = 100
	briefCommentLimit  = 200
	briefRiskLimit     = 100
)

// briefRestrictedArtifacts 需要更高权限（runtime 管理）才进 Brief 的产物
// classification 闭集；public/internal/空 对 PermRead 可见。库内默认
// classification=internal（0001），因此现网数据不受该过滤影响。
var briefRestrictedArtifacts = map[string]bool{"restricted": true, "secret": true}

// BriefOptions Brief 聚合选项。
type BriefOptions struct {
	// IncludeRestrictedArtifacts：调用方具备 runtime 管理权限（admin/owner）时
	// true；false 时 restricted/secret 产物被权限过滤（总数仍计全量）。
	IncludeRestrictedArtifacts bool
}

// BriefConclusion Coordinator 结论投影（非 Coordinator Task 字段为空，version=0）。
type BriefConclusion struct {
	CoordinatorStatus string
	Stage             string
	Summary           string
	NextAction        string
	Version           int
}

// BriefAttempt 一次尝试的服务端摘要（AttemptSummary）。
type BriefAttempt struct {
	Attempt    int
	Role       string // coordinator | worker | evaluation
	RunID      string
	AgentID    string
	AgentName  string
	Status     string
	StartedAt  *time.Time
	FinishedAt *time.Time
	RetryOf    string
	Failure    *domain.RunFailure
}

// BriefEvidence 证据条目（EvidenceItem）。
type BriefEvidence struct {
	ID         string
	SourceKind string // coordinator_event | run_status | evaluation_verdict | tool_result | artifact | change_set
	SourceID   string
	Label      string
	Status     string // passed | failed | warning | unknown
	Trust      string // control_plane | runtime_reported | model_reported
	OccurredAt time.Time
}

// BriefRunEvidence 单 Run 证据包（RunEvidence）。
type BriefRunEvidence struct {
	Run           *domain.ExecutionRun
	Summary       string
	Evidence      []BriefEvidence
	EvidenceTotal int
	Truncated     bool
}

// BriefFileChange 变更文件行（path, added, deleted, status）。
type BriefFileChange struct {
	Path    string
	Added   int
	Deleted int
	Status  string // added | modified | deleted | renamed
}

// BriefChangeSet 按 Run 分组的既有 run file-change read model（不混多 Run）。
type BriefChangeSet struct {
	RunID        string
	Files        []BriefFileChange
	TotalFiles   int
	TotalAdded   int
	TotalDeleted int
	Truncated    bool
}

// BriefBlocker 结构化 blocker 投影。
type BriefBlocker struct {
	Code      string
	Message   string
	Source    string
	RunID     *string
	CreatedAt time.Time
}

// BriefRisk 风险条目（RiskItem）。
type BriefRisk struct {
	SourceKind string // blocker | run_failure | coordinator_error | review_finding
	SourceID   string
	Code       string
	Message    string
	Severity   string // low | medium | high
}

// BriefFreshness 一致性水位。
type BriefFreshness struct {
	GeneratedAt    time.Time
	AsOfEventSeq   int64
	SourceVersions map[string]int64
	State          string // current | partial
	MissingSources []string
}

// BriefTruncation 各段超限标记。
type BriefTruncation struct {
	Attempts  bool
	Runs      bool
	Files     bool
	Artifacts bool
	Comments  bool
}

// DeliveryBrief 验收交付简报（服务端确定性聚合）。
type DeliveryBrief struct {
	WorkItem           *domain.WorkItem
	AcceptanceCriteria []string
	Conclusion         BriefConclusion
	Attempts           []BriefAttempt
	AttemptsTotal      int
	Runs               []BriefRunEvidence
	RunsTotal          int
	// LatestRunID 最新 Run（created_at+id 序）；无 Run 为空。
	LatestRunID string
	// Changes 是按 Run 分组的全部变更组（组内绝不混 Run，按 run created_at+id
	// 正序）；HTTP 契约目前只暴露单个可空 BriefChangeSet，取最后一组（最新有
	// 变更的 Run）。
	Changes        []BriefChangeSet
	Artifacts      []*domain.Artifact
	ArtifactsTotal int
	Blocker        *BriefBlocker
	Risks          []BriefRisk
	Comments       []*domain.TaskComment
	CommentsTotal  int
	Freshness      BriefFreshness
	Truncation     BriefTruncation
}

// briefAgg 聚合过程中的可变状态：非权限来源失败 → missing，继续收集可用部分。
type briefAgg struct {
	missing []string
}

func (a *briefAgg) miss(source string, err error) {
	a.missing = append(a.missing, source)
	log.Printf("brief: 来源 %s 读取失败（partial）: %v", source, err)
}

// DeliveryBrief 聚合指定 work item 的交付简报。Task/Chat 边界 fail closed：
// Chat 记录返回 422，绝不返回 partial brief。
func (s *Service) DeliveryBrief(ctx context.Context, workItemID string, opts BriefOptions) (*DeliveryBrief, error) {
	brief := &DeliveryBrief{
		Attempts:   []BriefAttempt{},
		Runs:       []BriefRunEvidence{},
		Artifacts:  []*domain.Artifact{},
		Risks:      []BriefRisk{},
		Comments:   []*domain.TaskComment{},
		Freshness:  BriefFreshness{GeneratedAt: time.Now().UTC(), State: "current"},
		Truncation: BriefTruncation{},
	}
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		// ① 边界与基准：work item 必须存在且是 Task（fail closed，不 partial）。
		wi, err := s.store.WorkItems().Get(ctx, workItemID)
		if err != nil {
			return err
		}
		if !isTaskWorkItem(wi) {
			return fmt.Errorf("%w: delivery brief 只对 Task 记录开放", domain.ErrValidation)
		}
		brief.WorkItem = wi
		brief.AcceptanceCriteria = append([]string(nil), wi.AcceptanceCriteria...)
		if len(brief.AcceptanceCriteria) == 0 {
			brief.AcceptanceCriteria = []string{}
		}

		agg := &briefAgg{}
		rootID, rootErr := s.workItemTreeRootID(ctx, wi.ID)
		if rootErr != nil {
			return rootErr // 树解析失败无法定位评论边界：fail closed
		}

		// ② Coordinator 结论（无 state 是合法空，不是 missing）。
		var state *domain.TaskCoordinatorState
		if st, stateErr := s.store.TaskCoordinators().GetStateForWorkItem(ctx, wi.ID); stateErr == nil {
			state = st
			brief.Conclusion = BriefConclusion{
				CoordinatorStatus: string(st.Status),
				Stage:             st.Phase,
				Summary:           st.Summary,
				NextAction:        st.CurrentAction,
				Version:           st.Version,
			}
		} else if !errors.Is(stateErr, domain.ErrNotFound) {
			agg.miss("coordinator", stateErr)
		}

		// ③ Coordinator 事件（attempts/evidence 的控制面来源；causal 正序）。
		var coordinatorEvents []*domain.TaskCoordinatorEvent
		if state != nil {
			events, evErr := s.store.TaskCoordinators().ListEvents(ctx, wi.ID, briefEvidenceLimit)
			if evErr != nil {
				agg.miss("coordinator_events", evErr)
			} else {
				coordinatorEvents = events
			}
		}

		// ④ Runs（created_at+id 正序；全量计数，窗口内做证据聚合）。
		var runs []*domain.ExecutionRun
		runsListed := false
		if listed, runsErr := s.store.Runs().ListByWorkItem(ctx, wi.ID); runsErr != nil {
			agg.miss("runs", runsErr)
		} else {
			runs, runsListed = listed, true
			sort.SliceStable(runs, func(a, b int) bool {
				if !runs[a].CreatedAt.Equal(runs[b].CreatedAt) {
					return runs[a].CreatedAt.Before(runs[b].CreatedAt)
				}
				return runs[a].ID < runs[b].ID
			})
		}
		if runsListed {
			brief.RunsTotal = len(runs)
			brief.AttemptsTotal = len(runs)
			if len(runs) > 0 {
				brief.LatestRunID = runs[len(runs)-1].ID
			}
			brief.Truncation.Runs = len(runs) > briefRunLimit
			brief.Truncation.Attempts = len(runs) > briefAttemptLimit

			agents := map[string]*domain.AgentProfile{}
			runWindow := runs
			if len(runWindow) > briefRunLimit {
				runWindow = runWindow[:briefRunLimit]
			}
			for i, run := range runWindow {
				events, listErr := s.store.Events().ListRunEvents(ctx, run.ID)
				if listErr != nil {
					agg.miss("run_events:"+run.ID, listErr)
					events = nil
				}
				// 证据包与按 Run 分组的变更组（不混 Run）。
				brief.Runs = append(brief.Runs, s.briefRunEvidence(ctx, run, events, coordinatorEvents))
				if changes := s.briefChangeSet(ctx, run); changes != nil {
					brief.Changes = append(brief.Changes, *changes)
				}
				// attempts 与 runs 同源（1 run = 1 attempt），窗口取前 50。
				if i < briefAttemptLimit {
					item := s.briefAttempt(ctx, run, i, events, coordinatorEvents, agents, agg)
					brief.Attempts = append(brief.Attempts, item)
				}
			}
			sort.SliceStable(brief.Attempts, func(a, b int) bool {
				if brief.Attempts[a].Attempt != brief.Attempts[b].Attempt {
					return brief.Attempts[a].Attempt < brief.Attempts[b].Attempt
				}
				return brief.Attempts[a].RunID < brief.Attempts[b].RunID
			})
			for _, cs := range brief.Changes {
				if cs.Truncated {
					brief.Truncation.Files = true
				}
			}

			// ⑤ Artifacts（classification 权限过滤；logical_path+id 排序）。
			s.collectBriefArtifacts(ctx, runWindow, opts, brief, agg)
		}

		// ⑥ Blocker 与 risks（risk 窗口上限 100）。
		if blocker, bErr := s.store.WorkItems().ActiveBlocker(ctx, wi.ID); bErr == nil {
			brief.Blocker = &BriefBlocker{
				Code: blocker.Code, Message: blocker.Message, Source: blocker.Source,
				CreatedAt: blocker.CreatedAt,
			}
			brief.Risks = append(brief.Risks, BriefRisk{
				SourceKind: "blocker", SourceID: blocker.ID, Code: blocker.Code,
				Message: blocker.Message, Severity: "high",
			})
		} else if !errors.Is(bErr, domain.ErrNotFound) {
			agg.miss("blocker", bErr)
		}
		if state != nil && state.LastError != "" {
			brief.appendRisk(BriefRisk{
				SourceKind: "coordinator_error", SourceID: state.ID, Code: "coordinator_error",
				Message: state.LastError, Severity: "medium",
			})
		}
		for _, run := range runs {
			if run.Failure == nil {
				continue
			}
			severity := "high"
			if run.Failure.Retryable {
				severity = "medium"
			}
			brief.appendRisk(BriefRisk{
				SourceKind: "run_failure", SourceID: run.ID, Code: run.Failure.Code,
				Message: run.Failure.Message, Severity: severity,
			})
		}

		// ⑦ Comments：requirement/review_feedback 按 root revision（分页取全量计数）。
		s.collectBriefComments(ctx, rootID, brief, agg)
		for _, c := range brief.Comments {
			if len(brief.Risks) >= briefRiskLimit {
				break
			}
			if c.Kind != domain.CommentReviewFeedback {
				continue
			}
			brief.appendRisk(BriefRisk{
				SourceKind: "review_finding", SourceID: c.ID, Code: string(c.Kind),
				Message: c.Body, Severity: "medium",
			})
		}

		// ⑧ Freshness：事务末尾读同一 snapshot 的 MAX(stream_seq)。
		seq, seqErr := s.store.Events().LatestSeq(ctx, wi.WorkspaceID)
		if seqErr != nil {
			agg.miss("event_seq", seqErr)
		}
		brief.Freshness.AsOfEventSeq = seq
		sourceVersions := map[string]int64{"work_item": int64(wi.Version)}
		if state != nil {
			sourceVersions["coordinator"] = int64(state.Version)
			if rev, revErr := s.store.TaskComments().LatestRevision(ctx, state.RootWorkItemID); revErr == nil {
				sourceVersions["comment_revision"] = rev
			} else if !errors.Is(revErr, domain.ErrNotFound) {
				agg.miss("comments", revErr)
			}
		}
		if len(runs) > 0 {
			sourceVersions["latest_run"] = int64(runs[len(runs)-1].Version)
		}
		brief.Freshness.SourceVersions = sourceVersions
		if len(agg.missing) > 0 {
			brief.Freshness.State = "partial"
			brief.Freshness.MissingSources = agg.missing
		} else {
			brief.Freshness.MissingSources = []string{}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return brief, nil
}

// appendRisk 风险窗口收口（上限 100，超限丢弃并在总数语义上保守——风险段无
// 独立计数字段，超限以 truncation 之外的下限保护响应规模）。
func (b *DeliveryBrief) appendRisk(r BriefRisk) {
	if len(b.Risks) >= briefRiskLimit {
		return
	}
	b.Risks = append(b.Risks, r)
}

// briefAttempt 单 Run → AttemptSummary：attempt/role 来自 Run input 的
// Coordinator envelope；started_at 优先 run.started 事件，缺失回退 Coordinator
// 派发事件；agent 名带 memo 读取。
func (s *Service) briefAttempt(ctx context.Context, run *domain.ExecutionRun, seq int,
	events []RunEvent, coordinatorEvents []*domain.TaskCoordinatorEvent,
	agents map[string]*domain.AgentProfile, agg *briefAgg) BriefAttempt {
	attempt, role := briefAttemptRole(run)
	if attempt == 0 {
		attempt = seq + 1 // 非编排 Run：按时间序落位
	}
	item := BriefAttempt{
		Attempt: attempt, Role: role, RunID: run.ID,
		AgentID: run.AgentProfileID, Status: string(run.Status),
		FinishedAt: run.FinishedAt, RetryOf: run.RetryOf, Failure: run.Failure,
	}
	if name, ok := briefAgentName(ctx, s, agents, run.AgentProfileID); ok {
		item.AgentName = name
	} else if run.AgentProfileID != "" {
		agg.miss("agents", fmt.Errorf("agent %s 不可读", run.AgentProfileID))
	}
	item.StartedAt = briefRunStartedAt(events)
	if item.StartedAt == nil {
		item.StartedAt = briefCoordinatorStartedAt(run.ID, coordinatorEvents)
	}
	if status := briefRunLatestStatus(events, string(run.Status)); status != "" {
		item.Status = status
	}
	return item
}

// briefAttemptRole 从 Run input 的 Coordinator envelope 解析 attempt 序号与
// role；非编排 Run 返回 (0, worker)（attempt 由调用方按时间序落位）。
func briefAttemptRole(run *domain.ExecutionRun) (int, string) {
	coord := coordinatorContextOf(run)
	if coord == nil {
		return 0, "worker"
	}
	attempt := coordinatorAttemptValue(coord["attempt"])
	switch coord["role"] {
	case coordinatorRole:
		if action, _ := coord["action"].(string); action == "evaluation" {
			return attempt, "evaluation"
		}
		return attempt, "coordinator"
	default:
		return attempt, "worker"
	}
}

// briefRunStartedAt run.started 事件的精确时间（未起跑的 queued Run 为 nil）。
func briefRunStartedAt(events []RunEvent) *time.Time {
	for _, ev := range events {
		if ev.EventType == domain.EventRunStarted {
			at := ev.OccurredAt
			return &at
		}
	}
	return nil
}

// briefRunLatestStatus 事件流里的最新状态标签（事件缺失时保留 run 行状态）。
func briefRunLatestStatus(events []RunEvent, fallback string) string {
	status := ""
	for _, ev := range events {
		switch ev.EventType {
		case domain.EventRunStatusChanged, domain.EventRunCompleted,
			domain.EventRunFailed, domain.EventRunCancelled, domain.EventRunLost:
			if s, _ := ev.Payload["status"].(string); s != "" {
				status = s
			}
		}
	}
	if status == "" {
		return fallback
	}
	return status
}

// briefCoordinatorStartedAt run.started 缺失时的回退：Coordinator 派发事件时间。
func briefCoordinatorStartedAt(runID string, events []*domain.TaskCoordinatorEvent) *time.Time {
	var fallback *time.Time
	for _, ev := range events {
		if ev.RunID != runID {
			continue
		}
		at := ev.OccurredAt
		if fallback == nil || at.Before(*fallback) {
			fallback = &at
		}
	}
	return fallback
}

// briefAgentName 带 memo 的 agent 读取；ok=false 表示读取失败（missing 来源）。
func briefAgentName(ctx context.Context, s *Service, memo map[string]*domain.AgentProfile, agentID string) (string, bool) {
	if agentID == "" {
		return "", true
	}
	if a, ok := memo[agentID]; ok {
		if a == nil {
			return "", false
		}
		return a.Name, true
	}
	a, err := s.store.Agents().Get(ctx, agentID)
	if err != nil {
		memo[agentID] = nil
		return "", false
	}
	memo[agentID] = a
	return a.Name, true
}

// collectBriefArtifacts 汇总窗口内 Run 的产物：classification 权限过滤、
// logical_path+id 排序、上限截断（总数保留）。
func (s *Service) collectBriefArtifacts(ctx context.Context, runs []*domain.ExecutionRun,
	opts BriefOptions, brief *DeliveryBrief, agg *briefAgg) {
	for _, run := range runs {
		arts, err := s.store.Runs().ListArtifacts(ctx, run.ID)
		if err != nil {
			agg.miss("artifacts:"+run.ID, err)
			continue
		}
		brief.ArtifactsTotal += len(arts)
		for _, art := range arts {
			if !opts.IncludeRestrictedArtifacts && briefRestrictedArtifacts[art.Classification] {
				continue
			}
			brief.Artifacts = append(brief.Artifacts, art)
		}
	}
	sort.SliceStable(brief.Artifacts, func(a, b int) bool {
		if brief.Artifacts[a].LogicalPath != brief.Artifacts[b].LogicalPath {
			return brief.Artifacts[a].LogicalPath < brief.Artifacts[b].LogicalPath
		}
		return brief.Artifacts[a].ID < brief.Artifacts[b].ID
	})
	brief.Truncation.Artifacts = brief.ArtifactsTotal > briefArtifactLimit
	if len(brief.Artifacts) > briefArtifactLimit {
		brief.Artifacts = brief.Artifacts[:briefArtifactLimit]
	}
}

// collectBriefComments 按 root revision 正序分页取全部评论，过滤
// requirement/review_feedback，保留前 200 条并保留全量计数。
func (s *Service) collectBriefComments(ctx context.Context, rootID string, brief *DeliveryBrief, agg *briefAgg) {
	const pageSize = 200
	after := int64(0)
	for {
		page, err := s.store.TaskComments().ListByRoot(ctx, rootID, after, pageSize)
		if err != nil {
			agg.miss("comments", err)
			break
		}
		if len(page) == 0 {
			break
		}
		for _, c := range page {
			if c.Kind == domain.CommentRequirement || c.Kind == domain.CommentReviewFeedback {
				brief.CommentsTotal++
				if len(brief.Comments) < briefCommentLimit {
					brief.Comments = append(brief.Comments, c)
				}
			}
		}
		if len(page) < pageSize {
			break
		}
		after = page[len(page)-1].Revision
	}
	brief.Truncation.Comments = brief.CommentsTotal > len(brief.Comments)
}

// briefRunEvidence 单 Run 证据包：白名单结构化事件 → EvidenceItem（trust 三态）；
// 评估 run 追加 verdict 证据（确定性解析，不调 LLM）；证据按 occurred_at+id
// 稳定排序，超 200 截断并保留总数。summary 是最终助手文本的确定性摘录（无 LLM）。
func (s *Service) briefRunEvidence(ctx context.Context, run *domain.ExecutionRun,
	events []RunEvent, coordinatorEvents []*domain.TaskCoordinatorEvent) BriefRunEvidence {
	out := BriefRunEvidence{Run: run, Evidence: []BriefEvidence{}}
	for _, ev := range events {
		if item, ok := briefEvidenceFromRunEvent(run.ID, ev); ok {
			out.Evidence = append(out.Evidence, item)
		}
	}
	for _, ce := range coordinatorEvents {
		if ce.RunID != run.ID {
			continue
		}
		out.Evidence = append(out.Evidence, BriefEvidence{
			ID: "coord:" + ce.ID, SourceKind: "coordinator_event", SourceID: ce.ID,
			Label: ce.Summary, Status: briefCoordinatorEventStatus(ce),
			Trust: "control_plane", OccurredAt: ce.OccurredAt,
		})
	}
	// 评估结论：verdict 围栏是模型文本产物 → model_reported；解析失败 warning。
	if eval, _ := run.Input["evaluation"].(bool); eval {
		item := BriefEvidence{
			ID: "verdict:" + run.ID, SourceKind: "evaluation_verdict", SourceID: run.ID,
			Label: "评估结论", Trust: "model_reported", OccurredAt: run.UpdatedAt,
		}
		text, err := s.runFinalText(ctx, run.ID)
		switch {
		case err != nil:
			item.Status = "unknown"
		default:
			block, ok := extractLastFencedBlock(text, "verdict")
			if !ok {
				item.Status = "warning"
			} else if pass, _, perr := parseVerdict(block); perr != nil {
				item.Status = "warning"
			} else if pass {
				item.Status = "passed"
			} else {
				item.Status = "failed"
			}
		}
		out.Evidence = append(out.Evidence, item)
	}
	sort.SliceStable(out.Evidence, func(a, b int) bool {
		if !out.Evidence[a].OccurredAt.Equal(out.Evidence[b].OccurredAt) {
			return out.Evidence[a].OccurredAt.Before(out.Evidence[b].OccurredAt)
		}
		return out.Evidence[a].ID < out.Evidence[b].ID
	})
	out.EvidenceTotal = len(out.Evidence)
	out.Truncated = out.EvidenceTotal > briefEvidenceLimit
	if out.Truncated {
		out.Evidence = out.Evidence[:briefEvidenceLimit]
	}
	out.Summary = truncateRunes(completedOrDeltaText(mainAgentEvents(events)), 280)
	return out
}

// briefEvidenceFromRunEvent 白名单结构化 run 事件 → 证据条目；普通
// message.delta/completed 等 Markdown 不进证据（§5.3：正文只由 AgentOutput 渲染）。
func briefEvidenceFromRunEvent(runID string, ev RunEvent) (BriefEvidence, bool) {
	switch ev.EventType {
	case domain.EventRunCreated, domain.EventRunStatusChanged, domain.EventRunCompleted,
		domain.EventRunFailed, domain.EventRunCancelled, domain.EventRunLost:
		status := "unknown"
		switch ev.EventType {
		case domain.EventRunCompleted:
			status = "passed"
		case domain.EventRunFailed:
			status = "failed"
		case domain.EventRunCancelled, domain.EventRunLost:
			status = "warning"
		}
		label := "run"
		if s, _ := ev.Payload["status"].(string); s != "" {
			label = "run " + s
		}
		return BriefEvidence{
			ID: fmt.Sprintf("ev:%s:%d", runID, ev.RunSeq), SourceKind: "run_status",
			SourceID: runID, Label: label, Status: status, Trust: "control_plane",
			OccurredAt: ev.OccurredAt,
		}, true
	case domain.EventToolStarted, domain.EventToolCompleted, domain.EventToolFailed:
		status := "unknown"
		switch ev.EventType {
		case domain.EventToolCompleted:
			status = "passed"
		case domain.EventToolFailed:
			status = "failed"
		}
		label, _ := ev.Payload["name"].(string)
		if label == "" {
			label = ev.EventType
		}
		return BriefEvidence{
			ID: fmt.Sprintf("ev:%s:%d", runID, ev.RunSeq), SourceKind: "tool_result",
			SourceID: runID, Label: label, Status: status, Trust: "runtime_reported",
			OccurredAt: ev.OccurredAt,
		}, true
	case domain.EventArtifactCreated, domain.EventArtifactUpdated:
		path, _ := ev.Payload["logical_path"].(string)
		return BriefEvidence{
			ID: fmt.Sprintf("ev:%s:%d", runID, ev.RunSeq), SourceKind: "artifact",
			SourceID: path, Label: path, Status: "unknown", Trust: "runtime_reported",
			OccurredAt: ev.OccurredAt,
		}, true
	default:
		return BriefEvidence{}, false
	}
}

func briefCoordinatorEventStatus(ce *domain.TaskCoordinatorEvent) string {
	status, _ := ce.Data["status"].(string)
	switch status {
	case "succeeded", "completed", "passed":
		return "passed"
	case "failed":
		return "failed"
	default:
		return "unknown"
	}
}

// briefChangeSet 复用既有 run file-change read model（runSnapshots +
// BuildPerRunSummary）：严格单 Run 分组，files 按 path 排序（fold 已排序），
// 超 200 截断且 total_files 保留全量。无变更快照的 Run 返回 nil。
func (s *Service) briefChangeSet(ctx context.Context, run *domain.ExecutionRun) *BriefChangeSet {
	_, turns, raw, err := s.runSnapshots(ctx, run.ID)
	if err != nil {
		return nil // 无快照等不可用情形：该 Run 无变更组
	}
	sum := filechanges.BuildPerRunSummary(turns)
	rawByPath := make(map[string]snapshotEvent, len(raw))
	for _, event := range raw {
		rawByPath[event.Path] = event
	}
	set := &BriefChangeSet{
		RunID: run.ID, Files: []BriefFileChange{},
		TotalFiles: sum.FileCount, TotalAdded: sum.Added, TotalDeleted: sum.Removed,
		Truncated: sum.FileCount > briefFileLimit,
	}
	for _, f := range sum.Files {
		if len(set.Files) >= briefFileLimit {
			break
		}
		status := "modified"
		if event, ok := rawByPath[f.Path]; ok {
			switch {
			case !event.BeforeExists:
				status = "added"
			case !event.AfterExists:
				status = "deleted"
			}
		}
		set.Files = append(set.Files, BriefFileChange{
			Path: f.Path, Added: f.Added, Deleted: f.Removed, Status: status,
		})
	}
	return set
}

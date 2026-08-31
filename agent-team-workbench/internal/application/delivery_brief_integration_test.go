package application_test

// delivery_brief_integration_test.go Delivery Brief 聚合行为（任务控制面
// RFC §4.11/§9.6/§15.5）：
//   - 全字段聚合：criteria/结论/attempts/runs 证据/evidence 白名单与 trust/
//     按 Run 分组变更/artifacts/评论过滤/risks；
//   - 稳定排序：runs created_at+id、files path、comments revision、attempts；
//   - truncation 位与完整总数；同一 read tx 的 as_of_event_seq；
//   - 非权限来源失败 → partial + missing_sources；Chat 边界 fail closed。

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestDeliveryBriefCoordinatorSources(t *testing.T) {
	ctx, svc, store, _, wsID, _ := seedCoordinatorEnv(t)

	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "简报任务", AutoCoordinate: true,
		AcceptanceCriteria: []string{"AC-1", "AC-2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if root.Version == 0 {
		t.Fatalf("前置：root 应已创建")
	}
	setCoordinatorWaitingUser(t, ctx, store, root.ID)

	// 评论：requirement(rev1) + note(rev2, 不进 brief)；requirement 使根回 execution。
	if _, err := svc.AppendTaskComment(ctx, application.AppendTaskCommentParams{
		WorkItemID: root.ID, Kind: domain.CommentRequirement, Body: "补充要求",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AppendTaskComment(ctx, application.AppendTaskCommentParams{
		WorkItemID: root.ID, Kind: domain.CommentNote, Body: "普通备注",
	}); err != nil {
		t.Fatal(err)
	}
	// review_feedback 只能由 ReturnWorkItem 生成：根钉回 review + Coordinator waiting_user。
	pinPhaseDirect(t, ctx, store, root.ID, domain.PhaseReview, time.Now().UTC())
	setCoordinatorWaitingUser(t, ctx, store, root.ID)
	rootNow, err := store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	returned, err := svc.ReturnWorkItem(ctx, root.ID, "打回理由", rootNow.Version)
	if err != nil {
		t.Fatal(err)
	}
	if returned.Phase != domain.PhaseExecution {
		t.Fatalf("打回后应回 execution: %+v", returned)
	}

	brief, err := svc.DeliveryBrief(ctx, root.ID, application.BriefOptions{IncludeRestrictedArtifacts: true})
	if err != nil {
		t.Fatal(err)
	}

	if fmt.Sprint(brief.AcceptanceCriteria) != fmt.Sprint([]string{"AC-1", "AC-2"}) {
		t.Fatalf("criteria 不符: %v", brief.AcceptanceCriteria)
	}
	// 结论来自 Coordinator state（Return 后 durable queued）。
	if brief.Conclusion.CoordinatorStatus == "" || brief.Conclusion.Version == 0 {
		t.Fatalf("conclusion 应来自 Coordinator state: %+v", brief.Conclusion)
	}
	// attempts 覆盖 Coordinator run：role=coordinator、按 attempt number 排序。
	if len(brief.Attempts) == 0 {
		t.Fatalf("attempts 不应为空")
	}
	for i := 1; i < len(brief.Attempts); i++ {
		if brief.Attempts[i-1].Attempt > brief.Attempts[i].Attempt {
			t.Fatalf("attempts 未按 attempt number 排序: %+v", brief.Attempts)
		}
	}
	var coordinatorRoleSeen bool
	for _, a := range brief.Attempts {
		if a.Role == "coordinator" {
			coordinatorRoleSeen = true
		}
		switch a.Role {
		case "coordinator", "worker", "evaluation":
		default:
			t.Fatalf("role 闭集外: %+v", a)
		}
	}
	if !coordinatorRoleSeen {
		t.Fatalf("Coordinator run 的 attempts role 应为 coordinator: %+v", brief.Attempts)
	}

	// comments：requirement/review_feedback 按 revision 正序，note 被过滤。
	if brief.CommentsTotal != 2 || len(brief.Comments) != 2 {
		t.Fatalf("评论应只有 requirement+review_feedback: total=%d n=%d", brief.CommentsTotal, len(brief.Comments))
	}
	if brief.Comments[0].Kind != domain.CommentRequirement || brief.Comments[1].Kind != domain.CommentReviewFeedback {
		t.Fatalf("评论种类/顺序不符: %+v", brief.Comments)
	}
	// risks：review_finding 来自结构化 review_feedback。
	risks := map[string]bool{}
	for _, r := range brief.Risks {
		risks[r.SourceKind] = true
	}
	if !risks["review_finding"] {
		t.Fatalf("risks 应含 review_finding: %v", risks)
	}

	// freshness：同一 read tx 的 MAX(stream_seq)；无失败来源 → current。
	if brief.Freshness.State != "current" || len(brief.Freshness.MissingSources) != 0 {
		t.Fatalf("freshness 应 current: %+v", brief.Freshness)
	}
	seq, err := store.Events().LatestSeq(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	if brief.Freshness.AsOfEventSeq != seq {
		t.Fatalf("as_of_event_seq 应等于 MAX(stream_seq): %d vs %d", brief.Freshness.AsOfEventSeq, seq)
	}
	if brief.Freshness.SourceVersions["work_item"] != int64(brief.WorkItem.Version) {
		t.Fatalf("source_versions.work_item 不符: %+v", brief.Freshness.SourceVersions)
	}
	if brief.Freshness.SourceVersions["comment_revision"] == 0 {
		t.Fatalf("source_versions.comment_revision 不应为 0: %+v", brief.Freshness.SourceVersions)
	}
	if brief.Truncation.Attempts || brief.Truncation.Runs || brief.Truncation.Files ||
		brief.Truncation.Artifacts || brief.Truncation.Comments {
		t.Fatalf("未超限时 truncation 全为 false: %+v", brief.Truncation)
	}
}

func TestDeliveryBriefRunEvidenceChangesAndRisks(t *testing.T) {
	ctx, svc, _, _, wsID, workerID := seedCoordinatorEnv(t)

	// 普通根 Task：允许直建 run；两轮——成功（带证据/变更/产物）+ 失败。
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "普通简报任务", AcceptanceCriteria: []string{"跑通测试"},
	})
	if err != nil {
		t.Fatal(err)
	}
	good, err := svc.CreateRun(ctx, root.ID, application.CreateRunParams{AgentProfileID: workerID, Instruction: "成功一轮"})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, good.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.RecordRunEvent(ctx, good.ID, domain.EventMessageDelta,
		map[string]any{"role": "assistant", "text": "这是普通 Markdown，不是证据"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunEvent(ctx, good.ID, domain.EventToolCompleted,
		map[string]any{"name": "apply_patch", "file_change_snapshot": map[string]any{
			"workspace_root": t.TempDir(), "path": "b/b.md",
			"before_content": "old", "before_exists": true, "after_content": "new", "after_exists": true,
			"before_hash": "x", "after_hash": "y", "write_count": 1,
		}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunEvent(ctx, good.ID, domain.EventToolCompleted,
		map[string]any{"name": "run_tests"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, good.ID, domain.RunSucceeding, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, good.ID, domain.RunSucceeded, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordArtifact(ctx, good.ID, &domain.Artifact{
		Sha256: "deadbeef", LogicalPath: "out/report.md", Mime: "text/markdown",
	}); err != nil {
		t.Fatal(err)
	}
	bad, err := svc.CreateRun(ctx, root.ID, application.CreateRunParams{AgentProfileID: workerID, Instruction: "失败一轮"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, bad.ID, domain.RunFailed, map[string]any{
		"code": "transport", "message": "链路中断", "retryable": true,
	}); err != nil {
		t.Fatal(err)
	}

	brief, err := svc.DeliveryBrief(ctx, root.ID, application.BriefOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if fmt.Sprint(brief.AcceptanceCriteria) != fmt.Sprint([]string{"跑通测试"}) {
		t.Fatalf("criteria 不符: %v", brief.AcceptanceCriteria)
	}
	if brief.LatestRunID != bad.ID {
		t.Fatalf("latest_run 应为最新 run: %s vs %s", brief.LatestRunID, bad.ID)
	}
	// runs：created_at+id 正序。
	if len(brief.Runs) != 2 || brief.Runs[0].Run.ID != good.ID || brief.Runs[1].Run.ID != bad.ID {
		t.Fatalf("runs 排序不符: %+v", brief.Runs)
	}
	// evidence 白名单：tool_result(runtime_reported)/run_status(control_plane) 在，
	// 普通 Markdown 不在。
	first := brief.Runs[0]
	kinds := map[string]int{}
	trusts := map[string]string{}
	for _, ev := range first.Evidence {
		kinds[ev.SourceKind]++
		trusts[ev.SourceKind] = ev.Trust
	}
	if kinds["tool_result"] < 2 || kinds["run_status"] == 0 {
		t.Fatalf("evidence 应含 tool_result/run_status: %v", kinds)
	}
	if trusts["tool_result"] != "runtime_reported" || trusts["run_status"] != "control_plane" {
		t.Fatalf("trust 三态不符: %v", trusts)
	}
	for _, ev := range first.Evidence {
		if strings.Contains(ev.Label, "Markdown") {
			t.Fatalf("普通 Markdown 不得成为证据: %+v", ev)
		}
	}
	if first.EvidenceTotal != len(first.Evidence) || first.Truncated {
		t.Fatalf("evidence 总数/截断位不符: %d/%v", first.EvidenceTotal, first.Truncated)
	}
	// 失败 run 证据含 run_status=failed。
	failedEvidence := false
	for _, ev := range brief.Runs[1].Evidence {
		if ev.SourceKind == "run_status" && ev.Status == "failed" {
			failedEvidence = true
		}
	}
	if !failedEvidence {
		t.Fatalf("失败 run 证据缺 run_status=failed: %+v", brief.Runs[1].Evidence)
	}

	// changes：按 Run 分组、files 按 path、状态 modified、totals 完整。
	if len(brief.Changes) != 1 {
		t.Fatalf("只有成功 run 有变更组: %+v", brief.Changes)
	}
	cs := brief.Changes[0]
	if cs.RunID != good.ID || cs.TotalFiles != 1 || cs.TotalAdded <= 0 {
		t.Fatalf("变更组不符: %+v", cs)
	}
	if cs.Files[0].Path != "b/b.md" || cs.Files[0].Status != "modified" {
		t.Fatalf("files 应按 path 排序且状态正确: %+v", cs.Files)
	}
	if brief.Truncation.Files {
		t.Fatalf("未超限时 files 截断位应为 false")
	}

	// artifacts。
	if brief.ArtifactsTotal != 1 || len(brief.Artifacts) != 1 || brief.Artifacts[0].LogicalPath != "out/report.md" {
		t.Fatalf("artifacts 不符: %+v", brief.Artifacts)
	}
	// risks：run_failure（retryable → medium）。
	runFailureSeen := false
	for _, r := range brief.Risks {
		if r.SourceKind == "run_failure" && r.Severity == "medium" && r.Code == "transport" {
			runFailureSeen = true
		}
	}
	if !runFailureSeen {
		t.Fatalf("risks 应含 run_failure(medium): %+v", brief.Risks)
	}
}

func TestDeliveryBriefTruncationKeepsTotals(t *testing.T) {
	ctx, svc, store, _, wsID, workerID := seedCoordinatorEnv(t)
	wi, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "截断任务"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: workerID, Instruction: "第一轮"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	err = store.InTx(ctx, func(ctx context.Context) error {
		// 直插 105 个 run（runs 上限 100 / attempts 上限 50）。
		for i := 0; i < 105; i++ {
			r := &domain.ExecutionRun{
				ID: fmt.Sprintf("run_brief_%03d", i), WorkspaceID: wsID, WorkItemID: wi.ID,
				AgentProfileID: workerID, Status: domain.RunQueued, RuntimeLabel: "mock",
				Input: map[string]any{}, Version: 1,
				CreatedAt: now.Add(time.Duration(i) * time.Millisecond), UpdatedAt: now,
			}
			if err := store.Runs().Create(ctx, r); err != nil {
				return err
			}
			if i == 0 {
				for j := 0; j < 105; j++ {
					art := &domain.Artifact{
						ID: fmt.Sprintf("art_brief_%03d", j), RunID: r.ID,
						LogicalPath: fmt.Sprintf("p/%03d.md", j), Sha256: "x",
						Status: domain.ArtifactDraft, CreatedAt: now,
					}
					if err := store.Runs().CreateArtifact(ctx, art); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	brief, err := svc.DeliveryBrief(ctx, wi.ID, application.BriefOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if brief.RunsTotal != 106 || !brief.Truncation.Runs || len(brief.Runs) != 100 {
		t.Fatalf("runs 截断不符: total=%d truncated=%v n=%d", brief.RunsTotal, brief.Truncation.Runs, len(brief.Runs))
	}
	if brief.AttemptsTotal != 106 || !brief.Truncation.Attempts || len(brief.Attempts) != 50 {
		t.Fatalf("attempts 截断不符: total=%d truncated=%v n=%d", brief.AttemptsTotal, brief.Truncation.Attempts, len(brief.Attempts))
	}
	if brief.ArtifactsTotal != 105 || !brief.Truncation.Artifacts || len(brief.Artifacts) != 100 {
		t.Fatalf("artifacts 截断不符: total=%d truncated=%v n=%d", brief.ArtifactsTotal, brief.Truncation.Artifacts, len(brief.Artifacts))
	}
	if !sort.SliceIsSorted(brief.Artifacts, func(a, b int) bool {
		if brief.Artifacts[a].LogicalPath != brief.Artifacts[b].LogicalPath {
			return brief.Artifacts[a].LogicalPath < brief.Artifacts[b].LogicalPath
		}
		return brief.Artifacts[a].ID < brief.Artifacts[b].ID
	}) {
		t.Fatalf("artifacts 未按 logical_path+id 排序")
	}

	// 评论截断：205 条 requirement → total 205，保留 200（按 revision 正序）。
	root := wi.ID
	if err := store.TaskComments().EnsureCursor(ctx, root); err != nil {
		t.Fatal(err)
	}
	err = store.InTx(ctx, func(ctx context.Context) error {
		for i := 0; i < 205; i++ {
			c := &domain.TaskComment{
				ID: fmt.Sprintf("cmt_brief_%03d", i), WorkspaceID: wsID,
				RootWorkItemID: root, WorkItemID: root,
				Kind: domain.CommentRequirement, Body: fmt.Sprintf("要求 %d", i),
				ActorKind: domain.CommentActorUser, ActorID: "user_demo", CreatedAt: now,
			}
			if _, err := store.TaskComments().Append(ctx, c); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	brief2, err := svc.DeliveryBrief(ctx, wi.ID, application.BriefOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if brief2.CommentsTotal != 205 || !brief2.Truncation.Comments || len(brief2.Comments) != 200 {
		t.Fatalf("comments 截断不符: total=%d truncated=%v n=%d", brief2.CommentsTotal, brief2.Truncation.Comments, len(brief2.Comments))
	}
	for i := 1; i < len(brief2.Comments); i++ {
		if brief2.Comments[i-1].Revision >= brief2.Comments[i].Revision {
			t.Fatalf("comments 未按 revision 排序")
		}
	}
}

func TestDeliveryBriefPartialAndChatFailClosed(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)

	// chat 边界：整体 fail closed（4xx），绝不返回 partial brief。
	chat, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "对话", RecordKind: domain.RecordKindChat, AgentProfileID: workerID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeliveryBrief(ctx, chat.ID, application.BriefOptions{}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("chat 请求 brief 应 fail closed，实际 %v", err)
	}

	// 非权限来源读取失败 → 可用部分 + missing_sources + state=partial。
	wi, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "部分来源任务", AutoCoordinate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	broken := &briefBrokenStore{Store: store}
	partialSvc := application.NewService(broken, dispatcher, noopNotifier{}, nil)
	brief, err := partialSvc.DeliveryBrief(ctx, wi.ID, application.BriefOptions{})
	if err != nil {
		t.Fatalf("非权限来源失败应返回 partial 而非错误: %v", err)
	}
	if brief.Freshness.State != "partial" {
		t.Fatalf("state 应 partial: %+v", brief.Freshness)
	}
	found := false
	for _, m := range brief.Freshness.MissingSources {
		if m == "coordinator" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing_sources 应含 coordinator: %v", brief.Freshness.MissingSources)
	}
	if brief.WorkItem == nil || brief.WorkItem.ID != wi.ID {
		t.Fatalf("partial 仍应携带可用部分: %+v", brief.WorkItem)
	}
	_ = store
}

// briefBrokenStore 只破坏 Coordinator 读取，其余来源透传（partial 路径）。
type briefBrokenStore struct {
	application.Store
}

func (s *briefBrokenStore) TaskCoordinators() application.TaskCoordinatorRepo {
	return briefBrokenCoordRepo{s.Store.TaskCoordinators()}
}

type briefBrokenCoordRepo struct {
	application.TaskCoordinatorRepo
}

func (r briefBrokenCoordRepo) GetStateForWorkItem(ctx context.Context, workItemID string) (*domain.TaskCoordinatorState, error) {
	return nil, errors.New("simulated coordinator read failure")
}

package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/chatsources"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/orchestrator"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

func analysisConversationCatalog(t *testing.T, run *domain.ExecutionRun) (string, string) {
	t.Helper()
	rawInput, ok := run.Input["analysis"].(map[string]any)
	if !ok {
		t.Fatalf("analysis input missing: %#v", run.Input)
	}
	rawCatalog, err := json.Marshal(rawInput["source_catalog"])
	if err != nil {
		t.Fatal(err)
	}
	var catalog []struct {
		Kind   string `json:"kind"`
		Ref    string `json:"ref"`
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal(rawCatalog, &catalog); err != nil {
		t.Fatal(err)
	}
	for _, entry := range catalog {
		if entry.Kind == "conversation" {
			return entry.Ref, entry.SHA256
		}
	}
	t.Fatalf("conversation source missing: %s", string(rawCatalog))
	return "", ""
}

func analysisEnvelope(conversationRef, conversationSHA, attachmentID, attachmentSHA string, question bool) string {
	questions := "[]"
	if question {
		questions = `[{"id":"scope-choice","prompt":"是否包含编辑能力？","selection":"single","options":[{"id":"read","label":"只读"},{"id":"edit","label":"包含编辑"}],"item_ids":["scope"]}]`
	}
	sources := `{"id":"conversation-source","kind":"conversation","ref":"` + conversationRef + `","sha256":"` + conversationSHA + `","read_status":"read","coverage":"用户消息"}`
	sourceIDs := `"conversation-source"`
	if attachmentID != "" {
		sources = `{"id":"attachment-source","kind":"attachment","ref":"` + attachmentID + `","sha256":"` + attachmentSHA + `","read_status":"read","coverage":"正文"},` + sources
		sourceIDs = `"attachment-source","conversation-source"`
	}
	return "说明文字\n```atw-analysis\n{" +
		`"version":"chat-analysis/v1","summary":"需求分析摘要","sources":[` + sources + `],` +
		`"items":[{"id":"scope","kind":"requirement","title":"能力范围","detail":"需要确认实现边界。","source_ids":[` + sourceIDs + `],"basis":"observed","impact":"影响实现范围","recommendation":"逐项确认"}],` +
		`"questions":` + questions + "}\n```"
}

func TestChatAnalysisTerminalProjectionAndAnswerCAS(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	base := seedRunEnv(t, ctx, svc, store)
	chat, err := svc.CreateWorkItem(ctx, base.WorkspaceID, application.CreateWorkItemParams{
		Title: "需求分析", RecordKind: domain.RecordKindChat, AgentProfileID: base.AgentProfileID,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetChatSourceStore(chatsources.NewStore(t.TempDir()))
	source, replayed, err := svc.CreateChatSource(ctx, application.ChatSourceUpload{
		ChatWorkItemID: chat.ID, Filename: "scope.md", Data: []byte("范围材料"), ClientKey: "analysis-source",
	})
	if err != nil || replayed {
		t.Fatalf("create source: source=%+v replayed=%v err=%v", source, replayed, err)
	}
	run, err := svc.CreateRun(ctx, chat.ID, application.CreateRunParams{
		AgentProfileID: chat.AgentProfileID, Instruction: "整理需求，逐项确认",
		OutputContract: orchestrator.OutputContractChatAnalysisV1,
	})
	if err != nil {
		t.Fatal(err)
	}
	conversationRef, conversationSHA := analysisConversationCatalog(t, run)
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, run.ID, analysisEnvelope(conversationRef, conversationSHA, source.ID, source.SHA256, true)); err != nil {
		t.Fatal(err)
	}
	view, err := svc.GetChatAnalysis(ctx, chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Projection.Status != domain.ChatAnalysisNeedsAnswer || view.Projection.Revision != 1 ||
		view.CurrentQuestion == nil || view.PendingCount != 1 {
		t.Fatalf("analysis projection not awaiting one answer: %+v", view)
	}
	answerParams := application.SaveChatAnalysisAnswerParams{
		ChatWorkItemID: chat.ID, ExpectedVersion: view.Projection.Version, Revision: view.Projection.Revision,
		QuestionID: "scope-choice", SelectedOptionIDs: []string{"read"}, Disposition: "answered", ClientKey: "answer-1",
	}
	answered, replayed, err := svc.SaveChatAnalysisAnswer(ctx, answerParams)
	if err != nil || replayed || answered.Projection.Status != domain.ChatAnalysisReady || answered.AnsweredCount != 1 {
		t.Fatalf("save answer: projection=%+v replayed=%v err=%v", answered, replayed, err)
	}
	answerParams.ExpectedVersion = 1
	replayedView, replayed, err := svc.SaveChatAnalysisAnswer(ctx, answerParams)
	if err != nil || !replayed || replayedView.Projection.Status != domain.ChatAnalysisReady {
		t.Fatalf("same client key must replay current projection: view=%+v replayed=%v err=%v", replayedView, replayed, err)
	}
	answerParams.Revision++
	if _, _, err := svc.SaveChatAnalysisAnswer(ctx, answerParams); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("same client key reused across revisions must conflict: %v", err)
	}
	answerParams.Revision--
	answerParams.Text = "different"
	if _, _, err := svc.SaveChatAnalysisAnswer(ctx, answerParams); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("same client key with different answer must conflict: %v", err)
	}
	next, err := svc.CreateRun(ctx, chat.ID, application.CreateRunParams{
		AgentProfileID: chat.AgentProfileID, Instruction: "继续整理需求",
		OutputContract: orchestrator.OutputContractChatAnalysisV1,
	})
	if err != nil {
		t.Fatal(err)
	}
	analysisContext, _ := next.Input["analysis_context"].(string)
	if !strings.Contains(analysisContext, "此前有效分析文档") || !strings.Contains(analysisContext, "scope-choice") ||
		!strings.Contains(analysisContext, "此前已保存回答") || !strings.Contains(analysisContext, "conversation:run:"+run.ID) {
		t.Fatalf("next analysis Run must receive stable conversation, document and answer context: %q", analysisContext)
	}
}

func TestChatAnalysisInvalidAttemptPreservesPreviousRevision(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	base := seedRunEnv(t, ctx, svc, store)
	chat, err := svc.CreateWorkItem(ctx, base.WorkspaceID, application.CreateWorkItemParams{
		Title: "需求分析失败恢复", RecordKind: domain.RecordKindChat, AgentProfileID: base.AgentProfileID,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.CreateRun(ctx, chat.ID, application.CreateRunParams{
		AgentProfileID: chat.AgentProfileID, Instruction: "分析",
		OutputContract: orchestrator.OutputContractChatAnalysisV1,
	})
	if err != nil {
		t.Fatal(err)
	}
	conversationRef, conversationSHA := analysisConversationCatalog(t, run)
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	first := analysisEnvelope(conversationRef, conversationSHA, "", "", false)
	if err := finishRun(ctx, svc, run.ID, first); err != nil {
		t.Fatal(err)
	}
	view, err := svc.GetChatAnalysis(ctx, chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Projection.Status != domain.ChatAnalysisReady || view.Projection.Revision != 1 || view.Document == nil {
		t.Fatalf("valid first attempt should create a revision: %+v", view.Projection)
	}
	// No attachment is required for this regression; a second malformed attempt
	// must remain failed and never manufacture a document.
	second, err := svc.CreateRun(ctx, chat.ID, application.CreateRunParams{
		AgentProfileID: chat.AgentProfileID, Instruction: "再次分析",
		OutputContract: orchestrator.OutputContractChatAnalysisV1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, second.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, second.ID, "普通文本，没有 atw-analysis fence"); err != nil {
		t.Fatal(err)
	}
	view, err = svc.GetChatAnalysis(ctx, chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Projection.Status != domain.ChatAnalysisFailed || view.Projection.Revision != 1 || view.Document == nil {
		t.Fatalf("malformed attempt must preserve the last valid revision: %+v", view.Projection)
	}
}

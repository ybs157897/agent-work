package domain

import (
	"testing"
	"time"
)

func testQuestion() *QuestionRequest {
	return &QuestionRequest{
		ID: "question_1", RunID: "run_1", WorkItemID: "wi_1", SessionRef: "session_1", ProviderID: "provider_q_1",
		Questions: []QuestionItem{{ID: "q_0", Question: "颜色？", Options: []QuestionOption{{ID: "opt_0_0", Label: "蓝色"}, {ID: "opt_0_1", Label: "绿色"}}}},
		Status:    QuestionPending, CreatedAt: time.Now().UTC(),
	}
}

func TestQuestionResponseRoundTripAndIdempotency(t *testing.T) {
	q := testQuestion()
	response := QuestionResponse{Answers: map[string]QuestionAnswer{"q_0": {Kind: "single", OptionID: "opt_0_0"}}, Method: "click"}
	if err := q.PrepareResponse(response); err != nil {
		t.Fatal(err)
	}
	if q.Status != QuestionPending {
		t.Fatalf("provider delivery must keep pending status: %s", q.Status)
	}
	if err := q.CompleteResponse(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if q.Status != QuestionAnswered || q.Response == nil {
		t.Fatalf("question not answered: %+v", q)
	}
	if err := q.CompleteResponse(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func TestQuestionRejectsCrossQuestionOption(t *testing.T) {
	q := testQuestion()
	response := QuestionResponse{Answers: map[string]QuestionAnswer{"q_0": {Kind: "single", OptionID: "opt_other"}}}
	if err := q.PrepareResponse(response); err == nil {
		t.Fatal("unknown option must be rejected")
	}
}

func TestQuestionPreparedResponseIsImmutable(t *testing.T) {
	q := testQuestion()
	first := QuestionResponse{Answers: map[string]QuestionAnswer{"q_0": {Kind: "single", OptionID: "opt_0_0"}}}
	second := QuestionResponse{Answers: map[string]QuestionAnswer{"q_0": {Kind: "single", OptionID: "opt_0_1"}}}
	if err := q.PrepareResponse(first); err != nil {
		t.Fatal(err)
	}
	if err := q.PrepareResponse(first); err != nil {
		t.Fatal("same response should be idempotent")
	}
	if err := q.PrepareResponse(second); err == nil {
		t.Fatal("different concurrent response must be rejected")
	}
}

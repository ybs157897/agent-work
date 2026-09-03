package observability

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type recordedEvent struct {
	runID     string
	eventType string
	data      map[string]any
}

func recorder(buf *[]recordedEvent) RecordFunc {
	return func(_ context.Context, runID, eventType string, data map[string]any) error {
		*buf = append(*buf, recordedEvent{runID: runID, eventType: eventType, data: data})
		return nil
	}
}

func TestJournalPhasePairEmitsEnteredThenClosed(t *testing.T) {
	var buf []recordedEvent
	j := NewJournal(recorder(&buf))
	clock := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	j.now = func() time.Time { return clock }

	timer, err := j.EnterPhase(context.Background(), "run_1", PhaseHandshake, 2, map[string]any{"resume": true})
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(150 * time.Millisecond)
	if err := timer.Close(PhaseFailed, &PhaseFailure{Code: "session_unknown", Message: "no rollout", Retryable: false}, nil); err != nil {
		t.Fatal(err)
	}

	if len(buf) != 2 {
		t.Fatalf("expected entered+closed pair, got %d events", len(buf))
	}
	entered := buf[0]
	if entered.eventType != domain.EventRunPhaseEntered || entered.runID != "run_1" {
		t.Fatalf("unexpected entered event: %+v", entered)
	}
	if entered.data["phase"] != PhaseHandshake || entered.data["attempt"] != 2 || entered.data["resume"] != true {
		t.Fatalf("entered payload malformed: %+v", entered.data)
	}
	closed := buf[1]
	if closed.eventType != domain.EventRunPhaseClosed {
		t.Fatalf("unexpected closed event: %+v", closed)
	}
	if closed.data["phase"] != PhaseHandshake || closed.data["outcome"] != string(PhaseFailed) {
		t.Fatalf("closed payload malformed: %+v", closed.data)
	}
	if got := closed.data["duration_ms"]; got != int64(150) {
		t.Fatalf("duration_ms=%v, want 150", got)
	}
	failure, ok := closed.data["failure"].(map[string]any)
	if !ok || failure["code"] != "session_unknown" || failure["retryable"] != false {
		t.Fatalf("failure payload malformed: %+v", closed.data["failure"])
	}
}

func TestJournalRejectsUnknownPhase(t *testing.T) {
	var buf []recordedEvent
	j := NewJournal(recorder(&buf))
	if _, err := j.EnterPhase(context.Background(), "run_1", "explode", 1, nil); err == nil {
		t.Fatal("unknown phase must fail loudly")
	} else if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("want ErrValidation, got %v", err)
	}
	if len(buf) != 0 {
		t.Fatalf("rejected phase must not emit, got %+v", buf)
	}
}

func TestJournalNilRecordIsSilent(t *testing.T) {
	j := NewJournal(nil)
	timer, err := j.EnterPhase(context.Background(), "run_1", PhaseSpawn, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := timer.Close(PhaseOK, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := j.LogLine(context.Background(), "run_1", "stderr", NewLogBudget(), "boom"); err != nil {
		t.Fatal(err)
	}
}

func TestLogBudgetExactFitThenExhaustion(t *testing.T) {
	b := NewLogBudget()
	b.remaining = 8
	stored, truncated, ok := b.Take("12345678")
	if !ok || truncated || stored != "12345678" {
		t.Fatalf("exact fit should pass whole chunk: %q truncated=%v ok=%v", stored, truncated, ok)
	}
	if _, _, ok := b.Take("x"); ok {
		t.Fatal("exhausted budget must refuse")
	}
}

func TestLogBudgetOverflowTruncatesOnce(t *testing.T) {
	b := NewLogBudget()
	b.remaining = 5
	stored, truncated, ok := b.Take("123456789")
	if !ok || !truncated || stored != "12345" {
		t.Fatalf("overflow should store prefix with truncated flag: %q truncated=%v ok=%v", stored, truncated, ok)
	}
	if _, _, ok := b.Take("more"); ok {
		t.Fatal("budget exhausted after truncation")
	}
}

func TestLogLineStopsWhenBudgetExhausted(t *testing.T) {
	var buf []recordedEvent
	j := NewJournal(recorder(&buf))
	b := NewLogBudget()
	b.remaining = 4
	if err := j.LogLine(context.Background(), "run_1", "stderr", b, "abcdef"); err != nil {
		t.Fatal(err)
	}
	if err := j.LogLine(context.Background(), "run_1", "stderr", b, "ghij"); err != nil {
		t.Fatal(err)
	}
	if len(buf) != 1 {
		t.Fatalf("only the truncating chunk may land, got %d", len(buf))
	}
	chunk := buf[0]
	if chunk.eventType != domain.EventRunLogChunk || chunk.data["chunk"] != "abcd" || chunk.data["truncated"] != true || chunk.data["stream"] != "stderr" {
		t.Fatalf("log chunk payload malformed: %+v", chunk.data)
	}
}

func TestPhaseOutcomesStayInDocumentedVocabulary(t *testing.T) {
	for _, outcome := range []PhaseOutcome{PhaseOK, PhaseFailed, PhaseSkipped} {
		if strings.TrimSpace(string(outcome)) == "" {
			t.Fatalf("empty outcome in vocabulary: %q", outcome)
		}
	}
	for _, phase := range []string{PhaseDispatch, PhaseSpawn, PhaseHandshake, PhaseFirstEvent, PhaseStreaming, PhaseSettle, PhasePost} {
		if !IsRunPhase(phase) {
			t.Fatalf("phase %q missing from vocabulary", phase)
		}
	}
}

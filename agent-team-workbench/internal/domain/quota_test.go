package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func quotaDomainDigest(ch byte) string {
	return "sha256:" + strings.Repeat(string("abcdef"[int(ch)%6]), 64)
}

func validQuotaReservationForTest(kind QuotaKind) *QuotaReservation {
	now := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	reservation := &QuotaReservation{
		Key: QuotaReservationKey{
			TurnKey: TurnKey{GoalID: "goal_quota", TodoID: "todo_quota", TurnSeq: 1},
			Kind:    kind,
		},
		Status:            QuotaReservationReserved,
		ReservedAmount:    10,
		PolicyLimit:       100,
		PolicyEnforcement: QuotaEnforcementAudit,
		PolicyDigest:      quotaDomainDigest('a'),
		Version:           1,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	return reservation
}

func TestQuotaReservationDomainContract(t *testing.T) {
	reservation := validQuotaReservationForTest(QuotaOutputTokens)
	if err := reservation.Validate(); err != nil {
		t.Fatal(err)
	}

	reservation.Status = QuotaReservationCommitted
	reservation.CommittedAmount = 7
	reservation.ReleasedAmount = 3
	if err := reservation.Validate(); err != nil {
		t.Fatalf("fully settled terminal reservation should validate: %v", err)
	}
	reservation.ReleasedAmount = 2
	if err := reservation.Validate(); !errors.Is(err, ErrValidation) {
		t.Fatalf("terminal reservation with unaccounted amount must fail: %v", err)
	}

	reservation = validQuotaReservationForTest(QuotaOutputTokens)
	if err := reservation.Transition(QuotaReservationCommitted, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := reservation.Validate(); !errors.Is(err, ErrValidation) {
		// Transition mutates only status/version; callers must settle amounts
		// before validating or persisting the candidate.
		t.Fatalf("transition without settling amounts must fail validation: %v", err)
	}
	reservation.CommittedAmount = reservation.ReservedAmount
	if err := reservation.Validate(); err != nil {
		t.Fatalf("settled transition should validate: %v", err)
	}

	reservation = validQuotaReservationForTest(QuotaOutputTokens)
	reservation.CommittedAmount = 8
	reservation.ReleasedAmount = 3
	if err := reservation.Validate(); !errors.Is(err, ErrValidation) {
		t.Fatalf("committed+released overflow must fail: %v", err)
	}
}

func TestQuotaCostReservationDoesNotOwnRunPrice(t *testing.T) {
	reservation := validQuotaReservationForTest(QuotaCostMicroUSD)
	if err := reservation.Validate(); err != nil {
		t.Fatalf("turn-level cost reservation must freeze policy/amount without assuming one Run price: %v", err)
	}
}

func TestQuotaSpendDomainContract(t *testing.T) {
	now := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	entry := &QuotaSpendEntry{
		Key: QuotaSpendKey{
			TurnKey: TurnKey{GoalID: "goal_quota", TodoID: "todo_quota", TurnSeq: 1},
			Kind:    QuotaOutputTokens,
			RunID:   "run_quota",
		},
		Amount:       4,
		UsageBasis:   "per_run",
		UsageDigest:  quotaDomainDigest('u'),
		PolicyDigest: quotaDomainDigest('a'),
		Status:       QuotaSpendCommitted,
		CreatedAt:    now,
	}
	if err := entry.Validate(); err != nil {
		t.Fatal(err)
	}
	entry.Status = QuotaSpendUnresolved
	entry.Amount = 1
	entry.Reason = "missing provider delta"
	if err := entry.Validate(); !errors.Is(err, ErrValidation) {
		t.Fatalf("unresolved spend must be zero-valued: %v", err)
	}
	entry.Amount = 0
	if err := entry.Validate(); err != nil {
		t.Fatal(err)
	}
	entry.Key.Kind = QuotaActiveWorker
	if err := entry.Validate(); !errors.Is(err, ErrValidation) {
		t.Fatalf("active_worker must not have spend entries: %v", err)
	}
	entry.Key.Kind = QuotaTurnCount
	if err := entry.Validate(); !errors.Is(err, ErrValidation) {
		t.Fatalf("turn_count must not have spend entries: %v", err)
	}
}

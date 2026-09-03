package domain

import (
	"errors"
	"testing"
	"time"
)

func TestTodoRenewClaimPreservesOriginalClaimedAt(t *testing.T) {
	todo := validTodoForTest()
	claimedAt := now.Add(-time.Hour)
	if err := todo.ClaimBy(todo.DecisionScope.AgentIDs[0], claimedAt, claimedAt.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	beforeVersion := todo.Version
	beforeClaimedAt := todo.Claim.ClaimedAt
	renewedAt := now
	expiresAt := now.Add(15 * time.Minute)
	if err := todo.RenewClaim(todo.Claim.OwnerAgentID, todo.ClaimVersion, renewedAt, expiresAt); err != nil {
		t.Fatal(err)
	}
	if !todo.Claim.ClaimedAt.Equal(beforeClaimedAt) {
		t.Fatalf("same-generation renewal must preserve claimed_at: before=%v after=%v", beforeClaimedAt, todo.Claim.ClaimedAt)
	}
	if !todo.Claim.ExpiresAt.Equal(expiresAt) || todo.Version != beforeVersion+1 || !todo.UpdatedAt.Equal(renewedAt) {
		t.Fatalf("renewal must only advance expiry/bookkeeping: todo=%+v", todo)
	}
	if err := todo.RenewClaim(todo.Claim.OwnerAgentID, todo.ClaimVersion, renewedAt, todo.Claim.ExpiresAt.Add(-time.Minute)); !errors.Is(err, ErrValidation) {
		t.Fatalf("renewal must reject expiry shortening: %v", err)
	}
}

func TestTodoCompletionRequiresLatestAdmittedTurn(t *testing.T) {
	todo := validTodoForTest()
	if err := todo.ClaimBy(todo.DecisionScope.AgentIDs[0], now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	todo.LastTurnSeq = 2
	previous := TurnKey{GoalID: todo.GoalID, TodoID: todo.ID, TurnSeq: 1}
	if err := todo.CompleteWithEvidence(previous, todo.GoalID, now); err == nil {
		t.Fatal("completion must reject an older admitted turn")
	}
}

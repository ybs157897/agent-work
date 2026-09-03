package sqlstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// QuotaRepo is the SQLite implementation of the immutable reservation and
// append-only spend ledger.  It deliberately does not own Goal policies:
// callers pass a frozen policy snapshot and the database enforces its identity
// against the admitted receipt turn. Prices belong to immutable Run snapshots.
type QuotaRepo struct{ store *Store }

var _ application.QuotaRepo = (*QuotaRepo)(nil)

const quotaMaxInt64 = int64(1<<63 - 1)

const quotaReservationCols = `goal_id, todo_id, turn_seq, quota_kind, status,
	reserved_amount, committed_amount, released_amount, policy_limit,
	policy_enforcement, policy_digest, version, created_at, updated_at`

const quotaSpendCols = `goal_id, todo_id, turn_seq, quota_kind, run_id,
	amount, usage_basis, usage_digest, policy_digest, price_digest, status, reason, created_at`

func (r *QuotaRepo) scanReservation(row interface{ Scan(...any) error }) (*domain.QuotaReservation, error) {
	reservation := &domain.QuotaReservation{}
	var created, updated scanTime
	if err := row.Scan(&reservation.Key.TurnKey.GoalID, &reservation.Key.TurnKey.TodoID,
		&reservation.Key.TurnKey.TurnSeq, &reservation.Key.Kind, &reservation.Status,
		&reservation.ReservedAmount, &reservation.CommittedAmount, &reservation.ReleasedAmount,
		&reservation.PolicyLimit, &reservation.PolicyEnforcement, &reservation.PolicyDigest,
		&reservation.Version, &created, &updated); err != nil {
		return nil, err
	}
	reservation.CreatedAt, reservation.UpdatedAt = mustTime(created), mustTime(updated)
	if err := reservation.Validate(); err != nil {
		return nil, err
	}
	return reservation, nil
}

func (r *QuotaRepo) Get(ctx context.Context, key domain.QuotaReservationKey) (*domain.QuotaReservation, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	reservation, err := r.scanReservation(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+quotaReservationCols+` FROM quota_reservations
		 WHERE goal_id=? AND todo_id=? AND turn_seq=? AND quota_kind=?`,
		key.TurnKey.GoalID, key.TurnKey.TodoID, key.TurnKey.TurnSeq, key.Kind))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return reservation, nil
}

func (r *QuotaRepo) ListByGoal(ctx context.Context, goalID string) ([]*domain.QuotaReservation, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+quotaReservationCols+` FROM quota_reservations WHERE goal_id=? ORDER BY todo_id, turn_seq, quota_kind`, goalID)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.QuotaReservation
	for rows.Next() {
		reservation, scanErr := r.scanReservation(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, reservation)
	}
	return out, rows.Err()
}

// Reserve inserts a reservation or returns created=false for a replay of the
// same frozen intent.  It never reopens a settled reservation.
func (r *QuotaRepo) Reserve(ctx context.Context, reservation *domain.QuotaReservation) (bool, error) {
	if reservation == nil {
		return false, fmt.Errorf("%w: quota reservation required", domain.ErrValidation)
	}
	if reservation.Status != domain.QuotaReservationReserved {
		return false, fmt.Errorf("%w: quota reservation must start reserved", domain.ErrValidation)
	}
	if err := reservation.Validate(); err != nil {
		return false, err
	}
	existing, err := r.Get(ctx, reservation.Key)
	if err == nil {
		if sameReservationIntent(existing, reservation) {
			return false, nil
		}
		return false, domain.ErrIdempotencyConflict
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return false, err
	}
	result, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO quota_reservations(`+quotaReservationCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		reservation.Key.TurnKey.GoalID, reservation.Key.TurnKey.TodoID,
		reservation.Key.TurnKey.TurnSeq, reservation.Key.Kind, reservation.Status,
		reservation.ReservedAmount, reservation.CommittedAmount, reservation.ReleasedAmount,
		reservation.PolicyLimit, reservation.PolicyEnforcement, reservation.PolicyDigest,
		reservation.Version, timeParam(reservation.CreatedAt), timeParam(reservation.UpdatedAt))
	if err == nil {
		if affected, affectedErr := result.RowsAffected(); affectedErr == nil && affected == 1 {
			return true, nil
		}
		// The idempotency trigger uses RAISE(IGNORE), which reports a
		// successful statement with zero affected rows.  Resolve that replay
		// exactly as we resolve a concurrent unique-key race below.
		existing, getErr := r.Get(ctx, reservation.Key)
		if getErr != nil {
			return false, getErr
		}
		if sameReservationIntent(existing, reservation) {
			return false, nil
		}
		return false, domain.ErrIdempotencyConflict
	}
	if !quotaReservationConflict(err) {
		return false, r.store.mapErr(err)
	}
	// A concurrent writer may have won between Get and INSERT.  Re-read and
	// compare the complete intent instead of leaking a generic unique error.
	existing, getErr := r.Get(ctx, reservation.Key)
	if getErr != nil {
		return false, getErr
	}
	if sameReservationIntent(existing, reservation) {
		return false, nil
	}
	return false, domain.ErrIdempotencyConflict
}

func sameReservationIntent(existing, incoming *domain.QuotaReservation) bool {
	return existing != nil && incoming != nil && existing.Key.Equal(incoming.Key) &&
		existing.ReservedAmount == incoming.ReservedAmount &&
		existing.PolicyLimit == incoming.PolicyLimit &&
		existing.PolicyEnforcement == incoming.PolicyEnforcement &&
		existing.PolicyDigest == incoming.PolicyDigest
}

// transition writes one reservation settlement state using optimistic CAS.
// The candidate carries the post-transition amounts/status and must already
// have version=expectedVersion+1.
func (r *QuotaRepo) transition(ctx context.Context, candidate *domain.QuotaReservation,
	expectedVersion int, target domain.QuotaReservationStatus) error {
	if candidate == nil {
		return fmt.Errorf("%w: quota reservation required", domain.ErrValidation)
	}
	if expectedVersion < 1 || expectedVersion == int(^uint(0)>>1) || candidate.Version != expectedVersion+1 {
		return domain.ErrVersionConflict
	}
	existing, err := r.Get(ctx, candidate.Key)
	if err != nil {
		return err
	}
	if existing.Version != expectedVersion {
		return domain.ErrVersionConflict
	}
	if candidate.Status != target {
		return fmt.Errorf("%w: quota reservation transition target must be %q", domain.ErrValidation, target)
	}
	if !sameReservationIntent(existing, candidate) {
		return fmt.Errorf("%w: quota reservation identity/policy is immutable", domain.ErrValidation)
	}
	if !existing.Status.CanTransitionTo(target) {
		return &domain.TransitionError{Entity: "quota_reservation", From: string(existing.Status), To: string(target)}
	}
	if candidate.CreatedAt.IsZero() {
		candidate.CreatedAt = existing.CreatedAt
	}
	if candidate.UpdatedAt.IsZero() {
		candidate.UpdatedAt = timeNow()
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE quota_reservations
		 SET status=?, committed_amount=?, released_amount=?, version=?, updated_at=?
		 WHERE goal_id=? AND todo_id=? AND turn_seq=? AND quota_kind=? AND version=?`,
		candidate.Status, candidate.CommittedAmount, candidate.ReleasedAmount,
		candidate.Version, timeParam(candidate.UpdatedAt), candidate.Key.TurnKey.GoalID,
		candidate.Key.TurnKey.TodoID, candidate.Key.TurnKey.TurnSeq, candidate.Key.Kind,
		expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrVersionConflict
	}
	return nil
}

func (r *QuotaRepo) Commit(ctx context.Context, reservation *domain.QuotaReservation, expectedVersion int) error {
	return r.transition(ctx, reservation, expectedVersion, domain.QuotaReservationCommitted)
}

func (r *QuotaRepo) Release(ctx context.Context, reservation *domain.QuotaReservation, expectedVersion int) error {
	return r.transition(ctx, reservation, expectedVersion, domain.QuotaReservationReleased)
}

func (r *QuotaRepo) Expire(ctx context.Context, reservation *domain.QuotaReservation, expectedVersion int) error {
	return r.transition(ctx, reservation, expectedVersion, domain.QuotaReservationExpired)
}

func (r *QuotaRepo) scanSpend(row interface{ Scan(...any) error }) (*domain.QuotaSpendEntry, error) {
	entry := &domain.QuotaSpendEntry{}
	var priceDigest *string
	var created scanTime
	if err := row.Scan(&entry.Key.TurnKey.GoalID, &entry.Key.TurnKey.TodoID,
		&entry.Key.TurnKey.TurnSeq, &entry.Key.Kind, &entry.Key.RunID, &entry.Amount,
		&entry.UsageBasis, &entry.UsageDigest, &entry.PolicyDigest, &priceDigest,
		&entry.Status, &entry.Reason, &created); err != nil {
		return nil, err
	}
	if priceDigest != nil {
		entry.PriceDigest = *priceDigest
	}
	entry.CreatedAt = mustTime(created)
	if err := entry.Validate(); err != nil {
		return nil, err
	}
	return entry, nil
}

func (r *QuotaRepo) GetSpend(ctx context.Context, key domain.QuotaSpendKey) (*domain.QuotaSpendEntry, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	entry, err := r.scanSpend(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+quotaSpendCols+` FROM quota_spend_entries
		 WHERE goal_id=? AND todo_id=? AND turn_seq=? AND quota_kind=? AND run_id=?`,
		key.TurnKey.GoalID, key.TurnKey.TodoID, key.TurnKey.TurnSeq, key.Kind, key.RunID))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return entry, nil
}

// AppendSpend appends one immutable per-Run result.  The reservation's
// frozen policy digest and (for cost) price digest must match the entry.
func (r *QuotaRepo) AppendSpend(ctx context.Context, entry *domain.QuotaSpendEntry) (bool, error) {
	if entry == nil {
		return false, fmt.Errorf("%w: quota spend entry required", domain.ErrValidation)
	}
	if err := entry.Validate(); err != nil {
		return false, err
	}
	existing, err := r.GetSpend(ctx, entry.Key)
	if err == nil {
		if sameSpendPayload(existing, entry) {
			return false, nil
		}
		return false, domain.ErrIdempotencyConflict
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return false, err
	}
	reservation, err := r.Get(ctx, domain.QuotaReservationKey{TurnKey: entry.Key.TurnKey, Kind: entry.Key.Kind})
	if err != nil {
		return false, err
	}
	if entry.PolicyDigest != reservation.PolicyDigest {
		return false, fmt.Errorf("%w: quota spend policy digest differs from reservation", domain.ErrValidation)
	}
	if reservation.Status != domain.QuotaReservationReserved {
		return false, fmt.Errorf("%w: quota reservation is already settled", domain.ErrStateConflict)
	}
	if err := r.validateSpendScopeAndCapacity(ctx, entry, reservation); err != nil {
		return false, err
	}
	result, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO quota_spend_entries(`+quotaSpendCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		entry.Key.TurnKey.GoalID, entry.Key.TurnKey.TodoID, entry.Key.TurnKey.TurnSeq,
		entry.Key.Kind, entry.Key.RunID, entry.Amount, entry.UsageBasis, entry.UsageDigest,
		entry.PolicyDigest, nullString(entry.PriceDigest), entry.Status, entry.Reason,
		timeParam(entry.CreatedAt))
	if err == nil {
		if affected, affectedErr := result.RowsAffected(); affectedErr == nil && affected == 1 {
			return true, nil
		}
		existing, getErr := r.GetSpend(ctx, entry.Key)
		if getErr != nil {
			return false, getErr
		}
		if sameSpendPayload(existing, entry) {
			return false, nil
		}
		return false, domain.ErrIdempotencyConflict
	}
	if !quotaSpendConflict(err) {
		return false, r.store.mapErr(err)
	}
	existing, getErr := r.GetSpend(ctx, entry.Key)
	if getErr != nil {
		return false, getErr
	}
	if sameSpendPayload(existing, entry) {
		return false, nil
	}
	return false, domain.ErrIdempotencyConflict
}

func (r *QuotaRepo) validateSpendScopeAndCapacity(ctx context.Context, entry *domain.QuotaSpendEntry,
	reservation *domain.QuotaReservation) error {
	var runInScope bool
	if err := r.store.queryRow(ctx, r.store.exec(ctx), `WITH RECURSIVE subtree(id) AS (
		SELECT root_work_item_id FROM goals WHERE id=?
		UNION
		SELECT child.id
		  FROM work_items child
		  JOIN subtree parent ON parent.id=child.parent_id
		 WHERE child.record_kind='task'
	)
	SELECT EXISTS(
		SELECT 1
		  FROM execution_runs run
		  JOIN goals goal ON goal.id=?
		  JOIN subtree item ON item.id=run.work_item_id
		 WHERE run.id=?
		   AND run.workspace_id=goal.workspace_id
		   AND run.status IN ('succeeded','interrupted','cancelled','lost','failed')
	)`, entry.Key.TurnKey.GoalID, entry.Key.TurnKey.GoalID, entry.Key.RunID).Scan(&runInScope); err != nil {
		return r.store.mapErr(err)
	}
	if !runInScope {
		return fmt.Errorf("%w: quota spend Run is not terminal in the Goal Task subtree", domain.ErrValidation)
	}
	run, err := r.store.Runs().Get(ctx, entry.Key.RunID)
	if err != nil {
		return err
	}
	if run.CanonicalUsage == nil || run.CanonicalUsageDigest != entry.UsageDigest {
		return fmt.Errorf("%w: quota spend usage digest differs from Run canonical usage", domain.ErrValidation)
	}
	var spent int64
	if err := r.store.queryRow(ctx, r.store.exec(ctx), `SELECT COALESCE(SUM(amount), 0)
		  FROM quota_spend_entries
		 WHERE goal_id=? AND todo_id=? AND turn_seq=? AND quota_kind=?`,
		entry.Key.TurnKey.GoalID, entry.Key.TurnKey.TodoID, entry.Key.TurnKey.TurnSeq,
		entry.Key.Kind).Scan(&spent); err != nil {
		return r.store.mapErr(err)
	}
	amount, resolved := canonicalQuotaAmount(run.CanonicalUsage, entry.Key.Kind)
	if !resolved {
		if entry.Status != domain.QuotaSpendUnresolved || entry.Amount != 0 {
			return fmt.Errorf("%w: unresolved canonical usage cannot produce committed spend", domain.ErrValidation)
		}
	} else if entry.Status == domain.QuotaSpendUnresolved {
		// Over-capacity exemption: a resolved counter must commit in full whenever
		// it provably still fits the reservation; only a counter that no longer
		// fits may be recorded as an unresolved gap.  The factual amount stays on
		// the Run's immutable canonical usage — the entry never truncates it.
		if entry.Amount != 0 {
			return fmt.Errorf("%w: unresolved quota spend amount must be zero", domain.ErrValidation)
		}
		if amount <= reservation.ReservedAmount-spent {
			return fmt.Errorf("%w: resolved canonical usage fits the reservation and must be committed", domain.ErrValidation)
		}
	} else if entry.Status != domain.QuotaSpendCommitted || entry.Amount != amount {
		return fmt.Errorf("%w: quota spend amount differs from Run canonical usage", domain.ErrValidation)
	}
	if entry.Key.Kind == domain.QuotaCostMicroUSD {
		price, priceErr := domain.PriceSnapshotFromRunInput(run.Input)
		if priceErr != nil {
			return priceErr
		}
		if price == nil {
			return fmt.Errorf("%w: cost spend Run lacks price snapshot", domain.ErrValidation)
		}
		if entry.PriceDigest != price.Digest || run.CanonicalUsage.PriceDigest != price.Digest {
			return fmt.Errorf("%w: quota spend price digest differs from Run snapshot", domain.ErrValidation)
		}
	}
	if spent > reservation.ReservedAmount || entry.Amount > reservation.ReservedAmount-spent {
		return fmt.Errorf("%w: quota spend exceeds reserved amount", domain.ErrValidation)
	}
	return nil
}

func canonicalQuotaAmount(usage *domain.CanonicalUsageV1, kind domain.QuotaKind) (int64, bool) {
	if usage == nil {
		return 0, false
	}
	var value *int64
	switch kind {
	case domain.QuotaInputTokensTotal:
		value = usage.Counters.InputTokensTotal
	case domain.QuotaInputUncachedTokens:
		value = usage.Counters.InputUncachedTokens
	case domain.QuotaCacheReadTokens:
		value = usage.Counters.CacheReadTokens
	case domain.QuotaCacheWriteTokens:
		value = usage.Counters.CacheWriteTokens
	case domain.QuotaOutputTokens:
		value = usage.Counters.OutputTokens
	case domain.QuotaCostMicroUSD:
		value = usage.CostMicroUSD
	}
	if value == nil {
		return 0, false
	}
	return *value, true
}

func priceSnapshotFromRunInput(input map[string]any) (*domain.PriceSnapshotRef, error) {
	value, ok := input["price_snapshot"]
	if !ok {
		return nil, fmt.Errorf("%w: cost spend Run lacks price snapshot", domain.ErrValidation)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: encode Run price snapshot: %v", domain.ErrValidation, err)
	}
	var price domain.PriceSnapshotRef
	if err := json.Unmarshal(raw, &price); err != nil {
		return nil, fmt.Errorf("%w: decode Run price snapshot: %v", domain.ErrValidation, err)
	}
	if err := price.Validate(); err != nil {
		return nil, err
	}
	return &price, nil
}

func sameSpendPayload(existing, incoming *domain.QuotaSpendEntry) bool {
	return existing != nil && incoming != nil && existing.Key.Equal(incoming.Key) &&
		existing.Amount == incoming.Amount && existing.UsageBasis == incoming.UsageBasis &&
		existing.UsageDigest == incoming.UsageDigest && existing.PolicyDigest == incoming.PolicyDigest &&
		existing.PriceDigest == incoming.PriceDigest && existing.Status == incoming.Status &&
		existing.Reason == incoming.Reason
}

// ListSpendByTurn returns every spend entry of one governance Turn ordered by
// (quota_kind, run_id) so settlement can compute per-kind committed totals and
// deterministic receipt payloads.
func (r *QuotaRepo) ListSpendByTurn(ctx context.Context, key domain.TurnKey) ([]*domain.QuotaSpendEntry, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+quotaSpendCols+` FROM quota_spend_entries
		 WHERE goal_id=? AND todo_id=? AND turn_seq=? ORDER BY quota_kind, run_id`,
		key.GoalID, key.TodoID, key.TurnSeq)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var entries []*domain.QuotaSpendEntry
	for rows.Next() {
		entry, scanErr := r.scanSpend(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (r *QuotaRepo) ListUnresolved(ctx context.Context, goalID string, kinds ...domain.QuotaKind) ([]*domain.QuotaSpendEntry, error) {
	if strings.TrimSpace(goalID) == "" {
		return nil, fmt.Errorf("%w: goal id required", domain.ErrValidation)
	}
	query := `SELECT ` + quotaSpendCols + ` FROM quota_spend_entries spend
		WHERE spend.goal_id=? AND spend.status='unresolved'
		  AND NOT EXISTS (
			  SELECT 1 FROM governance_quota_gap_resolutions resolution
			   WHERE resolution.goal_id=spend.goal_id
			     AND resolution.todo_id=spend.todo_id
			     AND resolution.turn_seq=spend.turn_seq
			     AND resolution.quota_kind=spend.quota_kind
			     AND resolution.run_id=spend.run_id
			     AND resolution.status='reconciled'
		  )`
	args := []any{goalID}
	if len(kinds) > 0 {
		placeholders, values, err := quotaKindFilter(kinds)
		if err != nil {
			return nil, err
		}
		query += ` AND spend.quota_kind IN (` + placeholders + `)`
		args = append(args, values...)
	}
	query += ` ORDER BY spend.created_at, spend.run_id, spend.quota_kind`
	rows, err := r.store.query(ctx, r.store.exec(ctx), query, args...)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var entries []*domain.QuotaSpendEntry
	for rows.Next() {
		entry, scanErr := r.scanSpend(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (r *QuotaRepo) SumCommitted(ctx context.Context, goalID string, kinds ...domain.QuotaKind) (int64, error) {
	if strings.TrimSpace(goalID) == "" {
		return 0, fmt.Errorf("%w: goal id required", domain.ErrValidation)
	}
	if len(kinds) == 0 {
		spend, err := r.sumCommittedSpend(ctx, goalID, nil)
		if err != nil {
			return 0, err
		}
		turns, err := r.sumCommittedTurnReservations(ctx, goalID)
		if err != nil {
			return 0, err
		}
		if turns > quotaMaxInt64-spend {
			return 0, fmt.Errorf("%w: committed quota sum overflow", domain.ErrValidation)
		}
		return spend + turns, nil
	}
	seen := make(map[domain.QuotaKind]struct{}, len(kinds))
	var total int64
	for _, kind := range kinds {
		if !kind.Valid() {
			return 0, fmt.Errorf("%w: quota kind %q", domain.ErrValidation, kind)
		}
		if _, ok := seen[kind]; ok {
			return 0, fmt.Errorf("%w: duplicate quota kind %q", domain.ErrValidation, kind)
		}
		seen[kind] = struct{}{}
		if kind == domain.QuotaActiveWorker {
			return 0, fmt.Errorf("%w: active_worker is a gauge; use ActiveWorkerCount", domain.ErrValidation)
		}
		var amount int64
		var err error
		if kind == domain.QuotaTurnCount {
			amount, err = r.sumCommittedTurnReservations(ctx, goalID)
		} else {
			amount, err = r.sumCommittedSpend(ctx, goalID, []domain.QuotaKind{kind})
		}
		if err != nil {
			return 0, err
		}
		if amount > quotaMaxInt64-total {
			return 0, fmt.Errorf("%w: committed quota sum overflow", domain.ErrValidation)
		}
		total += amount
	}
	return total, nil
}

func (r *QuotaRepo) sumCommittedSpend(ctx context.Context, goalID string, kinds []domain.QuotaKind) (int64, error) {
	query := `SELECT COALESCE(SUM(amount), 0) FROM (
		SELECT spend.amount FROM quota_spend_entries spend
		 WHERE spend.goal_id=? AND spend.status='committed'`
	args := []any{goalID}
	if len(kinds) > 0 {
		placeholders, values, err := quotaKindFilter(kinds)
		if err != nil {
			return 0, err
		}
		query += ` AND spend.quota_kind IN (` + placeholders + `)`
		args = append(args, values...)
	}
	query += `
		UNION ALL
		SELECT resolution.amount FROM governance_quota_gap_resolutions resolution
		 WHERE resolution.goal_id=? AND resolution.status='reconciled'`
	args = append(args, goalID)
	if len(kinds) > 0 {
		placeholders, values, err := quotaKindFilter(kinds)
		if err != nil {
			return 0, err
		}
		query += ` AND resolution.quota_kind IN (` + placeholders + `)`
		args = append(args, values...)
	}
	query += `)`
	var total int64
	if err := r.store.queryRow(ctx, r.store.exec(ctx), query, args...).Scan(&total); err != nil {
		return 0, r.store.mapErr(err)
	}
	return total, nil
}

func (r *QuotaRepo) sumCommittedTurnReservations(ctx context.Context, goalID string) (int64, error) {
	var total int64
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT COALESCE(SUM(committed_amount), 0)
		   FROM quota_reservations
		  WHERE goal_id=? AND quota_kind=? AND status='committed'`,
		goalID, domain.QuotaTurnCount).Scan(&total); err != nil {
		return 0, r.store.mapErr(err)
	}
	return total, nil
}

func quotaKindFilter(kinds []domain.QuotaKind) (string, []any, error) {
	placeholders := make([]string, len(kinds))
	values := make([]any, len(kinds))
	seen := make(map[domain.QuotaKind]struct{}, len(kinds))
	for i, kind := range kinds {
		if !kind.Valid() || kind == domain.QuotaTurnCount || kind == domain.QuotaActiveWorker {
			return "", nil, fmt.Errorf("%w: quota spend kind %q is not filterable", domain.ErrValidation, kind)
		}
		if _, ok := seen[kind]; ok {
			return "", nil, fmt.Errorf("%w: duplicate quota kind %q", domain.ErrValidation, kind)
		}
		seen[kind] = struct{}{}
		placeholders[i], values[i] = "?", kind
	}
	return strings.Join(placeholders, ","), values, nil
}

// SumActiveReserved returns the total reserved_amount of one kind still in
// reserved status for the Goal: budget frozen by in-flight Turns. Admission
// preflight adds it to committed spend so concurrent Turns cannot oversubscribe.
func (r *QuotaRepo) SumActiveReserved(ctx context.Context, goalID string, kind domain.QuotaKind) (int64, error) {
	if strings.TrimSpace(goalID) == "" {
		return 0, fmt.Errorf("%w: goal id required", domain.ErrValidation)
	}
	if !kind.Valid() {
		return 0, fmt.Errorf("%w: quota kind %q", domain.ErrValidation, kind)
	}
	var total int64
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT COALESCE(SUM(reserved_amount),0) FROM quota_reservations
		 WHERE goal_id=? AND quota_kind=? AND status='reserved'`,
		goalID, kind).Scan(&total); err != nil {
		return 0, r.store.mapErr(err)
	}
	return total, nil
}

// ActiveWorkerCount counts all non-terminal Task Runs in the Goal root's
// subtree, while excluding the system Task Coordinator profile.  It is a
// gauge, not a spend total; queued and reconnecting Runs remain active until
// they reach a terminal RunStatus.
func (r *QuotaRepo) ActiveWorkerCount(ctx context.Context, goalID string) (int, error) {
	if strings.TrimSpace(goalID) == "" {
		return 0, fmt.Errorf("%w: goal id required", domain.ErrValidation)
	}
	goal, err := r.store.Goals().Get(ctx, goalID)
	if err != nil {
		return 0, err
	}
	var count int
	const query = `WITH RECURSIVE subtree(id) AS (
		SELECT root_work_item_id
		  FROM goals
		 WHERE id=?
		   AND EXISTS (
		       SELECT 1 FROM work_items root
		        WHERE root.id=goals.root_work_item_id
		          AND root.workspace_id=goals.workspace_id
		          AND root.parent_id IS NULL
		          AND root.record_kind='task'
		   )
		UNION
		SELECT child.id
		  FROM work_items child
		  JOIN subtree parent ON parent.id=child.parent_id
		 WHERE child.record_kind='task'
	)
	SELECT COUNT(*)
	  FROM execution_runs run
	  JOIN subtree item ON item.id=run.work_item_id
	  LEFT JOIN agent_profiles agent ON agent.id=run.agent_profile_id
	 WHERE run.status NOT IN ('succeeded','interrupted','cancelled','lost','failed')
	   AND (agent.kind IS NULL OR agent.kind <> 'task_coordinator')
	   AND run.workspace_id=?`
	if err := r.store.queryRow(ctx, r.store.exec(ctx), query, goalID, goal.WorkspaceID).Scan(&count); err != nil {
		return 0, r.store.mapErr(err)
	}
	return count, nil
}

func quotaReservationConflict(err error) bool {
	return err != nil && (sqliteUniqueViolation(err) || strings.Contains(err.Error(), "quota reservation intent conflict"))
}

func quotaSpendConflict(err error) bool {
	return err != nil && (sqliteUniqueViolation(err) || strings.Contains(err.Error(), "quota spend digest conflict"))
}

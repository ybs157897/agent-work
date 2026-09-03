package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// QuotaReservationStatus is the lifecycle of one reserved quota unit for one
// admitted governance turn.  The identity is the reservation key; there is
// intentionally no second reservation id.
type QuotaReservationStatus string

const (
	QuotaReservationReserved  QuotaReservationStatus = "reserved"
	QuotaReservationCommitted QuotaReservationStatus = "committed"
	QuotaReservationReleased  QuotaReservationStatus = "released"
	QuotaReservationExpired   QuotaReservationStatus = "expired"
)

func (s QuotaReservationStatus) Valid() bool {
	switch s {
	case QuotaReservationReserved, QuotaReservationCommitted,
		QuotaReservationReleased, QuotaReservationExpired:
		return true
	default:
		return false
	}
}

func (s QuotaReservationStatus) IsTerminal() bool {
	return s == QuotaReservationCommitted || s == QuotaReservationReleased || s == QuotaReservationExpired
}

func (s QuotaReservationStatus) CanTransitionTo(to QuotaReservationStatus) bool {
	return s == QuotaReservationReserved && (to == QuotaReservationCommitted ||
		to == QuotaReservationReleased || to == QuotaReservationExpired)
}

// QuotaReservationKey is the immutable reservation identity.  It reuses the
// canonical governance TurnKey and adds exactly one closed quota dimension.
type QuotaReservationKey struct {
	TurnKey TurnKey   `json:"turn_key"`
	Kind    QuotaKind `json:"quota_kind"`
}

func (k QuotaReservationKey) Validate() error {
	if err := k.TurnKey.Validate(); err != nil {
		return err
	}
	if !k.Kind.Valid() {
		return fmt.Errorf("%w: quota reservation kind %q", ErrValidation, k.Kind)
	}
	return nil
}

func (k QuotaReservationKey) Equal(other QuotaReservationKey) bool {
	return k.TurnKey.Equal(other.TurnKey) && k.Kind == other.Kind
}

// String is a stable human-readable key for logs and receipt references.  It
// is not persisted as an additional identity column.
func (k QuotaReservationKey) String() string {
	return strings.Join([]string{k.TurnKey.GoalID, k.TurnKey.TodoID,
		strconv.FormatInt(k.TurnKey.TurnSeq, 10), string(k.Kind)}, ":")
}

// PriceSnapshotRef freezes the exact model price used by one Run. Prices are
// integer micro-USD per million tokens; floating point is not part of the
// accounting contract. A turn-level reservation deliberately does not own a
// price because one Turn may contain Runs using different models.
type PriceSnapshotRef struct {
	ModelRef                        string    `json:"model_ref" yaml:"model_ref"`
	Currency                        string    `json:"currency" yaml:"currency"`
	InputUncachedMicroUSDPerMillion int64     `json:"input_uncached_microusd_per_million" yaml:"input_uncached_microusd_per_million"`
	CacheReadMicroUSDPerMillion     int64     `json:"cache_read_microusd_per_million" yaml:"cache_read_microusd_per_million"`
	CacheWriteMicroUSDPerMillion    int64     `json:"cache_write_microusd_per_million" yaml:"cache_write_microusd_per_million"`
	OutputMicroUSDPerMillion        int64     `json:"output_microusd_per_million" yaml:"output_microusd_per_million"`
	EffectiveAt                     time.Time `json:"effective_at" yaml:"effective_at"`
	PriceVersion                    string    `json:"price_version" yaml:"price_version"`
	Digest                          string    `json:"digest,omitempty" yaml:"digest,omitempty"`
}

func (p *PriceSnapshotRef) Validate() error {
	return VerifyPriceSnapshotDigest(p)
}

func (p *PriceSnapshotRef) Equal(other *PriceSnapshotRef) bool {
	if p == nil || other == nil {
		return p == nil && other == nil
	}
	return p.ModelRef == other.ModelRef && p.Currency == other.Currency &&
		p.InputUncachedMicroUSDPerMillion == other.InputUncachedMicroUSDPerMillion &&
		p.CacheReadMicroUSDPerMillion == other.CacheReadMicroUSDPerMillion &&
		p.CacheWriteMicroUSDPerMillion == other.CacheWriteMicroUSDPerMillion &&
		p.OutputMicroUSDPerMillion == other.OutputMicroUSDPerMillion &&
		p.EffectiveAt.Equal(other.EffectiveAt) && p.PriceVersion == other.PriceVersion &&
		p.Digest == other.Digest
}

// QuotaReservation is a frozen admission reservation.  Policy and price
// snapshots are copied into this row at admission and never re-read from
// mutable Goal/model configuration during settlement.
type QuotaReservation struct {
	Key               QuotaReservationKey    `json:"key"`
	Status            QuotaReservationStatus `json:"status"`
	ReservedAmount    int64                  `json:"reserved_amount"`
	CommittedAmount   int64                  `json:"committed_amount"`
	ReleasedAmount    int64                  `json:"released_amount"`
	PolicyLimit       int64                  `json:"policy_limit"`
	PolicyEnforcement QuotaEnforcement       `json:"policy_enforcement"`
	PolicyDigest      string                 `json:"policy_digest"`
	Version           int                    `json:"version"`
	CreatedAt         time.Time              `json:"created_at"`
	UpdatedAt         time.Time              `json:"updated_at"`
}

func (r *QuotaReservation) Validate() error {
	if r == nil {
		return fmt.Errorf("%w: nil quota reservation", ErrValidation)
	}
	if err := r.Key.Validate(); err != nil {
		return err
	}
	if !r.Status.Valid() {
		return fmt.Errorf("%w: quota reservation status %q", ErrValidation, r.Status)
	}
	for field, value := range map[string]int64{
		"reserved_amount":  r.ReservedAmount,
		"committed_amount": r.CommittedAmount,
		"released_amount":  r.ReleasedAmount,
		"policy_limit":     r.PolicyLimit,
	} {
		if value < 0 {
			return fmt.Errorf("%w: quota reservation.%s must be >= 0", ErrValidation, field)
		}
	}
	if r.CommittedAmount > maxInt64-r.ReleasedAmount ||
		r.CommittedAmount+r.ReleasedAmount > r.ReservedAmount {
		return fmt.Errorf("%w: quota reservation committed+released exceeds reserved", ErrValidation)
	}
	if r.Status.IsTerminal() && r.CommittedAmount+r.ReleasedAmount != r.ReservedAmount {
		return fmt.Errorf("%w: terminal quota reservation must settle all reserved amount", ErrValidation)
	}
	if !r.PolicyEnforcement.Valid() {
		return fmt.Errorf("%w: quota reservation policy enforcement %q", ErrValidation, r.PolicyEnforcement)
	}
	if err := ValidateCanonicalDigest(r.PolicyDigest); err != nil {
		return fmt.Errorf("%w: quota reservation.policy_digest: %v", ErrValidation, err)
	}
	if r.Version < 1 {
		return fmt.Errorf("%w: quota reservation.version must be >= 1", ErrValidation)
	}
	if r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: quota reservation timestamps are required", ErrValidation)
	}
	return nil
}

// Transition applies the only legal reservation state transitions.  Amount
// changes are intentionally left to the caller so Commit can record actual
// spend and released remainder in one CAS.
func (r *QuotaReservation) Transition(to QuotaReservationStatus, now time.Time) error {
	if r == nil {
		return fmt.Errorf("%w: nil quota reservation", ErrValidation)
	}
	if !r.Status.CanTransitionTo(to) {
		return &TransitionError{Entity: "quota_reservation", From: string(r.Status), To: string(to)}
	}
	r.Status = to
	r.Version++
	r.UpdatedAt = now
	return nil
}

// QuotaSpendStatus is the terminal accounting result for one Run and one
// quota kind.  unresolved is deliberately zero-valued: it records the gap
// without inventing spend that cannot be proved.
type QuotaSpendStatus string

const (
	QuotaSpendCommitted  QuotaSpendStatus = "committed"
	QuotaSpendUnresolved QuotaSpendStatus = "unresolved"
	// QuotaUsageBasisPerRun is the only usage basis accepted by this ledger.
	QuotaUsageBasisPerRun = "per_run"
)

func (s QuotaSpendStatus) Valid() bool {
	return s == QuotaSpendCommitted || s == QuotaSpendUnresolved
}

// QuotaSpendKey is append-only spend identity.  turn_count is intentionally
// not represented by a spend row; it is charged by successful admission.
type QuotaSpendKey struct {
	TurnKey TurnKey   `json:"turn_key"`
	Kind    QuotaKind `json:"quota_kind"`
	RunID   string    `json:"run_id"`
}

func (k QuotaSpendKey) Validate() error {
	if err := k.TurnKey.Validate(); err != nil {
		return err
	}
	if !k.Kind.Valid() {
		return fmt.Errorf("%w: quota spend kind %q", ErrValidation, k.Kind)
	}
	if k.Kind == QuotaTurnCount || k.Kind == QuotaActiveWorker {
		return fmt.Errorf("%w: %s has no spend entry", ErrValidation, k.Kind)
	}
	if err := validateTypedID("quota spend.run_id", k.RunID, PrefixRun); err != nil {
		return err
	}
	return nil
}

func (k QuotaSpendKey) Equal(other QuotaSpendKey) bool {
	return k.TurnKey.Equal(other.TurnKey) && k.Kind == other.Kind && k.RunID == other.RunID
}

// String is a stable human-readable spend key for logs and reconciliation;
// the composite columns remain the persistence identity.
func (k QuotaSpendKey) String() string {
	return strings.Join([]string{k.TurnKey.GoalID, k.TurnKey.TodoID,
		strconv.FormatInt(k.TurnKey.TurnSeq, 10), string(k.Kind), k.RunID}, ":")
}

// QuotaSpendEntry is the immutable per-Run accounting result.  CreatedAt is
// the durable settlement timestamp; replay keeps the original timestamp.
type QuotaSpendEntry struct {
	Key          QuotaSpendKey    `json:"key"`
	Amount       int64            `json:"amount"`
	UsageBasis   string           `json:"usage_basis"`
	UsageDigest  string           `json:"usage_digest"`
	PolicyDigest string           `json:"policy_digest"`
	PriceDigest  string           `json:"price_digest,omitempty"`
	Status       QuotaSpendStatus `json:"status"`
	Reason       string           `json:"reason,omitempty"`
	CreatedAt    time.Time        `json:"created_at"`
}

func (e *QuotaSpendEntry) Validate() error {
	if e == nil {
		return fmt.Errorf("%w: nil quota spend entry", ErrValidation)
	}
	if err := e.Key.Validate(); err != nil {
		return err
	}
	if e.Amount < 0 {
		return fmt.Errorf("%w: quota spend.amount must be >= 0", ErrValidation)
	}
	if e.UsageBasis != QuotaUsageBasisPerRun {
		return fmt.Errorf("%w: quota spend.usage_basis must be per_run", ErrValidation)
	}
	if err := ValidateCanonicalDigest(e.UsageDigest); err != nil {
		return fmt.Errorf("%w: quota spend.usage_digest: %v", ErrValidation, err)
	}
	if err := ValidateCanonicalDigest(e.PolicyDigest); err != nil {
		return fmt.Errorf("%w: quota spend.policy_digest: %v", ErrValidation, err)
	}
	if !e.Status.Valid() {
		return fmt.Errorf("%w: quota spend.status %q", ErrValidation, e.Status)
	}
	if e.Status == QuotaSpendUnresolved {
		if e.Amount != 0 {
			return fmt.Errorf("%w: unresolved quota spend amount must be zero", ErrValidation)
		}
		if err := validateText("quota spend.reason", e.Reason, 2000); err != nil {
			return err
		}
	} else if e.Reason != "" {
		if err := validateText("quota spend.reason", e.Reason, 2000); err != nil {
			return err
		}
	}
	if e.Key.Kind == QuotaCostMicroUSD {
		if err := ValidateCanonicalDigest(e.PriceDigest); err != nil {
			return fmt.Errorf("%w: quota spend.price_digest: %v", ErrValidation, err)
		}
	} else if e.PriceDigest != "" {
		return fmt.Errorf("%w: price_digest is only valid for cost_microusd", ErrValidation)
	}
	if e.CreatedAt.IsZero() {
		return fmt.Errorf("%w: quota spend.created_at is required", ErrValidation)
	}
	return nil
}

const maxInt64 = int64(1<<63 - 1)

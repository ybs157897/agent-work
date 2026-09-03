package domain

import "fmt"

// CostUnresolvedReason explains why a provider usage report cannot be turned
// into a proven cost. Unresolved is distinct from arithmetic/shape failure:
// callers may record the former as usage_unresolved without inventing spend.
type CostUnresolvedReason string

const (
	CostUnresolvedPriceUnavailable     CostUnresolvedReason = "price_unavailable"
	CostUnresolvedMissingCounter       CostUnresolvedReason = "missing_counter"
	CostUnresolvedCacheSplit           CostUnresolvedReason = "cache_split_unavailable"
	CostUnresolvedCounterContradiction CostUnresolvedReason = "counter_contradiction"
)

// CostUnresolvedError is a classified, fail-closed usage gap. It unwraps to
// ErrValidation so existing application error handling remains conservative.
type CostUnresolvedError struct {
	Reason  CostUnresolvedReason
	Message string
}

func (e *CostUnresolvedError) Error() string {
	if e == nil {
		return "cost usage unresolved"
	}
	if e.Message == "" {
		return fmt.Sprintf("cost usage unresolved: %s", e.Reason)
	}
	return fmt.Sprintf("cost usage unresolved (%s): %s", e.Reason, e.Message)
}

func (e *CostUnresolvedError) Unwrap() error { return ErrValidation }

// CostArithmeticOperation identifies the checked integer operation that
// failed. ROUND refers to the final quotient increment, not an overflowing
// numerator offset; the implementation never adds 500000 to that numerator.
type CostArithmeticOperation string

const (
	CostArithmeticMultiply CostArithmeticOperation = "multiply"
	CostArithmeticAdd      CostArithmeticOperation = "add"
	CostArithmeticRound    CostArithmeticOperation = "round"
)

// CostArithmeticError is a fail-closed checked-arithmetic failure.
type CostArithmeticError struct {
	Operation CostArithmeticOperation
	Message   string
}

func (e *CostArithmeticError) Error() string {
	if e == nil {
		return "cost arithmetic failed"
	}
	return fmt.Sprintf("cost arithmetic %s failed: %s", e.Operation, e.Message)
}

func (e *CostArithmeticError) Unwrap() error { return ErrValidation }

// ComputeCostMicroUSD computes cost_microusd from a validated per-run counter
// set and one immutable price snapshot. The result is rounded once, after all
// four bucket products are added, using half-up integer division by one
// million. No floating-point operation participates in the calculation.
//
// When cache read/write counters are not both known, total input minus
// uncached input supplies only a combined cache bucket. That fallback is safe
// only when both cache prices are equal; otherwise the cost remains
// unresolved. input_tokens_total is not required when all billable buckets are
// explicitly present because it is a separate quota unit.
func ComputeCostMicroUSD(counters UsageCountersV1, price *PriceSnapshotRef) (int64, error) {
	if price == nil {
		return 0, &CostUnresolvedError{
			Reason:  CostUnresolvedPriceUnavailable,
			Message: "price snapshot is absent",
		}
	}
	if err := price.Validate(); err != nil {
		return 0, err
	}
	if err := validateCostCounters(counters); err != nil {
		return 0, err
	}

	if counters.InputUncachedTokens == nil {
		return 0, unresolvedCost(CostUnresolvedMissingCounter, "input_uncached_tokens is unknown")
	}
	if counters.OutputTokens == nil {
		return 0, unresolvedCost(CostUnresolvedMissingCounter, "output_tokens is unknown")
	}

	uncached := *counters.InputUncachedTokens
	output := *counters.OutputTokens
	if counters.CacheReadTokens != nil && counters.CacheWriteTokens != nil {
		read := *counters.CacheReadTokens
		write := *counters.CacheWriteTokens
		if counters.InputTokensTotal != nil {
			inputSum, err := checkedCostAdd(uncached, read)
			if err != nil {
				return 0, err
			}
			inputSum, err = checkedCostAdd(inputSum, write)
			if err != nil {
				return 0, err
			}
			if inputSum != *counters.InputTokensTotal {
				return 0, unresolvedCost(CostUnresolvedCounterContradiction,
					"input_tokens_total differs from uncached+cache_read+cache_write")
			}
		}
		return computeCostBuckets(uncached, read, write, output, price)
	}

	// At least one cache dimension is unknown. A total and uncached counter can
	// still prove the combined cache amount, but it cannot prove its split at
	// different prices.
	if counters.InputTokensTotal == nil {
		return 0, unresolvedCost(CostUnresolvedMissingCounter,
			"input_tokens_total is required when cache counters are incomplete")
	}
	combined, err := checkedCostSubtract(*counters.InputTokensTotal, uncached)
	if err != nil {
		return 0, unresolvedCost(CostUnresolvedCounterContradiction,
			"input_tokens_total is below input_uncached_tokens")
	}
	if counters.CacheReadTokens != nil && *counters.CacheReadTokens > combined {
		return 0, unresolvedCost(CostUnresolvedCounterContradiction,
			"cache_read_tokens exceeds combined cached input")
	}
	if counters.CacheWriteTokens != nil && *counters.CacheWriteTokens > combined {
		return 0, unresolvedCost(CostUnresolvedCounterContradiction,
			"cache_write_tokens exceeds combined cached input")
	}
	if price.CacheReadMicroUSDPerMillion != price.CacheWriteMicroUSDPerMillion {
		return 0, unresolvedCost(CostUnresolvedCacheSplit,
			"cache read/write prices differ while the provider exposed no complete split")
	}
	return computeCostBuckets(uncached, combined, 0, output, &PriceSnapshotRef{
		ModelRef:                        price.ModelRef,
		Currency:                        price.Currency,
		InputUncachedMicroUSDPerMillion: price.InputUncachedMicroUSDPerMillion,
		CacheReadMicroUSDPerMillion:     price.CacheReadMicroUSDPerMillion,
		CacheWriteMicroUSDPerMillion:    price.CacheWriteMicroUSDPerMillion,
		OutputMicroUSDPerMillion:        price.OutputMicroUSDPerMillion,
		EffectiveAt:                     price.EffectiveAt,
		PriceVersion:                    price.PriceVersion,
		Digest:                          price.Digest,
	})
}

func validateCostCounters(counters UsageCountersV1) error {
	for name, value := range map[string]*int64{
		"input_tokens_total":    counters.InputTokensTotal,
		"input_uncached_tokens": counters.InputUncachedTokens,
		"cache_read_tokens":     counters.CacheReadTokens,
		"cache_write_tokens":    counters.CacheWriteTokens,
		"output_tokens":         counters.OutputTokens,
	} {
		if value != nil && *value < 0 {
			return fmt.Errorf("%w: cost usage counter %s must be >= 0", ErrValidation, name)
		}
	}
	return nil
}

func computeCostBuckets(uncached, read, write, output int64, price *PriceSnapshotRef) (int64, error) {
	numerator := int64(0)
	for _, term := range []struct {
		name  string
		token int64
		rate  int64
	}{
		{name: "input_uncached", token: uncached, rate: price.InputUncachedMicroUSDPerMillion},
		{name: "cache_read", token: read, rate: price.CacheReadMicroUSDPerMillion},
		{name: "cache_write", token: write, rate: price.CacheWriteMicroUSDPerMillion},
		{name: "output", token: output, rate: price.OutputMicroUSDPerMillion},
	} {
		product, err := checkedCostMultiply(term.token, term.rate)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", term.name, err)
		}
		numerator, err = checkedCostAdd(numerator, product)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", term.name, err)
		}
	}
	return roundCostHalfUp(numerator)
}

func checkedCostMultiply(left, right int64) (int64, error) {
	if left < 0 || right < 0 {
		return 0, &CostArithmeticError{
			Operation: CostArithmeticMultiply,
			Message:   "negative operand",
		}
	}
	if left != 0 && right > maxInt64/left {
		return 0, &CostArithmeticError{
			Operation: CostArithmeticMultiply,
			Message:   "non-negative int64 multiplication overflow",
		}
	}
	return left * right, nil
}

func checkedCostAdd(left, right int64) (int64, error) {
	if left < 0 || right < 0 || left > maxInt64-right {
		return 0, &CostArithmeticError{
			Operation: CostArithmeticAdd,
			Message:   "non-negative int64 addition overflow",
		}
	}
	return left + right, nil
}

func checkedCostSubtract(left, right int64) (int64, error) {
	if left < 0 || right < 0 || right > left {
		return 0, &CostArithmeticError{
			Operation: CostArithmeticAdd,
			Message:   "non-negative int64 subtraction underflow",
		}
	}
	return left - right, nil
}

func roundCostHalfUp(numerator int64) (int64, error) {
	const million int64 = 1_000_000
	quotient, remainder := numerator/million, numerator%million
	if remainder >= million/2 {
		if quotient == maxInt64 {
			return 0, &CostArithmeticError{
				Operation: CostArithmeticRound,
				Message:   "rounded quotient overflow",
			}
		}
		quotient++
	}
	return quotient, nil
}

func unresolvedCost(reason CostUnresolvedReason, message string) error {
	return &CostUnresolvedError{Reason: reason, Message: message}
}

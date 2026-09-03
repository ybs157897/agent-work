package domain

import (
	"errors"
	"testing"
	"time"
)

func TestComputeCostMicroUSDFullBuckets(t *testing.T) {
	price := costPriceForTest(t, 2, 3, 5, 7)
	counters := UsageCountersV1{
		InputTokensTotal:    costCounter(6_000_000),
		InputUncachedTokens: costCounter(1_000_000),
		CacheReadTokens:     costCounter(2_000_000),
		CacheWriteTokens:    costCounter(3_000_000),
		OutputTokens:        costCounter(4_000_000),
	}
	got, err := ComputeCostMicroUSD(counters, price)
	if err != nil {
		t.Fatal(err)
	}
	if got != 51 {
		t.Fatalf("cost = %d, want 51", got)
	}
}

func TestComputeCostMicroUSDZeroPrice(t *testing.T) {
	price := costPriceForTest(t, 0, 0, 0, 0)
	counters := UsageCountersV1{
		InputTokensTotal:    costCounter(10),
		InputUncachedTokens: costCounter(4),
		CacheReadTokens:     costCounter(3),
		CacheWriteTokens:    costCounter(3),
		OutputTokens:        costCounter(2),
	}
	got, err := ComputeCostMicroUSD(counters, price)
	if err != nil || got != 0 {
		t.Fatalf("zero price must produce zero cost: got=%d err=%v", got, err)
	}
}

func TestComputeCostMicroUSDRoundsHalfUpWithoutFloat(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tokens int64
		want   int64
	}{
		{name: "below half", tokens: 499_999, want: 0},
		{name: "exact half", tokens: 500_000, want: 1},
		{name: "above half", tokens: 500_001, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			price := costPriceForTest(t, 1, 0, 0, 0)
			got, err := ComputeCostMicroUSD(UsageCountersV1{
				InputUncachedTokens: costCounter(tc.tokens),
				CacheReadTokens:     costCounter(0),
				CacheWriteTokens:    costCounter(0),
				OutputTokens:        costCounter(0),
			}, price)
			if err != nil || got != tc.want {
				t.Fatalf("rounded cost = %d, want %d (err=%v)", got, tc.want, err)
			}
		})
	}
}

func TestComputeCostMicroUSDSamePriceAllowsCombinedCache(t *testing.T) {
	price := costPriceForTest(t, 2, 5, 5, 7)
	got, err := ComputeCostMicroUSD(UsageCountersV1{
		InputTokensTotal:    costCounter(1_000_000),
		InputUncachedTokens: costCounter(400_000),
		// Provider exposed only total input and uncached input; the cache
		// read/write split is intentionally unknown.
		OutputTokens: costCounter(0),
	}, price)
	if err != nil {
		t.Fatal(err)
	}
	if got != 4 {
		t.Fatalf("combined cache cost = %d, want 4", got)
	}
}

func TestComputeCostMicroUSDDifferentCachePricesAreUnresolved(t *testing.T) {
	price := costPriceForTest(t, 2, 5, 6, 7)
	_, err := ComputeCostMicroUSD(UsageCountersV1{
		InputTokensTotal:    costCounter(1_000_000),
		InputUncachedTokens: costCounter(400_000),
		OutputTokens:        costCounter(0),
	}, price)
	var unresolved *CostUnresolvedError
	if !errors.As(err, &unresolved) || unresolved.Reason != CostUnresolvedCacheSplit {
		t.Fatalf("different cache prices must be classified unresolved: %v", err)
	}
}

func TestComputeCostMicroUSDMissingAndContradictoryCountersAreUnresolved(t *testing.T) {
	price := costPriceForTest(t, 2, 3, 3, 7)
	cases := []struct {
		name     string
		counters UsageCountersV1
		reason   CostUnresolvedReason
	}{
		{
			name: "missing output",
			counters: UsageCountersV1{
				InputUncachedTokens: costCounter(1), CacheReadTokens: costCounter(0),
				CacheWriteTokens: costCounter(0),
			},
			reason: CostUnresolvedMissingCounter,
		},
		{
			name: "decomposition mismatch",
			counters: UsageCountersV1{
				InputTokensTotal: costCounter(10), InputUncachedTokens: costCounter(4),
				CacheReadTokens: costCounter(3), CacheWriteTokens: costCounter(2),
				OutputTokens: costCounter(0),
			},
			reason: CostUnresolvedCounterContradiction,
		},
		{
			name: "total below uncached",
			counters: UsageCountersV1{
				InputTokensTotal: costCounter(3), InputUncachedTokens: costCounter(4),
				OutputTokens: costCounter(0),
			},
			reason: CostUnresolvedCounterContradiction,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ComputeCostMicroUSD(tc.counters, price)
			var unresolved *CostUnresolvedError
			if !errors.As(err, &unresolved) || unresolved.Reason != tc.reason {
				t.Fatalf("want unresolved reason %s, got %v", tc.reason, err)
			}
		})
	}
}

func TestComputeCostMicroUSDRejectsNegativeCounters(t *testing.T) {
	price := costPriceForTest(t, 1, 1, 1, 1)
	_, err := ComputeCostMicroUSD(UsageCountersV1{
		InputUncachedTokens: costCounter(-1), CacheReadTokens: costCounter(0),
		CacheWriteTokens: costCounter(0), OutputTokens: costCounter(0),
	}, price)
	var unresolved *CostUnresolvedError
	if err == nil || errors.As(err, &unresolved) || !errors.Is(err, ErrValidation) {
		t.Fatalf("negative counters must fail closed as validation: %v", err)
	}
}

func TestComputeCostMicroUSDCheckedArithmetic(t *testing.T) {
	max := int64(^uint64(0) >> 1)
	price := costPriceForTest(t, 2, 1, 1, 1)
	_, err := ComputeCostMicroUSD(UsageCountersV1{
		InputUncachedTokens: costCounter(max), CacheReadTokens: costCounter(0),
		CacheWriteTokens: costCounter(0), OutputTokens: costCounter(0),
	}, price)
	var arithmetic *CostArithmeticError
	if !errors.As(err, &arithmetic) || arithmetic.Operation != CostArithmeticMultiply {
		t.Fatalf("multiplication overflow must be classified: %v", err)
	}

	half := max/2 + 1
	price = costPriceForTest(t, 1, 1, 1, 1)
	_, err = ComputeCostMicroUSD(UsageCountersV1{
		InputUncachedTokens: costCounter(half), CacheReadTokens: costCounter(half),
		CacheWriteTokens: costCounter(0), OutputTokens: costCounter(0),
	}, price)
	if !errors.As(err, &arithmetic) || arithmetic.Operation != CostArithmeticAdd {
		t.Fatalf("addition overflow must be classified: %v", err)
	}
}

func TestComputeCostMicroUSDRoundGuardHandlesMaximumNumerator(t *testing.T) {
	max := int64(^uint64(0) >> 1)
	price := costPriceForTest(t, 1, 0, 0, 0)
	got, err := ComputeCostMicroUSD(UsageCountersV1{
		InputUncachedTokens: costCounter(max), CacheReadTokens: costCounter(0),
		CacheWriteTokens: costCounter(0), OutputTokens: costCounter(0),
	}, price)
	if err != nil {
		t.Fatal(err)
	}
	want := max/1_000_000 + 1 // max%1_000_000 >= 500_000; no +500000 overflow.
	if got != want {
		t.Fatalf("maximum numerator rounding = %d, want %d", got, want)
	}
}

func costCounter(value int64) *int64 { return &value }

func costPriceForTest(t *testing.T, input, read, write, output int64) *PriceSnapshotRef {
	t.Helper()
	price := &PriceSnapshotRef{
		ModelRef:                        "model:cost-test",
		Currency:                        "USD",
		InputUncachedMicroUSDPerMillion: input,
		CacheReadMicroUSDPerMillion:     read,
		CacheWriteMicroUSDPerMillion:    write,
		OutputMicroUSDPerMillion:        output,
		EffectiveAt:                     time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		PriceVersion:                    "price-v1",
	}
	if err := price.Normalize(); err != nil {
		t.Fatal(err)
	}
	return price
}

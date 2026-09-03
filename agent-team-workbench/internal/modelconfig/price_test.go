package modelconfig

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestRegistryPriceSnapshotRoundTripComputesDigestAndUTC(t *testing.T) {
	dir := t.TempDir()
	writeRegistry(t, dir, `
providers:
  - id: prov-priced
    provider: priced
    api_key_env: PRICED_API_KEY
    models:
      - id: priced-v1
        display_name: Priced v1
        model: priced-v1
        price:
          model_ref: priced-v1
          currency: USD
          input_uncached_microusd_per_million: 11
          cache_read_microusd_per_million: 2
          cache_write_microusd_per_million: 3
          output_microusd_per_million: 19
          effective_at: "2026-09-01T08:00:00+08:00"
          price_version: price-v1
`)

	entry, err := NewRegistry(dir).Get("priced-v1")
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil || entry.Price == nil {
		t.Fatalf("price snapshot must survive ModelDef -> Entry: %+v", entry)
	}
	if entry.Price.EffectiveAt.Location() != time.UTC || entry.Price.EffectiveAt.Hour() != 0 {
		t.Fatalf("effective_at must normalize to UTC: %v", entry.Price.EffectiveAt)
	}
	want, err := domain.ComputePriceSnapshotDigest(entry.Price)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Price.Digest != want {
		t.Fatalf("missing digest must be computed: got=%q want=%q", entry.Price.Digest, want)
	}

	// The old registry shape has no price block and remains readable.
	legacyDir := t.TempDir()
	writeRegistry(t, legacyDir, `
providers:
  - id: prov-legacy
    provider: legacy
    api_key_env: LEGACY_API_KEY
    models:
      - id: legacy-v1
        display_name: Legacy v1
        model: legacy-v1
`)
	legacy, err := NewRegistry(legacyDir).Get("legacy-v1")
	if err != nil || legacy == nil || legacy.Price != nil {
		t.Fatalf("registry without price must remain readable: entry=%+v err=%v", legacy, err)
	}
}

func TestRegistryPriceSnapshotRejectsContentTampering(t *testing.T) {
	price := &domain.PriceSnapshotRef{
		ModelRef:                        "priced-v1",
		Currency:                        "USD",
		InputUncachedMicroUSDPerMillion: 11,
		CacheReadMicroUSDPerMillion:     2,
		CacheWriteMicroUSDPerMillion:    3,
		OutputMicroUSDPerMillion:        19,
		EffectiveAt:                     mustParsePriceTime("2026-09-01T00:00:00Z"),
		PriceVersion:                    "price-v1",
	}
	if err := price.Normalize(); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf(`
providers:
  - id: prov-priced
    provider: priced
    api_key_env: PRICED_API_KEY
    models:
      - id: priced-v1
        display_name: Priced v1
        model: priced-v1
        price:
          model_ref: priced-v1
          currency: USD
          input_uncached_microusd_per_million: 11
          cache_read_microusd_per_million: 2
          cache_write_microusd_per_million: 3
          output_microusd_per_million: 20
          effective_at: "2026-09-01T00:00:00Z"
          price_version: price-v1
          digest: %s
`, price.Digest)
	dir := t.TempDir()
	writeRegistry(t, dir, content)
	if _, err := NewRegistry(dir).List(); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("price content tampering must be rejected: %v", err)
	}
}

func TestEntryPriceSnapshotUpsertRoundTrip(t *testing.T) {
	dir := t.TempDir()
	entry := &Entry{
		ID: "priced-upsert", Provider: "priced", Model: "priced-v1", APIKeyEnv: "PRICED_API_KEY",
		Price: &domain.PriceSnapshotRef{
			ModelRef:                        "priced-upsert",
			Currency:                        "USD",
			InputUncachedMicroUSDPerMillion: 1,
			CacheReadMicroUSDPerMillion:     2,
			CacheWriteMicroUSDPerMillion:    3,
			OutputMicroUSDPerMillion:        4,
			EffectiveAt:                     mustParsePriceTime("2026-09-01T00:00:00Z"),
			PriceVersion:                    "v1",
		},
	}
	if err := NewRegistry(dir).Upsert(entry); err != nil {
		t.Fatal(err)
	}
	got, err := NewRegistry(dir).Get(entry.ID)
	if err != nil || got == nil || got.Price == nil {
		t.Fatalf("upsert price roundtrip failed: entry=%+v err=%v", got, err)
	}
	if got.Price.Digest == "" || !strings.HasPrefix(got.Price.Digest, "sha256:") {
		t.Fatalf("upsert must persist computed digest: %+v", got.Price)
	}
	if got.Price.ModelRef != entry.ID {
		t.Fatalf("price snapshot must retain the model entry lineage: model_ref=%q entry=%q", got.Price.ModelRef, entry.ID)
	}
}

func TestRegistryRejectsPriceSnapshotForDifferentModel(t *testing.T) {
	dir := t.TempDir()
	writeRegistry(t, dir, `
providers:
  - id: prov-priced
    provider: priced
    api_key_env: PRICED_API_KEY
    models:
      - id: priced-v1
        display_name: Priced v1
        model: priced-v1
        price:
          model_ref: another-model
          currency: USD
          input_uncached_microusd_per_million: 11
          cache_read_microusd_per_million: 2
          cache_write_microusd_per_million: 3
          output_microusd_per_million: 19
          effective_at: "2026-09-01T00:00:00Z"
          price_version: price-v1
`)
	if _, err := NewRegistry(dir).List(); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("price snapshot with a foreign model_ref must be rejected: %v", err)
	}
}

func mustParsePriceTime(value string) (t time.Time) {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		panic(err)
	}
	return t
}

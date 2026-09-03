package application_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/orchestrator"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

func TestCreateRunPersistsIndependentPriceSnapshotWithoutChangingConfigDigest(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	wi := seedRunEnv(t, ctx, svc, store)

	agent, err := store.Agents().Get(ctx, wi.AgentProfileID)
	if err != nil {
		t.Fatal(err)
	}
	agent.ModelOverride.Ref = "priced-v1"
	agent.Version++
	agent.UpdatedAt = time.Now().UTC()
	if err := store.Agents().Update(ctx, agent, agent.Version-1); err != nil {
		t.Fatal(err)
	}

	price := testPriceSnapshot(t, 11)
	svc.ModelResolver = func(ref string) (orchestrator.ModelSpec, bool) {
		if ref != price.ModelRef {
			return orchestrator.ModelSpec{}, false
		}
		return orchestrator.ModelSpec{Ref: ref, Provider: "codex", Model: ref, PriceSnapshot: price}, true
	}
	run, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: wi.AgentProfileID, Instruction: "price snapshot",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := run.Input["price_snapshot"].(map[string]any); !ok {
		t.Fatalf("Run must carry an independent price_snapshot object: %#v", run.Input)
	}
	model, _ := run.Input["model"].(map[string]any)
	if _, ok := model["price_snapshot"]; ok {
		t.Fatal("price snapshot must not be nested in provider model config")
	}
	decoded := decodeRunPriceSnapshot(t, run.Input["price_snapshot"])
	if decoded.Digest != price.Digest || !decoded.EffectiveAt.Equal(price.EffectiveAt) {
		t.Fatalf("Run price snapshot roundtrip mismatch: got=%+v want=%+v", decoded, price)
	}

	conversation, _ := run.Input["conversation"].(map[string]any)
	baseDigest, _ := conversation["config_digest"].(string)
	run.Input["price_snapshot"].(map[string]any)["digest"] = "sha256:" + strings.Repeat("0", 64)
	if got := orchestrator.ConfigDigest(run.Input); got != baseDigest {
		t.Fatalf("price-only input changes must not change provider config digest: got=%s want=%s", got, baseDigest)
	}

	stored, err := store.Runs().Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stored.Input["price_snapshot"].(map[string]any); !ok {
		t.Fatalf("persisted Run price snapshot missing: %#v", stored.Input)
	}
}

func testPriceSnapshot(t *testing.T, output int64) *domain.PriceSnapshotRef {
	t.Helper()
	price := &domain.PriceSnapshotRef{
		ModelRef:                        "priced-v1",
		Currency:                        "USD",
		InputUncachedMicroUSDPerMillion: 1,
		CacheReadMicroUSDPerMillion:     2,
		CacheWriteMicroUSDPerMillion:    3,
		OutputMicroUSDPerMillion:        output,
		EffectiveAt:                     time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		PriceVersion:                    "v1",
	}
	if err := price.Normalize(); err != nil {
		t.Fatal(err)
	}
	return price
}

func decodeRunPriceSnapshot(t *testing.T, value any) domain.PriceSnapshotRef {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var price domain.PriceSnapshotRef
	if err := json.Unmarshal(raw, &price); err != nil {
		t.Fatal(err)
	}
	if err := price.Validate(); err != nil {
		t.Fatalf("Run price snapshot must satisfy domain digest contract: %v", err)
	}
	return price
}

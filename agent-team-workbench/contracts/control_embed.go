// Package contracts embeds machine contracts that production code must use
// verbatim. Source files under contracts/ remain the single editable truth.
package contracts

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"slices"
)

//go:embed control/plan-decision-v2.schema.json
var planDecisionV2Schema []byte

func PlanDecisionV2Schema() []byte {
	return slices.Clone(planDecisionV2Schema)
}

func PlanDecisionV2SchemaDigest() string {
	sum := sha256.Sum256(planDecisionV2Schema)
	return "sha256:" + hex.EncodeToString(sum[:])
}

package application

import (
	"github.com/ybs/agent-team-workbench/internal/domain"
	governancekernel "github.com/ybs/agent-team-workbench/internal/governance"
)

const emptySHA256Digest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

func ComputeTurnReceiptHeaderDigest(header *domain.TurnReceiptHeader) (string, error) {
	return governancekernel.ComputeHeaderDigest(header)
}

func ComputeTurnReceiptPhaseDigest(phase *domain.TurnReceiptPhase) (string, error) {
	return governancekernel.ComputePhaseDigest(phase)
}

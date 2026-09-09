package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// ProjectBaseline is a path-free, Host-observed checkout identity used by
// publication. Dirty checkouts are valid: the staged, unstaged, untracked and
// combined digests freeze actual content so a later probe can detect drift.
type ProjectBaseline struct {
	RepositoryIdentity string  `json:"repository_identity"`
	RefKind            RefKind `json:"ref_kind"`
	BranchName         string  `json:"branch_name,omitempty"`
	CheckoutRef        string  `json:"checkout_ref"`
	Head               string  `json:"head"`
	StagedDigest       string  `json:"staged_digest"`
	UnstagedDigest     string  `json:"unstaged_digest"`
	UntrackedDigest    string  `json:"untracked_digest"`
	ContentDigest      string  `json:"content_digest"`
}

func (b ProjectBaseline) Validate() error {
	if strings.TrimSpace(b.RepositoryIdentity) == "" || !b.RefKind.Valid() ||
		strings.TrimSpace(b.CheckoutRef) == "" || strings.TrimSpace(b.Head) == "" ||
		!validProjectBaselineDigest(b.StagedDigest) || !validProjectBaselineDigest(b.UnstagedDigest) ||
		!validProjectBaselineDigest(b.UntrackedDigest) || !validProjectBaselineDigest(b.ContentDigest) {
		return fmt.Errorf("%w: invalid project baseline", ErrValidation)
	}
	if b.RefKind == RefBranch && strings.TrimSpace(b.BranchName) == "" {
		return fmt.Errorf("%w: branch project baseline requires branch name", ErrValidation)
	}
	return nil
}

func validProjectBaselineDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// IsClean reports whether all four bounded change digests represent an empty
// staged/unstaged/untracked working tree. Dirty content remains valid and is
// accepted when these digests are frozen and later compare equal.
func (b ProjectBaseline) IsClean() bool {
	emptySum := sha256.Sum256(nil)
	empty := hex.EncodeToString(emptySum[:])
	return b.StagedDigest == empty && b.UnstagedDigest == empty &&
		b.UntrackedDigest == empty && b.ContentDigest == empty
}

// ComputeProjectBaselineDigest returns the stable fingerprint persisted with a
// publication. JSON is marshalled from a fixed struct so field order is part of
// the versioned contract and no host path can enter the digest accidentally.
func ComputeProjectBaselineDigest(b ProjectBaseline) string {
	payload := struct {
		Schema             string  `json:"schema"`
		RepositoryIdentity string  `json:"repository_identity"`
		RefKind            RefKind `json:"ref_kind"`
		BranchName         string  `json:"branch_name"`
		CheckoutRef        string  `json:"checkout_ref"`
		Head               string  `json:"head"`
		StagedDigest       string  `json:"staged_digest"`
		UnstagedDigest     string  `json:"unstaged_digest"`
		UntrackedDigest    string  `json:"untracked_digest"`
		ContentDigest      string  `json:"content_digest"`
	}{
		Schema:             "project-baseline/v1",
		RepositoryIdentity: b.RepositoryIdentity,
		RefKind:            b.RefKind,
		BranchName:         b.BranchName,
		CheckoutRef:        b.CheckoutRef,
		Head:               b.Head,
		StagedDigest:       b.StagedDigest,
		UnstagedDigest:     b.UnstagedDigest,
		UntrackedDigest:    b.UntrackedDigest,
		ContentDigest:      b.ContentDigest,
	}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

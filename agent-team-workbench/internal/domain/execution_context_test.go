package domain

import (
	"strings"
	"testing"
	"time"
)

func fixtureSnapshot() *ExecutionContextSnapshot {
	return &ExecutionContextSnapshot{
		ID:                  "ctxsnap_01JTEST0000000000000000001",
		RunID:               "run_01JTEST0000000000000000002",
		SchemaVersion:       SnapshotSchemaV1,
		WorkspaceID:         "ws_01JTEST0000000000000000003",
		WorkspaceLocationID: "wsloc_01JTEST0000000000000000004",
		LocationVersion:     3,
		MountGeneration:     "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae",
		ExecutionHostID:     LocalHostID,
		MountAlias:          "agent-work",
		RepositoryIdentity:  "repo_ybs_agent_work",
		RefKind:             RefWorktree,
		BranchName:          "codex/task-control-surface",
		CheckoutRef:         "wt_opaque_1",
		WorktreeRef:         "wt_opaque_1",
		BaseRevision:        "0366666b4ed41762fe93692ffef43a25307f5358",
		ContextGeneration:   1,
		Source:              SnapshotSourceCurrent,
		SnapshotDigest:      "",
		CreatedAt:           time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
	}
}

func TestSnapshotDigestStableAndDeterministic(t *testing.T) {
	s := fixtureSnapshot()
	d1 := s.ComputeDigest()
	for i := 0; i < 50; i++ {
		if got := s.ComputeDigest(); got != d1 {
			t.Fatalf("digest 不稳定：%q vs %q", got, d1)
		}
	}
	if len(d1) != 64 {
		t.Fatalf("digest 应为 sha256 hex（64 字符），got %d", len(d1))
	}
}

func TestSnapshotDigestIdentitySemantics(t *testing.T) {
	base := fixtureSnapshot()
	digest := base.ComputeDigest()

	// 非身份字段变化不影响 digest。
	mut := *base
	mut.RunID = "run_other"
	mut.Source = SnapshotSourceRetry
	mut.SourceSnapshotID = base.ID
	mut.CreatedAt = time.Now()
	if got := mut.ComputeDigest(); got != digest {
		t.Fatalf("非身份字段不应影响 digest：%q vs %q", got, digest)
	}

	// 任一身份字段变化必须改变 digest（alias 重指向/换代即 fail closed）。
	for name, apply := range map[string]func(*ExecutionContextSnapshot){
		"mount_generation":      func(s *ExecutionContextSnapshot) { s.MountGeneration = "ff" + s.MountGeneration[2:] },
		"repository_identity":   func(s *ExecutionContextSnapshot) { s.RepositoryIdentity = "repo_other" },
		"mount_alias":           func(s *ExecutionContextSnapshot) { s.MountAlias = "other" },
		"execution_host_id":     func(s *ExecutionContextSnapshot) { s.ExecutionHostID = "host_remote1" },
		"ref_kind":              func(s *ExecutionContextSnapshot) { s.RefKind = RefRoot },
		"worktree_ref":          func(s *ExecutionContextSnapshot) { s.WorktreeRef = "wt_opaque_2" },
		"context_generation":    func(s *ExecutionContextSnapshot) { s.ContextGeneration++ },
		"location_version":      func(s *ExecutionContextSnapshot) { s.LocationVersion++ },
		"workspace_location_id": func(s *ExecutionContextSnapshot) { s.WorkspaceLocationID = "wsloc_other" },
		"schema_version":        func(s *ExecutionContextSnapshot) { s.SchemaVersion = SnapshotSchemaLegacy },
	} {
		m := *base
		apply(&m)
		if got := m.ComputeDigest(); got == digest {
			t.Fatalf("身份字段 %s 变化必须改变 digest", name)
		}
	}
}

func TestSnapshotValidate(t *testing.T) {
	s := fixtureSnapshot()
	s.SnapshotDigest = s.ComputeDigest()
	if err := s.Validate(); err != nil {
		t.Fatalf("完整 v1 snapshot 应通过校验：%v", err)
	}

	bad := fixtureSnapshot()
	bad.SnapshotDigest = strings.Repeat("0", 64)
	if err := bad.Validate(); err == nil {
		t.Fatal("digest 与身份字段不一致必须拒绝")
	}

	missing := fixtureSnapshot()
	missing.MountGeneration = ""
	missing.SnapshotDigest = missing.ComputeDigest()
	if err := missing.Validate(); err == nil {
		t.Fatal("身份字段缺失必须拒绝")
	}

	legacy := &ExecutionContextSnapshot{
		ID: NewID(PrefixCtxSnapshot), RunID: "run_legacy",
		SchemaVersion: SnapshotSchemaLegacy, Source: SnapshotSourceLegacy,
	}
	if err := legacy.Validate(); err != nil {
		t.Fatalf("legacy-v0 审计快照应放宽校验：%v", err)
	}
}

func TestValidateRefCombo(t *testing.T) {
	cases := []struct {
		name                             string
		kind                             RefKind
		branch, checkoutRef, worktreeRef string
		wantErr                          bool
	}{
		{"root 合法", RefRoot, "", "", "", false},
		{"root 带 branch 拒绝", RefRoot, "main", "", "", true},
		{"branch 合法", RefBranch, "feat/x", "co_1", "", false},
		{"branch 缺 checkout 拒绝", RefBranch, "feat/x", "", "", true},
		{"branch 带 worktree_ref 拒绝", RefBranch, "feat/x", "co_1", "wt_1", true},
		{"worktree 合法", RefWorktree, "", "", "wt_1", false},
		{"worktree 缺 ref 拒绝", RefWorktree, "", "", "", true},
		{"未知 kind 拒绝", RefKind("tag"), "", "", "", true},
	}
	for _, tc := range cases {
		err := ValidateRefCombo(tc.kind, tc.branch, tc.checkoutRef, tc.worktreeRef)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s：wantErr=%v got=%v", tc.name, tc.wantErr, err)
		}
	}
}

func TestSnapshotCloneForRun(t *testing.T) {
	s := fixtureSnapshot()
	s.SnapshotDigest = s.ComputeDigest()
	now := time.Now().UTC()
	clone := s.CloneForRun("run_retry", SnapshotSourceRetry, now)

	if clone.ID == s.ID || clone.RunID != "run_retry" {
		t.Fatalf("clone 必须换 ID/RunID：%q %q", clone.ID, clone.RunID)
	}
	if clone.SourceSnapshotID != s.ID || clone.Source != SnapshotSourceRetry {
		t.Fatalf("clone 必须记录来源：%q %q", clone.SourceSnapshotID, clone.Source)
	}
	if clone.ComputeDigest() != s.ComputeDigest() {
		t.Fatal("clone 身份字段不变，digest 必须一致")
	}
	if err := clone.Validate(); err != nil {
		t.Fatalf("clone 应通过校验：%v", err)
	}
}

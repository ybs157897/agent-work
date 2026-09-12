package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// TestLibrarySourceUsageWriteContract pins the source write DTO to the same
// vocabulary the read side emits. decodeLibraryBody rejects unknown fields, so
// a missing field here silently breaks every source save from the admin page.
func TestLibrarySourceUsageWriteContract(t *testing.T) {
	body := []byte(`{"name":"common","kind":"common","repo_path":"common","default_ref":"main",
		"usages":[
		  {"consumer":"order-service","artifact":"com.example:common:1.4.2","environment":"prod","artifact_resolution":"declared"},
		  {"consumer":"device-service","artifact":"com.example:common:2.0.0","environment":"","artifact_resolution":"resolved"}
		]}`)
	var req librarySourceRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	usages := req.usagesOf()
	if len(usages) != 2 {
		t.Fatalf("usages lost: %+v", usages)
	}
	if usages[0].ArtifactResolution != "declared" || usages[1].ArtifactResolution != "resolved" {
		t.Fatalf("artifact_resolution must survive the write path: %+v", usages)
	}
	if usages[0].Environment != "prod" {
		t.Fatalf("environment must survive the write path: %+v", usages[0])
	}
	// The declared value must be reported as declared, never as the artifact a
	// build actually resolved.
	if got := usages[0].ArtifactState(); got != "declared" {
		t.Fatalf("a hand-registered version must stay declared, got %q", got)
	}
	if got := (domain.KnowledgeSourceUsage{Artifact: "x"}).ArtifactState(); got != "declared" {
		t.Fatalf("default resolution must be declared, got %q", got)
	}
	if got := (domain.KnowledgeSourceUsage{}).ArtifactState(); got != "unknown" {
		t.Fatalf("an empty artifact must be unknown, got %q", got)
	}
}

// TestLibrarySourceResolvedRequiresBasis covers F8: 'resolved' is a derived
// state, so a caller cannot mint it by sending a label. Without a verifiable
// basis the server records the honest declared value and says it downgraded.
func TestLibrarySourceResolvedRequiresBasis(t *testing.T) {
	body := []byte(`{"name":"common","kind":"common","repo_path":"common","usages":[
		{"consumer":"a","artifact":"com.example:common:1.4.2","artifact_resolution":"resolved"},
		{"consumer":"b","artifact":"com.example:common:2.0.0","artifact_resolution":"resolved","resolution_ref":"mvn-dependency-tree:report.xml#/dependencies"},
		{"consumer":"c","artifact":"com.example:common:3.0.0","artifact_resolution":"declared"}
	]}`)
	var req librarySourceRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	usages := req.usagesOf()
	if len(usages) != 3 {
		t.Fatalf("usages lost: %+v", usages)
	}
	if got := usages[0].ArtifactState(); got != "declared" {
		t.Fatalf("an unsupported resolved claim must fall back to declared, got %q", got)
	}
	if !usages[0].ResolutionDowngraded() {
		t.Fatal("the downgrade must be visible to the caller")
	}
	if got := usages[1].ArtifactState(); got != "resolved" {
		t.Fatalf("a resolved claim with a basis must stay resolved, got %q", got)
	}
	if usages[1].ResolutionDowngraded() {
		t.Fatal("a supported claim must not be reported as downgraded")
	}
	if got := usages[2].ArtifactState(); got != "declared" {
		t.Fatalf("declared must stay declared, got %q", got)
	}
}

// TestKnowledgeSourceUsageEffectiveState pins the state machine directly.
func TestKnowledgeSourceUsageEffectiveState(t *testing.T) {
	cases := []struct {
		name      string
		usage     domain.KnowledgeSourceUsage
		wantState string
		wantDown  bool
	}{
		{name: "no artifact", usage: domain.KnowledgeSourceUsage{}, wantState: "unknown"},
		{name: "declared default", usage: domain.KnowledgeSourceUsage{Artifact: "g:a:1"}, wantState: "declared"},
		{name: "explicit unknown", usage: domain.KnowledgeSourceUsage{Artifact: "g:a:1", ArtifactResolution: "unknown"}, wantState: "unknown"},
		{name: "resolved without basis", usage: domain.KnowledgeSourceUsage{Artifact: "g:a:1", ArtifactResolution: "resolved"}, wantState: "declared", wantDown: true},
		{name: "resolved with blank basis", usage: domain.KnowledgeSourceUsage{Artifact: "g:a:1", ArtifactResolution: "resolved", ResolutionRef: "   "}, wantState: "declared", wantDown: true},
		{name: "resolved with basis", usage: domain.KnowledgeSourceUsage{Artifact: "g:a:1", ArtifactResolution: "resolved", ResolutionRef: "build:42"}, wantState: "resolved"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.usage.ArtifactState(); got != tc.wantState {
				t.Fatalf("state = %q, want %q", got, tc.wantState)
			}
			if got := tc.usage.ResolutionDowngraded(); got != tc.wantDown {
				t.Fatalf("downgraded = %v, want %v", got, tc.wantDown)
			}
		})
	}
}

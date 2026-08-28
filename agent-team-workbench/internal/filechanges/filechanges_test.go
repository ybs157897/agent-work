package filechanges

import (
	"strings"
	"testing"
)

func str(s string) *string { return &s }

func TestSplitIntoLogicalLines(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		want        []string
	}{
		{"empty", "", []string{}}, {"nil", "", []string{}},
		{"tail newline", "a\nb\n", []string{"a", "b"}},
		{"blank line", "a\n\nb", []string{"a", "", "b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := splitLines(tc.input)
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("got %#v want %#v", got, tc.want)
			}
		})
	}
	if got := SplitIntoLogicalLines(nil); len(got) != 0 {
		t.Fatalf("nil got %#v", got)
	}
}

func TestComputeLineChangeStat(t *testing.T) {
	for _, tc := range []struct {
		name, before, after string
		added, removed      int
	}{
		{"new", "", "a\nb\n", 2, 0}, {"delete", "a\nb\n", "", 0, 2},
		{"replace", "a\nb\nc", "a\nx\nc", 1, 1},
		{"prefix suffix", "same\nold\ntail", "same\nnew\ntail", 1, 1},
		{"same", "a\n", "a\n", 0, 0}, {"blank", "a\n\nb", "a\nX\nb", 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, r := ComputeLineChangeStat(str(tc.before), tc.after)
			if a != tc.added || r != tc.removed {
				t.Fatalf("got +%d -%d want +%d -%d", a, r, tc.added, tc.removed)
			}
		})
	}
}

func TestComputeLineChangeStatLargeMidFallback(t *testing.T) {
	old := make([]string, 0, 633)
	next := make([]string, 0, 633)
	for i := 0; i < 633; i++ {
		old = append(old, "old")
		next = append(next, "new")
	}
	a, r := ComputeLineChangeStat(str(strings.Join(old, "\n")), strings.Join(next, "\n"))
	if a != 633 || r != 633 {
		t.Fatalf("got +%d -%d", a, r)
	}
}

func TestBuildPerRunSummaryFoldsWritesAndSorts(t *testing.T) {
	summary := BuildPerRunSummary([]TurnFileChanges{
		{TurnIndex: 3, Snapshots: []Snapshot{{Path: "z", BeforeContent: nil, AfterContent: "one\n", WriteCount: 1}, {Path: "a", BeforeContent: str("old\n"), AfterContent: "new\n"}}},
		{TurnIndex: 4, Snapshots: []Snapshot{{Path: "z", BeforeContent: str("one\n"), AfterContent: "one\ntwo\n", WriteCount: 2}, {Path: "a", BeforeContent: str("new\n"), AfterContent: "final\n"}}},
	})
	if summary.FileCount != 2 || summary.Added != 3 || summary.Removed != 1 {
		t.Fatalf("summary %#v", summary)
	}
	if summary.Files[0].Path != "a" || summary.Files[1].Path != "z" {
		t.Fatalf("not sorted %#v", summary.Files)
	}
	if summary.Files[1].Added != 2 || summary.Files[1].WriteCount != 3 || summary.Files[1].LastTurnIndex != 4 {
		t.Fatalf("fold %#v", summary.Files[1])
	}
}

func TestBuildPerRunSummaryDefaultsWriteCount(t *testing.T) {
	s := BuildPerTurnSummary(TurnFileChanges{TurnIndex: 1, Snapshots: []Snapshot{{Path: "a", AfterContent: "x"}}})
	if s.Files[0].WriteCount != 1 {
		t.Fatalf("got %d", s.Files[0].WriteCount)
	}
}

func TestUnifiedDiffAndHash(t *testing.T) {
	diff := UnifiedDiff(Snapshot{Path: "notes/a.md", BeforeContent: str("old\n"), AfterContent: "new\n"})
	for _, part := range []string{"--- notes/a.md", "+++ notes/a.md", "-old", "+new"} {
		if !strings.Contains(diff, part) {
			t.Fatalf("diff missing %q: %s", part, diff)
		}
	}
	context := UnifiedDiff(Snapshot{Path: "a", BeforeContent: str("one\ntwo\nthree\nfour\n"), AfterContent: "one\ntwo changed\nthree\nfour\n"})
	if strings.Contains(context, "-one") || strings.Contains(context, "+one") || !strings.Contains(context, " one") || !strings.Contains(context, "-two") || !strings.Contains(context, "+two changed") {
		t.Fatalf("context diff is not minimal: %s", context)
	}
	old := []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14"}
	newLines := append([]string(nil), old...)
	newLines[0], newLines[14] = "first", "last"
	far := UnifiedDiff(Snapshot{Path: "far", BeforeContent: str(strings.Join(old, "\n")), AfterContent: strings.Join(newLines, "\n")})
	if strings.Count(far, "@@ ") != 2 {
		t.Fatalf("distant changes should produce two hunks: %s", far)
	}
	if got := UnifiedDiff(Snapshot{Path: "image.png", Binary: true}); !strings.Contains(got, "cannot be previewed") {
		t.Fatal(got)
	}
	h := Hash("hello")
	if !HashMatches("hello", h) || HashMatches("bye", h) {
		t.Fatalf("hash check failed")
	}
}

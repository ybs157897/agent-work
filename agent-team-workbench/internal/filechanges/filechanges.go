// Package filechanges contains the source-of-truth calculations for the
// per-run file change summary shown in chat. It intentionally does not read
// git state: snapshots are captured by the runtime around each write.
package filechanges

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Snapshot is one write's before/after view of a file. A nil BeforeContent is
// treated as an empty file (the representation used for newly-created files).
type Snapshot struct {
	Path          string
	BeforeContent *string
	AfterContent  string
	WriteCount    int
	Binary        bool
}

// Before returns the snapshot's old text, normalising a missing file to empty.
func (s Snapshot) Before() string {
	if s.BeforeContent == nil {
		return ""
	}
	return *s.BeforeContent
}

type TurnFileChanges struct {
	TurnIndex int
	Snapshots []Snapshot
	FileState string // "applied" or "reverted", when known
}

type FileStat struct {
	Path          string `json:"path"`
	Added         int    `json:"added"`
	Removed       int    `json:"removed"`
	WriteCount    int    `json:"writeCount"`
	LastTurnIndex int    `json:"lastTurnIndex"`
	BeforeContent string `json:"-"`
	AfterContent  string `json:"-"`
	Binary        bool   `json:"binary,omitempty"`
}

type Summary struct {
	FileCount int        `json:"fileCount"`
	Added     int        `json:"added"`
	Removed   int        `json:"removed"`
	Files     []FileStat `json:"files"`
}

func SplitIntoLogicalLines(text *string) []string {
	if text == nil || *text == "" {
		return []string{}
	}
	return splitLines(*text)
}

func splitLines(text string) []string {
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// ComputeLineChangeStat computes line additions/deletions using the same
// prefix/suffix trimming and bounded one-dimensional LCS as ZCode.
func ComputeLineChangeStat(before *string, after string) (added, removed int) {
	oldL, newL := SplitIntoLogicalLines(before), splitLines(after)
	lo := 0
	for lo < len(oldL) && lo < len(newL) && oldL[lo] == newL[lo] {
		lo++
	}
	oi, ni := len(oldL)-1, len(newL)-1
	for oi >= lo && ni >= lo && oldL[oi] == newL[ni] {
		oi--
		ni--
	}
	oldMid, newMid := oldL[lo:oi+1], newL[lo:ni+1]
	if len(oldMid) == 0 {
		return len(newMid), 0
	}
	if len(newMid) == 0 {
		return 0, len(oldMid)
	}
	if len(oldMid)*len(newMid) > 400000 {
		return len(newMid), len(oldMid)
	}
	dp := make([]int, len(newMid)+1)
	for i := 1; i <= len(oldMid); i++ {
		prev := 0
		for j := 1; j <= len(newMid); j++ {
			tmp := dp[j]
			if oldMid[i-1] == newMid[j-1] {
				dp[j] = prev + 1
			} else if dp[j-1] > dp[j] {
				dp[j] = dp[j-1]
			}
			prev = tmp
		}
	}
	lcs := dp[len(newMid)]
	return len(newMid) - lcs, len(oldMid) - lcs
}

// BuildPerRunSummary folds all turns belonging to one run by path. A path's
// first before and final after are diffed exactly once; writes are summed.
func BuildPerRunSummary(turns []TurnFileChanges) Summary {
	type folded struct {
		before string
		after  string
		writes int
		turn   int
		binary bool
	}
	byPath := make(map[string]folded)
	for _, turn := range turns {
		for _, snap := range turn.Snapshots {
			if snap.Path == "" {
				continue
			}
			count := snap.WriteCount
			if count < 1 {
				count = 1
			}
			item, ok := byPath[snap.Path]
			if !ok {
				item = folded{before: snap.Before(), after: snap.AfterContent, writes: count, turn: turn.TurnIndex, binary: snap.Binary}
			} else {
				item.after, item.writes, item.turn = snap.AfterContent, item.writes+count, turn.TurnIndex
				item.binary = item.binary || snap.Binary
			}
			byPath[snap.Path] = item
		}
	}
	paths := make([]string, 0, len(byPath))
	for path := range byPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	result := Summary{FileCount: len(paths), Files: make([]FileStat, 0, len(paths))}
	for _, path := range paths {
		item := byPath[path]
		stat := FileStat{Path: path, WriteCount: item.writes, LastTurnIndex: item.turn, BeforeContent: item.before, AfterContent: item.after, Binary: item.binary}
		if item.binary {
			stat.Added, stat.Removed = 0, 0
		} else {
			stat.Added, stat.Removed = ComputeLineChangeStat(&item.before, item.after)
		}
		result.Added += stat.Added
		result.Removed += stat.Removed
		result.Files = append(result.Files, stat)
	}
	return result
}

// BuildPerTurnSummary is the convenient single-turn form of BuildPerRunSummary.
func BuildPerTurnSummary(turn TurnFileChanges) Summary {
	return BuildPerRunSummary([]TurnFileChanges{turn})
}

func Hash(text string) string { sum := sha256.Sum256([]byte(text)); return hex.EncodeToString(sum[:]) }
func HashMatches(text, expected string) bool {
	return strings.EqualFold(Hash(text), strings.TrimSpace(expected))
}

// UnifiedDiff returns a compact, reviewable patch for a text snapshot. Binary
// snapshots deliberately return a clear non-previewable marker.
func UnifiedDiff(snapshot Snapshot) string {
	if snapshot.Binary {
		return fmt.Sprintf("Binary file %s cannot be previewed", snapshot.Path)
	}
	oldL, newL := splitLines(snapshot.Before()), splitLines(snapshot.AfterContent)
	if len(oldL) == 0 && len(newL) == 0 {
		return ""
	}
	ops := diffOperations(oldL, newL)
	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n+++ %s\n", snapshot.Path, snapshot.Path)
	for _, hunk := range diffHunks(ops) {
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", hunk.oldStart, hunk.oldCount, hunk.newStart, hunk.newCount)
		for _, op := range ops[hunk.start:hunk.end] {
			b.WriteByte(op.kind)
			b.WriteString(op.line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

type diffOperation struct {
	kind byte
	line string
}
type diffHunk struct{ start, end, oldStart, oldCount, newStart, newCount int }

// diffOperations uses an LCS backtrace so unchanged lines remain context in a
// review. The same bound as the summary avoids allocating an unbounded matrix.
func diffOperations(oldLines, newLines []string) []diffOperation {
	n, m := len(oldLines), len(newLines)
	if n == 0 {
		out := make([]diffOperation, m)
		for i, line := range newLines {
			out[i] = diffOperation{'+', line}
		}
		return out
	}
	if m == 0 {
		out := make([]diffOperation, n)
		for i, line := range oldLines {
			out[i] = diffOperation{'-', line}
		}
		return out
	}
	if n*m > 400000 {
		return replacementOperations(oldLines, newLines)
	}
	dp := make([]int, (n+1)*(m+1))
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			at := i*(m+1) + j
			if oldLines[i] == newLines[j] {
				dp[at] = 1 + dp[(i+1)*(m+1)+j+1]
			} else if dp[(i+1)*(m+1)+j] >= dp[i*(m+1)+j+1] {
				dp[at] = dp[(i+1)*(m+1)+j]
			} else {
				dp[at] = dp[i*(m+1)+j+1]
			}
		}
	}
	ops := make([]diffOperation, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		if oldLines[i] == newLines[j] {
			ops = append(ops, diffOperation{' ', oldLines[i]})
			i++
			j++
			continue
		}
		if dp[(i+1)*(m+1)+j] >= dp[i*(m+1)+j+1] {
			ops = append(ops, diffOperation{'-', oldLines[i]})
			i++
		} else {
			ops = append(ops, diffOperation{'+', newLines[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOperation{'-', oldLines[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOperation{'+', newLines[j]})
	}
	return ops
}

func replacementOperations(oldLines, newLines []string) []diffOperation {
	ops := make([]diffOperation, 0, len(oldLines)+len(newLines))
	for _, line := range oldLines {
		ops = append(ops, diffOperation{'-', line})
	}
	for _, line := range newLines {
		ops = append(ops, diffOperation{'+', line})
	}
	return ops
}

func diffHunks(ops []diffOperation) []diffHunk {
	changed := make([]int, 0)
	for i, op := range ops {
		if op.kind != ' ' {
			changed = append(changed, i)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	hunks := make([]diffHunk, 0, len(changed))
	start := changed[0] - 3
	if start < 0 {
		start = 0
	}
	end := changed[0] + 4
	if end > len(ops) {
		end = len(ops)
	}
	for _, index := range changed[1:] {
		nextStart := index - 3
		if nextStart < 0 {
			nextStart = 0
		}
		nextEnd := index + 4
		if nextEnd > len(ops) {
			nextEnd = len(ops)
		}
		if nextStart < end {
			if nextEnd > end {
				end = nextEnd
			}
			continue
		}
		hunks = append(hunks, makeHunk(ops, start, end))
		start, end = nextStart, nextEnd
	}
	hunks = append(hunks, makeHunk(ops, start, end))
	return hunks
}

func makeHunk(ops []diffOperation, start, end int) diffHunk {
	oldBefore, newBefore := 0, 0
	for _, op := range ops[:start] {
		if op.kind != '+' {
			oldBefore++
		}
		if op.kind != '-' {
			newBefore++
		}
	}
	oldCount, newCount := 0, 0
	for _, op := range ops[start:end] {
		if op.kind != '+' {
			oldCount++
		}
		if op.kind != '-' {
			newCount++
		}
	}
	return diffHunk{start: start, end: end, oldStart: oldBefore + 1, oldCount: oldCount, newStart: newBefore + 1, newCount: newCount}
}

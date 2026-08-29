package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/filechanges"
)

var ErrRunChangesUnavailable = errors.New("run file changes unavailable")
var ErrRunChangesConflict = errors.New("run file changes conflict with external edits")
var ErrRunChangesInvalid = errors.New("invalid run file change request")

type RunFileChange struct {
	Path          string `json:"path"`
	Kind          string `json:"kind"`
	Additions     int    `json:"additions"`
	Deletions     int    `json:"deletions"`
	WriteCount    int    `json:"write_count"`
	LastTurnIndex int    `json:"last_turn_index"`
	Binary        bool   `json:"binary"`
}
type RunChanges struct {
	FileCount int             `json:"file_count"`
	Additions int             `json:"additions"`
	Deletions int             `json:"deletions"`
	Files     []RunFileChange `json:"files"`
	State     string          `json:"state"`
	CanRevert bool            `json:"can_revert"`
	Reason    string          `json:"reason,omitempty"`
	Version   int             `json:"version"`
}

type RunChangeDiff struct {
	Path      string `json:"path"`
	Diff      string `json:"diff"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary"`
}

type snapshotEvent struct {
	Path          string  `json:"path"`
	WorkspaceRoot string  `json:"workspace_root"`
	Before        *string `json:"before_content"`
	After         string  `json:"after_content"`
	WriteCount    int     `json:"write_count"`
	Binary        bool    `json:"binary"`
	BeforeHash    string  `json:"before_hash"`
	AfterHash     string  `json:"after_hash"`
	BeforeExists  bool    `json:"before_exists"`
	AfterExists   bool    `json:"after_exists"`
}

func foldSnapshotEvents(events []snapshotEvent) ([]snapshotEvent, error) {
	byPath := make(map[string]snapshotEvent)
	root := ""
	for _, event := range events {
		cleanRoot := filepath.Clean(event.WorkspaceRoot)
		if root == "" {
			root = cleanRoot
		} else if cleanRoot != root {
			return nil, ErrRunChangesUnavailable
		}
		current, ok := byPath[event.Path]
		if !ok {
			current = event
			if current.WriteCount < 1 {
				current.WriteCount = 1
			}
		} else {
			count := event.WriteCount
			if count < 1 {
				count = 1
			}
			current.After = event.After
			current.AfterExists = event.AfterExists
			current.AfterHash = event.AfterHash
			current.WriteCount += count
			current.Binary = current.Binary || event.Binary
		}
		byPath[event.Path] = current
	}
	paths := make([]string, 0, len(byPath))
	for path := range byPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	result := make([]snapshotEvent, 0, len(paths))
	for _, path := range paths {
		result = append(result, byPath[path])
	}
	return result, nil
}

func (s *Service) runSnapshots(ctx context.Context, runID string) (*domain.ExecutionRun, []filechanges.TurnFileChanges, []snapshotEvent, error) {
	r, err := s.store.Runs().Get(ctx, runID)
	if err != nil {
		return nil, nil, nil, err
	}
	events, err := s.store.Events().ListRunEvents(ctx, runID)
	if err != nil {
		return nil, nil, nil, err
	}
	turn := filechanges.TurnFileChanges{TurnIndex: 1}
	var rawSnapshots []snapshotEvent
	for _, ev := range events {
		raw, ok := ev.Payload["file_change_snapshot"]
		if !ok {
			continue
		}
		b, _ := json.Marshal(raw)
		var v snapshotEvent
		if json.Unmarshal(b, &v) != nil || v.Path == "" || filepath.IsAbs(v.Path) || v.WorkspaceRoot == "" {
			continue
		}
		if _, err := secureJoin(v.WorkspaceRoot, v.Path); err != nil {
			continue
		}
		if v.WriteCount < 1 {
			v.WriteCount = 1
		}
		if v.BeforeHash == "" && v.Before != nil {
			v.BeforeHash = filechanges.Hash(*v.Before)
		}
		if v.AfterHash == "" {
			v.AfterHash = filechanges.Hash(v.After)
		}
		turn.Snapshots = append(turn.Snapshots, filechanges.Snapshot{Path: v.Path, BeforeContent: v.Before, AfterContent: v.After, WriteCount: v.WriteCount, Binary: v.Binary})
		rawSnapshots = append(rawSnapshots, v)
	}
	if len(turn.Snapshots) == 0 {
		return r, nil, nil, ErrRunChangesUnavailable
	}
	folded, err := foldSnapshotEvents(rawSnapshots)
	if err != nil {
		return r, nil, nil, err
	}
	return r, []filechanges.TurnFileChanges{turn}, folded, nil
}

func (s *Service) RunChanges(ctx context.Context, runID string) (RunChanges, error) {
	_, turns, raw, err := s.runSnapshots(ctx, runID)
	if err != nil {
		return RunChanges{Files: []RunFileChange{}, State: "unavailable", Reason: err.Error(), Version: 1}, nil
	}
	sum := filechanges.BuildPerRunSummary(turns)
	out := RunChanges{FileCount: sum.FileCount, Additions: sum.Added, Deletions: sum.Removed, Files: make([]RunFileChange, 0, len(sum.Files)), State: "ready", CanRevert: sum.FileCount > 0, Version: 1}
	if events, e := s.store.Events().ListRunEvents(ctx, runID); e == nil {
		for _, ev := range events {
			if ev.EventType == domain.EventFileChangesReverted {
				out.State = "reverted"
				out.CanRevert = false
				out.Version = 2
			}
		}
	}
	rawByPath := make(map[string]snapshotEvent, len(raw))
	for _, event := range raw {
		rawByPath[event.Path] = event
		if event.Binary || event.AfterHash == "" || (event.BeforeExists && event.Before == nil) {
			out.CanRevert = false
			out.Reason = "存在无法安全恢复的文件快照"
		}
	}
	if out.CanRevert {
		for _, event := range raw {
			path, joinErr := secureJoin(event.WorkspaceRoot, event.Path)
			if joinErr != nil {
				out.CanRevert = false
				out.Reason = "文件路径无法安全解析"
				break
			}
			content, readErr := os.ReadFile(path)
			exists := readErr == nil
			if readErr != nil && !os.IsNotExist(readErr) {
				out.CanRevert = false
				out.Reason = "无法验证当前文件内容"
				break
			}
			if event.AfterExists != exists || (exists && !filechanges.HashMatches(string(content), event.AfterHash)) {
				out.CanRevert = false
				out.Reason = "文件已在本轮结束后继续修改"
				break
			}
		}
	}
	for _, f := range sum.Files {
		kind := "modified"
		event := rawByPath[f.Path]
		beforeExists, afterExists := event.BeforeExists, event.AfterExists
		if !beforeExists {
			kind = "added"
		}
		if !afterExists {
			kind = "deleted"
		}
		out.Files = append(out.Files, RunFileChange{Path: f.Path, Kind: kind, Additions: f.Added, Deletions: f.Removed, WriteCount: f.WriteCount, LastTurnIndex: f.LastTurnIndex, Binary: f.Binary})
	}
	return out, nil
}

// validChangePath 校验 diff 查询路径必须是干净的相对路径。path 只用于回放
// 记录的等值查找（从不触盘——触盘场景走 secureJoin），因此这里用纯字符串
// 判定：拒绝空串、绝对路径、Windows 盘符、".." 段与 NUL。
func validChangePath(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") || strings.HasPrefix(path, "\\") || strings.ContainsRune(path, 0) {
		return false
	}
	if len(path) >= 2 && path[1] == ':' {
		return false
	}
	for _, seg := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return false
		}
	}
	return true
}

func (s *Service) RunChangeDiff(ctx context.Context, runID, path string) (RunChangeDiff, error) {
	if !validChangePath(path) {
		return RunChangeDiff{}, fmt.Errorf("%w: invalid path", ErrRunChangesInvalid)
	}
	_, turns, _, err := s.runSnapshots(ctx, runID)
	if err != nil {
		return RunChangeDiff{}, err
	}
	sum := filechanges.BuildPerRunSummary(turns)
	for _, f := range sum.Files {
		if f.Path == path {
			return RunChangeDiff{
				Path:   path,
				Diff:   filechanges.UnifiedDiff(filechanges.Snapshot{Path: f.Path, BeforeContent: &f.BeforeContent, AfterContent: f.AfterContent, Binary: f.Binary}),
				Binary: f.Binary,
			}, nil
		}
	}
	return RunChangeDiff{}, ErrRunChangesUnavailable
}

func (s *Service) RevertRunChanges(ctx context.Context, runID, key string) (RunChanges, error) {
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	if events, e := s.store.Events().ListRunEvents(ctx, runID); e == nil {
		for _, ev := range events {
			if ev.EventType != domain.EventFileChangesReverted {
				continue
			}
			prior, _ := ev.Payload["idempotency_key"].(string)
			if prior == key {
				out, _ := s.RunChanges(ctx, runID)
				out.State = "reverted"
				out.CanRevert = false
				return out, nil
			}
			return RunChanges{}, ErrRunChangesConflict
		}
	}
	if prior, ok := s.revertedChanges[runID]; ok {
		if prior == key {
			out, _ := s.RunChanges(ctx, runID)
			out.State = "reverted"
			out.CanRevert = false
			return out, nil
		}
		return RunChanges{}, ErrRunChangesConflict
	}
	r, _, events, err := s.runSnapshots(ctx, runID)
	if err != nil {
		return RunChanges{}, err
	}
	type item struct {
		path         string
		beforeExists bool
		before       *string
		afterHash    string
		mode         os.FileMode
		exists       bool
		backup       []byte
	}
	items := make([]item, 0, len(events))
	for _, event := range events {
		abs, e := secureJoin(event.WorkspaceRoot, event.Path)
		if e != nil || event.Binary || (event.BeforeExists && event.Before == nil) {
			return RunChanges{}, ErrRunChangesConflict
		}
		it := item{path: abs, before: event.Before, beforeExists: event.BeforeExists, afterHash: event.AfterHash}
		b, e := os.ReadFile(it.path)
		exists := e == nil
		if e != nil && !os.IsNotExist(e) {
			return RunChanges{}, ErrRunChangesConflict
		}
		if !exists {
			b = nil
		}
		if event.AfterExists != exists || (exists && it.afterHash == "") {
			return RunChanges{}, ErrRunChangesConflict
		}
		if exists && !filechanges.HashMatches(string(b), it.afterHash) {
			return RunChanges{}, ErrRunChangesConflict
		}
		info, e := os.Stat(it.path)
		if e == nil {
			it.mode = info.Mode()
			it.exists = true
			it.backup = b
		}
		items = append(items, it)
	}
	restore := func() {
		for _, b := range items {
			if b.exists {
				_ = os.WriteFile(b.path, b.backup, b.mode.Perm())
			} else {
				_ = os.Remove(b.path)
			}
		}
	}
	for _, it := range items {
		if !it.beforeExists {
			if err := os.Remove(it.path); err != nil && !os.IsNotExist(err) {
				restore()
				return RunChanges{}, err
			}
			continue
		}
		tmp, e := os.CreateTemp(filepath.Dir(it.path), ".workbench-revert-")
		if e != nil {
			restore()
			return RunChanges{}, e
		}
		tmpName := tmp.Name()
		if _, e = tmp.WriteString(*it.before); e == nil {
			e = tmp.Chmod(it.mode.Perm())
		}
		tmp.Close()
		if e == nil {
			e = os.Rename(tmpName, it.path)
		} else {
			_ = os.Remove(tmpName)
		}
		if e != nil {
			restore()
			return RunChanges{}, e
		}
	}
	if e := s.emit(ctx, r.WorkspaceID, domain.EventFileChangesReverted, domain.AggregateExecutionRun, runID, r.Version, &RunEventRecord{RunID: runID, EventType: domain.EventFileChangesReverted, Payload: map[string]any{"idempotency_key": key}}, map[string]any{"idempotency_key": key}); e != nil {
		restore()
		return RunChanges{}, e
	}
	s.audit(ctx, r.WorkspaceID, "run.changes.reverted", runID, map[string]any{"idempotency_key": key, "files": len(items)})
	s.revertedChanges[runID] = key
	out, _ := s.RunChanges(ctx, runID)
	out.State = "reverted"
	out.CanRevert = false
	return out, nil
}

func secureJoin(root, rel string) (string, error) {
	if root == "" || rel == "" || filepath.IsAbs(rel) {
		return "", errors.New("invalid snapshot path")
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	p := filepath.Join(base, filepath.Clean(rel))
	r, err := filepath.Rel(base, p)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes workspace")
	}
	for cur := p; ; cur = filepath.Dir(cur) {
		if i, e := os.Lstat(cur); e == nil && i.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("symlink path")
		}
		n := filepath.Dir(cur)
		if n == cur || n == base {
			break
		}
	}
	return p, nil
}

package knowledgelib

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SourceSpec is one registered library source.
type SourceSpec struct {
	ID           string
	Name         string
	Kind         string
	RepoPath     string
	DefaultRef   string
	IncludeGlobs []string
	ExcludeGlobs []string
	// Usage is the concrete consumer/artifact pair this binding represents.
	Usage Usage
}

// Usage is one use binding of a logical source: consumer, artifact version and
// environment together form its identity.
type Usage struct {
	Consumer    string
	Artifact    string
	Environment string
}

// Identity is the canonical, collision-free key of this usage. Every part of
// the binding participates, so two artifacts for one consumer, or two
// environments for one artifact, are distinct bindings.
func (u Usage) Identity() string {
	return u.Consumer + "\x00" + u.Artifact + "\x00" + u.Environment
}

// QualifiedName is how an evidence request selects one binding of a source
// that participates in several usages.
func (u Usage) QualifiedName(sourceName string) string {
	name := sourceName
	if u.Consumer != "" {
		name += "@" + u.Consumer
	}
	if u.Artifact != "" {
		name += "#" + u.Artifact
	}
	if u.Environment != "" {
		name += "~" + u.Environment
	}
	return name
}

// Binding is the frozen state of one source in one snapshot. A logical source
// can appear under several bindings with different artifact versions, so
// common 1.4.2 for the order service and common 2.0.0 for the device service
// are two bindings of the same source.
type Binding struct {
	ID           string
	SnapshotID   string
	SourceID     string
	SourceName   string
	SourceKind   string
	RepoPath     string
	GitRef       string
	CommitSHA    string
	ObjectFormat string
	Dirty        bool
	DirtyDigest  string
	Untracked    []string
	Artifact     string
	Consumer     string
	Environment  string
	// DirtyPaths are worktree paths whose current bytes differ from the
	// commit, including untracked files. Reading them yields worktree origin.
	DirtyPaths map[string]bool
	// FrozenDir holds the byte-for-byte copy of every dirty/untracked file
	// taken at capture time. Reads never go back to a mutable worktree.
	FrozenDir string
	// RefPinned records that the pinned commit came from the registered ref
	// (or from a detached HEAD named as such) rather than from a floating
	// branch name.
	RefPinned bool
	// WorktreeNote explains why the working tree was not part of this input.
	WorktreeNote string
	// TreeDir is the exported pinned commit, with the frozen dirty files
	// overlaid. The agent reads this directory; the repository path is never
	// handed to it, so a later edit to the worktree cannot change its input.
	TreeDir string
	// CapturedAt is when this binding was frozen.
	CapturedAt time.Time
}

// QualifiedName is the stable, human-writable selector of this binding. Two
// usages of the same source share the repository but differ here.
func (b *Binding) QualifiedName() string {
	if b == nil {
		return ""
	}
	return Usage{Consumer: b.Consumer, Artifact: b.Artifact, Environment: b.Environment}.QualifiedName(b.SourceName)
}

// IdentityKey is the canonical identity of this binding, used for any
// path or directory that must not be shared with another binding.
func (b *Binding) IdentityKey() string {
	if b == nil {
		return ""
	}
	return b.SourceName + "\x00" + b.ID
}

// FrozenTreeDir is the read-only checkout of the pinned commit that the
// library agent is told to read. It is the frozen input, not the live
// repository working tree.
func (b *Binding) FrozenTreeDir() string {
	if b == nil || b.TreeDir == "" {
		return b.RepoPath
	}
	return b.TreeDir
}

// GitRunner abstracts process execution so snapshot capture is testable.
type GitRunner interface {
	Run(ctx context.Context, dir string, args ...string) (stdout []byte, err error)
}

// ExecGitRunner runs the local git binary.
type ExecGitRunner struct{}

func (ExecGitRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return out.Bytes(), nil
}

// CaptureBinding freezes the current state of one source. Reading a source is
// always relative to a binding, never to a floating branch name.
func CaptureBinding(ctx context.Context, runner GitRunner, snapshotID string, spec SourceSpec) (*Binding, error) {
	if runner == nil {
		runner = ExecGitRunner{}
	}
	repoPath := strings.TrimSpace(spec.RepoPath)
	if repoPath == "" {
		return nil, errf(ErrValidation, "source %q has no repository path", spec.Name)
	}
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, errf(ErrValidation, "resolve source %q: %v", spec.Name, err)
	}
	b := &Binding{
		ID:          "binding:" + ShortID(snapshotID, spec.ID, spec.Usage.Identity()),
		SnapshotID:  snapshotID,
		SourceID:    spec.ID,
		SourceName:  spec.Name,
		SourceKind:  spec.Kind,
		RepoPath:    abs,
		Artifact:    spec.Usage.Artifact,
		Consumer:    spec.Usage.Consumer,
		Environment: spec.Usage.Environment,
		DirtyPaths:  map[string]bool{},
		CapturedAt:  time.Now().UTC(),
	}
	if info, statErr := os.Stat(abs); statErr != nil || !info.IsDir() {
		return nil, errf(ErrValidation, "source %q repository path is not readable: %s", spec.Name, abs)
	}
	if _, err := runner.Run(ctx, abs, "rev-parse", "--git-dir"); err != nil {
		return nil, errf(ErrValidation, "source %q is not a git repository: %v", spec.Name, err)
	}
	head, err := runner.Run(ctx, abs, "rev-parse", "HEAD")
	if err != nil {
		return nil, errf(ErrValidation, "source %q has no HEAD commit: %v", spec.Name, err)
	}
	headSHA := strings.TrimSpace(string(head))
	b.CommitSHA = headSHA
	declaredRef := strings.TrimSpace(spec.DefaultRef)
	if declaredRef != "" {
		// A registered ref is a promise about which revision the library
		// reads. Silently snapshotting HEAD instead would label the input with
		// a branch it never came from, so an unresolvable ref is an error.
		resolved, refErr := runner.Run(ctx, abs, "rev-parse", "--verify", "--quiet", declaredRef+"^{commit}")
		if refErr != nil {
			return nil, errf(ErrValidation, "source %q declares ref %q but it cannot be resolved: %v", spec.Name, declaredRef, refErr)
		}
		b.CommitSHA = strings.TrimSpace(string(resolved))
		b.RefPinned = true
	}
	format, err := runner.Run(ctx, abs, "rev-parse", "--show-object-format")
	if err != nil {
		format = []byte("sha1")
	}
	b.ObjectFormat = strings.TrimSpace(string(format))
	if b.ObjectFormat == "" {
		b.ObjectFormat = "sha1"
	}
	if declaredRef != "" {
		b.GitRef = declaredRef
	} else if ref, err := runner.Run(ctx, abs, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		b.GitRef = strings.TrimSpace(string(ref))
	}
	if b.GitRef == "" {
		// Detached HEAD without a registered ref: say so instead of naming a
		// branch the checkout is not on.
		b.GitRef = "detached@" + shortSHA(headSHA)
		b.RefPinned = true
	}
	if b.CommitSHA != headSHA {
		// The declared ref is not what is checked out, so the working tree
		// belongs to a different revision. Overlaying it would publish
		// uncommitted edits as if they were part of the registered ref.
		b.WorktreeNote = fmt.Sprintf("登记 ref %s 指向 %s，与工作树检出提交 %s 不同，本轮不叠加未提交改动",
			declaredRef, shortSHA(b.CommitSHA), shortSHA(headSHA))
	} else {
		statusOut, err := runner.Run(ctx, abs, "status", "--porcelain=v1", "-z", "--untracked-files=all")
		if err != nil {
			return nil, errf(ErrValidation, "source %q status failed: %v", spec.Name, err)
		}
		dirtyPaths, dirtyDigest, untracked, err := analyseWorktree(abs, statusOut, spec)
		if err != nil {
			return nil, err
		}
		b.DirtyPaths = dirtyPaths
		b.Dirty = len(dirtyPaths) > 0
		b.DirtyDigest = dirtyDigest
		b.Untracked = untracked
	}
	return b, nil
}

// analyseWorktree parses porcelain v1 -z output and digests the actual
// content of every changed or untracked file, so a later edit to the worktree
// cannot silently change what a snapshot meant.
func analyseWorktree(repoPath string, statusOut []byte, spec SourceSpec) (map[string]bool, string, []string, error) {
	entries := bytes.Split(statusOut, []byte{0})
	type record struct{ status, path string }
	var records []record
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		status := string(e[:2])
		p := string(e[3:])
		if status[0] == 'R' || status[0] == 'C' {
			// Rename/copy records carry the original path as the next field.
			i++
		}
		if !includedInSource(p, spec) {
			continue
		}
		records = append(records, record{status: status, path: p})
	}
	if len(records) == 0 {
		return nil, "", nil, nil
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].path != records[j].path {
			return records[i].path < records[j].path
		}
		return records[i].status < records[j].status
	})
	h := sha256.New()
	dirtyPaths := map[string]bool{}
	var untracked []string
	for _, r := range records {
		fileDigest := "-"
		abs := filepath.Join(repoPath, filepath.FromSlash(r.path))
		if raw, err := os.ReadFile(abs); err == nil {
			sum := sha256.Sum256(raw)
			fileDigest = hex.EncodeToString(sum[:])
		} else {
			fileDigest = "missing"
		}
		if r.status == "??" {
			untracked = append(untracked, r.path)
		}
		dirtyPaths[r.path] = true
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00", r.status, r.path, fileDigest)
	}
	return dirtyPaths, "sha256:" + hex.EncodeToString(h.Sum(nil)), untracked, nil
}

// includedInSource applies the registered include/exclude globs plus the
// generated-output exclusions. A library never treats a derived document as
// its own source.
func includedInSource(p string, spec SourceSpec) bool {
	if p == "" {
		return false
	}
	if isDerivedOutput(p) {
		return false
	}
	if len(spec.IncludeGlobs) > 0 && !matchesAny(spec.IncludeGlobs, p) {
		return false
	}
	if matchesAny(spec.ExcludeGlobs, p) {
		return false
	}
	return true
}

// Derived outputs are excluded so generated documentation can never become
// its own evidence and feed a summary-of-a-summary loop.
func isDerivedOutput(p string) bool {
	for _, prefix := range []string{"knowledge/", "target/", "build/", "dist/", "node_modules/", ".git/"} {
		if strings.HasPrefix(p, prefix) || strings.Contains(p, "/"+prefix) {
			return true
		}
	}
	return false
}

func matchesAny(globs []string, p string) bool {
	for _, g := range globs {
		if ok, err := path.Match(g, p); err == nil && ok {
			return true
		}
		if strings.HasSuffix(g, "/**") && strings.HasPrefix(p, strings.TrimSuffix(g, "**")) {
			return true
		}
	}
	return false
}

// maxFrozenFileBytes bounds what a single frozen worktree file may cost. A
// larger dirty file is recorded as unfrozen rather than silently truncated.
const maxFrozenFileBytes = 8 << 20

// FreezeWorktreeContent copies the bytes of every dirty/untracked file into
// the library's snapshot area. This is what makes a development view
// reproducible: the snapshot keeps the actual input, not a hash of it.
func (l Library) FreezeWorktreeContent(b *Binding) error {
	if b == nil || len(b.DirtyPaths) == 0 {
		return nil
	}
	dir := l.SnapshotWorktreeDirFor(b.SnapshotID, b.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for rel := range b.DirtyPaths {
		src := filepath.Join(b.RepoPath, filepath.FromSlash(rel))
		info, err := os.Stat(src)
		if err != nil || info.IsDir() {
			continue
		}
		if info.Size() > maxFrozenFileBytes {
			return errf(ErrValidation, "worktree file %s in %s exceeds the freeze limit", rel, b.SourceName)
		}
		raw, err := os.ReadFile(src)
		if err != nil {
			return errf(ErrValidation, "freeze worktree file %s: %v", rel, err)
		}
		target := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, raw, 0o644); err != nil {
			return err
		}
	}
	b.FrozenDir = dir
	return nil
}

// BindingPath is one file read out of a binding.
type BindingPath struct {
	Binding *Binding
	Path    string
	Content []byte
	Origin  string
	// Digest is computed here, over the exact bytes returned.
	Digest string
}

// ReadBindingFile returns the bytes of one file inside a binding. Committed
// content is read from the pinned commit; a path the snapshot recorded as
// dirty or untracked is read from the worktree and reported as such.
func ReadBindingFile(ctx context.Context, runner GitRunner, b *Binding, relPath string) (*BindingPath, error) {
	if runner == nil {
		runner = ExecGitRunner{}
	}
	clean := path.Clean(strings.TrimPrefix(strings.ReplaceAll(relPath, "\\", "/"), "./"))
	if clean == "" || clean == "." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return nil, errf(ErrValidation, "path %q escapes the source repository", relPath)
	}
	if b.DirtyPaths[clean] {
		if b.FrozenDir == "" {
			return nil, errf(ErrStateConflict, "snapshot did not freeze worktree file %s", clean)
		}
		raw, err := os.ReadFile(filepath.Join(b.FrozenDir, filepath.FromSlash(clean)))
		if err != nil {
			return nil, errf(ErrValidation, "read frozen worktree file %s: %v", clean, err)
		}
		return &BindingPath{Binding: b, Path: clean, Content: raw, Origin: "worktree", Digest: DigestBytes(raw)}, nil
	}
	// Prefer the frozen tree: once a snapshot is captured, evidence must be
	// collectable from the snapshot's own bytes even if the source repository
	// moves, is rebuilt, or becomes unreadable. The live repository is only a
	// fallback for a snapshot whose tree was already released.
	if b.TreeDir != "" {
		if raw, readErr := os.ReadFile(filepath.Join(b.TreeDir, filepath.FromSlash(clean))); readErr == nil {
			return &BindingPath{Binding: b, Path: clean, Content: raw, Origin: "committed", Digest: DigestBytes(raw)}, nil
		}
	}
	if b.RepoPath == "" {
		return nil, errf(ErrNotFound, "frozen snapshot does not contain %s and no repository is available", clean)
	}
	out, err := runner.Run(ctx, b.RepoPath, "show", b.CommitSHA+":"+clean)
	if err != nil {
		return nil, errf(ErrNotFound, "read committed file %s@%s: %v", clean, shortSHA(b.CommitSHA), err)
	}
	return &BindingPath{Binding: b, Path: clean, Content: out, Origin: "committed", Digest: DigestBytes(out)}, nil
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// ListBindingFiles returns committed file paths at the pinned commit plus the
// worktree paths the snapshot recorded as dirty/untracked.
func ListBindingFiles(ctx context.Context, runner GitRunner, b *Binding, spec SourceSpec) ([]string, error) {
	if runner == nil {
		runner = ExecGitRunner{}
	}
	out, err := runner.Run(ctx, b.RepoPath, "ls-tree", "-r", "--name-only", "-z", b.CommitSHA)
	if err != nil {
		return nil, errf(ErrValidation, "list source %q files: %v", b.SourceName, err)
	}
	seen := map[string]bool{}
	var files []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" || !includedInSource(p, spec) {
			continue
		}
		seen[p] = true
		files = append(files, p)
	}
	for p := range b.DirtyPaths {
		if !includedInSource(p, spec) || seen[p] {
			continue
		}
		seen[p] = true
		files = append(files, p)
	}
	sort.Strings(files)
	return files, nil
}

// mediaTypeFor maps a repository path to a media type for the ledger.
func mediaTypeFor(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".java":
		return "text/x-java"
	case ".kt":
		return "text/x-kotlin"
	case ".go":
		return "text/x-go"
	case ".ts", ".tsx":
		return "text/typescript"
	case ".js":
		return "text/javascript"
	case ".xml":
		return "application/xml"
	case ".yml", ".yaml":
		return "application/yaml"
	case ".json":
		return "application/json"
	case ".sql":
		return "application/sql"
	case ".md":
		return "text/markdown"
	case ".properties":
		return "text/x-java-properties"
	default:
		return "text/plain"
	}
}

// ── Frozen read surface for the library agent ──────────────────────────

// maxFrozenTreeBytes bounds one exported source tree. A larger repository is
// refused rather than silently truncated, because a partial tree would look
// authoritative to the agent.
const maxFrozenTreeBytes = 512 << 20

// FreezeBindingTree exports the pinned commit into the snapshot area and
// overlays the frozen dirty/untracked bytes. The agent is pointed at this
// directory, so what it reads is exactly the snapshot's input: an edit to the
// repository worktree after capture cannot change it.
func (l Library) FreezeBindingTree(ctx context.Context, runner GitRunner, b *Binding, spec SourceSpec) error {
	if runner == nil {
		runner = ExecGitRunner{}
	}
	if b == nil {
		return errf(ErrValidation, "binding is required")
	}
	dir := l.SnapshotTreeDirFor(b.SnapshotID, b.ID)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := runner.Run(ctx, b.RepoPath, "archive", "--format=tar", b.CommitSHA)
	if err != nil {
		return errf(ErrValidation, "export %s@%s: %v", b.SourceName, shortSHA(b.CommitSHA), err)
	}
	if len(raw) > maxFrozenTreeBytes {
		return errf(ErrValidation, "source %q tree exceeds the freeze limit", b.SourceName)
	}
	if err := extractTar(raw, dir); err != nil {
		return errf(ErrValidation, "extract %s tree: %v", b.SourceName, err)
	}
	// Overlay the frozen worktree bytes recorded at capture time.
	for rel := range b.DirtyPaths {
		if !includedInSource(rel, spec) {
			continue
		}
		src := filepath.Join(b.FrozenDir, filepath.FromSlash(rel))
		content, readErr := os.ReadFile(src)
		if readErr != nil {
			continue
		}
		target := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, content, 0o644); err != nil {
			return err
		}
	}
	b.TreeDir = dir
	return nil
}

// extractTar unpacks a git archive into dir. Entries outside dir are refused.
func extractTar(raw []byte, dir string) error {
	reader := tar.NewReader(bytes.NewReader(raw))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		clean := filepath.Clean(filepath.FromSlash(header.Name))
		if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return errf(ErrValidation, "archive entry %q escapes the tree", header.Name)
		}
		target := filepath.Join(dir, clean)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, reader); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		default:
			// Symlinks, devices and submodules are not part of a readable
			// source tree; skipping them is recorded as a coverage limit.
		}
	}
}

// ReleaseSnapshotTrees removes the exported trees of one snapshot. The frozen
// values live on as content-addressed representations, so evidence stays
// readable long after the working copy is gone.
func (l Library) ReleaseSnapshotTrees(snapshotID string) error {
	if strings.TrimSpace(snapshotID) == "" {
		return nil
	}
	return os.RemoveAll(filepath.Join(l.SystemDir(), "snapshots", safeSegment(snapshotID), "tree"))
}

// SnapshotWorktreeDirFor is where the frozen bytes of a binding's
// dirty/untracked files live. Writer and reader must use this one helper: a
// path built by hand would disagree on the sanitised binding identity and the
// frozen file would look missing.
func (l Library) SnapshotWorktreeDirFor(snapshotID, bindingID string) string {
	return filepath.Join(l.SystemDir(), "snapshots", safeSegment(snapshotID), "worktree", safeSegment(bindingID))
}

// SnapshotTreeDirFor is where one binding's frozen checkout lives. The
// directory is keyed by the full binding ID, so two usages of one source can
// never share — or clean up — each other's frozen input.
func (l Library) SnapshotTreeDirFor(snapshotID, bindingID string) string {
	return filepath.Join(l.SystemDir(), "snapshots", safeSegment(snapshotID), "tree", safeSegment(bindingID))
}

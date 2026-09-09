package hostregistry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

const defaultProjectBaselineGitTimeout = 5 * time.Second

const (
	CodeProjectBaselineScanTooLarge = "workspace_project_baseline_too_large"
	CodeProjectBaselineScanFailed   = "workspace_project_baseline_scan_failed"
	CodeProjectBaselineNotGit       = "workspace_project_baseline_not_git"
	CodeProjectBaselineChanged      = "workspace_project_baseline_changed_during_scan"
)

// ProjectScanLimits bounds the Host-local Git probe. Limits apply to changed
// files and command output; the scanner never reads the whole checkout.
type ProjectScanLimits struct {
	Timeout          time.Duration
	MaxChangedFiles  int
	MaxTotalBytes    int64
	MaxFileBytes     int64
	MaxCommandOutput int
}

func (l ProjectScanLimits) withDefaults() ProjectScanLimits {
	if l.Timeout <= 0 {
		l.Timeout = defaultProjectBaselineGitTimeout
	}
	if l.MaxChangedFiles <= 0 {
		l.MaxChangedFiles = 2000
	}
	if l.MaxTotalBytes <= 0 {
		l.MaxTotalBytes = 32 << 20
	}
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = 8 << 20
	}
	if l.MaxCommandOutput <= 0 {
		l.MaxCommandOutput = 16 << 20
	}
	return l
}

var (
	errProjectBaselineScanTooLarge = errors.New("project baseline scan bound exceeded")
	errProjectBaselineNotGit       = errors.New("registered checkout is not a Git worktree root")
)

// ResolveProjectBaseline is the only Host-local baseline entry point. The
// snapshot has already been authorized by the trusted registry; this method
// returns no path and reads only the resolved checkout's HEAD and changed
// content. A dirty checkout is a valid baseline.
func (r *Registry) ResolveProjectBaseline(ctx context.Context, snapshot *domain.ExecutionContextSnapshot) (domain.ProjectBaseline, error) {
	resolved, err := r.Resolve(snapshot)
	if err != nil {
		return domain.ProjectBaseline{}, err
	}
	first, err := scanProjectBaseline(ctx, resolved.CWD, snapshot, ProjectScanLimits{})
	if err != nil {
		return domain.ProjectBaseline{}, err
	}
	// A baseline is an optimistic observation, not a filesystem lock. Re-resolve
	// and scan once more so a checkout/index/worktree mutation during the first
	// pass cannot silently produce a mixed snapshot.
	resolvedAgain, err := r.Resolve(snapshot)
	if err != nil {
		return domain.ProjectBaseline{}, resolveErr(CodeProjectBaselineChanged, "project identity changed during baseline scan")
	}
	second, err := scanProjectBaseline(ctx, resolvedAgain.CWD, snapshot, ProjectScanLimits{})
	if err != nil {
		return domain.ProjectBaseline{}, err
	}
	if resolved.CWD != resolvedAgain.CWD || domain.ComputeProjectBaselineDigest(first) != domain.ComputeProjectBaselineDigest(second) {
		return domain.ProjectBaseline{}, resolveErr(CodeProjectBaselineChanged, "project checkout changed during baseline scan")
	}
	return second, nil
}

// scanProjectBaseline is split out so application tests can exercise bounds
// without changing the public resolver contract or allowing caller paths.
func scanProjectBaseline(parent context.Context, root string, snapshot *domain.ExecutionContextSnapshot, limits ProjectScanLimits) (domain.ProjectBaseline, error) {
	if snapshot == nil || strings.TrimSpace(root) == "" {
		return domain.ProjectBaseline{}, resolveErr(CodeProjectBaselineScanFailed, "project snapshot is incomplete")
	}
	limits = limits.withDefaults()
	ctx, cancel := context.WithTimeout(parent, limits.Timeout)
	defer cancel()
	if err := verifyProjectBaselineGitRoot(ctx, root); err != nil {
		return domain.ProjectBaseline{}, projectBaselineScanError(err)
	}
	maxOutput := limits.MaxCommandOutput
	if derived := limits.MaxChangedFiles * 4096; derived > maxOutput {
		maxOutput = derived
	}
	inside, err := runProjectBaselineGit(ctx, root, 64, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(string(inside)) != "true" {
		return domain.ProjectBaseline{}, resolveErr(CodeProjectBaselineNotGit, "registered checkout is not a Git worktree")
	}
	head, err := runProjectBaselineGit(ctx, root, 128, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return domain.ProjectBaseline{}, projectBaselineScanError(err)
	}
	branchBytes, branchErr := runProjectBaselineGit(ctx, root, 1024, "symbolic-ref", "--quiet", "--short", "HEAD")
	branch := ""
	if branchErr == nil {
		branch = strings.TrimSpace(string(branchBytes))
	}
	indexRaw, err := runProjectBaselineGit(ctx, root, maxOutput, "ls-files", "--stage", "-z")
	if err != nil {
		return domain.ProjectBaseline{}, projectBaselineScanError(err)
	}
	index := parseProjectBaselineIndex(indexRaw)
	stagedRaw, err := runProjectBaselineGit(ctx, root, maxOutput, "diff", "--cached", "--name-only", "-z", "--no-renames", "--no-ext-diff", "--no-textconv")
	if err != nil {
		return domain.ProjectBaseline{}, projectBaselineScanError(err)
	}
	unstagedRaw, err := runProjectBaselineGit(ctx, root, maxOutput, "diff", "--name-only", "-z", "--no-renames", "--no-ext-diff", "--no-textconv")
	if err != nil {
		return domain.ProjectBaseline{}, projectBaselineScanError(err)
	}
	untrackedRaw, err := runProjectBaselineGit(ctx, root, maxOutput, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return domain.ProjectBaseline{}, projectBaselineScanError(err)
	}
	stagedPaths, err := parseProjectBaselinePaths(stagedRaw, limits.MaxChangedFiles)
	if err != nil {
		return domain.ProjectBaseline{}, projectBaselineScanError(err)
	}
	unstagedPaths, err := parseProjectBaselinePaths(unstagedRaw, limits.MaxChangedFiles)
	if err != nil {
		return domain.ProjectBaseline{}, projectBaselineScanError(err)
	}
	untrackedPaths, err := parseProjectBaselinePaths(untrackedRaw, limits.MaxChangedFiles)
	if err != nil {
		return domain.ProjectBaseline{}, projectBaselineScanError(err)
	}

	all := map[string]struct{}{}
	for _, paths := range [][]string{stagedPaths, unstagedPaths, untrackedPaths} {
		for _, path := range paths {
			all[path] = struct{}{}
			if len(all) > limits.MaxChangedFiles {
				return domain.ProjectBaseline{}, projectBaselineScanError(errProjectBaselineScanTooLarge)
			}
		}
	}
	files := make([]projectBaselineChangedFile, 0, len(stagedPaths)+len(unstagedPaths)+len(untrackedPaths))
	var totalBytes int64
	for _, path := range stagedPaths {
		size, fileDigest, readErr := hashProjectBaselineIndexObject(ctx, root, index[path], limits.MaxFileBytes)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return domain.ProjectBaseline{}, projectBaselineScanError(readErr)
		}
		var addErr error
		totalBytes, addErr = addProjectBaselineBytes(totalBytes, size, limits.MaxTotalBytes)
		if addErr != nil {
			return domain.ProjectBaseline{}, projectBaselineScanError(addErr)
		}
		files = append(files, projectBaselineChangedFile{kind: "staged", path: path, size: size, digest: fileDigest})
	}
	for _, path := range unstagedPaths {
		size, fileDigest, readErr := hashProjectBaselineWorkingFile(ctx, root, path, limits.MaxFileBytes)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				size, fileDigest = 0, ""
			} else {
				return domain.ProjectBaseline{}, projectBaselineScanError(readErr)
			}
		}
		var addErr error
		totalBytes, addErr = addProjectBaselineBytes(totalBytes, size, limits.MaxTotalBytes)
		if addErr != nil {
			return domain.ProjectBaseline{}, projectBaselineScanError(addErr)
		}
		files = append(files, projectBaselineChangedFile{kind: "unstaged", path: path, size: size, digest: fileDigest})
	}
	for _, path := range untrackedPaths {
		size, fileDigest, readErr := hashProjectBaselineWorkingFile(ctx, root, path, limits.MaxFileBytes)
		if readErr != nil {
			return domain.ProjectBaseline{}, projectBaselineScanError(readErr)
		}
		var addErr error
		totalBytes, addErr = addProjectBaselineBytes(totalBytes, size, limits.MaxTotalBytes)
		if addErr != nil {
			return domain.ProjectBaseline{}, projectBaselineScanError(addErr)
		}
		files = append(files, projectBaselineChangedFile{kind: "untracked", path: path, size: size, digest: fileDigest})
	}

	baseline := domain.ProjectBaseline{
		RepositoryIdentity: snapshot.RepositoryIdentity,
		RefKind:            snapshot.RefKind,
		BranchName:         branch,
		CheckoutRef:        projectBaselineCheckoutRef(snapshot),
		Head:               strings.TrimSpace(string(head)),
		StagedDigest:       digestProjectBaselineKind(files, "staged"),
		UnstagedDigest:     digestProjectBaselineKind(files, "unstaged"),
		UntrackedDigest:    digestProjectBaselineKind(files, "untracked"),
		ContentDigest:      digestProjectBaselineChanges(files),
	}
	if err := baseline.Validate(); err != nil {
		return domain.ProjectBaseline{}, err
	}
	return baseline, nil
}

func projectBaselineCheckoutRef(snapshot *domain.ExecutionContextSnapshot) string {
	if snapshot == nil {
		return ""
	}
	if snapshot.RefKind == domain.RefWorktree {
		return snapshot.WorktreeRef
	}
	if snapshot.RefKind == domain.RefBranch {
		return snapshot.CheckoutRef
	}
	return rootCheckoutRef
}

func projectBaselineScanError(err error) error {
	if errors.Is(err, errProjectBaselineNotGit) {
		return resolveErr(CodeProjectBaselineNotGit, "registered checkout is not a Git worktree root")
	}
	if errors.Is(err, errProjectBaselineScanTooLarge) {
		return resolveErr(CodeProjectBaselineScanTooLarge, "project baseline scan exceeds configured bounds")
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return resolveErr(CodeProjectBaselineScanFailed, "project baseline scan timed out or was cancelled")
	}
	return resolveErr(CodeProjectBaselineScanFailed, "project baseline scan failed")
}

func verifyProjectBaselineGitRoot(ctx context.Context, root string) error {
	output, err := runProjectBaselineGit(ctx, root, 4096, "rev-parse", "--show-toplevel")
	if err != nil {
		return errProjectBaselineNotGit
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return errProjectBaselineNotGit
	}
	topReal, err := filepath.EvalSymlinks(strings.TrimSpace(string(output)))
	if err != nil || topReal != rootReal {
		return errProjectBaselineNotGit
	}
	return nil
}

type projectBaselineBoundedBuffer struct {
	bytes.Buffer
	max int
}

func (b *projectBaselineBoundedBuffer) Write(p []byte) (int, error) {
	if b.max > 0 && b.Len()+len(p) > b.max {
		return 0, errProjectBaselineScanTooLarge
	}
	return b.Buffer.Write(p)
}

func runProjectBaselineGit(ctx context.Context, root string, maxOutput int, args ...string) ([]byte, error) {
	cmdArgs := []string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-C", root}
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"GIT_OPTIONAL_LOCKS=0",
	}
	var out projectBaselineBoundedBuffer
	out.max = maxOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if errors.Is(err, errProjectBaselineScanTooLarge) {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("git command failed")
	}
	return out.Bytes(), nil
}

type projectBaselineChangedFile struct {
	kind   string
	path   string
	size   int64
	digest string
}

func parseProjectBaselinePaths(raw []byte, max int) ([]string, error) {
	parts := bytes.Split(raw, []byte{0})
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		path := string(part)
		if _, ok := seen[path]; ok {
			continue
		}
		if err := validateProjectBaselinePath(path); err != nil {
			return nil, err
		}
		seen[path] = struct{}{}
		out = append(out, path)
		if len(out) > max {
			return nil, errProjectBaselineScanTooLarge
		}
	}
	sort.Strings(out)
	return out, nil
}

func parseProjectBaselineIndex(raw []byte) map[string]string {
	out := map[string]string{}
	for _, part := range bytes.Split(raw, []byte{0}) {
		line := string(part)
		idx := strings.IndexByte(line, '\t')
		if idx < 0 {
			continue
		}
		fields := strings.Fields(line[:idx])
		if len(fields) < 2 {
			continue
		}
		out[line[idx+1:]] = fields[1]
	}
	return out
}

func validateProjectBaselinePath(path string) error {
	if path == "" || strings.IndexByte(path, 0) >= 0 || filepath.IsAbs(path) {
		return errProjectBaselineScanTooLarge
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return errors.New("changed file escapes registered checkout")
	}
	return nil
}

func hashProjectBaselineIndexObject(ctx context.Context, root, oid string, maxBytes int64) (int64, string, error) {
	if oid == "" || strings.Trim(oid, "0") == "" {
		return 0, "", nil
	}
	data, err := runProjectBaselineGit(ctx, root, int(maxBytes+1), "cat-file", "blob", oid)
	if err != nil {
		if errors.Is(err, errProjectBaselineScanTooLarge) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return 0, "", err
		}
		// A gitlink is a commit object rather than a blob. Keep its OID as an
		// observed identity instead of silently omitting the staged entry.
		return 0, "object:" + oid, nil
	}
	if int64(len(data)) > maxBytes {
		return 0, "", errProjectBaselineScanTooLarge
	}
	sum := sha256.Sum256(data)
	return int64(len(data)), hex.EncodeToString(sum[:]), nil
}

func hashProjectBaselineWorkingFile(ctx context.Context, root, path string, maxBytes int64) (int64, string, error) {
	if err := validateProjectBaselinePath(path); err != nil {
		return 0, "", err
	}
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	full := filepath.Join(root, filepath.FromSlash(path))
	info, err := os.Lstat(full)
	if err != nil {
		return 0, "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(full)
		if err != nil {
			return 0, "", errors.New("cannot read changed symlink")
		}
		if int64(len(target)) > maxBytes {
			return 0, "", errProjectBaselineScanTooLarge
		}
		sum := sha256.Sum256([]byte(target))
		return int64(len(target)), hex.EncodeToString(sum[:]), nil
	}
	if info.IsDir() {
		return 0, "directory", nil
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return 0, "", errors.New("registered checkout root is unavailable")
	}
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return 0, "", err
	}
	rel, err := filepath.Rel(rootReal, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return 0, "", errors.New("changed file escapes registered checkout")
	}
	if info.Size() > maxBytes {
		return 0, "", errProjectBaselineScanTooLarge
	}
	file, err := os.Open(resolved)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, maxBytes+1))
	if err != nil {
		return 0, "", err
	}
	if n > maxBytes {
		return 0, "", errProjectBaselineScanTooLarge
	}
	return n, hex.EncodeToString(hash.Sum(nil)), nil
}

func addProjectBaselineBytes(total, value, max int64) (int64, error) {
	if value < 0 || total > max-value {
		return 0, errProjectBaselineScanTooLarge
	}
	return total + value, nil
}

func digestProjectBaselineChanges(files []projectBaselineChangedFile) string {
	sort.Slice(files, func(i, j int) bool {
		if files[i].kind != files[j].kind {
			return files[i].kind < files[j].kind
		}
		return files[i].path < files[j].path
	})
	hash := sha256.New()
	for _, file := range files {
		fmt.Fprintf(hash, "%s\x00%s\x00%d\x00%s\x00", file.kind, file.path, file.size, file.digest)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func digestProjectBaselineKind(files []projectBaselineChangedFile, kind string) string {
	selected := make([]projectBaselineChangedFile, 0, len(files))
	for _, file := range files {
		if file.kind == kind {
			selected = append(selected, file)
		}
	}
	return digestProjectBaselineChanges(selected)
}

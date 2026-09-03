package agentconfig

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ybs/agent-team-workbench/internal/agentwork"
	"github.com/ybs/agent-team-workbench/internal/agentwork/codexconfig"
	"github.com/ybs/agent-team-workbench/internal/agentwork/kimiconfig"
	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/orchestrator"
	"gopkg.in/yaml.v3"
)

// Importer 把 agents/ 目录导入为 DB 投影（启动与 reload 时调用）。
type Importer struct {
	dir     string
	store   application.Store
	runtime RuntimeConfig
	mu      sync.Mutex
}

func NewImporter(dir string, store application.Store) *Importer {
	return &Importer{dir: dir, store: store}
}

// RuntimeConfig supplies only process-local destinations and a registry
// resolver. Neither home path nor credential value is copied into an intent.
// The control plane configures this before startup reconciliation.
type RuntimeConfig struct {
	CodexHome    string
	KimiHome     string
	ResolveModel orchestrator.ModelResolver
}

func (im *Importer) SetRuntimeConfig(cfg RuntimeConfig) { im.runtime = cfg }

type ImportResult struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
	Skipped int `json:"skipped"`
}

// ReconcileResult reports durable intents consumed before file import.
type ReconcileResult struct {
	Applied int `json:"applied"`
}

// BundleManifest is an external recovery marker for the two-file Agent bundle.
// It contains only relative file names and digests; SQLite intent remains the
// authority for whether this bundle should be replayed.
type BundleManifest struct {
	SchemaVersion string       `json:"schema_version"`
	State         string       `json:"state"` // staged | complete
	IntentID      string       `json:"intent_id"`
	AgentID       string       `json:"agent_id"`
	WorkspaceID   string       `json:"workspace_id"`
	Slug          string       `json:"slug"`
	TargetDigest  string       `json:"target_digest"`
	Files         []BundleFile `json:"files"`
}

type BundleFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

type redactedIntentError struct {
	message string
	cause   error
}

func (e *redactedIntentError) Error() string { return e.message }
func (e *redactedIntentError) Unwrap() error { return e.cause }

// Reconcile replays every non-applied intent. It must be called before Import
// at startup; a failure stops the caller from treating a partially written
// agents/ bundle as DB truth.
func (im *Importer) Reconcile(ctx context.Context) (ReconcileResult, error) {
	im.mu.Lock()
	defer im.mu.Unlock()
	return im.reconcileLocked(ctx)
}

func (im *Importer) reconcileLocked(ctx context.Context) (ReconcileResult, error) {
	var result ReconcileResult
	intents, err := im.store.AgentConfigSyncIntents().ListActive(ctx)
	if err != nil {
		return result, err
	}
	for _, intent := range intents {
		if err := im.reconcileIntentLocked(ctx, intent); err != nil {
			return result, err
		}
		result.Applied++
	}
	return result, nil
}

// ReconcileAgent is the request-path equivalent of Reconcile: it consumes the
// sole active intent for an Agent and returns only after MarkApplied succeeds.
// A nil active intent is an idempotent no-op.
func (im *Importer) ReconcileAgent(ctx context.Context, agentID string) error {
	im.mu.Lock()
	defer im.mu.Unlock()
	intent, err := im.store.AgentConfigSyncIntents().GetActiveByAgent(ctx, agentID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return im.reconcileIntentLocked(ctx, intent)
}

// ReconcileIntent validates target identity against the current DB projection,
// applies all external effects from the frozen target, then marks applied. The
// current Agent is never used as an input to external writes after validation.
func (im *Importer) ReconcileIntent(ctx context.Context, intent *domain.AgentConfigSyncIntent) error {
	im.mu.Lock()
	defer im.mu.Unlock()
	return im.reconcileIntentLocked(ctx, intent)
}

func (im *Importer) reconcileIntentLocked(ctx context.Context, intent *domain.AgentConfigSyncIntent) error {
	if intent == nil {
		return fmt.Errorf("%w: nil agent config sync intent", domain.ErrValidation)
	}
	// Request-path reconciliation can race with startup/reload or another
	// request. Refresh under the importer lock so a prior caller's applied CAS
	// is treated as an idempotent success instead of a false version conflict.
	if latest, getErr := im.store.AgentConfigSyncIntents().Get(ctx, intent.ID); getErr == nil {
		intent = latest
	} else if errors.Is(getErr, domain.ErrNotFound) {
		return nil
	} else {
		return getErr
	}
	if intent.Status == domain.AgentConfigSyncApplied {
		return nil
	}
	if intent.Status == domain.AgentConfigSyncConflict {
		return fmt.Errorf("%w: intent %s remains conflicted", domain.ErrAgentConfigSyncConflict, intent.ID)
	}
	target, err := intent.DecodeTarget()
	if err != nil {
		return im.retainFailure(ctx, intent, err, true)
	}
	current, err := im.store.Agents().Get(ctx, intent.AgentID)
	if err != nil {
		return im.retainFailure(ctx, intent, err, true)
	}
	currentTarget, err := domain.AgentConfigTargetFromProfile(current)
	if err != nil {
		return im.retainFailure(ctx, intent, err, true)
	}
	// The resolved model is part of the frozen intent, not a live Agent field.
	// Attach it only for comparison so registry edits cannot make an old intent
	// appear divergent from the Agent CAS that created it.
	if target.ResolvedModel != nil {
		copyModel := *target.ResolvedModel
		currentTarget.ResolvedModel = &copyModel
	}
	currentDigest, err := domain.ComputeAgentConfigTargetDigest(currentTarget)
	if err != nil {
		return im.retainFailure(ctx, intent, err, true)
	}
	if current.Version != intent.TargetVersion || currentDigest != intent.TargetDigest {
		conflict := fmt.Errorf("%w: Agent %s version/digest differs from intent %s", domain.ErrAgentConfigSyncConflict, intent.AgentID, intent.ID)
		return im.retainFailure(ctx, intent, conflict, true)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := im.applyTarget(ctx, target, intent); err != nil {
		permanent := errors.Is(err, codexconfig.ErrStaticConfig) || errors.Is(err, kimiconfig.ErrStaticConfig)
		if permanent {
			err = fmt.Errorf("%w: %w", domain.ErrAgentConfigSyncConflict, err)
		}
		return im.retainFailure(ctx, intent, err, permanent)
	}
	if err := im.store.AgentConfigSyncIntents().MarkApplied(ctx, intent.ID, intent.Version, time.Now().UTC()); err != nil {
		// The external bundle is idempotent; retaining the intent lets the next
		// process replay it and retry this final SQLite CAS.
		return fmt.Errorf("mark agent config intent %s applied: %w", intent.ID, err)
	}
	return nil
}

func (im *Importer) retainFailure(ctx context.Context, intent *domain.AgentConfigSyncIntent, cause error, conflict bool) error {
	// SQLite recovery metadata must not become a side channel for machine-local
	// homes or full filesystem paths from os errors. The original error is still
	// returned to the caller/log; only the persisted diagnostic is redacted and
	// bounded.
	persistedMessage := im.redactIntentError(cause)
	var markErr error
	if conflict {
		markErr = im.store.AgentConfigSyncIntents().MarkConflict(ctx, intent.ID, intent.Version, persistedMessage)
	} else {
		markErr = im.store.AgentConfigSyncIntents().MarkFailed(ctx, intent.ID, intent.Version, persistedMessage)
	}
	if markErr != nil {
		joined := errors.Join(cause, fmt.Errorf("retain agent config intent failure: %w", markErr))
		return &redactedIntentError{message: im.redactIntentError(joined), cause: joined}
	}
	return &redactedIntentError{message: persistedMessage, cause: cause}
}

func (im *Importer) redactIntentError(err error) string {
	message := "agent config sync failed"
	if err != nil {
		message = err.Error()
	}
	for _, replacement := range []struct {
		path string
		name string
	}{
		{path: im.runtime.CodexHome, name: "<codex-home>"},
		{path: im.runtime.KimiHome, name: "<kimi-home>"},
		{path: im.dir, name: "<agent-config-dir>"},
	} {
		if root := strings.TrimSpace(replacement.path); root != "" {
			message = strings.ReplaceAll(message, root, replacement.name)
			if abs, absErr := filepath.Abs(root); absErr == nil {
				message = strings.ReplaceAll(message, abs, replacement.name)
			}
		}
	}
	if len(message) > 4096 {
		runes := []rune(message)
		if len(runes) > 4096 {
			message = string(runes[:4096])
		}
	}
	return message
}

func (im *Importer) applyTarget(ctx context.Context, target *domain.AgentConfigTarget, intent *domain.AgentConfigSyncIntent) error {
	profile, err := target.Profile()
	if err != nil {
		return fmt.Errorf("%w: %w", codexconfig.ErrStaticConfig, err)
	}
	preferred := strings.TrimSpace(target.RuntimePreference.Preferred)
	spec := im.modelSpec(target, profile)
	if preferred == "codex_local" {
		if err := codexconfig.ValidateSpec(spec); err != nil {
			return err
		}
	} else if preferred == "kimi_local" {
		if err := kimiconfig.ValidateSpec(spec); err != nil {
			return err
		}
	}
	if preferred == "codex_local" {
		if strings.TrimSpace(im.runtime.CodexHome) == "" {
			return fmt.Errorf("%w: Codex runtime home is not configured", domain.ErrCapabilityMissing)
		}
		if err := codexconfig.Apply(im.runtime.CodexHome, spec); err != nil {
			return fmt.Errorf("Codex runtime config: %w", err)
		}
	} else if preferred == "kimi_local" {
		if strings.TrimSpace(im.runtime.KimiHome) == "" {
			return fmt.Errorf("%w: Kimi runtime home is not configured", domain.ErrCapabilityMissing)
		}
		if err := kimiconfig.Apply(im.runtime.KimiHome, spec); err != nil {
			return fmt.Errorf("Kimi runtime config: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return im.writeBundle(ctx, target, intent)
}

func (im *Importer) modelSpec(target *domain.AgentConfigTarget, profile *domain.AgentProfile) orchestrator.ModelSpec {
	if target != nil && target.ResolvedModel != nil {
		m := target.ResolvedModel
		return orchestrator.ModelSpec{
			Ref: m.Ref, ProviderID: m.ProviderID, ProviderLabel: m.ProviderLabel,
			Provider: m.Provider, API: m.API, Model: m.Model, BaseURL: m.BaseURL,
			APIKeyEnv: m.APIKeyEnv, ContextWindow: m.ContextWindow, MaxTokens: m.MaxTokens,
			ReasoningEffort: m.ReasoningEffort,
		}
	}
	return orchestrator.EffectiveModel(profile, nil, im.runtime.ResolveModel)
}

func (im *Importer) writeBundle(ctx context.Context, target *domain.AgentConfigTarget, intent *domain.AgentConfigSyncIntent) error {
	slug := NormalizeSlug(target.Slug, target.Name)
	if !ValidSlug(slug) {
		return fmt.Errorf("%w: %w: invalid agent config slug %q", codexconfig.ErrStaticConfig, domain.ErrValidation, slug)
	}
	if !ValidSlug(intent.AgentID) {
		return fmt.Errorf("%w: %w: invalid agent config intent id %q", codexconfig.ErrStaticConfig, domain.ErrValidation, intent.ID)
	}
	bundleRoot, err := im.bundleRoot(ctx, target.WorkspaceID)
	if err != nil {
		return err
	}
	if err := ensureNoSymlinkUnder(im.dir, bundleRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(bundleRoot, 0o755); err != nil {
		return err
	}
	if bundleRoot != im.dir {
		if err := agentwork.SyncDir(im.dir); err != nil {
			return err
		}
	}
	sub := filepath.Join(bundleRoot, slug)
	if err := ensureNoSymlinkUnder(bundleRoot, sub); err != nil {
		return err
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		return err
	}
	if err := agentwork.SyncDir(bundleRoot); err != nil {
		return err
	}
	cfg := &FileConfig{Slug: slug}
	cfg.Name, cfg.Role, cfg.Skills, cfg.Avatar, cfg.Prompt = target.Name, target.Role, target.Skills, target.Avatar, target.Instructions
	cfg.Runtime.Preferred, cfg.Runtime.Fallbacks, cfg.Runtime.Mode, cfg.Runtime.AgentPreset = target.RuntimePreference.Preferred, target.RuntimePreference.Fallbacks, target.RuntimePreference.Mode, target.RuntimePreference.AgentPreset
	cfg.Model.Ref, cfg.Model.Provider, cfg.Model.Model, cfg.Model.ReasoningEffort = target.ModelOverride.Ref, target.ModelOverride.Provider, target.ModelOverride.Model, target.ModelOverride.ReasoningEffort
	cfg.Permissions.Tools, cfg.Permissions.ApprovalPolicy, cfg.Permissions.Sandbox, cfg.Permissions.Preset = target.Policy.Tools, target.Policy.ApprovalPolicy, target.Policy.Sandbox, target.Policy.PermissionPreset
	yamlData, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	files := []struct {
		name string
		data []byte
	}{
		{name: "agent.yaml", data: append([]byte("# Agent 配置文件（agents/<slug>/ 为真相源；Web 编辑会回写此文件）\n"), yamlData...)},
		{name: "prompt.md", data: []byte(cfg.Prompt)},
	}
	manifestFiles := make([]BundleFile, 0, len(files))
	for _, file := range files {
		sum := sha256.Sum256(file.data)
		manifestFiles = append(manifestFiles, BundleFile{Path: file.name, Digest: "sha256:" + hex.EncodeToString(sum[:])})
	}
	manifest := BundleManifest{
		SchemaVersion: "agent-config-bundle/v1", State: "staged", IntentID: intent.ID,
		AgentID: target.AgentID, WorkspaceID: target.WorkspaceID, Slug: slug,
		TargetDigest: intent.TargetDigest, Files: manifestFiles,
	}
	manifestPath := filepath.Join(bundleRoot, ".sync", intent.AgentID+".json")
	if err := ensureNoSymlinkUnder(bundleRoot, filepath.Dir(manifestPath)); err != nil {
		return err
	}
	if err := ensureNoSymlinkUnder(bundleRoot, manifestPath); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		return err
	}
	if err := ensureNoSymlinkUnder(bundleRoot, filepath.Dir(manifestPath)); err != nil {
		return err
	}
	if err := ensureNoSymlinkUnder(bundleRoot, manifestPath); err != nil {
		return err
	}
	if err := agentwork.SyncDir(bundleRoot); err != nil {
		return err
	}
	if err := writeManifest(manifestPath, manifest); err != nil {
		return err
	}
	for _, file := range files {
		if err := ensureNoSymlinkUnder(bundleRoot, filepath.Join(sub, file.name)); err != nil {
			return err
		}
		if err := writeAtomic(filepath.Join(sub, file.name), file.data); err != nil {
			return err
		}
	}
	manifest.State = "complete"
	return writeManifest(manifestPath, manifest)
}

// ensureNoSymlinkUnder protects all controlled bundle path components below
// the trusted AGENT_CONFIG_DIR root. Lstat is used deliberately: following a
// pre-existing symlink would let a valid slug publish files outside the
// configured workspace.
func ensureNoSymlinkUnder(root, path string) error {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: agent config path escapes configured root", domain.ErrValidation)
	}
	if rel == "." {
		return nil
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: agent config path component is a symlink", domain.ErrValidation)
		}
	}
	return nil
}

func writeManifest(path string, manifest BundleManifest) error {
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'))
}

func (im *Importer) bundleRoot(ctx context.Context, workspaceID string) (string, error) {
	if !ValidSlug(workspaceID) {
		return "", fmt.Errorf("%w: %w: invalid agent config workspace id %q", codexconfig.ErrStaticConfig, domain.ErrValidation, workspaceID)
	}
	workspaces, err := im.store.Workspaces().ListIDs(ctx)
	if err != nil {
		return "", err
	}
	if len(workspaces) <= 1 {
		return im.dir, nil
	}
	return filepath.Join(im.dir, "workspaces", workspaceID), nil
}

// Import 按 slug 对齐：已存在同 slug → 覆盖配置字段；同名未关联 → 关联 slug 并覆盖；
// 否则新建。availability/presence 等运行态字段不动。
func (im *Importer) Import(ctx context.Context, workspaceID string) (ImportResult, error) {
	im.mu.Lock()
	defer im.mu.Unlock()
	var res ImportResult
	// Keep direct callers safe as well as control-plane startup/reload: a file
	// scan may not become DB truth while any SQLite intent still owns an
	// unfinished external bundle.
	if _, err := im.reconcileLocked(ctx); err != nil {
		return res, err
	}
	bundleRoot, err := im.bundleRoot(ctx, workspaceID)
	if err != nil {
		return res, err
	}
	if bundleRoot != im.dir {
		// A root-level legacy bundle has no workspace identity. Refusing to
		// import it in a multi-workspace database prevents one workspace's
		// profile from becoming another workspace's projection.
		legacy, legacyErr := LoadDir(im.dir)
		if legacyErr != nil {
			return res, legacyErr
		}
		if len(legacy) > 0 {
			return res, fmt.Errorf("%w: agents/ 根目录配置无法在多 Workspace 下安全导入，请迁移到 agents/workspaces/<workspace_id>/", domain.ErrValidation)
		}
	}
	configs, err := LoadDir(bundleRoot)
	if err != nil {
		return res, err
	}
	if len(configs) == 0 {
		return res, nil
	}
	agents, err := im.store.Agents().List(ctx, workspaceID)
	if err != nil {
		return res, err
	}
	bySlug := map[string]*domain.AgentProfile{}
	byName := map[string]*domain.AgentProfile{}
	for _, a := range agents {
		if a.Slug != "" {
			bySlug[a.Slug] = a
		}
		byName[strings.ToLower(a.Name)] = a
	}

	for _, cfg := range configs {
		existing := bySlug[cfg.Slug]
		if existing == nil {
			existing = byName[strings.ToLower(cfg.Name)]
		}
		if existing == nil {
			now := time.Now().UTC()
			a := &domain.AgentProfile{
				ID: domain.NewID(domain.PrefixAgent), WorkspaceID: workspaceID,
				Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
				Version: 1, CreatedAt: now, UpdatedAt: now,
			}
			cfg.ToProfile(a)
			// M4 唤醒缺省与迁移列缺省一致（yaml 暂不覆盖这些字段，更新路径保留 DB 值）。
			a.WakeOnAssignment, a.WakeOnDemand = true, true
			if err := im.store.Agents().Create(ctx, a); err != nil {
				return res, err
			}
			res.Created++
			continue
		}
		cfg.ToProfile(existing)
		expected := existing.Version
		existing.UpdatedAt = time.Now().UTC()
		if err := im.store.Agents().Update(ctx, existing, expected); err != nil {
			if err == domain.ErrVersionConflict {
				res.Skipped++
				continue
			}
			return res, err
		}
		res.Updated++
	}
	return res, nil
}

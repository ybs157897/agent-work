package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gowebpki/jcs"
)

// AgentConfigSyncIntentStatus is the durable state of one external agent
// configuration synchronization attempt. An intent remains active until the
// complete external bundle has been observed successfully and is marked
// applied; failed/conflict intents are deliberately retained for recovery.
type AgentConfigSyncIntentStatus string

const (
	AgentConfigSyncPending  AgentConfigSyncIntentStatus = "pending"
	AgentConfigSyncFailed   AgentConfigSyncIntentStatus = "failed"
	AgentConfigSyncConflict AgentConfigSyncIntentStatus = "conflict"
	AgentConfigSyncApplied  AgentConfigSyncIntentStatus = "applied"
)

func (s AgentConfigSyncIntentStatus) Valid() bool {
	return s == AgentConfigSyncPending || s == AgentConfigSyncFailed ||
		s == AgentConfigSyncConflict || s == AgentConfigSyncApplied
}

// AgentConfigTarget is the complete non-sensitive desired configuration for
// one AgentProfile. It intentionally contains no credential value, runtime
// home, or host path. API-key environment names remain references only; the
// non-secret model registry fields are frozen when an intent is created.
type AgentConfigTarget struct {
	AgentID           string            `json:"agent_id"`
	WorkspaceID       string            `json:"workspace_id"`
	Kind              AgentProfileKind  `json:"kind"`
	Slug              string            `json:"slug"`
	Name              string            `json:"name"`
	Role              string            `json:"role"`
	Skills            []string          `json:"skills"`
	Instructions      string            `json:"instructions"`
	Avatar            string            `json:"avatar,omitempty"`
	RuntimePreference RuntimePreference `json:"runtime_preference"`
	ModelOverride     ModelRef          `json:"model_override"`
	// ResolvedModel freezes the non-secret registry fields consumed by Codex or
	// Kimi. APIKeyEnv is only an environment-variable name; the key itself is
	// intentionally never part of this snapshot.
	ResolvedModel *AgentConfigModelTarget `json:"resolved_model,omitempty"`
	Policy        AgentPolicy             `json:"policy"`
	AgentVersion  int                     `json:"agent_version"`
}

type AgentConfigModelTarget struct {
	Ref             string `json:"ref,omitempty"`
	ProviderID      string `json:"provider_id,omitempty"`
	ProviderLabel   string `json:"provider_label,omitempty"`
	Provider        string `json:"provider,omitempty"`
	API             string `json:"api,omitempty"`
	Model           string `json:"model,omitempty"`
	BaseURL         string `json:"base_url,omitempty"`
	APIKeyEnv       string `json:"api_key_env,omitempty"`
	ContextWindow   int    `json:"context_window,omitempty"`
	MaxTokens       int    `json:"max_tokens,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// AgentConfigSyncIntent is the append/update record joining the SQLite Agent
// CAS/event transaction to effects in agents/, Codex, and Kimi. TargetSnapshot
// is canonical JSON produced from AgentConfigTarget; it is never a free-form
// request body.
type AgentConfigSyncIntent struct {
	ID             string                      `json:"id"`
	AgentID        string                      `json:"agent_id"`
	WorkspaceID    string                      `json:"workspace_id"`
	TargetVersion  int                         `json:"target_version"`
	TargetSnapshot string                      `json:"target_snapshot"`
	TargetDigest   string                      `json:"target_digest"`
	Status         AgentConfigSyncIntentStatus `json:"status"`
	LastError      string                      `json:"last_error,omitempty"`
	Attempts       int                         `json:"attempts"`
	Version        int                         `json:"version"`
	CreatedAt      time.Time                   `json:"created_at"`
	UpdatedAt      time.Time                   `json:"updated_at"`
	AppliedAt      *time.Time                  `json:"applied_at,omitempty"`
}

// AgentConfigTargetFromProfile copies only the fields that external config
// writers are allowed to observe and freezes the Agent version used by CAS.
func AgentConfigTargetFromProfile(a *AgentProfile) (*AgentConfigTarget, error) {
	if a == nil {
		return nil, fmt.Errorf("%w: nil agent profile", ErrValidation)
	}
	if strings.TrimSpace(a.ID) == "" || strings.TrimSpace(a.WorkspaceID) == "" {
		return nil, fmt.Errorf("%w: agent id and workspace id required", ErrValidation)
	}
	if a.Kind.IsSystem() {
		return nil, fmt.Errorf("%w: system task coordinator profile cannot sync as user agent", ErrValidation)
	}
	if a.Version < 1 {
		return nil, fmt.Errorf("%w: agent version must be positive", ErrValidation)
	}
	skills := append([]string(nil), a.Skills...)
	if skills == nil {
		skills = []string{}
	}
	return &AgentConfigTarget{
		AgentID:           a.ID,
		WorkspaceID:       a.WorkspaceID,
		Kind:              AgentProfileKindUser,
		Slug:              a.Slug,
		Name:              a.Name,
		Role:              a.Role,
		Skills:            skills,
		Instructions:      a.Instructions,
		Avatar:            a.Avatar,
		RuntimePreference: cloneRuntimePreference(a.RuntimePreference),
		ModelOverride:     a.ModelOverride,
		Policy:            cloneAgentPolicy(a.Policy),
		AgentVersion:      a.Version,
	}, nil
}

func cloneRuntimePreference(p RuntimePreference) RuntimePreference {
	p.Fallbacks = append([]string(nil), p.Fallbacks...)
	if p.Fallbacks == nil {
		p.Fallbacks = []string{}
	}
	return p
}

func cloneAgentPolicy(p AgentPolicy) AgentPolicy {
	p.Tools = append([]string(nil), p.Tools...)
	if p.Tools == nil {
		p.Tools = []string{}
	}
	return p
}

// Profile materializes the target for external writers. Runtime-only fields
// are intentionally left at zero values and must never be copied from a live
// DB profile while applying an old intent.
func (t *AgentConfigTarget) Profile() (*AgentProfile, error) {
	if t == nil {
		return nil, fmt.Errorf("%w: nil agent config target", ErrValidation)
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return &AgentProfile{
		ID: t.AgentID, WorkspaceID: t.WorkspaceID, Kind: t.Kind, Slug: t.Slug,
		Name: t.Name, Role: t.Role, Skills: append([]string(nil), t.Skills...),
		Instructions: t.Instructions, Avatar: t.Avatar,
		RuntimePreference: cloneRuntimePreference(t.RuntimePreference),
		ModelOverride:     t.ModelOverride, Policy: cloneAgentPolicy(t.Policy),
		Version: t.AgentVersion,
	}, nil
}

func (t *AgentConfigTarget) Validate() error {
	if t == nil {
		return fmt.Errorf("%w: nil agent config target", ErrValidation)
	}
	if strings.TrimSpace(t.AgentID) == "" || strings.TrimSpace(t.WorkspaceID) == "" {
		return fmt.Errorf("%w: target agent/workspace id required", ErrValidation)
	}
	if t.Kind != "" && !t.Kind.Valid() {
		return fmt.Errorf("%w: target agent kind invalid", ErrValidation)
	}
	if t.Kind.IsSystem() {
		return fmt.Errorf("%w: target system agent is not writable", ErrValidation)
	}
	if t.AgentVersion < 1 {
		return fmt.Errorf("%w: target agent version must be positive", ErrValidation)
	}
	if t.ResolvedModel != nil {
		baseURL := strings.TrimSpace(t.ResolvedModel.BaseURL)
		if baseURL != "" {
			u, err := url.Parse(baseURL)
			if err != nil || u.Host == "" ||
				(!strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https")) ||
				u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return fmt.Errorf("%w: target resolved model base_url 必须是无凭据、无 query/fragment 的 http(s) URL", ErrValidation)
			}
		}
	}
	return nil
}

// CanonicalJSON returns the exact snapshot persisted in an intent. JCS makes
// digest verification independent of map/key ordering and JSON whitespace.
func (t *AgentConfigTarget) CanonicalJSON() ([]byte, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return nil, fmt.Errorf("marshal agent config target: %w", err)
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return nil, fmt.Errorf("canonicalize agent config target: %w", err)
	}
	return canonical, nil
}

func ComputeAgentConfigTargetDigest(t *AgentConfigTarget) (string, error) {
	canonical, err := t.CanonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// NewAgentConfigSyncIntent builds a validated durable intent from the exact
// Agent state after a successful CAS, before the surrounding transaction
// commits its event stream/outbox entries.
func NewAgentConfigSyncIntent(a *AgentProfile, now time.Time) (*AgentConfigSyncIntent, error) {
	target, err := AgentConfigTargetFromProfile(a)
	if err != nil {
		return nil, err
	}
	return NewAgentConfigSyncIntentForTarget(target, now)
}

// NewAgentConfigSyncIntentForTarget is used when application wiring has
// already resolved a model registry entry into the target. It keeps that
// resolution immutable across a crash/restart and registry edits.
func NewAgentConfigSyncIntentForTarget(target *AgentConfigTarget, now time.Time) (*AgentConfigSyncIntent, error) {
	canonical, err := target.CanonicalJSON()
	if err != nil {
		return nil, err
	}
	digest, err := ComputeAgentConfigTargetDigest(target)
	if err != nil {
		return nil, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return &AgentConfigSyncIntent{
		ID: domainNewAgentConfigSyncIntentID(), AgentID: target.AgentID, WorkspaceID: target.WorkspaceID,
		TargetVersion: target.AgentVersion, TargetSnapshot: string(canonical), TargetDigest: digest,
		Status: AgentConfigSyncPending, Attempts: 0, Version: 1,
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}, nil
}

// domainNewAgentConfigSyncIntentID is kept here rather than in the persistence
// layer so every implementation gets the same opaque identity contract.
func domainNewAgentConfigSyncIntentID() string { return NewID(PrefixAgentConfigSyncIntent) }

func (i *AgentConfigSyncIntent) DecodeTarget() (*AgentConfigTarget, error) {
	if i == nil {
		return nil, fmt.Errorf("%w: nil agent config sync intent", ErrValidation)
	}
	var target AgentConfigTarget
	if err := json.Unmarshal([]byte(i.TargetSnapshot), &target); err != nil {
		return nil, fmt.Errorf("%w: invalid target snapshot: %v", ErrValidation, err)
	}
	if err := target.Validate(); err != nil {
		return nil, err
	}
	canonical, err := target.CanonicalJSON()
	if err != nil {
		return nil, err
	}
	if string(canonical) != i.TargetSnapshot {
		return nil, fmt.Errorf("%w: target snapshot is not canonical JSON", ErrValidation)
	}
	digest, err := ComputeAgentConfigTargetDigest(&target)
	if err != nil {
		return nil, err
	}
	if i.TargetDigest != digest {
		return nil, fmt.Errorf("%w: target digest mismatch", ErrValidation)
	}
	if target.AgentID != i.AgentID || target.WorkspaceID != i.WorkspaceID || target.AgentVersion != i.TargetVersion {
		return nil, fmt.Errorf("%w: target identity/version mismatch", ErrValidation)
	}
	return &target, nil
}

func (i *AgentConfigSyncIntent) Validate() error {
	if i == nil {
		return fmt.Errorf("%w: nil agent config sync intent", ErrValidation)
	}
	if strings.TrimSpace(i.ID) == "" || strings.TrimSpace(i.AgentID) == "" || strings.TrimSpace(i.WorkspaceID) == "" {
		return fmt.Errorf("%w: intent identity required", ErrValidation)
	}
	if i.TargetVersion < 1 || i.Version < 1 || !i.Status.Valid() {
		return fmt.Errorf("%w: intent version/status invalid", ErrValidation)
	}
	if i.CreatedAt.IsZero() || i.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: intent timestamps required", ErrValidation)
	}
	if _, err := i.DecodeTarget(); err != nil {
		return err
	}
	if i.Status == AgentConfigSyncApplied && i.AppliedAt == nil {
		return fmt.Errorf("%w: applied intent requires applied_at", ErrValidation)
	}
	if i.Status != AgentConfigSyncApplied && i.AppliedAt != nil {
		return fmt.Errorf("%w: non-applied intent cannot have applied_at", ErrValidation)
	}
	return nil
}

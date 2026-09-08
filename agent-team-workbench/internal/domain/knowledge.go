package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Knowledge identity prefixes are deliberately separate from Run/Artifact
// identities.  A knowledge item is a durable subject; each version is an
// immutable content snapshot of that subject.
const (
	PrefixKnowledgeItem          = "kb_"
	PrefixKnowledgeVersion       = "kbv_"
	PrefixKnowledgeSource        = "kbs_"
	PrefixKnowledgeRelation      = "kbr_"
	PrefixKnowledgeRepeal        = "kbrp_"
	PrefixKnowledgeSubmission    = "kss_"
	PrefixKnowledgeQuerySnapshot = "kbq_"
	PrefixKnowledgeRunCapture    = "kbc_"
	// PrefixKnowledgeQuery is kept as the semantic name used by query callers;
	// persisted snapshots use the same identity namespace.
	PrefixKnowledgeQuery = PrefixKnowledgeQuerySnapshot
)

type KnowledgeStatus string

const (
	KnowledgeStatusCandidate  KnowledgeStatus = "candidate"
	KnowledgeStatusDraft      KnowledgeStatus = "draft"
	KnowledgeStatusEffective  KnowledgeStatus = "effective"
	KnowledgeStatusSuperseded KnowledgeStatus = "superseded"
	KnowledgeStatusRepealed   KnowledgeStatus = "repealed"
)

func (s KnowledgeStatus) Valid() bool {
	switch s {
	case KnowledgeStatusCandidate, KnowledgeStatusDraft, KnowledgeStatusEffective,
		KnowledgeStatusSuperseded, KnowledgeStatusRepealed:
		return true
	default:
		return false
	}
}

type KnowledgeVisibility string

const (
	KnowledgeVisibilityPrivate   KnowledgeVisibility = "private"
	KnowledgeVisibilityWorkspace KnowledgeVisibility = "workspace"
)

func (v KnowledgeVisibility) Valid() bool {
	return v == KnowledgeVisibilityPrivate || v == KnowledgeVisibilityWorkspace
}

type KnowledgeSourceKind string

const (
	KnowledgeSourceRun      KnowledgeSourceKind = "run"
	KnowledgeSourceArtifact KnowledgeSourceKind = "artifact"
	KnowledgeSourceWorkItem KnowledgeSourceKind = "work_item"
	KnowledgeSourceDocument KnowledgeSourceKind = "document"
	KnowledgeSourceCode     KnowledgeSourceKind = "code"
	KnowledgeSourceTest     KnowledgeSourceKind = "test"
	KnowledgeSourceUser     KnowledgeSourceKind = "user"
	KnowledgeSourceAgent    KnowledgeSourceKind = "agent"
)

func (k KnowledgeSourceKind) Valid() bool {
	switch k {
	case KnowledgeSourceRun, KnowledgeSourceArtifact, KnowledgeSourceWorkItem,
		KnowledgeSourceDocument, KnowledgeSourceCode, KnowledgeSourceTest,
		KnowledgeSourceUser, KnowledgeSourceAgent:
		return true
	default:
		return false
	}
}

type KnowledgeRelationKind string

const (
	KnowledgeRelationRelatedTo     KnowledgeRelationKind = "related_to"
	KnowledgeRelationDependsOn     KnowledgeRelationKind = "depends_on"
	KnowledgeRelationImpacts       KnowledgeRelationKind = "impacts"
	KnowledgeRelationTriggers      KnowledgeRelationKind = "triggers"
	KnowledgeRelationCalls         KnowledgeRelationKind = "calls"
	KnowledgeRelationSubscribesTo  KnowledgeRelationKind = "subscribes_to"
	KnowledgeRelationSharesState   KnowledgeRelationKind = "shares_state"
	KnowledgeRelationConstrainedBy KnowledgeRelationKind = "constrained_by"
	KnowledgeRelationConflictsWith KnowledgeRelationKind = "conflicts_with"
	KnowledgeRelationSupersedes    KnowledgeRelationKind = "supersedes"
)

func (k KnowledgeRelationKind) Valid() bool {
	switch k {
	case KnowledgeRelationRelatedTo, KnowledgeRelationDependsOn,
		KnowledgeRelationImpacts, KnowledgeRelationTriggers, KnowledgeRelationCalls,
		KnowledgeRelationSubscribesTo, KnowledgeRelationSharesState,
		KnowledgeRelationConstrainedBy, KnowledgeRelationConflictsWith,
		KnowledgeRelationSupersedes:
		return true
	default:
		return false
	}
}

type KnowledgeSubmissionStatus string

const (
	KnowledgeSubmissionReceived    KnowledgeSubmissionStatus = "received"
	KnowledgeSubmissionProcessing  KnowledgeSubmissionStatus = "processing"
	KnowledgeSubmissionAccepted    KnowledgeSubmissionStatus = "accepted"
	KnowledgeSubmissionMerged      KnowledgeSubmissionStatus = "merged"
	KnowledgeSubmissionRejected    KnowledgeSubmissionStatus = "rejected"
	KnowledgeSubmissionNeedsReview KnowledgeSubmissionStatus = "needs_review"
)

func (s KnowledgeSubmissionStatus) Valid() bool {
	switch s {
	case KnowledgeSubmissionReceived, KnowledgeSubmissionProcessing,
		KnowledgeSubmissionAccepted, KnowledgeSubmissionMerged,
		KnowledgeSubmissionRejected, KnowledgeSubmissionNeedsReview:
		return true
	default:
		return false
	}
}

type KnowledgeCoverageStatus string

const (
	KnowledgeCoverageComplete KnowledgeCoverageStatus = "complete"
	KnowledgeCoveragePartial  KnowledgeCoverageStatus = "partial"
	KnowledgeCoverageMissing  KnowledgeCoverageStatus = "missing"
	KnowledgeCoverageConflict KnowledgeCoverageStatus = "conflict"
)

func (s KnowledgeCoverageStatus) Valid() bool {
	return s == KnowledgeCoverageComplete || s == KnowledgeCoveragePartial ||
		s == KnowledgeCoverageMissing || s == KnowledgeCoverageConflict
}

// KnowledgeScope is deliberately open-ended.  The manager can retain project,
// repository, branch, feature, and condition metadata without making the
// persistence schema change for every new scope dimension.
type KnowledgeScope map[string]any

type KnowledgeItem struct {
	ID               string              `json:"id"`
	WorkspaceID      string              `json:"workspace_id"`
	OwnerAgentID     string              `json:"owner_agent_id,omitempty"`
	Visibility       KnowledgeVisibility `json:"visibility"`
	Kind             string              `json:"kind"`
	Title            string              `json:"title"`
	Summary          string              `json:"summary,omitempty"`
	Tags             []string            `json:"tags,omitempty"`
	Aliases          []string            `json:"aliases,omitempty"`
	Scope            KnowledgeScope      `json:"scope,omitempty"`
	CurrentVersionID string              `json:"current_version_id,omitempty"`
	CurrentVersion   int64               `json:"current_version"`
	Status           KnowledgeStatus     `json:"status"`
	RepealReason     string              `json:"repeal_reason,omitempty"`
	Version          int                 `json:"version"`
	CreatedAt        time.Time           `json:"created_at"`
	UpdatedAt        time.Time           `json:"updated_at"`
	// SearchExcerpt is populated only by a query-backed list response. It is
	// deliberately not persisted with the item projection.
	SearchExcerpt string `json:"search_excerpt,omitempty"`
}

// KnowledgeListOptions is the bounded, keyset-paginated read surface for the
// librarian inbox/UI.  Filters are applied before limit+1 pagination so a
// private or out-of-scope row can never consume a visible page slot.
type KnowledgeListOptions struct {
	Status       KnowledgeStatus     `json:"status,omitempty"`
	OwnerAgentID string              `json:"owner_agent_id,omitempty"`
	Scope        KnowledgeScope      `json:"scope,omitempty"`
	Kind         string              `json:"kind,omitempty"`
	Visibility   KnowledgeVisibility `json:"visibility,omitempty"`
	AfterID      string              `json:"after_id,omitempty"`
	Limit        int                 `json:"limit,omitempty"`
	Query        string              `json:"query,omitempty"`
}

func (o KnowledgeListOptions) Normalize() KnowledgeListOptions {
	if o.Status == "" {
		o.Status = KnowledgeStatusEffective
	}
	if o.Limit <= 0 || o.Limit > 1000 {
		o.Limit = 20
	}
	return o
}

func (o KnowledgeListOptions) Validate() error {
	if o.OwnerAgentID == "" {
		return nil
	}
	return validateTypedID("knowledge_list.owner_agent_id", o.OwnerAgentID, PrefixAgent)
}

type KnowledgeVersion struct {
	ID                  string          `json:"id"`
	ItemID              string          `json:"item_id"`
	Version             int64           `json:"version"`
	BaseVersion         int64           `json:"base_version"`
	Status              KnowledgeStatus `json:"status"`
	Kind                string          `json:"kind"`
	Title               string          `json:"title"`
	Summary             string          `json:"summary,omitempty"`
	BodyMarkdown        string          `json:"body_markdown"`
	Tags                []string        `json:"tags,omitempty"`
	Aliases             []string        `json:"aliases,omitempty"`
	Scope               KnowledgeScope  `json:"scope,omitempty"`
	Metadata            map[string]any  `json:"metadata,omitempty"`
	ContentDigest       string          `json:"content_digest"`
	CreatedByAgentID    string          `json:"created_by_agent_id"`
	CreatedByRunID      string          `json:"created_by_run_id,omitempty"`
	CreatedByWorkItemID string          `json:"created_by_work_item_id,omitempty"`
	SupersedesVersionID string          `json:"supersedes_version_id,omitempty"`
	PublishedAt         *time.Time      `json:"published_at,omitempty"`
	CreatedAt           time.Time       `json:"created_at"`
}

type KnowledgeSource struct {
	ID                 string              `json:"id"`
	WorkspaceID        string              `json:"workspace_id"`
	SubmittedByAgentID string              `json:"submitted_by_agent_id"`
	Kind               KnowledgeSourceKind `json:"kind"`
	Ref                string              `json:"ref"`
	Locator            string              `json:"locator,omitempty"`
	Excerpt            string              `json:"excerpt,omitempty"`
	Digest             string              `json:"digest,omitempty"`
	Metadata           map[string]any      `json:"metadata,omitempty"`
	CreatedAt          time.Time           `json:"created_at"`
}

// KnowledgeVersionSource records the exact evidence set used by one version.
// Role lets a manager distinguish primary evidence from context or a negative
// source while keeping the source row reusable across versions.
type KnowledgeVersionSource struct {
	VersionID string `json:"version_id"`
	SourceID  string `json:"source_id"`
	Role      string `json:"role,omitempty"`
}

type KnowledgeRelation struct {
	ID              string                `json:"id"`
	WorkspaceID     string                `json:"workspace_id"`
	SourceVersionID string                `json:"source_version_id"`
	FromItemID      string                `json:"from_item_id"`
	ToItemID        string                `json:"to_item_id"`
	Kind            KnowledgeRelationKind `json:"kind"`
	Condition       string                `json:"condition,omitempty"`
	Rationale       string                `json:"rationale,omitempty"`
	SourceIDs       []string              `json:"source_ids,omitempty"`
	CreatedAt       time.Time             `json:"created_at"`
}

type KnowledgeRelationDirection string

const (
	KnowledgeRelationBoth KnowledgeRelationDirection = "both"
	KnowledgeRelationOut  KnowledgeRelationDirection = "out"
	KnowledgeRelationIn   KnowledgeRelationDirection = "in"
)

func (d KnowledgeRelationDirection) Valid() bool {
	return d == KnowledgeRelationBoth || d == KnowledgeRelationOut || d == KnowledgeRelationIn
}

// KnowledgeChange is the unit of an agent's incremental post-run submission.
// ItemID may be empty for a new subject.  BaseVersion is the optimistic
// version the agent observed; zero means the subject did not exist yet.
type KnowledgeChange struct {
	ItemID       string                   `json:"item_id,omitempty"`
	BaseVersion  int64                    `json:"base_version"`
	OwnerAgentID string                   `json:"owner_agent_id,omitempty"`
	Visibility   KnowledgeVisibility      `json:"visibility,omitempty"`
	Title        string                   `json:"title"`
	Body         string                   `json:"body"`
	Kind         string                   `json:"kind"`
	Scope        KnowledgeScope           `json:"scope,omitempty"`
	Summary      string                   `json:"summary,omitempty"`
	Tags         []string                 `json:"tags,omitempty"`
	Aliases      []string                 `json:"aliases,omitempty"`
	Sources      []KnowledgeSourceInput   `json:"sources,omitempty"`
	Relations    []KnowledgeRelationInput `json:"relations,omitempty"`
}

type KnowledgeSourceInput struct {
	Kind     KnowledgeSourceKind `json:"kind"`
	Ref      string              `json:"ref"`
	Locator  string              `json:"locator,omitempty"`
	Excerpt  string              `json:"excerpt,omitempty"`
	Digest   string              `json:"digest,omitempty"`
	Metadata map[string]any      `json:"metadata,omitempty"`
	Role     string              `json:"role,omitempty"`
}

type KnowledgeRelationInput struct {
	FromItemID string                `json:"from_item_id,omitempty"`
	ToItemID   string                `json:"to_item_id"`
	Kind       KnowledgeRelationKind `json:"kind"`
	Condition  string                `json:"condition,omitempty"`
	Rationale  string                `json:"rationale,omitempty"`
	SourceIDs  []string              `json:"source_ids,omitempty"`
}

// KnowledgeSubmitCandidate is the public request envelope.  A successful
// no-change submission is durable evidence that the run considered knowledge
// capture and found nothing worth retaining.
type KnowledgeSubmitCandidate struct {
	WorkspaceID string            `json:"workspace_id"`
	AgentID     string            `json:"agent_id"`
	RunID       string            `json:"run_id,omitempty"`
	WorkItemID  string            `json:"work_item_id,omitempty"`
	ClientKey   string            `json:"client_key"`
	Changes     []KnowledgeChange `json:"changes,omitempty"`
	NoChange    bool              `json:"no_change,omitempty"`
}

// SubmitKnowledgeCandidateRequest is the descriptive public alias used by
// HTTP/Harness callers; both names intentionally share one JSON contract.
type SubmitKnowledgeCandidateRequest = KnowledgeSubmitCandidate

type KnowledgeSubmission struct {
	ID               string                    `json:"id"`
	WorkspaceID      string                    `json:"workspace_id"`
	AgentID          string                    `json:"agent_id"`
	RunID            string                    `json:"run_id,omitempty"`
	WorkItemID       string                    `json:"work_item_id,omitempty"`
	ClientKey        string                    `json:"client_key"`
	Request          KnowledgeSubmitCandidate  `json:"request"`
	RequestDigest    string                    `json:"request_digest"`
	Status           KnowledgeSubmissionStatus `json:"status"`
	ResultItemIDs    []string                  `json:"result_item_ids,omitempty"`
	ResultVersionIDs []string                  `json:"result_version_ids,omitempty"`
	ErrorMessage     string                    `json:"error_message,omitempty"`
	Version          int                       `json:"version"`
	CreatedAt        time.Time                 `json:"created_at"`
	UpdatedAt        time.Time                 `json:"updated_at"`
}

type KnowledgeBudget struct {
	MaxResults   int   `json:"max_results"`
	MaxDepth     int   `json:"max_depth"`
	MaxNodes     int   `json:"max_nodes"`
	MaxBytes     int64 `json:"max_bytes"`
	MaxSearches  int   `json:"max_searches"`
	MaxRelations int   `json:"max_relations"`
}

func (b KnowledgeBudget) Normalize() KnowledgeBudget {
	if b.MaxResults <= 0 || b.MaxResults > 100 {
		b.MaxResults = 20
	}
	if b.MaxDepth <= 0 || b.MaxDepth > 8 {
		b.MaxDepth = 3
	}
	if b.MaxNodes <= 0 || b.MaxNodes > 1000 {
		b.MaxNodes = 100
	}
	if b.MaxBytes <= 0 || b.MaxBytes > 2<<20 {
		b.MaxBytes = 256 << 10
	}
	if b.MaxSearches <= 0 || b.MaxSearches > 100 {
		b.MaxSearches = 20
	}
	if b.MaxRelations <= 0 || b.MaxRelations > 1000 {
		b.MaxRelations = 200
	}
	return b
}

type KnowledgeQuery struct {
	WorkspaceID      string                     `json:"workspace_id"`
	RequesterAgentID string                     `json:"requester_agent_id"`
	Scope            KnowledgeScope             `json:"scope,omitempty"`
	Question         string                     `json:"question"`
	Context          string                     `json:"context,omitempty"`
	Terms            []string                   `json:"terms,omitempty"`
	ItemIDs          []string                   `json:"item_ids,omitempty"`
	Status           KnowledgeStatus            `json:"status,omitempty"`
	Direction        KnowledgeRelationDirection `json:"direction,omitempty"`
	Budget           KnowledgeBudget            `json:"budget"`
}

type KnowledgeHit struct {
	Item      KnowledgeItem       `json:"item"`
	Version   KnowledgeVersion    `json:"version"`
	Score     float64             `json:"score"`
	Snippet   string              `json:"snippet,omitempty"`
	Relations []KnowledgeRelation `json:"relations,omitempty"`
	Sources   []KnowledgeSource   `json:"sources,omitempty"`
}

type KnowledgeCoverageEntry struct {
	Subject        string                  `json:"subject"`
	Status         KnowledgeCoverageStatus `json:"status"`
	EvidenceIDs    []string                `json:"evidence_ids,omitempty"`
	RelatedItemIDs []string                `json:"related_item_ids,omitempty"`
	Missing        []string                `json:"missing,omitempty"`
	Note           string                  `json:"note,omitempty"`
}

type KnowledgeCoverage struct {
	Status           KnowledgeCoverageStatus  `json:"status"`
	Entries          []KnowledgeCoverageEntry `json:"entries"`
	VisitedNodes     int                      `json:"visited_nodes"`
	VisitedRelations int                      `json:"visited_relations"`
	Truncated        bool                     `json:"truncated"`
	Budget           KnowledgeBudget          `json:"budget"`
}

type KnowledgeQuerySnapshot struct {
	ID               string            `json:"id"`
	WorkspaceID      string            `json:"workspace_id"`
	RequesterAgentID string            `json:"requester_agent_id"`
	Question         string            `json:"question"`
	Context          string            `json:"context,omitempty"`
	Scope            KnowledgeScope    `json:"scope,omitempty"`
	Budget           KnowledgeBudget   `json:"budget"`
	IndexRevision    int64             `json:"index_revision"`
	Results          []KnowledgeHit    `json:"results"`
	Coverage         KnowledgeCoverage `json:"coverage"`
	ResultJSON       string            `json:"result_json,omitempty"`
	CoverageJSON     string            `json:"coverage_json,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
}

type KnowledgeIndexState struct {
	WorkspaceID string    `json:"workspace_id"`
	Revision    int64     `json:"revision"`
	ItemCount   int       `json:"item_count"`
	BuiltAt     time.Time `json:"built_at"`
}

// KnowledgeRepeal is the append-only audit record for withdrawing an item from
// the effective read projection. Historical versions remain readable to an
// authorized caller, while their content is never rewritten.
type KnowledgeRepeal struct {
	ID             string    `json:"id"`
	WorkspaceID    string    `json:"workspace_id"`
	ItemID         string    `json:"item_id"`
	VersionID      string    `json:"version_id"`
	CurrentVersion int64     `json:"current_version"`
	Reason         string    `json:"reason"`
	CreatedAt      time.Time `json:"created_at"`
}

// KnowledgeRunCapture is the durable post-terminal handoff from an ordinary
// Agent Run to the librarian. It is intentionally an inbox row rather than a
// second Run/Lease state machine: the consumer later reads the original Run
// evidence and records a normal KnowledgeSubmission.
type KnowledgeRunCapture struct {
	ID            string     `json:"id"`
	WorkspaceID   string     `json:"workspace_id"`
	RunID         string     `json:"run_id"`
	CreatedAt     time.Time  `json:"created_at"`
	ProcessedAt   *time.Time `json:"processed_at,omitempty"`
	SubmissionID  string     `json:"submission_id,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty"`
	Version       int        `json:"version"`
}

// ValidateReadScope is shared by repository and engine callers.  The database
// query must still repeat this predicate; this method prevents accidental
// construction of an unscoped query object in application code.
func (q *KnowledgeQuery) ValidateReadScope() error {
	if q == nil {
		return fmt.Errorf("%w: nil knowledge query", ErrValidation)
	}
	if strings.TrimSpace(q.WorkspaceID) == "" {
		return fmt.Errorf("%w: knowledge query workspace_id is required", ErrValidation)
	}
	if strings.TrimSpace(q.RequesterAgentID) == "" {
		return fmt.Errorf("%w: knowledge query requester_agent_id is required", ErrValidation)
	}
	if strings.TrimSpace(q.Question) == "" && len(q.Terms) == 0 && len(q.ItemIDs) == 0 {
		return fmt.Errorf("%w: knowledge query requires question, terms, or item_ids", ErrValidation)
	}
	if q.Status != "" && !q.Status.Valid() {
		return fmt.Errorf("%w: unknown knowledge status %q", ErrValidation, q.Status)
	}
	if q.Direction != "" && !q.Direction.Valid() {
		return fmt.Errorf("%w: unknown knowledge relation direction %q", ErrValidation, q.Direction)
	}
	return nil
}

func (i *KnowledgeItem) Validate() error {
	if i == nil {
		return fmt.Errorf("%w: nil knowledge item", ErrValidation)
	}
	if err := validateTypedID("knowledge_item.id", i.ID, PrefixKnowledgeItem); err != nil {
		return err
	}
	if strings.TrimSpace(i.WorkspaceID) == "" {
		return fmt.Errorf("%w: knowledge_item.workspace_id is required", ErrValidation)
	}
	if !i.Visibility.Valid() {
		return fmt.Errorf("%w: invalid knowledge visibility %q", ErrValidation, i.Visibility)
	}
	if i.Visibility == KnowledgeVisibilityPrivate && strings.TrimSpace(i.OwnerAgentID) == "" {
		return fmt.Errorf("%w: private knowledge requires owner_agent_id", ErrValidation)
	}
	if strings.TrimSpace(i.Kind) == "" || strings.TrimSpace(i.Title) == "" {
		return fmt.Errorf("%w: knowledge item kind and title are required", ErrValidation)
	}
	if !i.Status.Valid() {
		return fmt.Errorf("%w: invalid knowledge item status %q", ErrValidation, i.Status)
	}
	if i.CurrentVersion < 0 || i.Version < 1 {
		return fmt.Errorf("%w: invalid knowledge item version state", ErrValidation)
	}
	return nil
}

func (v *KnowledgeVersion) Validate() error {
	if v == nil {
		return fmt.Errorf("%w: nil knowledge version", ErrValidation)
	}
	if err := validateTypedID("knowledge_version.id", v.ID, PrefixKnowledgeVersion); err != nil {
		return err
	}
	if err := validateTypedID("knowledge_version.item_id", v.ItemID, PrefixKnowledgeItem); err != nil {
		return err
	}
	if v.Version < 1 || v.BaseVersion < 0 {
		return fmt.Errorf("%w: knowledge version number must be positive and base_version non-negative", ErrValidation)
	}
	if !v.Status.Valid() {
		return fmt.Errorf("%w: invalid knowledge version status %q", ErrValidation, v.Status)
	}
	if strings.TrimSpace(v.Kind) == "" || strings.TrimSpace(v.Title) == "" || strings.TrimSpace(v.BodyMarkdown) == "" {
		return fmt.Errorf("%w: knowledge version kind, title, and body_markdown are required", ErrValidation)
	}
	if strings.TrimSpace(v.CreatedByAgentID) == "" {
		return fmt.Errorf("%w: knowledge version created_by_agent_id is required", ErrValidation)
	}
	if v.ContentDigest != "" && !strings.HasPrefix(v.ContentDigest, "sha256:") {
		return fmt.Errorf("%w: knowledge version content_digest must be sha256", ErrValidation)
	}
	return nil
}

// ComputeKnowledgeContentDigest seals only the version's content fields.  The
// numeric version and timestamps are excluded so the same content can be
// compared across candidate retries and a later immutable version number.
func ComputeKnowledgeContentDigest(v *KnowledgeVersion) (string, error) {
	if v == nil {
		return "", fmt.Errorf("%w: nil knowledge version", ErrValidation)
	}
	payload := struct {
		Kind     string         `json:"kind"`
		Title    string         `json:"title"`
		Summary  string         `json:"summary"`
		Body     string         `json:"body_markdown"`
		Tags     []string       `json:"tags"`
		Aliases  []string       `json:"aliases"`
		Scope    KnowledgeScope `json:"scope"`
		Metadata map[string]any `json:"metadata"`
	}{
		Kind: v.Kind, Title: v.Title, Summary: v.Summary, Body: v.BodyMarkdown,
		Tags: NormalizeKnowledgeTags(v.Tags), Aliases: NormalizeKnowledgeTags(v.Aliases),
		Scope: v.Scope, Metadata: v.Metadata,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("%w: marshal knowledge content: %v", ErrValidation, err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// SealContent fills a missing digest and rejects a supplied digest that does
// not match the immutable Markdown/metadata content.
func (v *KnowledgeVersion) SealContent() error {
	if v == nil {
		return fmt.Errorf("%w: nil knowledge version", ErrValidation)
	}
	want, err := ComputeKnowledgeContentDigest(v)
	if err != nil {
		return err
	}
	if v.ContentDigest != "" && v.ContentDigest != want {
		return fmt.Errorf("%w: knowledge version content_digest does not match content", ErrValidation)
	}
	v.ContentDigest = want
	return v.Validate()
}

func (s *KnowledgeSource) Validate() error {
	if s == nil {
		return fmt.Errorf("%w: nil knowledge source", ErrValidation)
	}
	if err := validateTypedID("knowledge_source.id", s.ID, PrefixKnowledgeSource); err != nil {
		return err
	}
	if strings.TrimSpace(s.WorkspaceID) == "" || strings.TrimSpace(s.SubmittedByAgentID) == "" ||
		!s.Kind.Valid() || strings.TrimSpace(s.Ref) == "" {
		return fmt.Errorf("%w: incomplete knowledge source", ErrValidation)
	}
	return nil
}

func (r *KnowledgeRelation) Validate() error {
	if r == nil {
		return fmt.Errorf("%w: nil knowledge relation", ErrValidation)
	}
	if err := validateTypedID("knowledge_relation.id", r.ID, PrefixKnowledgeRelation); err != nil {
		return err
	}
	if err := validateTypedID("knowledge_relation.source_version_id", r.SourceVersionID, PrefixKnowledgeVersion); err != nil {
		return err
	}
	if err := validateTypedID("knowledge_relation.from_item_id", r.FromItemID, PrefixKnowledgeItem); err != nil {
		return err
	}
	if err := validateTypedID("knowledge_relation.to_item_id", r.ToItemID, PrefixKnowledgeItem); err != nil {
		return err
	}
	if r.FromItemID == r.ToItemID || !r.Kind.Valid() {
		return fmt.Errorf("%w: invalid knowledge relation endpoints or kind", ErrValidation)
	}
	return nil
}

func (s *KnowledgeSubmitCandidate) Validate() error {
	if s == nil {
		return fmt.Errorf("%w: nil knowledge candidate submission", ErrValidation)
	}
	if strings.TrimSpace(s.WorkspaceID) == "" || strings.TrimSpace(s.AgentID) == "" ||
		strings.TrimSpace(s.ClientKey) == "" {
		return fmt.Errorf("%w: candidate submission workspace, agent, and client_key are required", ErrValidation)
	}
	if !s.NoChange && len(s.Changes) == 0 {
		return fmt.Errorf("%w: candidate submission requires changes or no_change", ErrValidation)
	}
	if s.NoChange && len(s.Changes) != 0 {
		return fmt.Errorf("%w: no_change submission cannot contain changes", ErrValidation)
	}
	for n := range s.Changes {
		c := &s.Changes[n]
		if c.BaseVersion < 0 || strings.TrimSpace(c.Title) == "" || strings.TrimSpace(c.Body) == "" || strings.TrimSpace(c.Kind) == "" {
			return fmt.Errorf("%w: invalid candidate change %d", ErrValidation, n)
		}
		if c.Visibility != "" && !c.Visibility.Valid() {
			return fmt.Errorf("%w: invalid candidate visibility %d", ErrValidation, n)
		}
		if c.Visibility == KnowledgeVisibilityPrivate && strings.TrimSpace(c.OwnerAgentID) == "" {
			return fmt.Errorf("%w: private candidate requires owner_agent_id %d", ErrValidation, n)
		}
		for _, source := range c.Sources {
			if !source.Kind.Valid() || strings.TrimSpace(source.Ref) == "" {
				return fmt.Errorf("%w: invalid source in candidate change %d", ErrValidation, n)
			}
		}
		for _, relation := range c.Relations {
			if strings.TrimSpace(relation.ToItemID) == "" || !relation.Kind.Valid() {
				return fmt.Errorf("%w: invalid relation in candidate change %d", ErrValidation, n)
			}
		}
	}
	return nil
}

func (s *KnowledgeSubmission) Validate() error {
	if s == nil {
		return fmt.Errorf("%w: nil knowledge submission", ErrValidation)
	}
	if err := validateTypedID("knowledge_submission.id", s.ID, PrefixKnowledgeSubmission); err != nil {
		return err
	}
	if s.Request.WorkspaceID == "" {
		s.Request.WorkspaceID = s.WorkspaceID
	}
	if s.Request.AgentID == "" {
		s.Request.AgentID = s.AgentID
	}
	if s.Request.ClientKey == "" {
		s.Request.ClientKey = s.ClientKey
	}
	if err := s.Request.Validate(); err != nil {
		return err
	}
	if s.WorkspaceID != s.Request.WorkspaceID || s.AgentID != s.Request.AgentID || s.ClientKey != s.Request.ClientKey {
		return fmt.Errorf("%w: submission envelope and request identity differ", ErrValidation)
	}
	if !s.Status.Valid() {
		return fmt.Errorf("%w: invalid knowledge submission status %q", ErrValidation, s.Status)
	}
	return nil
}

// SealRequest canonicalizes the request and computes its idempotency digest.
// The request digest covers every change, source, and relation, so a replay
// with the same key but a different payload fails closed.
func (s *KnowledgeSubmission) SealRequest() error {
	if s == nil {
		return fmt.Errorf("%w: nil knowledge submission", ErrValidation)
	}
	if err := s.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(s.Request)
	if err != nil {
		return fmt.Errorf("%w: marshal knowledge submission: %v", ErrValidation, err)
	}
	sum := sha256.Sum256(raw)
	s.RequestDigest = "sha256:" + hex.EncodeToString(sum[:])
	return nil
}

func (s *KnowledgeQuerySnapshot) Validate() error {
	if s == nil {
		return fmt.Errorf("%w: nil knowledge query snapshot", ErrValidation)
	}
	if err := validateTypedID("knowledge_query_snapshot.id", s.ID, PrefixKnowledgeQuerySnapshot); err != nil {
		return err
	}
	if strings.TrimSpace(s.WorkspaceID) == "" || strings.TrimSpace(s.RequesterAgentID) == "" || strings.TrimSpace(s.Question) == "" {
		return fmt.Errorf("%w: incomplete knowledge query snapshot", ErrValidation)
	}
	if s.IndexRevision < 0 {
		return fmt.Errorf("%w: knowledge query snapshot index_revision must be non-negative", ErrValidation)
	}
	if !s.Coverage.Status.Valid() {
		return fmt.Errorf("%w: invalid knowledge coverage status", ErrValidation)
	}
	return nil
}

func (r *KnowledgeRepeal) Validate() error {
	if r == nil {
		return fmt.Errorf("%w: nil knowledge repeal", ErrValidation)
	}
	if err := validateTypedID("knowledge_repeal.id", r.ID, PrefixKnowledgeRepeal); err != nil {
		return err
	}
	if err := validateTypedID("knowledge_repeal.item_id", r.ItemID, PrefixKnowledgeItem); err != nil {
		return err
	}
	if err := validateTypedID("knowledge_repeal.version_id", r.VersionID, PrefixKnowledgeVersion); err != nil {
		return err
	}
	if strings.TrimSpace(r.WorkspaceID) == "" || r.CurrentVersion < 1 || strings.TrimSpace(r.Reason) == "" {
		return fmt.Errorf("%w: incomplete knowledge repeal", ErrValidation)
	}
	return nil
}

func (c *KnowledgeRunCapture) Validate() error {
	if c == nil {
		return fmt.Errorf("%w: nil knowledge run capture", ErrValidation)
	}
	if err := validateTypedID("knowledge_run_capture.id", c.ID, PrefixKnowledgeRunCapture); err != nil {
		return err
	}
	if strings.TrimSpace(c.WorkspaceID) == "" || strings.TrimSpace(c.RunID) == "" {
		return fmt.Errorf("%w: knowledge run capture workspace_id and run_id are required", ErrValidation)
	}
	if c.CreatedAt.IsZero() || c.Version < 1 {
		return fmt.Errorf("%w: knowledge run capture created_at and positive version are required", ErrValidation)
	}
	if c.ProcessedAt == nil && strings.TrimSpace(c.SubmissionID) != "" {
		return fmt.Errorf("%w: pending knowledge run capture cannot have submission_id", ErrValidation)
	}
	return nil
}

// NormalizeTerms produces deterministic, case-insensitive terms while
// preserving a caller's first occurrence order for snippets and auditing.
func NormalizeKnowledgeTerms(terms []string) []string {
	out := make([]string, 0, len(terms))
	seen := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		term = strings.TrimSpace(strings.ToLower(term))
		if term == "" {
			continue
		}
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		out = append(out, term)
	}
	return out
}

func NormalizeKnowledgeTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(strings.ToLower(tag))
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	slices.Sort(out)
	return out
}

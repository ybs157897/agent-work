package domain

import "time"

// ── Unified knowledge library ───────────────────────────────────────────
//
// One workspace owns exactly one library. A library holds the Markdown
// knowledge of every service, common module and document collection of the
// workspace's complete business project, so cross-service relations are
// relations inside one library rather than joins across separate ones.

// KnowledgeLibrary is the workspace-scoped library root.
type KnowledgeLibrary struct {
	ID                string
	WorkspaceID       string
	RootPath          string
	LibrarianAgentID  string
	Enabled           bool
	CurrentReleaseID  string
	CurrentSnapshotID string
	IndexRevision     int
	Version           int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// KnowledgeSourceKind classifies a registered source.
type KnowledgeSourceKind string

const (
	SourceKindService   KnowledgeSourceKind = "service"
	SourceKindCommon    KnowledgeSourceKind = "common"
	SourceKindDocuments KnowledgeSourceKind = "documents"
	// SourceKindRequirement is an external requirement input the library froze
	// from an imported requirement document. It is a source so the librarian
	// can cite its text as evidence, exactly like a repository binding.
	SourceKindRequirement KnowledgeSourceKind = "requirement"
	SourceKindOther       KnowledgeSourceKind = "other"
)

func (k KnowledgeSourceKind) Valid() bool {
	switch k {
	case SourceKindService, SourceKindCommon, SourceKindDocuments, SourceKindRequirement, SourceKindOther:
		return true
	}
	return false
}

// KnowledgeSourceUsage is one concrete use binding of a logical source: which
// consumer depends on which artifact version. A common library typically has
// one usage per consuming service, and the versions may differ.
type KnowledgeSourceUsage struct {
	Consumer    string `json:"consumer"`
	Artifact    string `json:"artifact"`
	Environment string `json:"environment,omitempty"`
	// ArtifactResolution says how Artifact was obtained. A value typed into the
	// admin form is 'declared'; 'resolved' is a derived state that requires a
	// verifiable basis in ResolutionRef; an unknown mapping stays 'unknown'.
	ArtifactResolution string `json:"artifact_resolution,omitempty"`
	// ResolutionRef is the verifiable basis for a 'resolved' claim, for example
	// a build/dependency-resolution artifact reference. Without it the claim is
	// downgraded to 'declared': a caller cannot mint a verified state by
	// sending a string.
	ResolutionRef string `json:"resolution_ref,omitempty"`
}

// ArtifactState is the *effective* state of the artifact claim.
//
// This build has no dependency-resolution or build-artifact capability, so it
// can never verify a version: every registered value is a declaration. A
// caller may say 'resolved' and may attach a reference, but a string is not
// evidence — a reference is only kept as an unverified note, and the effective
// state stays 'declared'. Reporting otherwise would let any caller mint a
// verified state by sending a field.
func (u KnowledgeSourceUsage) ArtifactState() string {
	if u.Artifact == "" {
		return "unknown"
	}
	return "declared"
}

// ResolutionClaimed returns what the caller asserted, for display next to the
// effective state. It is a claim, never a state.
func (u KnowledgeSourceUsage) ResolutionClaimed() string {
	switch u.ArtifactResolution {
	case "resolved", "unknown":
		return u.ArtifactResolution
	default:
		return "declared"
	}
}

// ResolutionDowngraded reports whether the caller's claim is stronger than
// what this build can establish. It is true for any 'resolved' claim, because
// nothing in this build can verify a version.
func (u KnowledgeSourceUsage) ResolutionDowngraded() bool {
	return u.ResolutionClaimed() == "resolved"
}

// QualifiedName is the selector an evidence request uses to address this
// binding. It must be unique across the bindings of one source, so it grows
// with the identity rather than collapsing to source@consumer.
func (u KnowledgeSourceUsage) QualifiedName(sourceName string) string {
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

// KnowledgeSource is one registered repository. Usages carries the concrete
// use bindings; an empty list means one implicit usage with no consumer and no
// artifact.
type KnowledgeSource struct {
	ID           string
	LibraryID    string
	Name         string
	Kind         KnowledgeSourceKind
	RepoPath     string
	DefaultRef   string
	IncludeGlobs []string
	ExcludeGlobs []string
	Usages       []KnowledgeSourceUsage
	Enabled      bool
	Version      int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// EffectiveUsages returns the declared usages, or one implicit usage so a
// plain service repository still produces exactly one binding.
func (s *KnowledgeSource) EffectiveUsages() []KnowledgeSourceUsage {
	if len(s.Usages) == 0 {
		return []KnowledgeSourceUsage{{}}
	}
	return s.Usages
}

// KnowledgeSnapshot is one immutable input set.
type KnowledgeSnapshot struct {
	ID               string
	LibraryID        string
	ViewID           string
	ParentSnapshotID string
	Reason           string
	CapturedAt       time.Time
	Bindings         []KnowledgeSnapshotBinding
}

// KnowledgeRequirementInput is one imported requirement document, frozen at
// acceptance time. Identity is the digest of the accepted bytes, so a later
// edit to the referenced path cannot change what a task documented.
type KnowledgeRequirementInput struct {
	ID                 string
	LibraryID          string
	EventID            string
	SourceID           string
	RequirementID      string
	RequirementVersion string
	Title              string
	ContentRef         string
	StoredPath         string
	ContentDigest      string
	ByteSize           int
	PayloadJSON        string
	// InputOrigin is where the frozen body came from: the referenced document
	// ('content_ref') or the event's inline payload ('inline_payload').
	InputOrigin string
	// ReferenceError records that a referenced document was declared but could
	// not be read. It survives even when an inline copy was accepted, so the
	// gap is never lost.
	ReferenceError string
	CreatedAt      time.Time
}

// Requirement input origins.
const (
	RequirementOriginContentRef    = "content_ref"
	RequirementOriginInlinePayload = "inline_payload"
)

// KnowledgeSnapshotBinding freezes one source inside one snapshot.
type KnowledgeSnapshotBinding struct {
	ID                    string
	SnapshotID            string
	SourceID              string
	SourceName            string
	SourceKind            string
	RepoPath              string
	GitRef                string
	CommitSHA             string
	ObjectFormat          string
	Dirty                 bool
	DirtyDigest           string
	Untracked             []string
	Artifact              string
	ArtifactResolution    string
	ArtifactResolutionRef string
	Consumer              string
	Environment           string
	CapturedAt            time.Time
}

// KnowledgeRepresentation is a frozen content representation.
type KnowledgeRepresentation struct {
	ID            string
	BindingID     string
	Path          string
	MediaType     string
	Encoding      string
	DigestAlgo    string
	ContentDigest string
	ByteSize      int
	Coverage      string
	Transform     string
	StoredPath    string
	Origin        string
	CreatedAt     time.Time
}

// KnowledgeEvidence is collector-verified evidence.
type KnowledgeEvidence struct {
	ID               string
	LibraryID        string
	SnapshotID       string
	BindingID        string
	RepresentationID string
	LocatorJSON      string
	LocatorKind      string
	Excerpt          string
	ExcerptDigest    string
	MatchCount       int
	Availability     string
	CollectedAt      time.Time
}

// KnowledgeEntity is a stable project entity.
type KnowledgeEntity struct {
	ID                string
	LibraryID         string
	Kind              string
	Namespace         string
	CanonicalName     string
	Aliases           []string
	SourceID          string
	PrimaryDocumentID string
	Version           int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// KnowledgeDocument is the logical identity of one Markdown topic document.
type KnowledgeDocument struct {
	ID             string
	LibraryID      string
	Path           string
	Kind           string
	Title          string
	Summary        string
	Domains        []string
	Status         string
	RenamedFrom    string
	RenameNote     string
	CurrentVersion int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// KnowledgeDocumentVersion is one immutable content version.
type KnowledgeDocumentVersion struct {
	ID         string
	DocumentID string
	LibraryID  string
	Version    int
	Title      string
	// Path is the document path as of this version, so a historical release
	// keeps reading the location it was published under.
	Path            string
	ContentMarkdown string
	FrontmatterJSON string
	ContentDigest   string
	// EvidenceAliasJSON maps the staged evidence keys this version was written
	// with to the canonical evidence IDs the collector assigned.
	EvidenceAliasJSON string
	SnapshotID        string
	TaskID            string
	ReleaseID         string
	DerivedFromID     string
	CreatedAt         time.Time
}

// KnowledgeAssertion is one materialized assertion of a document version.
type KnowledgeAssertion struct {
	RowID             string
	ID                string
	LibraryID         string
	DocumentID        string
	DocumentVersionID string
	Heading           string
	About             []string
	Perspective       string
	Basis             string
	Statement         string
	ScopeJSON         string
	EvidenceJSON      string
	UnknownNotes      string
	Ordinal           int
	ContentDigest     string
	CreatedAt         time.Time
}

// KnowledgeAssertionRelation is one materialized typed relation.
type KnowledgeAssertionRelation struct {
	RowID             string
	ID                string
	LibraryID         string
	DocumentID        string
	DocumentVersionID string
	FromKind          string
	FromID            string
	Predicate         string
	ToKind            string
	ToID              string
	ToResolved        bool
	ToRaw             string
	Perspective       string
	Basis             string
	Condition         string
	EvidenceJSON      string
	Ordinal           int
	ContentDigest     string
	CreatedAt         time.Time
}

// KnowledgeRelease is one published, queryable knowledge version.
type KnowledgeRelease struct {
	ID               string
	LibraryID        string
	Seq              int
	SnapshotID       string
	TaskID           string
	ParentReleaseID  string
	ProjectionDigest string
	// DocumentCount/AssertionCount/RelationCount/EvidenceCount describe what
	// the release contains, inherited documents included.
	DocumentCount  int
	AssertionCount int
	RelationCount  int
	EvidenceCount  int
	// WrittenDocumentCount/WrittenAssertionCount/WrittenRelationCount describe
	// what this publish actually wrote. An incremental release carries most of
	// its documents forward, so the two pairs are different numbers and are
	// reported separately instead of being conflated.
	WrittenDocumentCount  int
	WrittenAssertionCount int
	WrittenRelationCount  int
	CoverageJSON          string
	Notes                 string
	Status                string
	PublishedAt           time.Time
}

// KnowledgeReleaseDocument pins one document version into a release.
type KnowledgeReleaseDocument struct {
	ReleaseID         string
	DocumentID        string
	DocumentVersionID string
	IsRemoved         bool
}

// KnowledgeBridgeView is a derived cross-service chain.
type KnowledgeBridgeView struct {
	ID               string
	LibraryID        string
	ReleaseID        string
	BaseReleaseID    string
	AnchorEntityID   string
	Title            string
	Summary          string
	StepsJSON        string
	ParticipantsJSON string
	CoverageJSON     string
	GapsJSON         string
	BuilderVersion   string
	ContentDigest    string
	CreatedAt        time.Time
}

// ── Events and the FIFO write queue ────────────────────────────────────

// KnowledgeEventStatus is the external processing state of an event.
type KnowledgeEventStatus string

const (
	KnowledgeEventAccepted   KnowledgeEventStatus = "accepted"
	KnowledgeEventQueued     KnowledgeEventStatus = "queued"
	KnowledgeEventProcessing KnowledgeEventStatus = "processing"
	KnowledgeEventCompleted  KnowledgeEventStatus = "completed"
	KnowledgeEventFailed     KnowledgeEventStatus = "failed"
	KnowledgeEventBlocked    KnowledgeEventStatus = "blocked"
)

// KnowledgeLibraryEvent is an immutable external report plus its processing
// state. A receipt means "durably accepted", never "knowledge updated".
type KnowledgeLibraryEvent struct {
	ID              string
	LibraryID       string
	ProtocolVersion string
	EventType       string
	Source          string
	SubjectJSON     string
	ContentRef      string
	PayloadJSON     string
	ClientKey       string
	RequestDigest   string
	Status          KnowledgeEventStatus
	TaskID          string
	ReceivedAt      time.Time
	UpdatedAt       time.Time
}

// KnowledgeTaskStatus is the state of one FIFO write task.
type KnowledgeTaskStatus string

const (
	KnowledgeTaskQueued        KnowledgeTaskStatus = "queued"
	KnowledgeTaskRunning       KnowledgeTaskStatus = "running"
	KnowledgeTaskAwaitingAgent KnowledgeTaskStatus = "awaiting_agent"
	KnowledgeTaskRetryWait     KnowledgeTaskStatus = "retry_wait"
	KnowledgeTaskBlocked       KnowledgeTaskStatus = "blocked"
	KnowledgeTaskCompleted     KnowledgeTaskStatus = "completed"
	KnowledgeTaskFailed        KnowledgeTaskStatus = "failed"
	KnowledgeTaskCancelled     KnowledgeTaskStatus = "cancelled"
)

// IsTerminal reports whether the task releases the queue head.
func (s KnowledgeTaskStatus) IsTerminal() bool {
	return s == KnowledgeTaskCompleted || s == KnowledgeTaskFailed || s == KnowledgeTaskCancelled
}

// OccupiesHead reports whether the task still holds the single-writer slot.
func (s KnowledgeTaskStatus) OccupiesHead() bool {
	return !s.IsTerminal()
}

// KnowledgeWriteTask is one complete unit of knowledge writing. All
// persistent knowledge changes are tasks in this one queue.
type KnowledgeWriteTask struct {
	ID        string
	LibraryID string
	Seq       int
	Kind      string
	// ClientKey is the caller's idempotency key for a queue request that has no
	// external event behind it (an administrator-requested reindex). An event
	// task carries the event's own key instead.
	ClientKey string
	EventID   string
	// RequirementInputID is the frozen requirement document this task must
	// document. The text is read from the frozen copy, never from content_ref.
	RequirementInputID string
	Status             KnowledgeTaskStatus
	BaseReleaseID      string
	TargetReleaseID    string
	SnapshotID         string
	ViewID             string
	FocusJSON          string
	PlanJSON           string
	CoverageJSON       string
	StagingPath        string
	WorkItemID         string
	CurrentRunID       string
	Attempt            int
	RepairAttempt      int
	MaxAttempts        int
	MaxRepairAttempts  int
	TurnSeq            int
	NextAttemptAt      *time.Time
	OwnerToken         string
	LastError          string
	BlockedReason      string
	DiagnosticsJSON    string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	FinishedAt         *time.Time
}

// KnowledgeTaskTurn is one model turn belonging to a task.
type KnowledgeTaskTurn struct {
	ID           string
	TaskID       string
	TurnSeq      int
	RunID        string
	Purpose      string
	Status       string
	RequestJSON  string
	ResultJSON   string
	ErrorMessage string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// KnowledgePublication is the publish journal entry for one task.
type KnowledgePublication struct {
	ID               string
	LibraryID        string
	TaskID           string
	ReleaseID        string
	Status           string
	ProjectionDigest string
	PreparedAt       time.Time
	CommittedAt      *time.Time
}

// ── Read models ────────────────────────────────────────────────────────

// KnowledgeQueryRequest is one release-pinned query.
type KnowledgeQueryRequest struct {
	WorkspaceID string
	LibraryID   string
	ReleaseID   string
	Question    string
	Terms       []string
	Limit       int
}

// KnowledgeQueryHit is one assertion matched by a query.
type KnowledgeQueryHit struct {
	Assertion KnowledgeAssertion       `json:"assertion"`
	Document  KnowledgeDocument        `json:"document"`
	Version   KnowledgeDocumentVersion `json:"version"`
	Score     float64                  `json:"score"`
	Snippet   string                   `json:"snippet"`
	Evidence  []KnowledgeEvidence      `json:"evidence"`
}

// KnowledgeCoverage reports what a result set did and did not cover.
//
// Truncated is about this search: the hit budget ran out. Status is about the
// knowledge behind the answer: whether the pinned release reports gaps and
// whether the matched statements carry evidence at all. They are separate
// facts and must not be collapsed into one "complete" badge.
type KnowledgeCoverage struct {
	Status          string   `json:"status"`
	Truncated       bool     `json:"truncated"`
	ScannedVersions int      `json:"scanned_versions"`
	Gaps            int      `json:"gap_count"`
	EvidenceMissing int      `json:"evidence_missing"`
	Unknowns        int      `json:"unknown_count"`
	Notes           []string `json:"notes"`
}

// KnowledgeFreshness reports version and staleness for a query answer.
type KnowledgeFreshness struct {
	ReleaseID             string     `json:"release_id"`
	PublishedAt           *time.Time `json:"published_at"`
	PendingEvents         int        `json:"pending_events"`
	NewerReleaseAvailable bool       `json:"newer_release_available"`
	StaleSources          []string   `json:"stale_sources"`
}

// KnowledgeQueryResult is a complete, bounded answer.
type KnowledgeQueryResult struct {
	Release     *KnowledgeRelease
	Hits        []KnowledgeQueryHit
	Coverage    KnowledgeCoverage
	Freshness   KnowledgeFreshness
	Unknowns    []string
	Expandables []KnowledgeExpandHandle
}

// KnowledgeExpandHandle is a reference the caller can expand later.
// The JSON tags are the wire contract: the client sends kind/id straight back
// to the expand endpoint, so a missing tag makes every handle unusable.
type KnowledgeExpandHandle struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Label string `json:"label"`
}

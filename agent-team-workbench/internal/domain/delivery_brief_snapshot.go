package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gowebpki/jcs"
)

// DeliveryBriefSnapshot is an immutable, evidence-grade capture of the
// deterministic Delivery Brief read model. The JSON payload is deliberately
// kept as canonical text: callers cannot silently re-marshal it with a
// different numeric or time representation between capture and validation.
//
// Snapshot freshness is metadata rather than a substitute for the payload
// digest. A snapshot with partial freshness may be retained for audit, but it
// can never satisfy a passed governance evidence item.
type DeliveryBriefSnapshot struct {
	ID              string           `json:"id"`
	SchemaVersion   string           `json:"schema_version"`
	GoalID          string           `json:"goal_id"`
	TodoID          string           `json:"todo_id"`
	WorkItemID      string           `json:"work_item_id"`
	SnapshotJSON    string           `json:"snapshot_json"`
	CanonicalDigest string           `json:"canonical_digest"`
	AsOfEventSeq    int64            `json:"as_of_event_seq"`
	SourceVersions  map[string]int64 `json:"source_versions"`
	FreshnessState  string           `json:"freshness_state"`
	CreatedAt       time.Time        `json:"created_at"`
	ClientKey       string           `json:"client_key,omitempty"`
}

// DeliveryBriefSnapshotSchemaVersion identifies the closed canonical DTO
// shape. It is part of the digest so a future payload change cannot silently
// reuse a v1 evidence record.
const DeliveryBriefSnapshotSchemaVersion = "delivery-brief-snapshot/v1"

// Validate checks both the immutable record shape and the canonical payload.
// It does not check Goal/Todo/tree ownership; that is an application/storage
// boundary because it requires loading the authoritative aggregates.
func (s *DeliveryBriefSnapshot) Validate() error {
	if s == nil {
		return fmt.Errorf("%w: nil delivery brief snapshot", ErrValidation)
	}
	if err := validateTypedID("delivery_brief_snapshot.id", s.ID, PrefixDeliveryBriefSnapshot); err != nil {
		return err
	}
	if s.SchemaVersion != DeliveryBriefSnapshotSchemaVersion {
		return fmt.Errorf("%w: delivery_brief_snapshot.schema_version %q", ErrValidation, s.SchemaVersion)
	}
	if err := validateTypedID("delivery_brief_snapshot.goal_id", s.GoalID, PrefixGoal); err != nil {
		return err
	}
	if err := validateTypedID("delivery_brief_snapshot.todo_id", s.TodoID, PrefixTodo); err != nil {
		return err
	}
	if err := validateTypedID("delivery_brief_snapshot.work_item_id", s.WorkItemID, PrefixWorkItem); err != nil {
		return err
	}
	if s.AsOfEventSeq < 0 {
		return fmt.Errorf("%w: delivery_brief_snapshot.as_of_event_seq must be >= 0", ErrValidation)
	}
	if s.FreshnessState != "current" && s.FreshnessState != "partial" {
		return fmt.Errorf("%w: delivery_brief_snapshot.freshness_state %q", ErrValidation, s.FreshnessState)
	}
	if s.SourceVersions == nil {
		return fmt.Errorf("%w: delivery_brief_snapshot.source_versions is required", ErrValidation)
	}
	for key, value := range s.SourceVersions {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("%w: delivery_brief_snapshot.source_versions has an empty key", ErrValidation)
		}
		if value < 0 {
			return fmt.Errorf("%w: delivery_brief_snapshot.source_versions[%q] must be >= 0", ErrValidation, key)
		}
	}
	if s.CreatedAt.IsZero() {
		return fmt.Errorf("%w: delivery_brief_snapshot.created_at is required", ErrValidation)
	}
	if s.ClientKey != "" {
		if strings.TrimSpace(s.ClientKey) != s.ClientKey || len(s.ClientKey) > 256 {
			return fmt.Errorf("%w: delivery_brief_snapshot.client_key must be 1..256 trimmed bytes", ErrValidation)
		}
	}
	canonical, err := canonicalDeliveryBriefSnapshotJSON(s.SnapshotJSON)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, []byte(s.SnapshotJSON)) {
		return fmt.Errorf("%w: delivery_brief_snapshot.snapshot_json must be canonical JSON", ErrValidation)
	}
	if !ValidCanonicalDigest(s.CanonicalDigest) {
		return fmt.Errorf("%w: delivery_brief_snapshot.canonical_digest must be a canonical sha256 digest", ErrValidation)
	}
	want, err := ComputeDeliveryBriefSnapshotDigest(s)
	if err != nil {
		return err
	}
	if want != s.CanonicalDigest {
		return fmt.Errorf("%w: delivery_brief_snapshot.canonical_digest does not match immutable content", ErrValidation)
	}
	return nil
}

// Seal canonicalizes the payload and fills a missing digest. A supplied
// digest is verified rather than replaced, so a caller cannot hide tampering
// by re-sealing a changed snapshot.
func (s *DeliveryBriefSnapshot) Seal() error {
	if s == nil {
		return fmt.Errorf("%w: nil delivery brief snapshot", ErrValidation)
	}
	canonical, err := canonicalDeliveryBriefSnapshotJSON(s.SnapshotJSON)
	if err != nil {
		return err
	}
	s.SnapshotJSON = string(canonical)
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	} else {
		s.CreatedAt = s.CreatedAt.UTC()
	}
	want, err := ComputeDeliveryBriefSnapshotDigest(s)
	if err != nil {
		return err
	}
	if s.CanonicalDigest != "" && s.CanonicalDigest != want {
		return fmt.Errorf("%w: delivery_brief_snapshot.canonical_digest does not match immutable content", ErrValidation)
	}
	s.CanonicalDigest = want
	return s.Validate()
}

// ComputeDeliveryBriefSnapshotDigest hashes the canonical closed payload plus
// its governance identity and read watermarks. Including the watermarks makes
// metadata tampering detectable even before the finish gate compares current
// authoritative state.
func ComputeDeliveryBriefSnapshotDigest(s *DeliveryBriefSnapshot) (string, error) {
	if s == nil {
		return "", fmt.Errorf("%w: nil delivery brief snapshot", ErrValidation)
	}
	canonical, err := canonicalDeliveryBriefSnapshotJSON(s.SnapshotJSON)
	if err != nil {
		return "", err
	}
	payload := struct {
		SchemaVersion  string           `json:"schema_version"`
		GoalID         string           `json:"goal_id"`
		TodoID         string           `json:"todo_id"`
		WorkItemID     string           `json:"work_item_id"`
		Snapshot       json.RawMessage  `json:"snapshot"`
		AsOfEventSeq   int64            `json:"as_of_event_seq"`
		SourceVersions map[string]int64 `json:"source_versions"`
		FreshnessState string           `json:"freshness_state"`
	}{
		SchemaVersion: s.SchemaVersion, GoalID: s.GoalID, TodoID: s.TodoID, WorkItemID: s.WorkItemID,
		Snapshot: canonical, AsOfEventSeq: s.AsOfEventSeq,
		SourceVersions: s.SourceVersions, FreshnessState: s.FreshnessState,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("%w: marshal delivery brief snapshot digest: %v", ErrValidation, err)
	}
	canonicalDigestPayload, err := jcs.Transform(raw)
	if err != nil {
		return "", fmt.Errorf("%w: canonicalize delivery brief snapshot digest: %v", ErrValidation, err)
	}
	sum := sha256.Sum256(canonicalDigestPayload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// VerifyDeliveryBriefSnapshotDigest requires a sealed, untampered snapshot.
func VerifyDeliveryBriefSnapshotDigest(s *DeliveryBriefSnapshot) error {
	if s == nil {
		return fmt.Errorf("%w: nil delivery brief snapshot", ErrValidation)
	}
	return s.Validate()
}

func canonicalDeliveryBriefSnapshotJSON(raw string) ([]byte, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("%w: delivery_brief_snapshot.snapshot_json is required", ErrValidation)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		return nil, fmt.Errorf("%w: delivery_brief_snapshot.snapshot_json must be a JSON object: %v", ErrValidation, err)
	}
	if object == nil {
		return nil, fmt.Errorf("%w: delivery_brief_snapshot.snapshot_json must be a JSON object", ErrValidation)
	}
	// v1 is intentionally closed. The capture service constructs exactly this
	// set of public read-model sections; accepting arbitrary top-level keys
	// would let a future caller smuggle a second, unaudited source into an
	// evidence record without a schema/version decision.
	required := []string{
		"work_item", "acceptance_criteria", "conclusion", "attempts", "runs",
		"changes", "artifacts", "blocker", "risks", "comments", "freshness", "truncation",
	}
	for _, key := range required {
		if _, found := object[key]; !found {
			return nil, fmt.Errorf("%w: delivery_brief_snapshot.snapshot_json missing %q", ErrValidation, key)
		}
	}
	allowed := make(map[string]struct{}, len(required))
	for _, key := range required {
		allowed[key] = struct{}{}
	}
	for key := range object {
		if _, found := allowed[key]; !found {
			return nil, fmt.Errorf("%w: delivery_brief_snapshot.snapshot_json has unknown field %q", ErrValidation, key)
		}
	}
	// generated_at is wall-clock metadata and would make two captures of the
	// same source differ without changing any authoritative fact. It belongs on
	// the live read response, never in evidence content.
	if _, found := object["generated_at"]; found {
		return nil, fmt.Errorf("%w: delivery_brief_snapshot.snapshot_json cannot contain generated_at", ErrValidation)
	}
	if freshness, found := object["freshness"]; found {
		var freshnessObject map[string]json.RawMessage
		if err := json.Unmarshal(freshness, &freshnessObject); err != nil || freshnessObject == nil {
			return nil, fmt.Errorf("%w: delivery_brief_snapshot.snapshot_json freshness must be an object", ErrValidation)
		}
		if _, generated := freshnessObject["generated_at"]; generated {
			return nil, fmt.Errorf("%w: delivery_brief_snapshot.snapshot_json freshness cannot contain generated_at", ErrValidation)
		}
	}
	canonical, err := jcs.Transform([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: canonicalize delivery brief snapshot JSON: %v", ErrValidation, err)
	}
	return canonical, nil
}

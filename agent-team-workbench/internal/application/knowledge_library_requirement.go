package application

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
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/knowledgelib"
)

// maxRequirementBytes bounds one imported requirement document. An oversized
// input is refused outright: storing a prefix and digesting that prefix would
// let a truncated file masquerade as the accepted requirement.
const maxRequirementBytes = 1024 * 1024

// errRequirementRefRefused marks a content_ref the library will not accept at
// all: outside the authorized root, a non-local scheme, a directory, or too
// large. Such an event is refused instead of being queued with a missing body,
// because the caller asked to import something the library cannot take.
var errRequirementRefRefused = errors.New("requirement content_ref refused")

// requirementTextKeys are the payload fields an external system may use to
// deliver the requirement body, in the order they are preferred.
var requirementTextKeys = []string{
	"text", "requirement_text", "content", "body", "statement", "description", "summary",
}

// requirementMetaKeys are scalar payload fields worth showing the agent
// verbatim. They are declared metadata, never evidence of implementation.
var requirementMetaKeys = []string{
	"acceptance", "acceptance_criteria", "owner", "priority", "status", "tags",
	"source_url", "effective_at", "reason", "note", "notes",
}

// requirementDraft is the accepted requirement before it is stored: the exact
// bytes, their digest, and the identity the caller declared.
type requirementDraft struct {
	RequirementID string
	Version       string
	Title         string
	ContentRef    string
	Text          string
	Digest        string
	PayloadJSON   string
	Extra         map[string]string
	// Origin records where Text came from. An inline payload copy is a
	// different input from the referenced document and is labelled as such.
	Origin string
	// ReferenceError is the reason a declared reference could not be read. It
	// is kept even when an inline copy was accepted.
	ReferenceError string
	// Unresolved explains why no text could be accepted. A task without text
	// documents the gap instead of guessing a requirement.
	Unresolved string
}

// prepareRequirementDraft reads the declared requirement text and freezes its
// identity, without touching the database or the library files.
//
// The referenced path is resolved against the workspace root with the same
// authorization rule registered sources use, so an import cannot read
// arbitrary local files, and a symlink that escapes the root is refused rather
// than followed. Reading happens here, at acceptance: two queued requirements
// that name the same path must not both see whatever the path holds when they
// reach the head.
func (s *Service) prepareRequirementDraft(ctx context.Context, workspaceID string, event *domain.KnowledgeLibraryEvent) (*requirementDraft, error) {
	payload := decodeJSONObject(event.PayloadJSON)
	draft := &requirementDraft{
		RequirementID: firstString(payload, "requirement_id", "req_id", "requirement", "id"),
		Version:       firstString(payload, "version", "requirement_version", "revision"),
		Title:         firstString(payload, "title", "name", "summary"),
		ContentRef:    strings.TrimSpace(event.ContentRef),
		PayloadJSON:   event.PayloadJSON,
		Extra:         map[string]string{},
	}
	if draft.RequirementID == "" {
		draft.RequirementID = strings.TrimSpace(subjectString(event.SubjectJSON, "requirement_id"))
	}
	for _, key := range requirementMetaKeys {
		if v := firstString(payload, key); v != "" {
			draft.Extra[key] = v
		}
	}
	if len(draft.Extra) == 0 {
		draft.Extra = nil
	}

	inline := firstString(payload, requirementTextKeys...)
	text := inline
	draft.Origin = domain.RequirementOriginInlinePayload
	// A referenced document is the authority when both are present, because a
	// payload body is often only a summary of it.
	if draft.ContentRef != "" {
		raw, err := s.readAuthorizedContentRef(ctx, workspaceID, draft.ContentRef)
		switch {
		case errors.Is(err, errRequirementRefRefused):
			return nil, err
		case err != nil:
			// The reference was declared and could not be read. The failure is
			// recorded either way, so an inline copy can never pass as the
			// referenced original.
			draft.ReferenceError = err.Error()
			draft.Unresolved = err.Error()
		case strings.TrimSpace(raw) == "":
			draft.ReferenceError = fmt.Sprintf("引用 %s 的内容为空", draft.ContentRef)
			draft.Unresolved = draft.ReferenceError
		default:
			text = raw
			draft.Origin = domain.RequirementOriginContentRef
		}
	}
	if strings.TrimSpace(text) == "" {
		if draft.Unresolved == "" {
			draft.Unresolved = "事件既没有可直接读取的 content_ref，也没有正文 payload"
		}
		return draft, nil
	}
	// The accepted bytes are frozen exactly as delivered: trimming before
	// digesting would make the digest describe a text nobody submitted.
	if len(text) > maxRequirementBytes {
		return nil, fmt.Errorf("%w: 需求正文 %d 字节，超过单份需求上限 %d 字节；请拆分需求或登记为来源仓库文档",
			domain.ErrValidation, len(text), maxRequirementBytes)
	}
	sum := sha256.Sum256([]byte(text))
	draft.Digest = hex.EncodeToString(sum[:])
	draft.Text = text
	return draft, nil
}

// readAuthorizedContentRef reads one requirement document inside the
// workspace's authorized root.
func (s *Service) readAuthorizedContentRef(ctx context.Context, workspaceID, ref string) (string, error) {
	path := strings.TrimSpace(ref)
	switch {
	case strings.HasPrefix(path, "file://"):
		path = strings.TrimPrefix(path, "file://")
	case strings.Contains(path, "://"):
		scheme := path[:strings.Index(path, "://")]
		return "", fmt.Errorf("%w: content_ref 使用 %s 协议，本版本只能读取工作区内的本地文件",
			errRequirementRefRefused, scheme)
	}
	root, err := s.workspaceRoot(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	resolved, err := resolveSourcePath(root, path)
	if err != nil {
		return "", fmt.Errorf("%w: content_ref %s 不在工作区授权范围内：%v",
			errRequirementRefRefused, ref, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("content_ref %s 不可读：%v", ref, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%w: content_ref %s 是目录，不是需求文档", errRequirementRefRefused, ref)
	}
	if info.Size() > maxRequirementBytes {
		return "", fmt.Errorf("%w: content_ref %s 有 %d 字节，超过单份需求上限 %d 字节",
			errRequirementRefRefused, ref, info.Size(), maxRequirementBytes)
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("content_ref %s 不可读：%v", ref, err)
	}
	return string(raw), nil
}

// freezeRequirementInput stores the accepted bytes and registers the input so
// the librarian can cite the exact text that was accepted.
func (s *Service) freezeRequirementInput(ctx context.Context, lib *domain.KnowledgeLibrary, kl knowledgelib.Library, eventID string, draft *requirementDraft) (*domain.KnowledgeRequirementInput, error) {
	requirementID := strings.TrimSpace(draft.RequirementID)
	if requirementID == "" {
		// A requirement without a declared identity is still a requirement:
		// the event ID keeps its text addressable and traceable.
		requirementID = "requirement:" + eventID
	}
	label := strings.TrimSpace(draft.Version)
	if label == "" {
		label = "v1"
	}
	origin := draft.Origin
	if origin == "" {
		origin = domain.RequirementOriginContentRef
	}
	stored, err := kl.StoreRequirementInput(requirementID, label, draft.Digest, []byte(draft.Text))
	if err != nil {
		return nil, err
	}
	source, err := s.ensureRequirementSource(ctx, lib, kl, requirementID, origin)
	if err != nil {
		return nil, err
	}
	input := &domain.KnowledgeRequirementInput{
		ID: domain.NewID("kreq_"), LibraryID: lib.ID, EventID: eventID, SourceID: source.ID,
		RequirementID: requirementID, RequirementVersion: label, Title: draft.Title,
		ContentRef: draft.ContentRef, StoredPath: stored, ContentDigest: "sha256:" + draft.Digest,
		ByteSize: len(draft.Text), PayloadJSON: draft.PayloadJSON, InputOrigin: origin,
		ReferenceError: draft.ReferenceError, CreatedAt: time.Now().UTC(),
	}
	if err := s.store.Library().CreateRequirementInput(ctx, input); err != nil {
		return nil, err
	}
	return input, nil
}

// ensureRequirementSource keeps one source row per imported requirement, so
// evidence collected from its frozen text has a binding that the ledger, the
// brief and the admin page can all name.
func (s *Service) ensureRequirementSource(ctx context.Context, lib *domain.KnowledgeLibrary, kl knowledgelib.Library, requirementID, origin string) (*domain.KnowledgeSource, error) {
	// An inline payload copy is a separate source from the referenced file:
	// evidence collected from it must not claim to quote the original document.
	name := "requirement:" + requirementID
	if origin == domain.RequirementOriginInlinePayload {
		name = "requirement-inline:" + requirementID
	}
	sources, err := s.store.Library().ListSources(ctx, lib.ID)
	if err != nil {
		return nil, err
	}
	for _, src := range sources {
		if src.Name == name {
			return src, nil
		}
	}
	now := time.Now().UTC()
	src := &domain.KnowledgeSource{
		ID: domain.NewID("ksrc_"), LibraryID: lib.ID, Name: name,
		Kind: domain.SourceKindRequirement, RepoPath: kl.RequirementStoreDir(),
		IncludeGlobs: []string{}, ExcludeGlobs: []string{}, Usages: []domain.KnowledgeSourceUsage{},
		Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.Library().CreateSource(ctx, src); err != nil {
		return nil, err
	}
	return src, nil
}

// requirementBinding turns one frozen input into the snapshot binding the
// librarian cites, the collector resolves and the ledger references.
func requirementBinding(snapshotID string, input *domain.KnowledgeRequirementInput, sourceName string) *knowledgelib.Binding {
	dir := filepath.Dir(input.StoredPath)
	ref := input.RequirementVersion
	if ref == "" {
		ref = "v1"
	}
	return &knowledgelib.Binding{
		ID:           "binding:" + knowledgelib.ShortID(snapshotID, input.ID, "requirement"),
		SnapshotID:   snapshotID,
		SourceID:     input.SourceID,
		SourceName:   sourceName,
		SourceKind:   string(domain.SourceKindRequirement),
		RepoPath:     dir,
		TreeDir:      dir,
		GitRef:       input.RequirementID + "@" + ref,
		CommitSHA:    input.ContentDigest,
		ObjectFormat: "sha256",
		RefPinned:    true,
		DirtyPaths:   map[string]bool{},
		CapturedAt:   input.CreatedAt,
	}
}

// requirementFromInput hands the agent the text that was accepted, read from
// the frozen copy rather than from the referenced path, which may have been
// edited since.
func requirementFromInput(input *domain.KnowledgeRequirementInput, staging string) *knowledgelib.Requirement {
	raw, err := os.ReadFile(input.StoredPath)
	if err != nil {
		return &knowledgelib.Requirement{
			RequirementID: input.RequirementID, Title: input.Title, Version: input.RequirementVersion,
			ContentRef: input.ContentRef, Digest: strings.TrimPrefix(input.ContentDigest, "sha256:"),
			Unresolved: "冻结的需求原文不可读：" + err.Error(),
		}
	}
	// The frozen copy is inlined as it was accepted.
	text := string(raw)
	extra := map[string]string{}
	for _, key := range requirementMetaKeys {
		if v := firstString(decodeJSONObject(input.PayloadJSON), key); v != "" {
			extra[key] = v
		}
	}
	if len(extra) == 0 {
		extra = nil
	}
	req := &knowledgelib.Requirement{
		RequirementID: input.RequirementID, Title: input.Title, Version: input.RequirementVersion,
		ContentRef: input.ContentRef, Digest: strings.TrimPrefix(input.ContentDigest, "sha256:"),
		Text: text, Extra: extra, Origin: input.InputOrigin, Unresolved: input.ReferenceError,
	}
	if strings.TrimSpace(staging) != "" {
		req.Path = filepath.Join(staging, knowledgelib.RequirementFileName)
	}
	return req
}

func decodeJSONObject(raw string) map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]any{}
	}
	return out
}

func subjectString(subjectJSON, key string) string {
	if v, ok := decodeJSONObject(subjectJSON)[key].(string); ok {
		return v
	}
	return ""
}

// firstString returns the first non-empty scalar for the given keys, rendered
// the way an operator would expect to read it.
func firstString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		v, ok := object[key]
		if !ok || v == nil {
			continue
		}
		switch typed := v.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				return strings.TrimSpace(typed)
			}
		case bool:
			return fmt.Sprintf("%t", typed)
		case float64:
			return strings.TrimSpace(strings.TrimSuffix(fmt.Sprintf("%f", typed), ".000000"))
		case []any:
			parts := make([]string, 0, len(typed))
			for _, item := range typed {
				if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
					parts = append(parts, strings.TrimSpace(s))
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, "；")
			}
		}
	}
	return ""
}

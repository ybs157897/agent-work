package knowledgelib

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// EntitySpec is an entity the librarian agent declares in staging. Entities
// are explicit so a name in prose can never silently become a stable ID.
type EntitySpec struct {
	ID        string   `yaml:"id" json:"id"`
	Kind      string   `yaml:"kind" json:"kind"`
	Namespace string   `yaml:"namespace" json:"namespace"`
	Name      string   `yaml:"name" json:"name"`
	Aliases   []string `yaml:"aliases" json:"aliases"`
	Source    string   `yaml:"source" json:"source"`
}

// EntityKinds is the closed vocabulary for declared entities.
var EntityKinds = map[string]bool{
	"service": true, "common": true, "repository": true, "module": true,
	"api": true, "event": true, "data": true, "capability": true,
	"symbol": true, "artifact": true, "topic": true, "component": true,
}

// ProjectedDocument is a document ready to be written as an immutable version.
type ProjectedDocument struct {
	DocumentID      string
	Path            string
	Kind            string
	Title           string
	Summary         string
	Domains         []string
	Aliases         []string
	Tags            []string
	ContentMarkdown string
	ContentDigest   string
	FrontmatterJSON string
	Assertions      []ProjectedAssertion
	Relations       []ProjectedRelation
}

// ProjectedAssertion is one materialized assertion row.
type ProjectedAssertion struct {
	RowID         string
	AssertionID   string
	Heading       string
	About         []string
	Perspective   string
	Basis         string
	Statement     string
	Scope         Scope
	Evidence      []EvidenceRef
	UnknownNotes  string
	Ordinal       int
	ContentDigest string
}

// ProjectedRelation is one materialized relation row.
type ProjectedRelation struct {
	RowID         string
	RelationID    string
	FromKind      string
	FromID        string
	Predicate     string
	ToKind        string
	ToID          string
	ToResolved    bool
	ToRaw         string
	Perspective   string
	Basis         string
	Condition     string
	Evidence      []EvidenceRef
	Ordinal       int
	ContentDigest string
}

// Projection is the validated candidate content of one release.
type Projection struct {
	// EvidenceAliases maps each staged evidence key to the canonical evidence
	// ID the collector assigned, so the published version can always resolve
	// the citations its own Markdown was written with.
	EvidenceAliases map[string]string
	Documents       []ProjectedDocument
	Entities        []EntitySpec
	Evidence        []*CollectedEvidence
	Removals        []string
	Renames         []Rename
	Coverage        Coverage
	Digest          string
}

// ProjectionInput is everything the builder needs.
type ProjectionInput struct {
	Staged *Staged
	// KnownEntities are entity IDs already in the library.
	KnownEntities map[string]bool
	// SourceEntities are entity IDs derived from registered sources.
	SourceEntities map[string]bool
	// KnownEvidence are evidence IDs already published.
	KnownEvidence map[string]bool
	// KnownDocumentIDs maps a document ID to its current library path.
	KnownDocumentIDs map[string]string
	// KnownPaths maps a library content path to its document ID.
	KnownPaths map[string]string
	// KnownAssertionIDs are assertion IDs already present in the library.
	KnownAssertionIDs map[string]bool
	Collected         map[string]*CollectedEvidence
	Now               func() time.Time
}

// BuildProjection validates a staging directory and produces the candidate
// release content. Every failure message names the exact object so it can be
// quoted straight back to the model in a repair turn.
func BuildProjection(in ProjectionInput) (*Projection, error) {
	staged := in.Staged
	if staged == nil {
		return nil, errf(ErrValidation, "缺少 staging 产物")
	}
	if len(staged.Documents) == 0 && len(staged.Plan.Removals) == 0 && len(staged.Plan.Renames) == 0 {
		return nil, errf(ErrValidation, "本次任务既没有产出文档，也没有声明删除或重命名；资料 agent 必须说明本轮结果")
	}
	proj := &Projection{
		Removals: append([]string(nil), staged.Plan.Removals...),
		Renames:  append([]Rename(nil), staged.Plan.Renames...),
		Coverage: staged.Plan.Coverage,
	}
	// Evidence: canonical identity is derived by the collector; the staged key
	// is only a local alias and is rewritten before publication.
	for _, req := range staged.Evidence {
		ce, ok := in.Collected[req.Key]
		if !ok {
			return nil, errf(ErrValidation, "证据 %q 未被采集（内部不一致）", req.Key)
		}
		proj.Evidence = append(proj.Evidence, ce)
	}
	sort.Slice(proj.Evidence, func(i, j int) bool { return proj.Evidence[i].ID < proj.Evidence[j].ID })

	entities := map[string]EntitySpec{}
	for _, e := range staged.Entities {
		if err := validateEntitySpec(e); err != nil {
			return nil, err
		}
		if _, dup := entities[e.ID]; dup {
			return nil, errf(ErrValidation, "实体 %q 重复声明", e.ID)
		}
		entities[e.ID] = e
	}
	proj.Entities = make([]EntitySpec, 0, len(entities))
	for _, e := range entities {
		proj.Entities = append(proj.Entities, e)
	}
	sort.Slice(proj.Entities, func(i, j int) bool { return proj.Entities[i].ID < proj.Entities[j].ID })

	problems := &ValidationErrors{}
	knownEntity := func(id string) bool {
		if in.KnownEntities[id] || in.SourceEntities[id] {
			return true
		}
		_, ok := entities[id]
		return ok
	}
	knownDoc := func(id string) bool {
		if _, ok := in.KnownDocumentIDs[id]; ok {
			return true
		}
		for _, d := range staged.Documents {
			if d.Doc.Frontmatter.ID == id {
				return true
			}
		}
		return false
	}

	// A declared rename is the one sanctioned way for a stable document ID to
	// change path.
	declaredRename := map[string]Rename{}
	for _, rn := range staged.Plan.Renames {
		declaredRename[rn.DocumentID] = rn
	}
	seenDocID := map[string]string{}
	seenPath := map[string]string{}
	seenAssertion := map[string]string{}
	seenRelation := map[string]string{}

	for _, sd := range staged.Documents {
		doc := sd.Doc
		docID := doc.Frontmatter.ID
		// Staged paths are relative to the staging content/ zone; the library
		// path is that zone plus the topic layout.
		normalizedPath, err := NormalizeContentPath(ContentZone + "/" + sd.RelPath)
		if err != nil {
			return nil, err
		}
		if other, dup := seenDocID[docID]; dup {
			return nil, errf(ErrValidation, "文档 ID %q 在本次产物中出现多次（%s 与 %s）", docID, other, normalizedPath)
		}
		seenDocID[docID] = normalizedPath
		if other, dup := seenPath[normalizedPath]; dup {
			return nil, errf(ErrValidation, "路径 %q 被两个文档占用（%s 与 %s）", normalizedPath, other, docID)
		}
		seenPath[normalizedPath] = docID
		if owner, exists := in.KnownPaths[normalizedPath]; exists && owner != docID {
			return nil, errf(ErrValidation, "路径 %q 已属于文档 %s，不能改由 %s 占用", normalizedPath, owner, docID)
		}
		if existingPath, exists := in.KnownDocumentIDs[docID]; exists && existingPath != normalizedPath {
			declared, ok := declaredRename[docID]
			if !ok || declared.ToPath != normalizedPath || declared.FromPath != existingPath {
				return nil, errf(ErrValidation,
					"文档 %s 的稳定 ID 未变，路径必须保持 %s（如需移动请在 plan.json 的 renames 中登记 {document_id, from_path: %s, to_path: %s}）",
					docID, existingPath, existingPath, normalizedPath)
			}
		}
		frontJSON, err := json.Marshal(doc.Frontmatter)
		if err != nil {
			return nil, err
		}
		pd := ProjectedDocument{
			DocumentID:      docID,
			Path:            normalizedPath,
			Kind:            doc.Frontmatter.Kind,
			Title:           doc.Frontmatter.Title,
			Summary:         doc.Frontmatter.Summary,
			Domains:         doc.Frontmatter.Domains,
			Aliases:         doc.Frontmatter.Aliases,
			Tags:            doc.Frontmatter.Tags,
			ContentMarkdown: string(Canonicalize(sd.Raw)),
			ContentDigest:   doc.Digest,
			FrontmatterJSON: string(frontJSON),
		}
		for _, a := range doc.Assertions {
			if owner, dup := seenAssertion[a.ID]; dup {
				return nil, errf(ErrValidation, "Assertion ID %q 在 %s 与 %s 中重复", a.ID, owner, normalizedPath)
			}
			seenAssertion[a.ID] = normalizedPath
			assertionOK := true
			for _, ref := range a.About {
				if !knownEntity(ref) {
					problems.Addf("Assertion %s 的 about 引用未声明实体 %q（%s）：请在 entities.yaml 中登记，或改用上表已有的实体 ID",
						a.ID, ref, normalizedPath)
					assertionOK = false
				}
			}
			ev, err := rewriteEvidence(a.ID, a.Evidence, in, proj, normalizedPath)
			if err != nil {
				problems.Add(err)
				assertionOK = false
			}
			if !assertionOK {
				continue
			}
			pa := ProjectedAssertion{
				RowID:        "kba_" + ShortID(docID, a.ID, doc.Digest),
				AssertionID:  a.ID,
				Heading:      a.Heading,
				About:        append([]string(nil), a.About...),
				Perspective:  a.Perspective,
				Basis:        a.Basis,
				Statement:    a.Statement,
				Scope:        a.Scope,
				Evidence:     ev,
				UnknownNotes: a.UnknownNotes,
				Ordinal:      a.Ordinal,
			}
			pa.ContentDigest = DigestBytes([]byte(strings.Join([]string{
				a.ID, a.Perspective, a.Basis, a.Statement, a.UnknownNotes, scopeFingerprint(a.Scope),
			}, "\x00")))
			pd.Assertions = append(pd.Assertions, pa)
		}
		for _, r := range doc.Relations {
			if owner, dup := seenRelation[r.ID]; dup {
				return nil, errf(ErrValidation, "Relation ID %q 在 %s 与 %s 中重复", r.ID, owner, normalizedPath)
			}
			seenRelation[r.ID] = normalizedPath
			ev, err := rewriteEvidence(r.ID, r.Evidence, in, proj, normalizedPath)
			if err != nil {
				problems.Add(err)
				continue
			}
			pr := ProjectedRelation{
				RowID:       "kbr_" + ShortID(docID, r.ID, doc.Digest),
				RelationID:  r.ID,
				FromKind:    NormalizeEndpointKind(r.From.Kind),
				FromID:      r.From.ID,
				Predicate:   r.Predicate,
				ToKind:      NormalizeEndpointKind(r.To.Kind),
				ToID:        r.To.ID,
				ToResolved:  true,
				Perspective: r.Perspective,
				Basis:       r.Basis,
				Condition:   strings.Join(r.Scope.Conditions, "\n"),
				Evidence:    ev,
				Ordinal:     r.Ordinal,
			}
			if err := resolveEndpoint(r.ID, "from", r.From, knownEntity, knownDoc); err != nil {
				problems.Addf("relation %s 的 from 端点 %q 未登记（%s）：实体必须出现在 entities.yaml 或来源实体表中", r.ID, r.From.ID, normalizedPath)
				continue
			}
			if err := resolveEndpoint(r.ID, "to", r.To, knownEntity, knownDoc); err != nil {
				// An unknown target is preserved as unresolved rather than
				// invented; it stays visible as a coverage gap.
				pr.ToResolved = false
				pr.ToRaw = r.To.ID
				pr.ToID = r.To.ID
			}
			pr.ContentDigest = DigestBytes([]byte(strings.Join([]string{
				r.ID, r.From.Kind, r.From.ID, r.Predicate, r.To.Kind, r.To.ID, r.Perspective, r.Basis, pr.Condition,
			}, "\x00")))
			pd.Relations = append(pd.Relations, pr)
		}
		proj.Documents = append(proj.Documents, pd)
	}
	if err := problems.OrNil(); err != nil {
		return nil, err
	}
	if len(proj.Documents) == 0 && len(proj.Removals) == 0 && len(proj.Renames) == 0 {
		return nil, errf(ErrValidation, "解析后没有任何可发布文档")
	}
	for _, rm := range proj.Removals {
		if _, ok := in.KnownDocumentIDs[rm]; !ok {
			return nil, errf(ErrValidation, "plan.json 声明删除的文档 %q 不在当前资料库中", rm)
		}
	}
	for _, rn := range proj.Renames {
		if _, ok := in.KnownDocumentIDs[rn.DocumentID]; !ok {
			return nil, errf(ErrValidation, "plan.json 声明重命名的文档 %q 不在当前资料库中", rn.DocumentID)
		}
		if _, err := NormalizeContentPath(rn.ToPath); err != nil {
			return nil, err
		}
	}
	// Publish the canonical form: the Markdown a reader opens must cite the
	// same evidence IDs the projection and ledger use.
	keyToID := map[string]string{}
	for key, ce := range in.Collected {
		keyToID[key] = ce.ID
	}
	for i := range proj.Documents {
		canonical := CanonicalizeDocumentMarkdown([]byte(proj.Documents[i].ContentMarkdown), keyToID)
		proj.Documents[i].ContentMarkdown = string(canonical)
		proj.Documents[i].ContentDigest = DigestMarkdown(canonical)
	}
	proj.EvidenceAliases = keyToID
	proj.Digest = projectionDigest(proj)
	return proj, nil
}

// rewriteEvidence maps staged keys to canonical evidence IDs and rejects
// references that resolve to nothing.
func rewriteEvidence(owner string, refs []EvidenceRef, in ProjectionInput, proj *Projection, path string) ([]EvidenceRef, error) {
	out := make([]EvidenceRef, 0, len(refs))
	for _, ref := range refs {
		id := ref.EvidenceID
		if ce, ok := in.Collected[id]; ok {
			id = ce.ID
		} else if !in.KnownEvidence[id] {
			return nil, errf(ErrValidation, "记录 %s（%s）引用了未知证据 %q：证据必须在本轮 evidence.yaml 中声明并由程序采集", owner, path, ref.EvidenceID)
		}
		out = append(out, EvidenceRef{EvidenceID: id, Role: ref.Role})
	}
	return out, nil
}

func resolveEndpoint(owner, field string, e Endpoint, knownEntity, knownDoc func(string) bool) error {
	e.Kind = NormalizeEndpointKind(e.Kind)
	switch e.Kind {
	case EndpointEntity:
		if !knownEntity(e.ID) {
			return errf(ErrNotFound, "端点为未知实体 %q", e.ID)
		}
	case EndpointDocument:
		if !knownDoc(e.ID) {
			return errf(ErrNotFound, "端点为未知文档 %q", e.ID)
		}
	case EndpointAssertion:
		if !strings.HasPrefix(e.ID, "assertion:") {
			return errf(ErrValidation, "relation %s 的 %s 断言端点 %q 非法", owner, field, e.ID)
		}
	}
	return nil
}

func validateEntitySpec(e EntitySpec) error {
	if !strings.HasPrefix(e.ID, "entity:") {
		return errf(ErrValidation, "实体 ID %q 必须以 entity: 开头", e.ID)
	}
	if !EntityKinds[e.Kind] {
		return errf(ErrValidation, "实体 %q 的 kind %q 不在闭集内", e.ID, e.Kind)
	}
	if strings.TrimSpace(e.Name) == "" {
		return errf(ErrValidation, "实体 %q 缺少名称", e.ID)
	}
	return nil
}

// NormalizeContentPath validates and cleans a library-relative content path.
func NormalizeContentPath(rel string) (string, error) {
	clean := strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/"))
	clean = strings.TrimPrefix(clean, "./")
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || strings.HasPrefix(clean, "..") || strings.Contains(clean, "../") {
		return "", errf(ErrValidation, "文档路径 %q 非法", rel)
	}
	if !strings.HasSuffix(clean, ".md") {
		return "", errf(ErrValidation, "文档路径 %q 必须是 .md", rel)
	}
	if !strings.HasPrefix(clean, "content/") {
		return "", errf(ErrValidation, "文档路径 %q 必须位于 content/ 之下", rel)
	}
	return clean, nil
}

func scopeFingerprint(s Scope) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// projectionDigest binds the release to its exact content projection.
func projectionDigest(p *Projection) string {
	parts := []string{}
	for _, d := range p.Documents {
		parts = append(parts, d.DocumentID, d.Path, d.ContentDigest)
		for _, a := range d.Assertions {
			parts = append(parts, a.AssertionID, a.ContentDigest)
		}
		for _, r := range d.Relations {
			parts = append(parts, r.RelationID, r.ContentDigest)
		}
	}
	for _, e := range p.Evidence {
		parts = append(parts, e.ID, e.Representation.ContentDigest, e.LocatorJSON)
	}
	sort.Strings(parts)
	return digestParts("sha256:", parts...)
}

// EntityIDFromSource is the deterministic entity identity of a registered
// source, so a service always resolves to the same stable ID.
func EntityIDFromSource(kind, name string) string {
	k := "service"
	switch kind {
	case "common":
		k = "common"
	case "documents":
		k = "component"
	}
	return fmt.Sprintf("entity:%s:%s", k, name)
}

// CollectSourceEntities derives the entity set implied by the source ledger.
func CollectSourceEntities(sources []SourceSpec) map[string]bool {
	out := map[string]bool{}
	for _, s := range sources {
		out[EntityIDFromSource(s.Kind, s.Name)] = true
	}
	return out
}

// CollectSourceFiles is a helper for the brief: the bounded file list the
// agent should consider for one binding.
func CollectSourceFiles(ctx context.Context, runner GitRunner, b *Binding, spec SourceSpec, limit int) ([]string, error) {
	files, err := ListBindingFiles(ctx, runner, b, spec)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(files) > limit {
		files = files[:limit]
	}
	return files, nil
}

// CanonicalizeDocumentMarkdown rewrites the staged evidence keys inside a
// document's record metadata to the canonical evidence IDs the collector
// assigned. The published Markdown must name the same evidence the projection
// and the ledger do, or reading the Markdown could not resolve its own
// citations. Only `evidence_id` values inside fenced YAML blocks are touched.
func CanonicalizeDocumentMarkdown(raw []byte, keyToID map[string]string) []byte {
	if len(keyToID) == 0 {
		return raw
	}
	text := string(Canonicalize(raw))
	lines := strings.Split(text, "\n")
	inFence := false
	fenceMarker := ""
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inFence {
			if m := fenceOpen(trimmed); m != "" {
				inFence, fenceMarker = true, m
				continue
			}
			continue
		}
		if strings.HasPrefix(trimmed, fenceMarker) {
			inFence, fenceMarker = false, ""
			continue
		}
		idx := strings.Index(line, "evidence_id:")
		if idx < 0 {
			continue
		}
		rest := line[idx+len("evidence_id:"):]
		value := strings.TrimSpace(rest)
		if value == "" {
			continue
		}
		if canonical, ok := keyToID[value]; ok {
			replaced := line[:idx+len("evidence_id:")]
			if strings.HasSuffix(rest, " ") {
				replaced += " " + canonical + " "
			} else {
				replaced += " " + canonical
			}
			lines[i] = replaced
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

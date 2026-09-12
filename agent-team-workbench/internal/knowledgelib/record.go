// Package knowledgelib implements the unified project knowledge library:
// Markdown topic documents carrying stable-ID Assertion and Relation blocks,
// an immutable source/evidence ledger, release-pinned queries, and a single
// FIFO write queue.
//
// Markdown under the library root is the writing truth for knowledge. The
// SQLite rows are a projection that can be rebuilt from the published
// documents plus the source ledger, so the library is readable and portable
// without this program.
package knowledgelib

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// RecordSyntaxVersion is the Markdown record syntax this parser accepts.
const RecordSyntaxVersion = "kb-note/0.2-draft"

// Perspective and basis vocabularies from the confirmed record convention.
const (
	PerspectiveNormative   = "normative"
	PerspectiveDescriptive = "descriptive"

	BasisSourceStatement = "source_statement"
	BasisCodeStatic      = "code_static"
	BasisRuntimeObserved = "runtime_observed"
	BasisInferred        = "inferred"

	EvidenceRoleSupports    = "supports"
	EvidenceRoleContradicts = "contradicts"
	EvidenceRoleContext     = "context"

	BlockKindAssertion = "assertion"
	BlockKindRelation  = "relation"

	EndpointEntity    = "entity"
	EndpointDocument  = "document"
	EndpointAssertion = "assertion"
)

// RelationPredicates is the controlled vocabulary for relation blocks.
var RelationPredicates = map[string]bool{
	"calls": true, "depends_on": true, "publishes": true, "consumes": true,
	"reads": true, "writes": true, "implements": true, "contains": true,
	"deviates_from": true, "contradicts": true, "supersedes": true,
	"impacts": true, "triggers": true, "shares_data": true,
}

// DocumentFrontmatter is the lightweight catalog header. It deliberately
// carries no release, review or confirmation state: those live in the ledger
// so a document cannot declare itself approved.
type DocumentFrontmatter struct {
	SchemaVersion string   `yaml:"schema_version" json:"schema_version"`
	ID            string   `yaml:"id" json:"id"`
	Kind          string   `yaml:"kind" json:"kind"`
	Title         string   `yaml:"title" json:"title"`
	Summary       string   `yaml:"summary" json:"summary"`
	About         []string `yaml:"about" json:"about"`
	Domains       []string `yaml:"domains" json:"domains"`
	Aliases       []string `yaml:"aliases" json:"aliases"`
	Tags          []string `yaml:"tags" json:"tags"`
}

// Scope records the applicability conditions of one record. An empty
// condition list means "not recorded", never "applies unconditionally".
// The JSON tags matter: the record is re-serialized into the projection, so a
// missing tag would publish Go field names to every client.
type Scope struct {
	Conditions   []string `yaml:"conditions" json:"conditions"`
	Environments []string `yaml:"environments" json:"environments"`
	ValidFrom    string   `yaml:"valid_from,omitempty" json:"valid_from,omitempty"`
	ValidUntil   string   `yaml:"valid_until,omitempty" json:"valid_until,omitempty"`
}

// EvidenceRef points at collector-created evidence. The record only names the
// evidence and its role; it never carries a digest or an approval flag.
type EvidenceRef struct {
	EvidenceID string `yaml:"evidence_id" json:"evidence_id"`
	Role       string `yaml:"role" json:"role"`
}

// Endpoint is a typed relation endpoint.
type Endpoint struct {
	Kind string `yaml:"kind" json:"kind"`
	ID   string `yaml:"id" json:"id"`
}

// Assertion is one independently checkable statement.
type Assertion struct {
	Kind        string        `yaml:"kind"`
	ID          string        `yaml:"id"`
	About       []string      `yaml:"about"`
	Perspective string        `yaml:"perspective"`
	Basis       string        `yaml:"basis"`
	Scope       Scope         `yaml:"scope"`
	Evidence    []EvidenceRef `yaml:"evidence"`

	Heading      string `yaml:"-"`
	Statement    string `yaml:"-"`
	UnknownNotes string `yaml:"-"`
	Ordinal      int    `yaml:"-"`
}

// Relation is one typed, evidenced connection between two endpoints.
type Relation struct {
	Kind        string        `yaml:"kind"`
	ID          string        `yaml:"id"`
	From        Endpoint      `yaml:"from"`
	Predicate   string        `yaml:"predicate"`
	To          Endpoint      `yaml:"to"`
	Perspective string        `yaml:"perspective"`
	Basis       string        `yaml:"basis"`
	Scope       Scope         `yaml:"scope"`
	Evidence    []EvidenceRef `yaml:"evidence"`

	Heading   string `yaml:"-"`
	Statement string `yaml:"-"`
	Ordinal   int    `yaml:"-"`
}

// Document is one parsed topic document.
type Document struct {
	Frontmatter DocumentFrontmatter
	Body        string
	Assertions  []Assertion
	Relations   []Relation
	// Digest is the SHA-256 over the exact markdown bytes the library stores.
	Digest string
}

// ParseError carries the precise reason a document was rejected, so a repair
// turn can quote it back to the model.
type ParseError struct {
	Path   string
	Line   int
	Reason string
}

func (e *ParseError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", e.Path, e.Line, e.Reason)
	}
	return fmt.Sprintf("%s: %s", e.Path, e.Reason)
}

func parseErrf(path string, line int, format string, args ...any) error {
	return &ParseError{Path: path, Line: line, Reason: fmt.Sprintf(format, args...)}
}

// DigestMarkdown returns the canonical content digest of a document.
func DigestMarkdown(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ParseDocument parses one Markdown topic document. Unknown YAML keys,
// duplicated record IDs, a missing statement section and a model-supplied
// statement in the block metadata are all hard errors.
func ParseDocument(path string, content []byte) (*Document, error) {
	front, body, err := splitFrontmatter(path, content)
	if err != nil {
		return nil, err
	}
	doc := &Document{Frontmatter: front, Body: body, Digest: DigestMarkdown(content)}
	if err := validateFrontmatter(path, front); err != nil {
		return nil, err
	}
	blocks, err := scanBlocks(path, body)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for i := range blocks {
		b := blocks[i]
		var id string
		switch b.kind {
		case BlockKindAssertion:
			a, err := parseAssertionBlock(path, b)
			if err != nil {
				return nil, err
			}
			id = a.ID
			a.Ordinal = len(doc.Assertions)
			doc.Assertions = append(doc.Assertions, *a)
		case BlockKindRelation:
			r, err := parseRelationBlock(path, b)
			if err != nil {
				return nil, err
			}
			id = r.ID
			r.Ordinal = len(doc.Relations)
			doc.Relations = append(doc.Relations, *r)
		default:
			return nil, parseErrf(path, b.headingLine, "块 %q 的 kind 必须是 assertion 或 relation", b.kind)
		}
		if seen[id] {
			return nil, parseErrf(path, b.headingLine, "记录 ID %q 在同一文档中重复", id)
		}
		seen[id] = true
	}
	return doc, nil
}

func validateFrontmatter(path string, f DocumentFrontmatter) error {
	if strings.TrimSpace(f.ID) == "" {
		return parseErrf(path, 0, "frontmatter 缺少稳定文档 ID")
	}
	if !strings.HasPrefix(f.ID, "doc:") {
		return parseErrf(path, 0, "文档 ID %q 必须以 doc: 开头", f.ID)
	}
	if strings.TrimSpace(f.Title) == "" {
		return parseErrf(path, 0, "frontmatter 缺少 title")
	}
	if strings.TrimSpace(f.Kind) == "" {
		return parseErrf(path, 0, "frontmatter 缺少 kind")
	}
	if f.SchemaVersion != RecordSyntaxVersion {
		return parseErrf(path, 0, "schema_version 必须是 %q，实际 %q", RecordSyntaxVersion, f.SchemaVersion)
	}
	for _, a := range f.About {
		if err := validateRef(a, "frontmatter about"); err != nil {
			return parseErrf(path, 0, "%v", err)
		}
	}
	return nil
}

func validateRef(ref, where string) error {
	if strings.TrimSpace(ref) == "" {
		return fmt.Errorf("%s 含空引用", where)
	}
	if !strings.Contains(ref, ":") {
		return fmt.Errorf("%s 的 %q 不是稳定 ID（缺少类型前缀）", where, ref)
	}
	return nil
}

// splitFrontmatter separates a leading YAML frontmatter block from the body.
func splitFrontmatter(path string, content []byte) (DocumentFrontmatter, string, error) {
	var front DocumentFrontmatter
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return front, "", parseErrf(path, 1, "文档必须以 YAML frontmatter 开始")
	}
	rest := text[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return front, "", parseErrf(path, 1, "frontmatter 缺少结束分隔符 ---")
	}
	raw := rest[:end]
	body := rest[end+4:]
	body = strings.TrimPrefix(body, "\n")
	dec := yaml.NewDecoder(strings.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&front); err != nil {
		return front, "", parseErrf(path, 1, "frontmatter 解析失败: %v", err)
	}
	return front, body, nil
}

// block is one heading-delimited record candidate.
type block struct {
	kind         string
	heading      string
	headingLine  int
	headingLevel int
	metaRaw      string
	metaLine     int
	sections     map[string]string
	sectionLine  map[string]int
}

// scanBlocks walks the body and returns every heading block that carries a
// record metadata fence. Headings inside code fences are ignored, and a block
// ends at the next heading of the same or higher level.
func scanBlocks(path, body string) ([]block, error) {
	lines := strings.Split(body, "\n")
	type heading struct {
		line  int
		level int
		text  string
	}
	var headings []heading
	inFence := false
	var fenceMarker string
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inFence {
			if m := fenceOpen(trimmed); m != "" {
				inFence, fenceMarker = true, m
				continue
			}
			if lvl, text, ok := parseHeading(trimmed); ok {
				headings = append(headings, heading{line: i, level: lvl, text: text})
			}
			continue
		}
		if strings.HasPrefix(trimmed, fenceMarker) {
			inFence, fenceMarker = false, ""
		}
	}
	var out []block
	for hi, h := range headings {
		end := len(lines)
		for hj := hi + 1; hj < len(headings); hj++ {
			if headings[hj].level <= h.level {
				end = headings[hj].line
				break
			}
		}
		// Sub-sections of this heading.
		sections := map[string]string{}
		sectionLine := map[string]int{}
		for hj := hi + 1; hj < len(headings) && headings[hj].line < end; hj++ {
			sub := headings[hj]
			subEnd := end
			for hk := hj + 1; hk < len(headings) && headings[hk].line < end; hk++ {
				if headings[hk].level <= sub.level {
					subEnd = headings[hk].line
					break
				}
			}
			sections[normalizeHeading(sub.text)] = strings.Join(lines[sub.line+1:subEnd], "\n")
			sectionLine[normalizeHeading(sub.text)] = sub.line + 1
		}
		// The record metadata must be the first fenced YAML block directly
		// under this heading: a fence that only appears after a sub-heading
		// belongs to that sub-heading, not to this one.
		metaEnd := end
		if hi+1 < len(headings) && headings[hi+1].line < end {
			metaEnd = headings[hi+1].line
		}
		meta, metaLine, ok := firstYAMLFence(lines[h.line+1 : metaEnd])
		if !ok {
			continue
		}
		kind, err := peekKind(path, meta, h.line+metaLine)
		if err != nil {
			return nil, err
		}
		if kind == "" {
			continue
		}
		out = append(out, block{
			kind: kind, heading: h.text, headingLine: h.line + 1, headingLevel: h.level,
			metaRaw: meta, metaLine: h.line + metaLine, sections: sections, sectionLine: sectionLine,
		})
	}
	return out, nil
}

func normalizeHeading(s string) string {
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(s), "#"))
}

func fenceOpen(trimmed string) string {
	if strings.HasPrefix(trimmed, "```") {
		rest := strings.TrimPrefix(trimmed, "```")
		marker := "```"
		if len(rest) > 0 && rest[0] == '`' {
			marker = "````"
		}
		return marker
	}
	return ""
}

func parseHeading(trimmed string) (int, string, bool) {
	if !strings.HasPrefix(trimmed, "#") {
		return 0, "", false
	}
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level > 6 || level >= len(trimmed) || trimmed[level] != ' ' {
		return 0, "", false
	}
	return level, strings.TrimSpace(trimmed[level:]), true
}

// firstYAMLFence returns the first fenced YAML block in a line range.
func firstYAMLFence(lines []string) (string, int, bool) {
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(trimmed, "```") {
			continue
		}
		lang := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
		if lang != "yaml" && lang != "yml" {
			// Skip a non-YAML fence wholesale so its content is not scanned.
			for j := i + 1; j < len(lines); j++ {
				if strings.HasPrefix(strings.TrimSpace(lines[j]), "```") {
					i = j
					break
				}
			}
			continue
		}
		var body []string
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(strings.TrimSpace(lines[j]), "```") {
				return strings.Join(body, "\n"), i + 2, true
			}
			body = append(body, lines[j])
		}
		return "", 0, false
	}
	return "", 0, false
}

func peekKind(path, meta string, line int) (string, error) {
	var probe struct {
		Kind string `yaml:"kind"`
	}
	dec := yaml.NewDecoder(strings.NewReader(meta))
	dec.KnownFields(false)
	if err := dec.Decode(&probe); err != nil {
		return "", parseErrf(path, line, "块元数据不是合法 YAML: %v", err)
	}
	return strings.TrimSpace(probe.Kind), nil
}

func decodeStrict(path string, line int, meta string, target any) error {
	dec := yaml.NewDecoder(strings.NewReader(meta))
	dec.KnownFields(true)
	if err := dec.Decode(target); err != nil {
		return parseErrf(path, line, "块元数据含未知或非法字段: %v%s", err, fieldGuidance(err))
	}
	return nil
}

// fieldGuidance turns a "field X not found" error into the concrete change the
// writer has to make, so a bounded repair turn can act on it directly instead
// of guessing which of the two block grammars it violated.
func fieldGuidance(err error) string {
	msg := err.Error()
	if !strings.Contains(msg, "field ") || !strings.Contains(msg, "not found") {
		return ""
	}
	field := ""
	rest := msg[strings.Index(msg, "field ")+len("field "):]
	if idx := strings.Index(rest, " "); idx > 0 {
		field = rest[:idx]
	}
	switch field {
	case "about", "statement":
		return "；relation 块不使用 " + field + "：请把两个对象分别写在 from 与 to，把解释写进「陈述」小节；需要描述单个对象的结论时改用 kind: assertion"
	case "from", "to", "predicate":
		return "；relation 块必须写 from: {kind, id}、predicate、to: {kind, id}"
	case "perspective", "basis", "scope", "evidence":
		return "；assertion 与 relation 都必须写 perspective、basis、scope、evidence"
	default:
		return ""
	}
}

func parseAssertionBlock(path string, b block) (*Assertion, error) {
	var a Assertion
	if err := decodeStrict(path, b.metaLine, b.metaRaw, &a); err != nil {
		return nil, err
	}
	if err := checkNoStatementKey(path, b.metaLine, b.metaRaw); err != nil {
		return nil, err
	}
	if a.Kind != BlockKindAssertion {
		return nil, parseErrf(path, b.metaLine, "kind 必须是 assertion")
	}
	if !strings.HasPrefix(a.ID, "assertion:") {
		return nil, parseErrf(path, b.metaLine, "assertion ID %q 必须以 assertion: 开头", a.ID)
	}
	if len(a.About) == 0 {
		return nil, parseErrf(path, b.metaLine, "assertion %q 缺少 about", a.ID)
	}
	for _, ref := range a.About {
		if err := validateRef(ref, "about"); err != nil {
			return nil, parseErrf(path, b.metaLine, "assertion %q: %v", a.ID, err)
		}
	}
	if a.Perspective != PerspectiveNormative && a.Perspective != PerspectiveDescriptive {
		return nil, parseErrf(path, b.metaLine, "assertion %q 的 perspective 必须是 normative 或 descriptive", a.ID)
	}
	switch a.Basis {
	case BasisSourceStatement, BasisCodeStatic, BasisRuntimeObserved, BasisInferred:
	default:
		return nil, parseErrf(path, b.metaLine, "assertion %q 的 basis %q 不在闭集内", a.ID, a.Basis)
	}
	if err := validateEvidenceRefs(path, b.metaLine, a.ID, a.Evidence); err != nil {
		return nil, err
	}
	statement, ok := b.sections[statementSection]
	if !ok {
		return nil, parseErrf(path, b.headingLine, "assertion %q 缺少「%s」小节", a.ID, statementSection)
	}
	statement = strings.TrimSpace(statement)
	if statement == "" {
		return nil, parseErrf(path, b.headingLine, "assertion %q 的「%s」小节为空", a.ID, statementSection)
	}
	if strings.HasPrefix(statement, "```yaml") {
		return nil, parseErrf(path, b.headingLine, "assertion %q 的陈述不能又是元数据块", a.ID)
	}
	a.Heading = b.heading
	a.Statement = statement
	a.UnknownNotes = strings.TrimSpace(b.sections[unknownSection])
	return &a, nil
}

func parseRelationBlock(path string, b block) (*Relation, error) {
	var r Relation
	if err := decodeStrict(path, b.metaLine, b.metaRaw, &r); err != nil {
		return nil, err
	}
	if err := checkNoStatementKey(path, b.metaLine, b.metaRaw); err != nil {
		return nil, err
	}
	if r.Kind != BlockKindRelation {
		return nil, parseErrf(path, b.metaLine, "kind 必须是 relation")
	}
	if !strings.HasPrefix(r.ID, "relation:") {
		return nil, parseErrf(path, b.metaLine, "relation ID %q 必须以 relation: 开头", r.ID)
	}
	if err := validateEndpoint(path, b.metaLine, r.ID, "from", r.From); err != nil {
		return nil, err
	}
	if err := validateEndpoint(path, b.metaLine, r.ID, "to", r.To); err != nil {
		return nil, err
	}
	if !RelationPredicates[r.Predicate] {
		return nil, parseErrf(path, b.metaLine, "relation %q 的 predicate %q 不在受控词表内", r.ID, r.Predicate)
	}
	if r.From.Kind == r.To.Kind && r.From.ID == r.To.ID {
		return nil, parseErrf(path, b.metaLine, "relation %q 的起点与终点相同", r.ID)
	}
	if r.Perspective != PerspectiveNormative && r.Perspective != PerspectiveDescriptive {
		return nil, parseErrf(path, b.metaLine, "relation %q 的 perspective 非法", r.ID)
	}
	switch r.Basis {
	case BasisSourceStatement, BasisCodeStatic, BasisRuntimeObserved, BasisInferred:
	default:
		return nil, parseErrf(path, b.metaLine, "relation %q 的 basis %q 不在闭集内", r.ID, r.Basis)
	}
	if err := validateEvidenceRefs(path, b.metaLine, r.ID, r.Evidence); err != nil {
		return nil, err
	}
	r.Heading = b.heading
	r.Statement = strings.TrimSpace(b.sections[statementSection])
	return &r, nil
}

func validateEndpoint(path string, line int, id, field string, e Endpoint) error {
	// An endpoint names the object class it points at. Every project object
	// (service, event, api, data, capability, ...) is an entity, so an entity
	// kind is accepted as a spelling of `entity` rather than rejected: the
	// resolved endpoint still has to be a declared, stable entity ID.
	if NormalizeEndpointKind(e.Kind) != "" {
		return nil
	}
	switch e.Kind {
	case EndpointEntity, EndpointDocument, EndpointAssertion:
	default:
		return parseErrf(path, line, "relation %q 的 %s.kind %q 非法（允许 entity/document/assertion，或 entity 的 kind：%s）",
			id, field, e.Kind, strings.Join(sortedEntityKinds(), "/"))
	}
	if strings.TrimSpace(e.ID) == "" {
		return parseErrf(path, line, "relation %q 的 %s.id 为空", id, field)
	}
	if e.Kind == "unresolved" {
		return parseErrf(path, line, "relation %q 的 %s 未解析目标必须登记在 gaps 中", id, field)
	}
	return nil
}

// NormalizeEndpointKind maps an endpoint kind onto the closed set. Entity
// kinds collapse to `entity`; document/assertion stay as they are; anything
// unknown returns "".
func NormalizeEndpointKind(kind string) string {
	switch kind {
	case EndpointEntity, EndpointDocument, EndpointAssertion:
		return kind
	}
	if EntityKinds[kind] {
		return EndpointEntity
	}
	return ""
}

func sortedEntityKinds() []string {
	out := make([]string, 0, len(EntityKinds))
	for k := range EntityKinds {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func validateEvidenceRefs(path string, line int, id string, refs []EvidenceRef) error {
	seen := map[string]bool{}
	for _, ref := range refs {
		if strings.TrimSpace(ref.EvidenceID) == "" {
			return parseErrf(path, line, "%s 含空的 evidence_id", id)
		}
		switch ref.Role {
		case EvidenceRoleSupports, EvidenceRoleContradicts, EvidenceRoleContext:
		default:
			return parseErrf(path, line, "%s 的证据角色 %q 非法", id, ref.Role)
		}
		if seen[ref.EvidenceID] {
			return parseErrf(path, line, "%s 重复引用证据 %q", id, ref.EvidenceID)
		}
		seen[ref.EvidenceID] = true
	}
	return nil
}

// checkNoStatementKey rejects a block that also writes the statement in YAML:
// the statement has exactly one writing location, the body section.
func checkNoStatementKey(path string, line int, meta string) error {
	var probe map[string]any
	if err := yaml.Unmarshal([]byte(meta), &probe); err != nil {
		return parseErrf(path, line, "块元数据不是合法 YAML: %v", err)
	}
	for _, banned := range []string{"statement", "digest", "content_digest", "approved", "approval", "confirmed", "review_state", "freshness", "release_id", "version"} {
		if _, ok := probe[banned]; ok {
			return parseErrf(path, line, "块元数据不得包含 %q：该信息由资料 harness 维护或必须写在正文小节", banned)
		}
	}
	return nil
}

const (
	statementSection = "陈述"
	unknownSection   = "说明与未知"
)

// SortedIDs is a deterministic helper for reporting and diffing.
func SortedIDs(ids []string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

// Canonicalize normalizes line endings so digests are stable across platforms.
func Canonicalize(content []byte) []byte {
	return bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
}

package knowledgelib

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// maxExcerptRunes bounds the display cache stored beside a locator. It is a
// cache of located source text, never a model rewrite.
const maxExcerptRunes = 4000

// Representation is one frozen content representation inside a binding.
type Representation struct {
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
}

// CollectedEvidence is evidence whose locator was resolved against real
// bytes by this program. Every field here is computed, not reported.
type CollectedEvidence struct {
	ID               string
	Key              string
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
	Representation   Representation
	Binding          *Binding
	Request          EvidenceRequest
}

// frozenContent is the canonical bytes and representation of one source file,
// cached so several locators in the same file share one frozen copy.
type frozenContent struct {
	repr    Representation
	content []byte
}

// EvidenceCollector turns evidence requests into collected evidence.
type EvidenceCollector struct {
	Library Library
	Runner  GitRunner
	Now     func() time.Time
}

func (c *EvidenceCollector) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

// Collect resolves every request against the pinned snapshot. A request that
// cannot be resolved is an error: unverifiable evidence is never registered.
func (c *EvidenceCollector) Collect(ctx context.Context, snapshotID string, bindings []*Binding, reqs []EvidenceRequest) (map[string]*CollectedEvidence, error) {
	out := make(map[string]*CollectedEvidence, len(reqs))
	reprCache := map[string]frozenContent{}
	// Report every unresolvable request in one pass: the repair turn should
	// see the whole list, not the first failure.
	problems := &ValidationErrors{}
	for _, req := range reqs {
		binding, err := ResolveBinding(bindings, req.Binding)
		if err != nil {
			problems.Add(err)
			continue
		}
		ce, err := c.collectOne(ctx, snapshotID, binding, req, reprCache)
		if err != nil {
			problems.Add(err)
			continue
		}
		out[req.Key] = ce
	}
	if err := problems.OrNil(); err != nil {
		return nil, err
	}
	return out, nil
}

// ResolveBinding selects one binding by binding ID, full qualified name,
// source@consumer, or plain source name. Matching is tiered: the most specific
// tier that matches anything decides, and an ambiguous tier is refused rather
// than silently resolved to an arbitrary usage — two usages of one source may
// pin different artifact versions or environments.
func ResolveBinding(bindings []*Binding, selector string) (*Binding, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return nil, errf(ErrValidation, "证据必须指定 binding")
	}
	tiers := []func(*Binding) string{
		func(b *Binding) string { return b.ID },
		func(b *Binding) string { return b.QualifiedName() },
		func(b *Binding) string { return b.usageName() },
		func(b *Binding) string { return b.SourceName },
	}
	for _, key := range tiers {
		var matches []*Binding
		for _, b := range bindings {
			if key(b) == selector {
				matches = append(matches, b)
			}
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			names := make([]string, 0, len(matches))
			for _, b := range matches {
				names = append(names, b.QualifiedName())
			}
			sort.Strings(names)
			return nil, errf(ErrValidation, "binding %q 命中多个绑定，请写完整名称：%s", selector, strings.Join(names, "、"))
		}
	}
	names := make([]string, 0, len(bindings))
	for _, b := range bindings {
		names = append(names, b.QualifiedName())
	}
	sort.Strings(names)
	return nil, errf(ErrValidation, "证据引用了未登记的来源 %q（本快照可用绑定：%s）", selector, strings.Join(names, ", "))
}

// usageName is the source@consumer tier, ignoring artifact and environment.
func (b *Binding) usageName() string {
	if b.Consumer == "" {
		return b.SourceName
	}
	return b.SourceName + "@" + b.Consumer
}

func (c *EvidenceCollector) collectOne(ctx context.Context, snapshotID string, binding *Binding, req EvidenceRequest, cache map[string]frozenContent) (*CollectedEvidence, error) {
	if strings.TrimSpace(req.Path) == "" {
		return nil, errf(ErrValidation, "证据 %q 缺少 path", req.Key)
	}
	cacheKey := binding.ID + "\x00" + req.Path
	entry, cached := cache[cacheKey]
	var repr Representation
	var content []byte
	if cached {
		repr, content = entry.repr, entry.content
	} else {
		read, err := ReadBindingFile(ctx, c.Runner, binding, req.Path)
		if err != nil {
			return nil, errf(ErrValidation, "证据 %q 无法读取 %s@%s:%s：%v", req.Key, binding.SourceName, shortSHA(binding.CommitSHA), req.Path, err)
		}
		content = Canonicalize(read.Content)
		digest := DigestBytes(content)
		stored, err := c.Library.StoreRepresentation(digest, content)
		if err != nil {
			return nil, err
		}
		repr = Representation{
			ID:            "repr:" + ShortID(binding.ID, req.Path, digest),
			BindingID:     binding.ID,
			Path:          req.Path,
			MediaType:     mediaTypeFor(req.Path),
			Encoding:      "utf-8",
			DigestAlgo:    "sha256",
			ContentDigest: digest,
			ByteSize:      len(content),
			Coverage:      "full",
			StoredPath:    stored,
			Origin:        read.Origin,
		}
		cache[cacheKey] = frozenContent{repr: repr, content: content}
	}
	locatorJSON, locatorKind, excerpt, matchCount, err := resolveLocator(req.Key, repr, content, req.Locator)
	if err != nil {
		return nil, err
	}
	id := "evidence:" + ShortID(binding.ID, req.Path, locatorJSON, repr.ContentDigest)
	return &CollectedEvidence{
		ID:               id,
		Key:              req.Key,
		SnapshotID:       snapshotID,
		BindingID:        binding.ID,
		RepresentationID: repr.ID,
		LocatorJSON:      locatorJSON,
		LocatorKind:      locatorKind,
		Excerpt:          excerpt,
		ExcerptDigest:    DigestBytes([]byte(excerpt)),
		MatchCount:       matchCount,
		Availability:     "available",
		CollectedAt:      c.now(),
		Representation:   repr,
		Binding:          binding,
		Request:          req,
	}, nil
}

// resolveLocator turns a requested locator into an exact, program-checked
// fragment of a frozen representation. An ambiguous quote is refused rather
// than resolved by guessing.
func resolveLocator(key string, repr Representation, content []byte, loc Locator) (locatorJSON, locatorKind, excerpt string, matchCount int, err error) {
	kind := strings.TrimSpace(loc.Kind)
	if kind == "" {
		kind = "source_text"
	}
	switch kind {
	case "source_text", "document_section":
	default:
		return "", "", "", 0, errf(ErrValidation, "证据 %q 的 locator.kind %q 不受支持", key, kind)
	}
	interval := strings.TrimSpace(loc.Interval)
	if interval == "" {
		interval = "half_open"
	}
	if interval != "half_open" && interval != "closed" {
		return "", "", "", 0, errf(ErrValidation, "证据 %q 的 locator.interval %q 非法", key, interval)
	}
	text := string(content)
	lines := strings.Split(text, "\n")

	quote := strings.TrimSpace(loc.Quote)
	if quote != "" {
		matchCount = strings.Count(text, quote)
		if matchCount == 0 {
			return "", "", "", 0, errf(ErrValidation, "证据 %q 的 quote 在 %s 中不存在", key, repr.Path)
		}
		if matchCount > 1 && !hasExplicitRange(loc) {
			return "", "", "", 0, errf(ErrValidation, "证据 %q 的 quote 在 %s 中命中 %d 处，必须给出明确起止位置", key, repr.Path, matchCount)
		}
	}
	if !hasExplicitRange(loc) {
		if quote == "" {
			return "", "", "", 0, errf(ErrValidation, "证据 %q 既没有 quote 也没有起止位置", key)
		}
		start := strings.Index(text, quote)
		excerpt = clipRunes(quote, maxExcerptRunes)
		loc.Start = offsetToPosition(text, start)
		loc.End = offsetToPosition(text, start+len(quote))
		return encodeLocator(kind, loc, interval), kind, excerpt, matchCount, nil
	}
	if loc.Start.Line < 0 || loc.End.Line < 0 {
		return "", "", "", 0, errf(ErrValidation, "证据 %q 的起止行不能为负（坐标为 0-based）", key)
	}
	if loc.End.Line > len(lines) {
		return "", "", "", 0, errf(ErrValidation, "证据 %q 的结束行 %d 超出 %s 的实际行数 %d", key, loc.End.Line, repr.Path, len(lines))
	}
	startLine, endLine := loc.Start.Line, loc.End.Line
	if startLine > endLine {
		return "", "", "", 0, errf(ErrValidation, "证据 %q 的起始行晚于结束行", key)
	}
	if interval == "half_open" {
		// End is exclusive; a half-open range ending at the start of a line
		// must not lose the final line's content.
		if endLine > startLine && loc.End.Column == 0 {
			endLine--
		}
	}
	if endLine >= len(lines) {
		return "", "", "", 0, errf(ErrValidation, "证据 %q 的结束行 %d 超出 %s 的实际行数 %d", key, endLine, repr.Path, len(lines))
	}
	segments := make([]string, 0, endLine-startLine+1)
	for i := startLine; i <= endLine; i++ {
		line := lines[i]
		if i == startLine && loc.Start.Column > 0 {
			line = sliceColumns(line, loc.Start.Column, -1)
		}
		if i == endLine && loc.End.Column > 0 {
			line = sliceColumns(line, 0, loc.End.Column)
		}
		segments = append(segments, line)
	}
	excerpt = strings.TrimRight(strings.Join(segments, "\n"), "\n")
	if strings.TrimSpace(excerpt) == "" {
		return "", "", "", 0, errf(ErrValidation, "证据 %q 定位到空内容（%s 第 %d-%d 行）", key, repr.Path, startLine+1, endLine+1)
	}
	if quote != "" && !strings.Contains(excerpt, quote) {
		return "", "", "", 0, errf(ErrValidation, "证据 %q 的 quote 不落在所声明的行范围内", key)
	}
	if matchCount == 0 {
		matchCount = 1
	}
	excerpt = clipRunes(excerpt, maxExcerptRunes)
	return encodeLocator(kind, loc, interval), kind, excerpt, matchCount, nil
}

func hasExplicitRange(loc Locator) bool {
	return loc.End.Line > 0 || loc.End.Column > 0
}

func offsetToPosition(text string, offset int) Position {
	if offset < 0 {
		return Position{}
	}
	line := strings.Count(text[:offset], "\n")
	lastNL := strings.LastIndex(text[:offset], "\n")
	col := offset
	if lastNL >= 0 {
		col = offset - lastNL - 1
	}
	return Position{Line: line, Column: col}
}

// sliceColumns slices a line by rune columns; a negative end means "to EOL".
func sliceColumns(line string, start, end int) string {
	runes := []rune(line)
	if start < 0 {
		start = 0
	}
	if start > len(runes) {
		return ""
	}
	if end < 0 || end > len(runes) {
		end = len(runes)
	}
	if end < start {
		end = start
	}
	return string(runes[start:end])
}

func clipRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "\n…（摘录已截断）"
}

func encodeLocator(kind string, loc Locator, interval string) string {
	lineBase := loc.LineBase
	colBase := loc.ColumnBase
	unit := strings.TrimSpace(loc.ColumnUnit)
	if unit == "" {
		unit = "utf8_rune"
	}
	return fmt.Sprintf(
		`{"kind":%q,"line_base":%d,"column_base":%d,"column_unit":%q,"interval":%q,"start":{"line":%d,"column":%d},"end":{"line":%d,"column":%d}}`,
		kind, lineBase, colBase, unit, interval, loc.Start.Line, loc.Start.Column, loc.End.Line, loc.End.Column,
	)
}

// Package chatanalysis validates model proposals. It never approves product
// decisions or executes a model; the application owns source authorization.
package chatanalysis

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const Version = "chat-analysis/v1"
const MaxDocumentBytes = 128 << 10

var ErrInvalid = errors.New("invalid chat analysis")
var slug = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var fence = regexp.MustCompile("(?ms)^ {0,3}```atw-analysis[ \\t]*\\r?\\n(.*?)\\r?\\n {0,3}```[ \\t]*(?:\\r?$)")

type Document struct {
	Version             string     `json:"version"`
	Summary             string     `json:"summary"`
	Sources             []Source   `json:"sources"`
	Items               []Item     `json:"items"`
	Questions           []Question `json:"questions"`
	PreserveItemIDs     []string   `json:"preserve_item_ids,omitempty"`
	PreserveQuestionIDs []string   `json:"preserve_question_ids,omitempty"`
}

type Source struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Ref         string `json:"ref"`
	SHA256      string `json:"sha256"`
	Version     int    `json:"version,omitempty"`
	Locator     string `json:"locator,omitempty"`
	ReadStatus  string `json:"read_status"`
	Coverage    string `json:"coverage,omitempty"`
	Limitations string `json:"limitations,omitempty"`
	Quote       string `json:"quote,omitempty"`
}

type Item struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	Title          string   `json:"title"`
	Detail         string   `json:"detail"`
	SourceIDs      []string `json:"source_ids"`
	Basis          string   `json:"basis"`
	Impact         string   `json:"impact,omitempty"`
	Recommendation string   `json:"recommendation,omitempty"`
}

type Question struct {
	ID        string   `json:"id"`
	Prompt    string   `json:"prompt"`
	Selection string   `json:"selection"`
	Options   []Option `json:"options"`
	ItemIDs   []string `json:"item_ids"`
}

type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Decode accepts only the requested, complete machine fence. Ordinary model
// prose, truncated JSON and an earlier competing proposal cannot become state.
// A full document is required because there is no previous revision to expand.
func Decode(text string) (*Document, error) {
	return DecodeWithPrevious(text, nil)
}

// DecodeWithPrevious accepts a complete document or a structured delta against
// a previously validated document. Preserved items and questions are copied
// from previous verbatim at the typed-value level, while omitted source
// records needed by the resulting items are filled from previous. The caller
// still owns source authorization; this function only validates the document
// shape and lineage-safe merge.
func DecodeWithPrevious(text string, previous *Document) (*Document, error) {
	if len(text) > MaxDocumentBytes*4 || !utf8.ValidString(text) {
		return nil, invalid("output too large or invalid UTF-8")
	}
	blocks := fence.FindAllStringSubmatch(text, 2)
	if len(blocks) != 1 {
		return nil, invalid("one complete atw-analysis block is required")
	}
	raw := []byte(blocks[0][1])
	if len(raw) > MaxDocumentBytes {
		return nil, invalid("analysis block exceeds size limit")
	}
	if err := uniqueJSONKeys(raw); err != nil {
		return nil, invalid("ambiguous JSON: %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var doc Document
	if err := decoder.Decode(&doc); err != nil {
		return nil, invalid("schema: %v", err)
	}
	if err := expandDelta(&doc, previous); err != nil {
		return nil, err
	}
	if err := Validate(&doc); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(&doc)
	if err != nil {
		return nil, invalid("schema: %v", err)
	}
	if len(encoded) > MaxDocumentBytes {
		return nil, invalid("expanded analysis exceeds size limit")
	}
	return &doc, nil
}

func Validate(doc *Document) error {
	if doc == nil || doc.Version != Version || !textOK(doc.Summary, 4000, true) {
		return invalid("version and summary are required")
	}
	if len(doc.PreserveItemIDs) != 0 || len(doc.PreserveQuestionIDs) != 0 {
		return invalid("preserve ids must be expanded with DecodeWithPrevious")
	}
	if len(doc.Sources) == 0 || len(doc.Sources) > 40 || len(doc.Items) == 0 || len(doc.Items) > 100 || len(doc.Questions) > 30 {
		return invalid("source, item or question count out of bounds")
	}
	sources := make(map[string]Source, len(doc.Sources))
	for _, source := range doc.Sources {
		if !slug.MatchString(source.ID) || !oneOf(source.Kind, "attachment", "conversation", "code", "knowledge") || !textOK(source.Ref, 1024, true) || !digest.MatchString(source.SHA256) {
			return invalid("invalid source identity or digest")
		}
		if _, exists := sources[source.ID]; exists {
			return invalid("duplicate source id %q", source.ID)
		}
		if source.Kind == "code" && (path.IsAbs(source.Ref) || path.Clean(source.Ref) != source.Ref || source.Ref == "." || source.Ref == ".." || strings.HasPrefix(source.Ref, "../") || strings.ContainsAny(source.Ref, "\\\x00:")) {
			return invalid("code reference must be a relative project path")
		}
		if source.Kind == "knowledge" && (source.Version < 1 || strings.Contains(source.Ref, "@")) {
			return invalid("knowledge source %q requires a bare item ref and separate positive integer version; do not append @version to ref", source.ID)
		}
		if source.Kind != "knowledge" && source.Version != 0 {
			return invalid("source %q: version is only used for knowledge sources", source.ID)
		}
		if !oneOf(source.ReadStatus, "read", "partial", "failed") || !textOK(source.Locator, 1000, false) || !textOK(source.Coverage, 2000, false) || !textOK(source.Limitations, 2000, source.ReadStatus != "read") || !textOK(source.Quote, 4000, false) {
			return invalid("invalid source reading coverage")
		}
		sources[source.ID] = source
	}
	items := make(map[string]Item, len(doc.Items))
	for _, item := range doc.Items {
		if !slug.MatchString(item.ID) || !oneOf(item.Kind, "requirement", "normal", "exception", "conflict", "unknown") || !oneOf(item.Basis, "observed", "proposed", "unverified") || !textOK(item.Title, 500, true) || !textOK(item.Detail, 8000, true) {
			return invalid("invalid analysis item")
		}
		if _, exists := items[item.ID]; exists {
			return invalid("duplicate item id %q", item.ID)
		}
		if !textOK(item.Impact, 4000, item.Kind == "conflict") || !textOK(item.Recommendation, 4000, item.Kind == "conflict") {
			return invalid("conflicts require impact and recommendation")
		}
		if len(item.SourceIDs) == 0 || len(item.SourceIDs) > 12 || !uniqueStrings(item.SourceIDs) || (item.Kind == "conflict" && len(item.SourceIDs) < 2) {
			return invalid("invalid item source references")
		}
		for _, id := range item.SourceIDs {
			source, exists := sources[id]
			if !exists || (item.Basis == "observed" && source.ReadStatus == "failed") {
				return invalid("item references missing or unread evidence")
			}
		}
		items[item.ID] = item
	}
	questions := make(map[string]bool, len(doc.Questions))
	for _, question := range doc.Questions {
		if !slug.MatchString(question.ID) || questions[question.ID] || !textOK(question.Prompt, 2000, true) || !oneOf(question.Selection, "single", "multiple", "text") {
			return invalid("invalid question")
		}
		questions[question.ID] = true
		if (question.Selection == "text" && len(question.Options) != 0) || (question.Selection != "text" && (len(question.Options) < 2 || len(question.Options) > 8)) {
			return invalid("invalid question options")
		}
		options := make(map[string]bool)
		for _, option := range question.Options {
			if !slug.MatchString(option.ID) || options[option.ID] || !textOK(option.Label, 500, true) {
				return invalid("invalid or repeated option")
			}
			options[option.ID] = true
		}
		if len(question.ItemIDs) == 0 || len(question.ItemIDs) > 12 || !uniqueStrings(question.ItemIDs) {
			return invalid("invalid question item references")
		}
		for _, id := range question.ItemIDs {
			if _, exists := items[id]; !exists {
				return invalid("question references missing item")
			}
		}
	}
	return nil
}

func expandDelta(doc *Document, previous *Document) error {
	if doc == nil {
		return invalid("document is required")
	}
	preserveItems := append([]string(nil), doc.PreserveItemIDs...)
	preserveQuestions := append([]string(nil), doc.PreserveQuestionIDs...)
	if len(preserveItems) != 0 || len(preserveQuestions) != 0 {
		if previous == nil {
			return invalid("preserve ids require a previous document")
		}
	}
	if previous != nil {
		if err := Validate(previous); err != nil {
			return invalid("previous document is invalid: %v", err)
		}
	}
	if !uniqueStrings(preserveItems) || !uniqueStrings(preserveQuestions) {
		return invalid("preserve ids must be unique")
	}
	for _, id := range preserveItems {
		if !slug.MatchString(id) {
			return invalid("invalid preserved item id %q", id)
		}
	}
	for _, id := range preserveQuestions {
		if !slug.MatchString(id) {
			return invalid("invalid preserved question id %q", id)
		}
	}

	previousItems := make(map[string]Item)
	previousQuestions := make(map[string]Question)
	if previous != nil {
		previousItems = make(map[string]Item, len(previous.Items))
		for _, item := range previous.Items {
			previousItems[item.ID] = item
		}
		previousQuestions = make(map[string]Question, len(previous.Questions))
		for _, question := range previous.Questions {
			previousQuestions[question.ID] = question
		}
	}

	explicitItems := make(map[string]struct{}, len(doc.Items))
	for _, item := range doc.Items {
		if _, exists := explicitItems[item.ID]; exists {
			return invalid("duplicate item id %q", item.ID)
		}
		explicitItems[item.ID] = struct{}{}
	}
	explicitQuestions := make(map[string]struct{}, len(doc.Questions))
	for _, question := range doc.Questions {
		if _, exists := explicitQuestions[question.ID]; exists {
			return invalid("duplicate question id %q", question.ID)
		}
		explicitQuestions[question.ID] = struct{}{}
	}
	for _, id := range preserveItems {
		if _, exists := explicitItems[id]; exists {
			return invalid("preserved item id %q overlaps an explicit item", id)
		}
		if _, exists := previousItems[id]; !exists {
			return invalid("preserved item id %q is absent from previous document", id)
		}
	}
	for _, id := range preserveQuestions {
		if _, exists := explicitQuestions[id]; exists {
			return invalid("preserved question id %q overlaps an explicit question", id)
		}
		if _, exists := previousQuestions[id]; !exists {
			return invalid("preserved question id %q is absent from previous document", id)
		}
	}

	if len(preserveItems) != 0 {
		for _, id := range preserveItems {
			doc.Items = append(doc.Items, cloneItem(previousItems[id]))
		}
	}
	if len(preserveQuestions) != 0 {
		for _, id := range preserveQuestions {
			doc.Questions = append(doc.Questions, cloneQuestion(previousQuestions[id]))
		}
	}

	explicitSources := make(map[string]Source, len(doc.Sources))
	for _, source := range doc.Sources {
		if _, exists := explicitSources[source.ID]; exists {
			return invalid("duplicate source id %q", source.ID)
		}
		explicitSources[source.ID] = source
	}
	requiredSources := make(map[string]struct{})
	for _, item := range doc.Items {
		for _, sourceID := range item.SourceIDs {
			requiredSources[sourceID] = struct{}{}
		}
	}
	if previous != nil {
		for _, oldSource := range previous.Sources {
			if _, required := requiredSources[oldSource.ID]; !required {
				continue
			}
			newSource, hadExplicit := explicitSources[oldSource.ID]
			if hadExplicit && !sameSourceIdentity(oldSource, newSource) {
				return invalid("source %q changed identity across delta", oldSource.ID)
			}
			if !hadExplicit {
				doc.Sources = append(doc.Sources, cloneSource(oldSource))
			}
		}
	}

	doc.PreserveItemIDs = nil
	doc.PreserveQuestionIDs = nil
	return nil
}

func cloneSource(source Source) Source {
	return source
}

func cloneItem(item Item) Item {
	if item.SourceIDs != nil {
		item.SourceIDs = append([]string{}, item.SourceIDs...)
	}
	return item
}

func cloneQuestion(question Question) Question {
	if question.Options != nil {
		question.Options = append([]Option{}, question.Options...)
	}
	if question.ItemIDs != nil {
		question.ItemIDs = append([]string{}, question.ItemIDs...)
	}
	return question
}

func sameSourceIdentity(left, right Source) bool {
	return left.Kind == right.Kind && left.Ref == right.Ref && left.SHA256 == right.SHA256 && left.Version == right.Version
}

func ValidateAnswer(doc *Document, questionID string, selected []string, text, disposition string) error {
	if doc == nil || !oneOf(disposition, "answered", "deferred") || !textOK(text, 8000, false) || !uniqueStrings(selected) {
		return invalid("invalid answer")
	}
	var question *Question
	for i := range doc.Questions {
		if doc.Questions[i].ID == questionID {
			question = &doc.Questions[i]
			break
		}
	}
	if question == nil {
		return invalid("unknown question")
	}
	if disposition == "deferred" {
		if len(selected) != 0 {
			return invalid("deferred answers cannot select options")
		}
		return nil
	}
	if len(selected) == 0 && strings.TrimSpace(text) == "" {
		return invalid("an explicit answer is required")
	}
	if (question.Selection == "text" && len(selected) > 0) || (question.Selection == "single" && len(selected) > 1) {
		return invalid("answer does not match selection mode")
	}
	for _, id := range selected {
		found := false
		for _, option := range question.Options {
			found = found || option.ID == id
		}
		if !found {
			return invalid("answer references unknown option")
		}
	}
	return nil
}

// Fingerprints include only the item/question's dependencies. A new unrelated
// source cannot invalidate an otherwise unchanged answer or product decision.
func ItemFingerprint(doc *Document, item Item) string {
	type identity struct {
		Kind, Ref, SHA256 string
		Version           int
	}
	sources := make([]identity, 0, len(item.SourceIDs))
	for _, id := range item.SourceIDs {
		for _, source := range doc.Sources {
			if source.ID == id {
				sources = append(sources, identity{source.Kind, source.Ref, source.SHA256, source.Version})
			}
		}
	}
	sort.Slice(sources, func(i, j int) bool { return hashJSON(sources[i]) < hashJSON(sources[j]) })
	item.SourceIDs = nil
	return hashJSON(struct {
		Item    Item
		Sources []identity
	}{item, sources})
}

func QuestionFingerprint(doc *Document, question Question) string {
	items := make([]string, 0, len(question.ItemIDs))
	for _, id := range question.ItemIDs {
		for _, item := range doc.Items {
			if item.ID == id {
				items = append(items, ItemFingerprint(doc, item))
			}
		}
	}
	sort.Strings(items)
	question.ItemIDs = nil
	return hashJSON(struct {
		Question Question
		Items    []string
	}{question, items})
}

func hashJSON(value any) string {
	raw, _ := json.Marshal(value)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func invalid(format string, values ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, values...))
}

func textOK(value string, limit int, required bool) bool {
	return utf8.ValidString(value) && len(value) <= limit && !strings.ContainsRune(value, 0) && (!required || strings.TrimSpace(value) != "")
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) bool {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func uniqueJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var read func(int) error
	read = func(depth int) error {
		if depth > 32 {
			return errors.New("JSON nesting too deep")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		start, compound := token.(json.Delim)
		if !compound {
			return nil
		}
		seen := map[string]bool{}
		for decoder.More() {
			if start == '{' {
				token, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := token.(string)
				if !ok || seen[key] {
					return errors.New("duplicate or invalid object key")
				}
				seen[key] = true
			}
			if err := read(depth + 1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	}
	if err := read(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

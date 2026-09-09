package chatanalysis

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func sample() Document {
	return Document{
		Version: Version, Summary: "首期范围存在冲突，需开发与产品确认。",
		Sources: []Source{
			{ID: "reading-doc", Kind: "attachment", Ref: "src_reading", SHA256: strings.Repeat("a", 64), Locator: "第1页", ReadStatus: "partial", Coverage: "正文", Limitations: "未读取缩略图"},
			{ID: "editing-slides", Kind: "attachment", Ref: "src_editing", SHA256: strings.Repeat("b", 64), Locator: "第1张", ReadStatus: "read"},
		},
		Items: []Item{
			{ID: "java-scope", Kind: "conflict", Title: "只读或编辑", Detail: "Word 建议只读，PPT 提议支持编辑。", SourceIDs: []string{"reading-doc", "editing-slides"}, Basis: "observed", Impact: "影响保存和回滚处理", Recommendation: "向产品确认首期范围"},
		},
		Questions: []Question{
			{ID: "initial-scope", Prompt: "首期应包含哪些 Java 能力？", Selection: "single", Options: []Option{{ID: "reading", Label: "文件阅读与导航"}, {ID: "editing", Label: "包含跨文件编辑"}}, ItemIDs: []string{"java-scope"}},
		},
	}
}

func framed(doc Document) string {
	raw, _ := json.Marshal(doc)
	return "分析仅为提议。\n\n```atw-analysis\n" + string(raw) + "\n```"
}

func TestDecodeProposalsWithPartialSourceAndUnansweredQuestion(t *testing.T) {
	doc, err := Decode(framed(sample()))
	if err != nil || doc == nil || doc.Sources[0].ReadStatus != "partial" || len(doc.Questions) != 1 {
		t.Fatalf("valid source-backed proposal: %+v, %v", doc, err)
	}
	if doc.Items[0].Basis != "observed" || doc.Questions[0].ID != "initial-scope" {
		t.Fatalf("proposal and unanswered question were changed: %+v", doc)
	}
}

func TestDecodeRejectsAmbiguousAndPrivilegedModelOutput(t *testing.T) {
	valid := framed(sample())
	cases := map[string]string{
		"ordinary prose is not analysis": "已经确认，开始开发。",
		"truncated fence":                strings.TrimSuffix(valid, "\n```"),
		"competing proposals":            valid + "\n" + valid,
		"unknown approval authority":     strings.Replace(valid, `"summary":`, `"approved":true,"summary":`, 1),
		"duplicate top-level key":        strings.Replace(valid, `"version":`, `"version":"wrong","version":`, 1),
		"duplicate nested key":           strings.Replace(valid, `"label":"文件阅读与导航"`, `"label":"文件阅读与导航","label":"自动发布"`, 1),
		"trailing second JSON object":    strings.TrimSuffix(valid, "\n```") + "{}\n```",
		"oversize envelope":              "```atw-analysis\n" + strings.Repeat(" ", MaxDocumentBytes+1) + "\n```",
		"wrong fence language":           strings.Replace(valid, "```atw-analysis", "```json", 1),
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(text); !errors.Is(err, ErrInvalid) {
				t.Fatalf("ambiguous output accepted: %v", err)
			}
		})
	}
}

func TestValidateRejectsInventedReferencesAndUnreadFacts(t *testing.T) {
	cases := map[string]func(*Document){
		"dangling source":            func(d *Document) { d.Items[0].SourceIDs[0] = "absent" },
		"dangling item":              func(d *Document) { d.Questions[0].ItemIDs[0] = "absent" },
		"duplicate source id":        func(d *Document) { d.Sources[1].ID = d.Sources[0].ID },
		"duplicate item id":          func(d *Document) { d.Items = append(d.Items, d.Items[0]) },
		"duplicate question id":      func(d *Document) { d.Questions = append(d.Questions, d.Questions[0]) },
		"duplicate option id":        func(d *Document) { d.Questions[0].Options[1].ID = "reading" },
		"failed source observed":     func(d *Document) { d.Sources[0].ReadStatus = "failed" },
		"partial without scope":      func(d *Document) { d.Sources[0].Limitations = "" },
		"approval masquerades basis": func(d *Document) { d.Items[0].Basis = "confirmed" },
		"conflict omits impact":      func(d *Document) { d.Items[0].Impact = "" },
		"invalid digest":             func(d *Document) { d.Sources[0].SHA256 = "sha256:" + d.Sources[0].SHA256 },
		"knowledge omits version":    func(d *Document) { d.Sources[0].Kind = "knowledge" },
		"oversize summary":           func(d *Document) { d.Summary = strings.Repeat("x", 4001) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			doc := sample()
			mutate(&doc)
			if err := Validate(&doc); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid proposal accepted: %v", err)
			}
		})
	}
	for _, ref := range []string{"/etc/hosts", "../foreign", "src/../../foreign", "src/../file.go", `C:\secret`, `src\file.go`, "."} {
		t.Run(ref, func(t *testing.T) {
			doc := sample()
			doc.Sources[0].Kind, doc.Sources[0].Ref = "code", ref
			if err := Validate(&doc); !errors.Is(err, ErrInvalid) {
				t.Fatalf("code scope escape accepted: %v", err)
			}
		})
	}
}

func TestAnswersRequireExplicitValidChoiceAndAllowDeferral(t *testing.T) {
	doc := sample()
	for name, answer := range map[string]struct {
		question, text, disposition string
		options                     []string
		wantError                   bool
	}{
		"blank cannot silently approve": {question: "initial-scope", disposition: "answered", wantError: true},
		"explicit choice":               {question: "initial-scope", disposition: "answered", options: []string{"reading"}},
		"free text alternate":           {question: "initial-scope", disposition: "answered", text: "先与产品讨论范围"},
		"defer remains explicit":        {question: "initial-scope", disposition: "deferred", text: "需要产品回应"},
		"single rejects multiple":       {question: "initial-scope", disposition: "answered", options: []string{"reading", "editing"}, wantError: true},
		"unknown option":                {question: "initial-scope", disposition: "answered", options: []string{"ship-all"}, wantError: true},
		"unknown question":              {question: "foreign", disposition: "answered", text: "yes", wantError: true},
		"duplicate selected id":         {question: "initial-scope", disposition: "answered", options: []string{"reading", "reading"}, wantError: true},
		"defer is not a choice":         {question: "initial-scope", disposition: "deferred", options: []string{"reading"}, wantError: true},
		"cannot write product state":    {question: "initial-scope", disposition: "confirmed", text: "yes", wantError: true},
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateAnswer(&doc, answer.question, answer.options, answer.text, answer.disposition)
			if (err != nil) != answer.wantError {
				t.Fatalf("got %v; want error %v", err, answer.wantError)
			}
		})
	}
	doc.Questions[0].Selection = "multiple"
	if err := ValidateAnswer(&doc, "initial-scope", []string{"reading", "editing"}, "", "answered"); err != nil {
		t.Fatal(err)
	}
}

func TestQuestionFingerprintTracksOnlyItsDependencies(t *testing.T) {
	doc := sample()
	before := QuestionFingerprint(&doc, doc.Questions[0])
	doc.Sources = append(doc.Sources, Source{ID: "unrelated", Ref: "new-file"})
	doc.Items = append(doc.Items, Item{ID: "unrelated", Detail: "不相关的补充"})
	if after := QuestionFingerprint(&doc, doc.Questions[0]); after != before {
		t.Fatal("unrelated material invalidated this answer")
	}
	doc.Sources[0].SHA256 = strings.Repeat("c", 64)
	if after := QuestionFingerprint(&doc, doc.Questions[0]); after == before {
		t.Fatal("changed source digest left an answer fingerprint valid")
	}
}

func TestSourceDisplayAliasesDoNotReopenAnUnchangedQuestion(t *testing.T) {
	doc := sample()
	before := QuestionFingerprint(&doc, doc.Questions[0])
	doc.Sources[0].ID = "another-display-alias"
	doc.Sources[0].Coverage = "已补看页眉与页脚"
	doc.Items[0].SourceIDs = []string{"editing-slides", "another-display-alias"}
	if after := QuestionFingerprint(&doc, doc.Questions[0]); before != after {
		t.Fatal("display alias, source ordering or coverage prose changed an unchanged business question")
	}
	doc.Items[0].Detail = "新增了实际业务限制"
	if after := QuestionFingerprint(&doc, doc.Questions[0]); before == after {
		t.Fatal("changed business meaning did not invalidate the question")
	}
}

func TestKnowledgeVersionMustBeExplicitRatherThanEncodedInRef(t *testing.T) {
	doc := sample()
	doc.Sources[0].Kind, doc.Sources[0].Ref = "knowledge", "kb_test@1"
	if err := Validate(&doc); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "separate") {
		t.Fatalf("ambiguous knowledge version accepted: %v", err)
	}
	doc.Sources[0].Ref, doc.Sources[0].Version = "kb_test", 1
	if err := Validate(&doc); err != nil {
		t.Fatalf("explicit knowledge version rejected: %v", err)
	}
}

func TestDecodeWithPreviousPreservesUnchangedObjectsAndSources(t *testing.T) {
	previous := sample()
	delta := Document{
		Version:             Version,
		Summary:             "补充了新材料，未变内容沿用上一版。",
		PreserveItemIDs:     []string{"java-scope"},
		PreserveQuestionIDs: []string{"initial-scope"},
	}

	got, err := DecodeWithPrevious(framed(delta), &previous)
	if err != nil {
		t.Fatalf("preserved delta rejected: %v", err)
	}
	if got.PreserveItemIDs != nil || got.PreserveQuestionIDs != nil {
		t.Fatalf("preserve fields leaked into expanded document: %+v", got)
	}
	if len(got.Items) != len(previous.Items) || !reflect.DeepEqual(got.Items[0], previous.Items[0]) {
		t.Fatalf("unchanged item was rewritten: got %+v, want %+v", got.Items, previous.Items)
	}
	if len(got.Questions) != len(previous.Questions) || !reflect.DeepEqual(got.Questions[0], previous.Questions[0]) {
		t.Fatalf("unchanged question was rewritten: got %+v, want %+v", got.Questions, previous.Questions)
	}
	if !reflect.DeepEqual(got.Sources, previous.Sources) {
		t.Fatalf("required old sources were not retained in order: got %+v, want %+v", got.Sources, previous.Sources)
	}
	if got.Summary == previous.Summary {
		t.Fatal("delta summary was unexpectedly replaced by previous summary")
	}
}

func TestDecodeWithPreviousChangedItemKeepsPreservedQuestionAndChangesFingerprint(t *testing.T) {
	previous := sample()
	delta := previous
	delta.Summary = "实现细节发生变化。"
	delta.Items = []Item{{
		ID:             previous.Items[0].ID,
		Kind:           previous.Items[0].Kind,
		Title:          previous.Items[0].Title,
		Detail:         previous.Items[0].Detail + " 新增了 JDK 版本约束。",
		SourceIDs:      append([]string(nil), previous.Items[0].SourceIDs...),
		Basis:          previous.Items[0].Basis,
		Impact:         previous.Items[0].Impact,
		Recommendation: previous.Items[0].Recommendation,
	}}
	delta.Sources = nil
	delta.Questions = nil
	delta.PreserveItemIDs = nil
	delta.PreserveQuestionIDs = []string{previous.Questions[0].ID}

	got, err := DecodeWithPrevious(framed(delta), &previous)
	if err != nil {
		t.Fatalf("changed delta rejected: %v", err)
	}
	if len(got.Sources) != len(previous.Sources) || !reflect.DeepEqual(got.Sources, previous.Sources) {
		t.Fatalf("old source dependencies were not filled: got %+v, want %+v", got.Sources, previous.Sources)
	}
	if got.Items[0].ID != previous.Items[0].ID || got.Items[0].Detail == previous.Items[0].Detail {
		t.Fatalf("changed item was not retained: got %+v", got.Items[0])
	}
	if len(got.Questions) != 1 || !reflect.DeepEqual(got.Questions[0], previous.Questions[0]) {
		t.Fatalf("preserved question was rewritten: got %+v, want %+v", got.Questions, previous.Questions)
	}
	if ItemFingerprint(&previous, previous.Items[0]) == ItemFingerprint(got, got.Items[0]) {
		t.Fatal("changed item kept the previous fingerprint")
	}
	if QuestionFingerprint(&previous, previous.Questions[0]) == QuestionFingerprint(got, got.Questions[0]) {
		t.Fatal("preserved question did not reflect the changed item fingerprint")
	}
}

func TestDecodeWithPreviousRejectsPreserveCollisionsAndMissingBase(t *testing.T) {
	previous := sample()
	cases := map[string]struct {
		previous *Document
		delta    Document
	}{
		"explicit item collision": {
			previous: &previous,
			delta: Document{
				Version:         Version,
				Summary:         "冲突",
				Items:           []Item{previous.Items[0]},
				PreserveItemIDs: []string{previous.Items[0].ID},
			},
		},
		"explicit question collision": {
			previous: &previous,
			delta: Document{
				Version:             Version,
				Summary:             "冲突",
				Questions:           []Question{previous.Questions[0]},
				PreserveQuestionIDs: []string{previous.Questions[0].ID},
			},
		},
		"missing previous document": {
			previous: nil,
			delta: Document{
				Version:         Version,
				Summary:         "缺少基线",
				PreserveItemIDs: []string{previous.Items[0].ID},
			},
		},
		"unknown preserved item": {
			previous: &previous,
			delta: Document{
				Version:         Version,
				Summary:         "未知对象",
				PreserveItemIDs: []string{"missing-item"},
			},
		},
		"duplicate preserved item": {
			previous: &previous,
			delta: Document{
				Version:         Version,
				Summary:         "重复引用",
				PreserveItemIDs: []string{previous.Items[0].ID, previous.Items[0].ID},
			},
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeWithPrevious(framed(testCase.delta), testCase.previous); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid preserve delta accepted: %v", err)
			}
		})
	}
}

func TestDecodeWithPreviousRejectsChangedSourceIdentityForFinalItem(t *testing.T) {
	previous := sample()
	delta := Document{
		Version: Version,
		Summary: "来源身份发生变化。",
		Sources: []Source{{
			ID:          previous.Sources[0].ID,
			Kind:        previous.Sources[0].Kind,
			Ref:         previous.Sources[0].Ref,
			SHA256:      strings.Repeat("c", 64),
			ReadStatus:  previous.Sources[0].ReadStatus,
			Limitations: previous.Sources[0].Limitations,
		}},
		Items: []Item{{
			ID:        "changed-item",
			Kind:      "normal",
			Title:     "新的条目",
			Detail:    "仍然引用旧来源 ID，但来源身份已变化。",
			SourceIDs: []string{previous.Sources[0].ID},
			Basis:     "proposed",
		}},
	}
	if _, err := DecodeWithPrevious(framed(delta), &previous); !errors.Is(err, ErrInvalid) {
		t.Fatalf("changed source identity accepted: %v", err)
	}
}

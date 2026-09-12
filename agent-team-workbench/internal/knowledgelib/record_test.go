package knowledgelib

import (
	"encoding/json"
	"strings"
	"testing"
)

const sampleDoc = `---
schema_version: kb-note/0.2-draft
id: doc:order-cancel
kind: business.rule
title: 订单取消与设备占用
summary: 取消订单时设备占用的处理要求。
about: [entity:order-service, entity:device-service]
domains: [order, device]
---

# 订单取消与设备占用

## 知识条目

### 设备占用释放要求

` + "```yaml" + `
kind: assertion
id: assertion:cancel-release-policy
about: [entity:order-cancel, entity:device-reservation]
perspective: normative
basis: source_statement
scope:
  conditions:
    - 订单取消成功，且该订单存在关联的设备占用。
  environments: []
evidence:
  - evidence_id: evidence:req-cancel-release
    role: supports
` + "```" + `

#### 陈述

系统应释放该订单关联的设备占用。

#### 说明与未知

原需求没有明确释放时限。

### 订单服务发布取消事件

` + "```yaml" + `
kind: relation
id: relation:order-publishes-cancel
from: {kind: entity, id: entity:order-service}
predicate: publishes
to: {kind: entity, id: entity:order-cancelled-event}
perspective: descriptive
basis: code_static
scope:
  conditions: []
  environments: []
evidence:
  - evidence_id: evidence:order-publish-call
    role: supports
` + "```" + `
`

func TestParseDocumentAccepted(t *testing.T) {
	doc, err := ParseDocument("content/business/rules/order-cancel.md", []byte(sampleDoc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if doc.Frontmatter.ID != "doc:order-cancel" {
		t.Fatalf("unexpected id %q", doc.Frontmatter.ID)
	}
	if len(doc.Assertions) != 1 || len(doc.Relations) != 1 {
		t.Fatalf("expected 1 assertion and 1 relation, got %d/%d", len(doc.Assertions), len(doc.Relations))
	}
	a := doc.Assertions[0]
	if a.Statement != "系统应释放该订单关联的设备占用。" {
		t.Fatalf("statement extraction failed: %q", a.Statement)
	}
	if a.UnknownNotes != "原需求没有明确释放时限。" {
		t.Fatalf("unknown notes extraction failed: %q", a.UnknownNotes)
	}
	if a.Scope.Conditions[0] != "订单取消成功，且该订单存在关联的设备占用。" {
		t.Fatalf("scope conditions not preserved: %+v", a.Scope)
	}
	if a.Evidence[0].EvidenceID != "evidence:req-cancel-release" || a.Evidence[0].Role != "supports" {
		t.Fatalf("evidence ref wrong: %+v", a.Evidence)
	}
	r := doc.Relations[0]
	if r.Predicate != "publishes" || r.From.ID != "entity:order-service" || r.To.ID != "entity:order-cancelled-event" {
		t.Fatalf("relation wrong: %+v", r)
	}
	if doc.Digest == "" || !strings.HasPrefix(doc.Digest, "sha256:") {
		t.Fatalf("missing canonical digest: %q", doc.Digest)
	}
}

func TestParseDocumentRejections(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(string) string
		wantSub string
	}{
		{
			name: "statement duplicated in metadata",
			mutate: func(s string) string {
				return strings.Replace(s, "perspective: normative", "perspective: normative\nstatement: 另一份陈述", 1)
			},
			wantSub: "statement",
		},
		{
			name: "model supplied digest",
			mutate: func(s string) string {
				return strings.Replace(s, "perspective: normative", "perspective: normative\ncontent_digest: sha256:deadbeef", 1)
			},
			wantSub: "content_digest",
		},
		{
			name: "model declared approval",
			mutate: func(s string) string {
				return strings.Replace(s, "perspective: normative", "perspective: normative\nconfirmed: true", 1)
			},
			wantSub: "confirmed",
		},
		{
			name: "unknown metadata key",
			mutate: func(s string) string {
				return strings.Replace(s, "perspective: normative", "perspective: normative\nconfidence: 0.9", 1)
			},
			wantSub: "未知",
		},
		{
			name: "bad perspective",
			mutate: func(s string) string {
				return strings.Replace(s, "perspective: normative", "perspective: maybe", 1)
			},
			wantSub: "perspective",
		},
		{
			name: "bad basis",
			mutate: func(s string) string {
				return strings.Replace(s, "basis: source_statement", "basis: vibes", 1)
			},
			wantSub: "basis",
		},
		{
			name: "empty about",
			mutate: func(s string) string {
				return strings.Replace(s, "about: [entity:order-cancel, entity:device-reservation]", "about: []", 1)
			},
			wantSub: "about",
		},
		{
			name: "missing statement section",
			mutate: func(s string) string {
				return strings.Replace(s, "#### 陈述\n\n系统应释放该订单关联的设备占用。\n", "", 1)
			},
			wantSub: "陈述",
		},
		{
			name: "empty statement section",
			mutate: func(s string) string {
				return strings.Replace(s, "系统应释放该订单关联的设备占用。", "   ", 1)
			},
			wantSub: "为空",
		},
		{
			name: "relation with unknown predicate",
			mutate: func(s string) string {
				return strings.Replace(s, "predicate: publishes", "predicate: likes", 1)
			},
			wantSub: "predicate",
		},
		{
			name: "relation self loop",
			mutate: func(s string) string {
				return strings.Replace(s, "to: {kind: entity, id: entity:order-cancelled-event}", "to: {kind: entity, id: entity:order-service}", 1)
			},
			wantSub: "相同",
		},
		{
			name: "evidence role outside vocabulary",
			mutate: func(s string) string {
				return strings.Replace(s, "role: supports", "role: proves", 1)
			},
			wantSub: "角色",
		},
		{
			name: "wrong schema version",
			mutate: func(s string) string {
				return strings.Replace(s, "schema_version: kb-note/0.2-draft", "schema_version: kb-note/9.9", 1)
			},
			wantSub: "schema_version",
		},
		{
			name: "document id without prefix",
			mutate: func(s string) string {
				return strings.Replace(s, "id: doc:order-cancel", "id: order-cancel", 1)
			},
			wantSub: "doc:",
		},
		{
			name: "missing frontmatter",
			mutate: func(s string) string {
				return strings.TrimPrefix(s, "---\nschema_version: kb-note/0.2-draft\nid: doc:order-cancel\nkind: business.rule\ntitle: 订单取消与设备占用\nsummary: 取消订单时设备占用的处理要求。\nabout: [entity:order-service, entity:device-service]\ndomains: [order, device]\n---\n")
			},
			wantSub: "frontmatter",
		},
		{
			name: "duplicate record id",
			mutate: func(s string) string {
				return s + "\n### 重复块\n\n```yaml\nkind: assertion\nid: assertion:cancel-release-policy\nabout: [entity:order-cancel]\nperspective: normative\nbasis: inferred\nscope: {conditions: [], environments: []}\nevidence: []\n```\n\n#### 陈述\n\n重复。\n"
			},
			wantSub: "重复",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDocument("content/x.md", []byte(tc.mutate(sampleDoc)))
			if err == nil {
				t.Fatalf("expected rejection, got success")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not mention %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestParseIgnoresHeadingsInsideFences(t *testing.T) {
	fence := "```"
	doc := strings.Join([]string{
		"---",
		"schema_version: kb-note/0.2-draft",
		"id: doc:fenced",
		"kind: components.service",
		"title: 围栏测试",
		"summary: 代码围栏中的标题不参与块边界判断。",
		"about: []",
		"domains: []",
		"---",
		"",
		"## 知识条目",
		"",
		"### 块一",
		"",
		fence + "yaml",
		"kind: assertion",
		"id: assertion:fenced-one",
		"about: [entity:a]",
		"perspective: descriptive",
		"basis: code_static",
		"scope: {conditions: [], environments: []}",
		"evidence: []",
		fence,
		"",
		"#### 陈述",
		"",
		"第一块。",
		"",
		fence + "java",
		"### 这不是标题",
		fence,
		"",
		"#### 说明与未知",
		"",
		"已忽略围栏内标题。",
		"",
	}, "\n")
	doc2, err := ParseDocument("content/components/fenced.md", []byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(doc2.Assertions) != 1 || doc2.Assertions[0].ID != "assertion:fenced-one" {
		t.Fatalf("unexpected assertions: %+v", doc2.Assertions)
	}
	// The fenced pseudo-heading must not split the block: the 说明与未知
	// section after it still belongs to this assertion.
	if doc2.Assertions[0].UnknownNotes != "已忽略围栏内标题。" {
		t.Fatalf("fenced heading split the block: %+v", doc2.Assertions[0])
	}
	if !strings.Contains(doc2.Assertions[0].Statement, "### 这不是标题") {
		t.Fatalf("statement should keep the fenced block verbatim: %q", doc2.Assertions[0].Statement)
	}
}

func TestNormalizeEndpointKindAcceptsEntityKinds(t *testing.T) {
	for _, kind := range []string{"entity", "service", "event", "api", "data", "capability", "component", "topic"} {
		if got := NormalizeEndpointKind(kind); got != EndpointEntity {
			t.Fatalf("kind %q should normalise to entity, got %q", kind, got)
		}
	}
	if got := NormalizeEndpointKind("document"); got != EndpointDocument {
		t.Fatalf("document must stay document, got %q", got)
	}
	if got := NormalizeEndpointKind("assertion"); got != EndpointAssertion {
		t.Fatalf("assertion must stay assertion, got %q", got)
	}
	if got := NormalizeEndpointKind("nonsense"); got != "" {
		t.Fatalf("an unknown kind must stay unknown, got %q", got)
	}
}

func TestParseDocumentAcceptsEntityKindEndpoint(t *testing.T) {
	doc := strings.Replace(sampleDoc,
		"from: {kind: entity, id: entity:order-service}",
		"from: {kind: service, id: entity:order-service}", 1)
	if _, err := ParseDocument("content/x.md", []byte(doc)); err != nil {
		t.Fatalf("an entity kind must be an accepted endpoint spelling: %v", err)
	}
	bad := strings.Replace(sampleDoc,
		"from: {kind: entity, id: entity:order-service}",
		"from: {kind: banana, id: entity:order-service}", 1)
	if _, err := ParseDocument("content/x.md", []byte(bad)); err == nil {
		t.Fatal("an unknown endpoint kind must still be rejected")
	}
}

func TestRelationFieldGuidanceIsActionable(t *testing.T) {
	doc := strings.Replace(sampleDoc,
		"predicate: publishes",
		"about: [entity:order-service]\npredicate: publishes", 1)
	_, err := ParseDocument("content/x.md", []byte(doc))
	if err == nil {
		t.Fatal("about on a relation must be rejected")
	}
	if !strings.Contains(err.Error(), "relation 块不使用 about") {
		t.Fatalf("the rejection must say how to fix it: %v", err)
	}
	missing := strings.Replace(sampleDoc, "perspective: descriptive\n", "", 1)
	if _, err := ParseDocument("content/x.md", []byte(missing)); err == nil {
		t.Fatal("a relation without perspective must be rejected")
	}
}

// TestRecordJSONUsesContractKeys pins the wire shape of the records the
// library re-serializes into its projection. A missing JSON tag would publish
// Go field names (EvidenceID, Conditions) to every client.
func TestRecordJSONUsesContractKeys(t *testing.T) {
	scope, err := json.Marshal(Scope{Conditions: []string{"c"}, Environments: []string{"prod"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(scope) != `{"conditions":["c"],"environments":["prod"]}` {
		t.Fatalf("scope wire shape wrong: %s", scope)
	}
	refs, err := json.Marshal([]EvidenceRef{{EvidenceID: "evidence:x", Role: "supports"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(refs) != `[{"evidence_id":"evidence:x","role":"supports"}]` {
		t.Fatalf("evidence wire shape wrong: %s", refs)
	}
	endpoint, err := json.Marshal(Endpoint{Kind: "entity", ID: "entity:x"})
	if err != nil {
		t.Fatal(err)
	}
	if string(endpoint) != `{"kind":"entity","id":"entity:x"}` {
		t.Fatalf("endpoint wire shape wrong: %s", endpoint)
	}
	front, err := json.Marshal(DocumentFrontmatter{SchemaVersion: RecordSyntaxVersion, ID: "doc:x", Title: "t", Kind: "k"})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"schema_version", "\"id\"", "\"title\"", "\"kind\""} {
		if !strings.Contains(string(front), key) {
			t.Fatalf("frontmatter wire shape missing %s: %s", key, front)
		}
	}
}

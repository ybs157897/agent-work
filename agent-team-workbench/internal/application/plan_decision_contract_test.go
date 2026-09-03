package application

// This file guards the canonical schema structure without implementing a
// second JSON Schema validator. Runtime rejection is proved by decoder fixture
// tests when the production decoder consumes this same schema.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

const planDecisionV2SchemaID = "https://workbench.example/contracts/control/plan-decision-v2.schema.json"

func loadPlanDecisionV2Schema(t *testing.T) map[string]any {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位 PlanDecisionV2 契约测试文件")
	}
	dir := filepath.Dir(file)
	for {
		path := filepath.Join(dir, "contracts", "control", "plan-decision-v2.schema.json")
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("读取 %s 失败: %v", path, readErr)
			}
			var doc map[string]any
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatalf("解析 %s 失败（契约必须是合法 JSON）: %v", path, err)
			}
			return doc
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("无法定位同时包含 go.mod 与 PlanDecisionV2 schema 的模块根")
		}
		dir = parent
	}
}

func contractObject(t *testing.T, raw any, path string) map[string]any {
	t.Helper()
	obj, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("%s 必须是 object，实际 %T", path, raw)
	}
	return obj
}

func contractArray(t *testing.T, raw any, path string) []any {
	t.Helper()
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("%s 必须是 array，实际 %T", path, raw)
	}
	return items
}

func contractStrings(t *testing.T, raw any, path string) []string {
	t.Helper()
	items := contractArray(t, raw, path)
	out := make([]string, 0, len(items))
	for i, item := range items {
		value, ok := item.(string)
		if !ok {
			t.Fatalf("%s[%d] 必须是 string，实际 %T", path, i, item)
		}
		out = append(out, value)
	}
	return out
}

func requireExactStrings(t *testing.T, path string, got, want []string) {
	t.Helper()
	got = append([]string(nil), got...)
	want = append([]string(nil), want...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("%s 字段闭集不一致：got=%v want=%v", path, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s 字段闭集不一致：got=%v want=%v", path, got, want)
		}
	}
}

func requireExactProperties(t *testing.T, def map[string]any, path string, want ...string) map[string]any {
	t.Helper()
	props := contractObject(t, def["properties"], path+".properties")
	got := make([]string, 0, len(props))
	for name := range props {
		got = append(got, name)
	}
	requireExactStrings(t, path+".properties", got, want)
	return props
}

func requireExactRequired(t *testing.T, def map[string]any, path string, want ...string) {
	t.Helper()
	requireExactStrings(t, path+".required", contractStrings(t, def["required"], path+".required"), want)
}

func requireClosedObject(t *testing.T, def map[string]any, path string) {
	t.Helper()
	if def["type"] != "object" {
		t.Fatalf("%s.type 必须是 object，实际 %v", path, def["type"])
	}
	if def["additionalProperties"] != false {
		t.Fatalf("%s.additionalProperties 必须为 false，未知字段不得静默进入控制面", path)
	}
}

func requireJSONNumber(t *testing.T, def map[string]any, path, key string, want float64) {
	t.Helper()
	if def[key] != want {
		t.Fatalf("%s.%s 必须为 %v，实际 %v", path, key, want, def[key])
	}
}

func requireJSONType(t *testing.T, def map[string]any, path, want string) {
	t.Helper()
	if def["type"] != want {
		t.Fatalf("%s.type 必须为 %s，实际 %v", path, want, def["type"])
	}
}

func planDecisionDef(t *testing.T, doc map[string]any, name string) map[string]any {
	t.Helper()
	defs := contractObject(t, doc["$defs"], "$defs")
	def, ok := defs[name]
	if !ok {
		t.Fatalf("$defs.%s 缺失", name)
	}
	return contractObject(t, def, "$defs."+name)
}

func TestPlanDecisionV2EnvelopeIsClosedAndVersioned(t *testing.T) {
	doc := loadPlanDecisionV2Schema(t)
	if doc["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("$schema 必须固定为 draft 2020-12，实际 %v", doc["$schema"])
	}
	if doc["$id"] != planDecisionV2SchemaID {
		t.Fatalf("$id 漂移：got=%v want=%s", doc["$id"], planDecisionV2SchemaID)
	}
	requireClosedObject(t, doc, "PlanDecisionV2")
	requireExactRequired(t, doc, "PlanDecisionV2", "schema_version", "kind", "reason", "next_action", "steps")
	props := requireExactProperties(t, doc, "PlanDecisionV2", "schema_version", "kind", "reason", "next_action", "steps")

	version := contractObject(t, props["schema_version"], "properties.schema_version")
	if version["const"] != "plan-decision/v2" {
		t.Fatalf("schema_version.const 漂移：%v", version["const"])
	}
	kind := contractObject(t, props["kind"], "properties.kind")
	if kind["const"] != "plan" {
		t.Fatalf("kind.const 必须为 plan，实际 %v", kind["const"])
	}

	reason := contractObject(t, props["reason"], "properties.reason")
	if reason["type"] != "string" {
		t.Fatalf("reason 必须是 string")
	}
	requireJSONNumber(t, reason, "properties.reason", "minLength", 1)
	requireJSONNumber(t, reason, "properties.reason", "maxLength", 2000)

	nextAction := contractObject(t, props["next_action"], "properties.next_action")
	if nextAction["type"] != "string" {
		t.Fatalf("next_action 必须是 string")
	}
	requireJSONNumber(t, nextAction, "properties.next_action", "minLength", 1)
	requireJSONNumber(t, nextAction, "properties.next_action", "maxLength", 1000)

	steps := contractObject(t, props["steps"], "properties.steps")
	if steps["type"] != "array" {
		t.Fatalf("steps 必须是 array")
	}
	requireJSONNumber(t, steps, "properties.steps", "minItems", 1)
	requireJSONNumber(t, steps, "properties.steps", "maxItems", 64)
}

func TestPlanDecisionV2VerbClosureMatchesDomain(t *testing.T) {
	doc := loadPlanDecisionV2Schema(t)
	stepDefs := map[domain.PlanVerb]string{
		domain.PlanVerbDispatch:         "DispatchStep",
		domain.PlanVerbConsultKnowledge: "ConsultKnowledgeStep",
		domain.PlanVerbDefer:            "DeferStep",
		domain.PlanVerbJoin:             "JoinStep",
		domain.PlanVerbFinish:           "FinishStep",
	}

	props := contractObject(t, doc["properties"], "properties")
	steps := contractObject(t, props["steps"], "properties.steps")
	items := contractObject(t, steps["items"], "properties.steps.items")
	oneOf := contractArray(t, items["oneOf"], "properties.steps.items.oneOf")
	gotRefs := make([]string, 0, len(oneOf))
	for i, raw := range oneOf {
		entry := contractObject(t, raw, "properties.steps.items.oneOf")
		ref, ok := entry["$ref"].(string)
		if !ok {
			t.Fatalf("steps.items.oneOf[%d] 必须只引用闭合 step definition", i)
		}
		gotRefs = append(gotRefs, ref)
	}
	wantRefs := make([]string, 0, len(stepDefs))
	for verb, defName := range stepDefs {
		if !domain.ValidPlanVerb(verb) {
			t.Errorf("schema verb %q 未被 domain.ValidPlanVerb 接受", verb)
		}
		wantRefs = append(wantRefs, "#/$defs/"+defName)

		def := planDecisionDef(t, doc, defName)
		requireClosedObject(t, def, "$defs."+defName)
		props := contractObject(t, def["properties"], "$defs."+defName+".properties")
		verbDef := contractObject(t, props["verb"], "$defs."+defName+".properties.verb")
		if verbDef["const"] != string(verb) {
			t.Errorf("$defs.%s verb.const=%v，want=%q", defName, verbDef["const"], verb)
		}
	}
	requireExactStrings(t, "steps.items.oneOf", gotRefs, wantRefs)
	if domain.ValidPlanVerb(domain.PlanVerb("unknown")) {
		t.Fatal("domain.ValidPlanVerb 不得接受未知动词")
	}
}

func TestPlanDecisionV2StepFieldMatrixAndBounds(t *testing.T) {
	doc := loadPlanDecisionV2Schema(t)
	tests := []struct {
		name       string
		required   []string
		properties []string
	}{
		{
			name:       "DispatchStep",
			required:   []string{"verb", "agent_id", "title", "instruction", "acceptance"},
			properties: []string{"verb", "agent_id", "title", "instruction", "acceptance", "priority", "knowledge_from"},
		},
		{
			name:       "ConsultKnowledgeStep",
			required:   []string{"verb", "corpus", "terms"},
			properties: []string{"verb", "corpus", "terms", "limit"},
		},
		{name: "DeferStep", required: []string{"verb"}, properties: []string{"verb", "wake_at"}},
		{name: "JoinStep", required: []string{"verb", "children"}, properties: []string{"verb", "children", "wake_at"}},
		{name: "FinishStep", required: []string{"verb"}, properties: []string{"verb", "evaluation"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			def := planDecisionDef(t, doc, tc.name)
			requireClosedObject(t, def, "$defs."+tc.name)
			requireExactRequired(t, def, "$defs."+tc.name, tc.required...)
			requireExactProperties(t, def, "$defs."+tc.name, tc.properties...)
		})
	}

	dispatch := requireExactProperties(t, planDecisionDef(t, doc, "DispatchStep"), "$defs.DispatchStep",
		"verb", "agent_id", "title", "instruction", "acceptance", "priority", "knowledge_from")
	agentIDRef := contractObject(t, dispatch["agent_id"], "$defs.DispatchStep.properties.agent_id")
	if agentIDRef["$ref"] != "#/$defs/AgentID" {
		t.Fatalf("dispatch.agent_id 必须引用 AgentID，实际 %v", agentIDRef["$ref"])
	}
	for _, field := range []struct {
		name string
		min  float64
		max  float64
	}{
		{name: "title", min: 1, max: 200},
		{name: "instruction", min: 1, max: 20000},
	} {
		def := contractObject(t, dispatch[field.name], "$defs.DispatchStep.properties."+field.name)
		requireJSONType(t, def, "$defs.DispatchStep.properties."+field.name, "string")
		requireJSONNumber(t, def, "$defs.DispatchStep.properties."+field.name, "minLength", field.min)
		requireJSONNumber(t, def, "$defs.DispatchStep.properties."+field.name, "maxLength", field.max)
	}
	acceptance := contractObject(t, dispatch["acceptance"], "$defs.DispatchStep.properties.acceptance")
	requireJSONType(t, acceptance, "$defs.DispatchStep.properties.acceptance", "array")
	requireJSONNumber(t, acceptance, "$defs.DispatchStep.properties.acceptance", "minItems", 1)
	requireJSONNumber(t, acceptance, "$defs.DispatchStep.properties.acceptance", "maxItems", 32)
	acceptanceItem := contractObject(t, acceptance["items"], "$defs.DispatchStep.properties.acceptance.items")
	requireJSONType(t, acceptanceItem, "$defs.DispatchStep.properties.acceptance.items", "string")
	requireJSONNumber(t, acceptanceItem, "$defs.DispatchStep.properties.acceptance.items", "minLength", 1)
	requireJSONNumber(t, acceptanceItem, "$defs.DispatchStep.properties.acceptance.items", "maxLength", 1000)
	priority := contractObject(t, dispatch["priority"], "$defs.DispatchStep.properties.priority")
	requireJSONType(t, priority, "$defs.DispatchStep.properties.priority", "string")
	requireExactStrings(t, "$defs.DispatchStep.properties.priority.enum",
		contractStrings(t, priority["enum"], "$defs.DispatchStep.properties.priority.enum"),
		[]string{"low", "medium", "high", "urgent"})
	knowledgeFrom := contractObject(t, dispatch["knowledge_from"], "$defs.DispatchStep.properties.knowledge_from")
	requireJSONType(t, knowledgeFrom, "$defs.DispatchStep.properties.knowledge_from", "integer")
	requireJSONNumber(t, knowledgeFrom, "$defs.DispatchStep.properties.knowledge_from", "minimum", 0)
	requireJSONNumber(t, knowledgeFrom, "$defs.DispatchStep.properties.knowledge_from", "maximum", 63)

	consult := requireExactProperties(t, planDecisionDef(t, doc, "ConsultKnowledgeStep"), "$defs.ConsultKnowledgeStep",
		"verb", "corpus", "terms", "limit")
	corpus := contractObject(t, consult["corpus"], "$defs.ConsultKnowledgeStep.properties.corpus")
	requireJSONType(t, corpus, "$defs.ConsultKnowledgeStep.properties.corpus", "string")
	requireJSONNumber(t, corpus, "$defs.ConsultKnowledgeStep.properties.corpus", "minLength", 1)
	requireJSONNumber(t, corpus, "$defs.ConsultKnowledgeStep.properties.corpus", "maxLength", 128)
	if corpus["pattern"] != "^[^/\\\\\\r\\n]+$" {
		t.Fatalf("corpus pattern 必须拒绝路径分隔符与换行，实际 %v", corpus["pattern"])
	}
	corpusNot := contractObject(t, corpus["not"], "$defs.ConsultKnowledgeStep.properties.corpus.not")
	requireExactStrings(t, "$defs.ConsultKnowledgeStep.properties.corpus.not.enum",
		contractStrings(t, corpusNot["enum"], "$defs.ConsultKnowledgeStep.properties.corpus.not.enum"),
		[]string{".", ".."})
	terms := contractObject(t, consult["terms"], "$defs.ConsultKnowledgeStep.properties.terms")
	requireJSONType(t, terms, "$defs.ConsultKnowledgeStep.properties.terms", "array")
	requireJSONNumber(t, terms, "$defs.ConsultKnowledgeStep.properties.terms", "minItems", 1)
	requireJSONNumber(t, terms, "$defs.ConsultKnowledgeStep.properties.terms", "maxItems", 32)
	term := contractObject(t, terms["items"], "$defs.ConsultKnowledgeStep.properties.terms.items")
	requireJSONType(t, term, "$defs.ConsultKnowledgeStep.properties.terms.items", "string")
	requireJSONNumber(t, term, "$defs.ConsultKnowledgeStep.properties.terms.items", "minLength", 1)
	requireJSONNumber(t, term, "$defs.ConsultKnowledgeStep.properties.terms.items", "maxLength", 500)
	limit := contractObject(t, consult["limit"], "$defs.ConsultKnowledgeStep.properties.limit")
	requireJSONType(t, limit, "$defs.ConsultKnowledgeStep.properties.limit", "integer")
	requireJSONNumber(t, limit, "$defs.ConsultKnowledgeStep.properties.limit", "minimum", 1)
	requireJSONNumber(t, limit, "$defs.ConsultKnowledgeStep.properties.limit", "maximum", 50)

	join := requireExactProperties(t, planDecisionDef(t, doc, "JoinStep"), "$defs.JoinStep", "verb", "children", "wake_at")
	children := contractObject(t, join["children"], "$defs.JoinStep.properties.children")
	childrenOneOf := contractArray(t, children["oneOf"], "$defs.JoinStep.properties.children.oneOf")
	if len(childrenOneOf) != 2 {
		t.Fatalf("join.children.oneOf 必须恰好为 all 或 WorkItem ID array")
	}
	all := contractObject(t, childrenOneOf[0], "$defs.JoinStep.properties.children.oneOf[0]")
	if all["const"] != "all" {
		t.Fatalf("join.children string 分支只能是 all")
	}
	childIDs := contractObject(t, childrenOneOf[1], "$defs.JoinStep.properties.children.oneOf[1]")
	requireJSONNumber(t, childIDs, "$defs.JoinStep.properties.children.oneOf[1]", "minItems", 1)
	requireJSONNumber(t, childIDs, "$defs.JoinStep.properties.children.oneOf[1]", "maxItems", 128)
	if childIDs["uniqueItems"] != true {
		t.Fatalf("join.children array 必须 uniqueItems=true")
	}
	childItem := contractObject(t, childIDs["items"], "$defs.JoinStep.properties.children.oneOf[1].items")
	if childItem["$ref"] != "#/$defs/WorkItemID" {
		t.Fatalf("join.children array 必须引用 WorkItemID，实际 %v", childItem["$ref"])
	}
}

func TestPlanDecisionV2IDAndTimeVocabularyIsTyped(t *testing.T) {
	doc := loadPlanDecisionV2Schema(t)
	agentID := planDecisionDef(t, doc, "AgentID")
	requireJSONType(t, agentID, "$defs.AgentID", "string")
	const agentIDPattern = `^agent_\S+$`
	if agentID["pattern"] != agentIDPattern {
		t.Fatalf("AgentID pattern 漂移：%v", agentID["pattern"])
	}
	agentPattern := regexp.MustCompile(agentIDPattern)
	for _, value := range []string{"agent_coordinator_worker", "agent_01ARZ3NDEKTSV4RRFFQ69G5FAV"} {
		if !agentPattern.MatchString(value) {
			t.Fatalf("AgentID pattern must accept opaque/generated ID %q", value)
		}
	}
	for _, value := range []string{"agent_", "agent_worker id", "agent_worker\t"} {
		if agentPattern.MatchString(value) {
			t.Fatalf("AgentID pattern must reject empty/whitespace suffix %q", value)
		}
	}
	workItemID := planDecisionDef(t, doc, "WorkItemID")
	requireJSONType(t, workItemID, "$defs.WorkItemID", "string")
	const workItemIDPattern = `^wi_\S+$`
	if workItemID["pattern"] != workItemIDPattern {
		t.Fatalf("WorkItemID pattern 漂移：%v", workItemID["pattern"])
	}
	workItemPattern := regexp.MustCompile(workItemIDPattern)
	for _, value := range []string{"wi_x", "wi_01ARZ3NDEKTSV4RRFFQ69G5FAV"} {
		if !workItemPattern.MatchString(value) {
			t.Fatalf("WorkItemID pattern must accept opaque/generated ID %q", value)
		}
	}
	for _, value := range []string{"wi_", "wi_child id", "wi_child\n"} {
		if workItemPattern.MatchString(value) {
			t.Fatalf("WorkItemID pattern must reject empty/whitespace suffix %q", value)
		}
	}
	wakeAt := planDecisionDef(t, doc, "WakeAt")
	if wakeAt["type"] != "string" || wakeAt["format"] != "date-time" {
		t.Fatalf("WakeAt 必须是 RFC3339 date-time string，实际 %v", wakeAt)
	}
}

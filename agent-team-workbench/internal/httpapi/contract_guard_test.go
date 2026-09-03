package httpapi

// 契约一致性门禁：让「改 handler 忘改 contracts/」在 CI 直接红灯。
//
//   门禁 A  代码路由（internal/httpapi/server.go 的 mux.HandleFunc 手工链）
//          ↔ contracts/web/openapi.yaml paths，(method, path) 集合双向对账。
//   门禁 B  事件名常量（internal/domain/events.go 的 Event* 常量）
//          ↔ contracts/events/asyncapi.yaml CanonicalEventEnvelope.type.enum 双向对账；
//            另校验 channel 地址落在已注册 SSE 路由上、aggregate 枚举与 domain 常量一致。
//
// 自省全部只读：go/parser 提取源码事实 + yaml 解析契约文件，不触碰生产代码、不起服务。
// 源码结构变化导致提取不到任何条目时直接 Fatal（防止门禁静默空转）。

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"gopkg.in/yaml.v3"
)

// ── 定位与解析基础设施 ───────────────────────────────────────────────

// moduleRootForContracts 从测试文件自身位置（go test 运行时 cwd 即包目录，双保险）
// 逐级向上找模块根；以 go.mod + 两份契约 + 两份被自省源码共存的目录为准。
func moduleRootForContracts(t *testing.T) string {
	t.Helper()
	var anchors []string
	if _, file, _, ok := runtime.Caller(0); ok && filepath.IsAbs(file) {
		anchors = append(anchors, filepath.Dir(file))
	}
	if wd, err := os.Getwd(); err == nil {
		anchors = append(anchors, wd)
	}
	for _, start := range anchors {
		dir := start
		for {
			if isModuleRootWithContracts(dir) {
				return dir
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	t.Fatal("无法定位模块根（需同时存在 go.mod、contracts/web/openapi.yaml、" +
		"contracts/events/asyncapi.yaml、internal/httpapi/server.go、internal/domain/events.go）")
	return ""
}

func isModuleRootWithContracts(dir string) bool {
	for _, rel := range []string{
		filepath.Join("go.mod"),
		filepath.Join("contracts", "web", "openapi.yaml"),
		filepath.Join("contracts", "events", "asyncapi.yaml"),
		filepath.Join("internal", "httpapi", "server.go"),
		filepath.Join("internal", "domain", "events.go"),
	} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			return false
		}
	}
	return true
}

func parseYAMLFile(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取契约文件失败: %v", err)
	}
	if err := yaml.Unmarshal(data, out); err != nil {
		t.Fatalf("解析 %s 失败: %v", path, err)
	}
}

// extractRegisteredRoutes 用 go/parser 提取 server.go 中所有
// `mux.HandleFunc("METHOD /path", ...)` 注册项，key 形如 "GET /api/v1/me"，
// value 为源码位置（便于失败信息定位）。
func extractRegisteredRoutes(t *testing.T, root string) map[string]string {
	t.Helper()
	src := filepath.Join(root, "internal", "httpapi", "server.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, src, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", src, err)
	}
	routes := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HandleFunc" {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		pattern, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		key, ok := normalizeRoutePattern(pattern)
		if !ok {
			return true
		}
		routes[key] = fset.Position(lit.Pos()).String()
		return true
	})
	if len(routes) == 0 {
		t.Fatalf("从 %s 未提取到任何路由——路由注册方式变更后必须同步更新本门禁的自省逻辑", src)
	}
	return routes
}

// normalizeRoutePattern 把 `"METHOD /p/{id}"` 归一化为 "METHOD /p/{id}"；
// 兼容旧式 ":id" 通配写法并归一到 "{id}"，路径参数形态两侧统一后可比对。
func normalizeRoutePattern(pattern string) (string, bool) {
	sp := strings.IndexByte(pattern, ' ')
	if sp <= 0 {
		return "", false
	}
	method, path := pattern[:sp], pattern[sp+1:]
	if method != strings.ToUpper(method) || !strings.HasPrefix(path, "/") {
		return "", false
	}
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		if strings.HasPrefix(seg, ":") && len(seg) > 1 {
			segs[i] = "{" + seg[1:] + "}"
		}
	}
	return method + " " + strings.Join(segs, "/"), true
}

// extractEventNameConstants 提取 internal/domain/events.go 中所有
// Event* 字符串字面量常量；出现重复字面量即 Fatal（拷贝粘贴漂移防护）。
func extractEventNameConstants(t *testing.T, root string) map[string]string {
	t.Helper()
	src := filepath.Join(root, "internal", "domain", "events.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, src, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", src, err)
	}
	consts := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		gd, ok := n.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			return true
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Values) != len(vs.Names) {
				continue // iota 等非对位声明不参与
			}
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Event") {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				val, err := strconv.Unquote(lit.Value)
				if err == nil {
					consts[name.Name] = val
				}
			}
		}
		return true
	})
	if len(consts) == 0 {
		t.Fatalf("从 %s 未提取到任何 Event* 常量——事件定义方式变更后必须同步更新本门禁的自省逻辑", src)
	}
	valueOwner := map[string]string{}
	for constName, val := range consts {
		if owner, dup := valueOwner[val]; dup {
			t.Fatalf("事件名字面量重复：%s 与 %s 均为 %q——先消除重复再对账契约", owner, constName, val)
		}
		valueOwner[val] = constName
	}
	return consts
}

type contractType string

func (t *contractType) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		*t = contractType(node.Value)
		return nil
	case yaml.SequenceNode:
		values := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode {
				return fmt.Errorf("schema type 数组只能包含字符串标量")
			}
			values = append(values, item.Value)
		}
		*t = contractType(strings.Join(values, "|"))
		return nil
	default:
		return fmt.Errorf("schema type 必须是字符串或字符串数组")
	}
}

type contractSchema struct {
	Type                 contractType               `yaml:"type"`
	Const                string                     `yaml:"const"`
	Enum                 []string                   `yaml:"enum"`
	Pattern              string                     `yaml:"pattern"`
	MinItems             int                        `yaml:"minItems"`
	MaxItems             int                        `yaml:"maxItems"`
	Required             []string                   `yaml:"required"`
	AdditionalProperties yaml.Node                  `yaml:"additionalProperties"`
	Properties           map[string]*contractSchema `yaml:"properties"`
	AllOf                []contractSchemaAllOf      `yaml:"allOf"`
	Lifecycle            string                     `yaml:"x-lifecycle"`
	EventName            string                     `yaml:"x-event-name"`
	ProducerStatus       string                     `yaml:"x-producer-status"`
	Ref                  string                     `yaml:"$ref"`
	ReadOnly             bool                       `yaml:"readOnly"`
}

type contractSchemaAllOf struct {
	If   contractSchemaCondition `yaml:"if"`
	Then contractSchemaCondition `yaml:"then"`
}

type contractSchemaCondition struct {
	Required   []string                   `yaml:"required"`
	AnyOf      []contractSchemaCondition  `yaml:"anyOf"`
	AllOf      []contractSchemaCondition  `yaml:"allOf"`
	Not        *contractSchemaCondition   `yaml:"not"`
	Properties map[string]*contractSchema `yaml:"properties"`
}

func conditionContainsMinItems(condition contractSchemaCondition, minimum int) bool {
	for _, schema := range condition.Properties {
		if schema != nil && schema.MinItems >= minimum {
			return true
		}
	}
	for _, nested := range append(append([]contractSchemaCondition{}, condition.AnyOf...), condition.AllOf...) {
		if conditionContainsMinItems(nested, minimum) {
			return true
		}
	}
	return condition.Not != nil && conditionContainsMinItems(*condition.Not, minimum)
}

var httpOperationMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

// extractOpenAPIOperations 解析 openapi.yaml：servers[0].url 作为前缀拼上每个
// path key，取其下的 HTTP operation 键，返回 (METHOD /full/path) 集合。
func extractOpenAPIOperations(t *testing.T, root string) map[string]bool {
	t.Helper()
	path := filepath.Join(root, "contracts", "web", "openapi.yaml")
	var doc struct {
		Servers []struct {
			URL string `yaml:"url"`
		} `yaml:"servers"`
		Paths map[string]yaml.Node `yaml:"paths"`
	}
	parseYAMLFile(t, path, &doc)

	prefix := ""
	if len(doc.Servers) > 0 {
		prefix = strings.TrimSuffix(doc.Servers[0].URL, "/")
	}
	ops := map[string]bool{}
	for p, node := range doc.Paths {
		full := prefix + normalizeWildcardsOnly(p)
		var items map[string]yaml.Node
		if err := node.Decode(&items); err != nil {
			t.Fatalf("openapi path %q 结构异常: %v", p, err)
		}
		for key := range items {
			if httpOperationMethods[strings.ToLower(key)] && key == strings.ToLower(key) {
				ops[strings.ToUpper(key)+" "+full] = true
			}
		}
	}
	if len(ops) == 0 {
		t.Fatalf("%s 未解析出任何 operation——契约结构变更后必须同步更新本门禁的解析逻辑", path)
	}
	return ops
}

// normalizeWildcardsOnly 仅做路径参数形态归一（":id" → "{id}"），OpenAPI path key 本就为 {id}。
func normalizeWildcardsOnly(p string) string {
	out, _ := normalizeRoutePattern("GET " + p)
	return strings.TrimPrefix(out, "GET ")
}

// assertSetDiff 对账两个集合，任一方向有差即 Errorf 并列出差集明细。
func assertSetDiff(t *testing.T, what string, codeSide map[string]bool, contractName string, contractSide map[string]bool) {
	t.Helper()
	var onlyInCode, onlyInContract []string
	for k := range codeSide {
		if !contractSide[k] {
			onlyInCode = append(onlyInCode, k)
		}
	}
	for k := range contractSide {
		if !codeSide[k] {
			onlyInContract = append(onlyInContract, k)
		}
	}
	sort.Strings(onlyInCode)
	sort.Strings(onlyInContract)
	if len(onlyInCode) == 0 && len(onlyInContract) == 0 {
		t.Logf("%s 对账通过：代码 %d 条 ↔ %s %d 条", what, len(codeSide), contractName, len(contractSide))
		return
	}
	var b strings.Builder
	if len(onlyInCode) > 0 {
		b.WriteString("代码侧有而 " + contractName + " 未声明（改了代码忘改契约）：\n")
		for _, k := range onlyInCode {
			b.WriteString("  - " + k + "\n")
		}
	}
	if len(onlyInContract) > 0 {
		b.WriteString(contractName + " 声明了而代码不存在（契约过期或路径拼写不一致）：\n")
		for _, k := range onlyInContract {
			b.WriteString("  - " + k + "\n")
		}
	}
	t.Errorf("%s 不一致：\n%s", what, strings.TrimRight(b.String(), "\n"))
}

func assertExactStringValues(t *testing.T, what string, got, want []string) {
	t.Helper()
	gotSet := make(map[string]bool, len(got))
	for _, value := range got {
		gotSet[value] = true
	}
	wantSet := make(map[string]bool, len(want))
	for _, value := range want {
		wantSet[value] = true
	}
	if len(got) != len(gotSet) {
		t.Errorf("%s 含重复值：%v", what, got)
	}
	assertSetDiff(t, what, gotSet, "expected values", wantSet)
}

// ── 门禁 A：REST 路由 ↔ openapi.yaml ─────────────────────────────────

func TestContractGuardRoutesMatchOpenAPI(t *testing.T) {
	root := moduleRootForContracts(t)
	registered := extractRegisteredRoutes(t, root)
	routeSet := make(map[string]bool, len(registered))
	for k := range registered {
		routeSet[k] = true
	}
	assertSetDiff(t, "REST 路由 ↔ openapi.yaml", routeSet, "contracts/web/openapi.yaml",
		extractOpenAPIOperations(t, root))
}

func TestContractGuardProposedGovernanceOpenAPISchemas(t *testing.T) {
	root := moduleRootForContracts(t)
	path := filepath.Join(root, "contracts", "web", "openapi.yaml")
	var doc struct {
		Paths      map[string]yaml.Node `yaml:"paths"`
		Components struct {
			Schemas map[string]*contractSchema `yaml:"schemas"`
		} `yaml:"components"`
	}
	parseYAMLFile(t, path, &doc)

	planDecision := doc.Components.Schemas["PlanDecisionV2"]
	if planDecision == nil || planDecision.Lifecycle != "live" ||
		planDecision.Ref != "../control/plan-decision-v2.schema.json" {
		t.Fatalf("PlanDecisionV2 必须是 live 且只引用 canonical JSON Schema，实际 %+v", planDecision)
	}
	if _, err := os.Stat(filepath.Join(root, "contracts", "web", planDecision.Ref)); err != nil {
		t.Fatalf("PlanDecisionV2 external $ref 不可解析: %v", err)
	}

	scalarEnums := map[string][]string{
		"PlannerControlCapability": {"structured_transport", "schema_constrained_output", "control_tool_call"},
		"GovernanceErrorCode":      {"plan_json_syntax", "plan_schema_validation", "plan_semantic_validation", "plan_authority_denied", "plan_quota_denied", "cost_price_unavailable", "usage_unresolved"},
		"GoalStatus":               {"draft", "active", "waiting", "blocked", "completed", "cancelled"},
		"TodoClass":                {"advancement", "monitor", "user_gate", "blocker", "validation"},
		"TodoStatus":               {"pending", "claimed", "running", "waiting", "completed", "blocked", "cancelled"},
		"TurnReceiptPhaseName":     {"decision_decode", "validation", "durable_writeback", "plan_compile", "dispatch", "quota_spend", "projection_outbox"},
		"QuotaKind":                {"turn_count", "active_worker", "input_tokens_total", "input_uncached_tokens", "cache_read_tokens", "cache_write_tokens", "output_tokens", "cost_microusd"},
		"HandoffStatus":            {"pending", "accepted", "transferred", "rejected", "cancelled"},
	}
	liveScalarEnums := map[string]bool{
		"PlannerControlCapability": true,
		"GovernanceErrorCode":      true,
		"GoalStatus":               true,
		"TodoClass":                true,
		"TodoStatus":               true,
		"TurnReceiptPhaseName":     true,
		"QuotaKind":                true,
		"HandoffStatus":            true,
	}
	for name, want := range scalarEnums {
		schema := doc.Components.Schemas[name]
		if schema == nil {
			t.Errorf("components.schemas.%s 缺失", name)
			continue
		}
		lifecycle := "proposed"
		if liveScalarEnums[name] {
			lifecycle = "live"
		}
		if schema.Lifecycle != lifecycle || schema.Type != "string" {
			t.Errorf("%s 必须是 x-lifecycle=%s 的 string schema", name, lifecycle)
		}
		assertExactStringValues(t, "components.schemas."+name+".enum", schema.Enum, want)
	}

	closedObjects := []string{
		"Goal", "DecisionScope", "TodoClaim", "Todo", "TurnKey", "TurnDecision",
		"GovernanceValidationError", "TurnReceiptHeader", "TurnReceiptPhase", "QuotaPolicy",
		"QuotaReservation", "CanonicalUsage", "PriceSnapshotRef", "GovernanceActorRef",
		"Handoff", "GovernanceEvidenceItem", "DeliveryBriefSnapshotCreate", "DeliveryBriefSnapshot", "ProjectionRepair", "GoalList",
		"GovernanceVersionCommand", "TodoList", "TodoClaimCommand", "TodoReleaseCommand",
		"TodoUserActionCommand", "TurnReceipt", "QuotaSpendEntry", "QuotaKindSummary", "GoalQuota", "QuotaTurn", "GoalGovernanceMetrics", "GovernanceMetrics",
		"QuotaSpendKey", "QuotaGapReconciliationCreate", "QuotaGapResolution", "QuotaGapResolutionList",
		"UsageCountersV1", "UsageProvenanceV1",
	}
	liveObjects := map[string]bool{
		"Goal": true, "DecisionScope": true, "TodoClaim": true, "Todo": true, "TurnKey": true,
		"TurnDecision":              true,
		"GovernanceValidationError": true, "TurnReceiptHeader": true, "TurnReceiptPhase": true,
		"QuotaKind": true, "QuotaPolicy": true, "QuotaReservation": true,
		"GovernanceEvidenceItem": true, "GoalList": true, "GovernanceVersionCommand": true,
		"TodoList": true, "TodoClaimCommand": true, "TodoReleaseCommand": true,
		"TodoUserActionCommand": true,
		"TurnReceipt":           true, "QuotaSpendEntry": true, "QuotaKindSummary": true,
		"CanonicalUsage": true, "UsageCountersV1": true, "UsageProvenanceV1": true, "PriceSnapshotRef": true,
		"GoalQuota": true, "QuotaTurn": true,
		"QuotaSpendKey": true, "QuotaGapReconciliationCreate": true,
		"QuotaGapResolution": true, "QuotaGapResolutionList": true,
		"GovernanceActorRef": true, "Handoff": true, "DeliveryBriefSnapshotCreate": true, "DeliveryBriefSnapshot": true, "ProjectionRepair": true,
		"GovernanceEvidenceList": true, "GovernanceGoalProjection": true,
		"ProjectionRepairList": true, "ProjectionRepairCommand": true,
		"ProjectionRepairResult": true, "GoalGovernanceMetrics": true, "GovernanceMetrics": true,
	}
	livePathRefs := map[string]bool{}
	for _, pathNode := range doc.Paths {
		collectContractRefs(&pathNode, livePathRefs)
	}
	proposedNames := []string{}
	for name := range scalarEnums {
		if !liveScalarEnums[name] {
			proposedNames = append(proposedNames, name)
		}
	}
	for _, name := range closedObjects {
		if !liveObjects[name] {
			proposedNames = append(proposedNames, name)
		}
	}
	for _, name := range proposedNames {
		if livePathRefs["#/components/schemas/"+name] {
			t.Errorf("proposed schema %s 不得在 handler 落地前被 live path 引用", name)
		}
	}
	for _, name := range closedObjects {
		schema := doc.Components.Schemas[name]
		if schema == nil {
			t.Errorf("components.schemas.%s 缺失", name)
			continue
		}
		lifecycle := "proposed"
		if liveObjects[name] {
			lifecycle = "live"
		}
		if schema.Lifecycle != lifecycle || schema.Type != "object" || !contractSchemaIsClosed(schema) {
			t.Errorf("%s 必须是 x-lifecycle=%s、additionalProperties=false 的 object", name, lifecycle)
		}
		if _, forbidden := schema.Properties["turn_id"]; forbidden {
			t.Errorf("%s 不得另造 turn_id；唯一身份是 goal_id/todo_id/turn_seq", name)
		}
	}

	turnKey := doc.Components.Schemas["TurnKey"]
	assertExactStringValues(t, "TurnKey.required", turnKey.Required, []string{"goal_id", "todo_id", "turn_seq"})
	assertExactStringValues(t, "TurnKey.properties", contractPropertyNames(turnKey), []string{"goal_id", "todo_id", "turn_seq"})
	todo := doc.Components.Schemas["Todo"]
	requireContractFields(t, "Todo.required", todo.Required, "claim", "claim_version", "last_turn_seq",
		"completion_turn_key", "completion_evidence_id")
	requireContractProperties(t, "Todo.properties", todo, "claim", "claim_version", "last_turn_seq",
		"completion_turn_key", "completion_evidence_id")

	header := doc.Components.Schemas["TurnReceiptHeader"]
	if !header.ReadOnly {
		t.Error("TurnReceiptHeader 必须 readOnly/immutable")
	}
	assertExactStringValues(t, "TurnReceiptHeader.required", header.Required,
		[]string{"turn_key", "attempt", "schema_version", "input_snapshot_digest", "admission_client_key", "canonical_digest", "created_at"})
	recoveryFields := []string{"source_run_id", "plan_client_key", "decision_digest"}
	requireContractProperties(t, "TurnReceiptHeader.properties", header, recoveryFields...)
	if len(header.AllOf) != 1 {
		t.Errorf("TurnReceiptHeader 必须有一个 recovery checkpoint allOf，实际 %d", len(header.AllOf))
	} else {
		condition := header.AllOf[0]
		if len(condition.If.AnyOf) != len(recoveryFields) {
			t.Errorf("TurnReceiptHeader allOf.if.anyOf 必须覆盖三个 checkpoint 字段，实际 %d", len(condition.If.AnyOf))
		} else {
			seen := make([]string, 0, len(condition.If.AnyOf))
			for _, item := range condition.If.AnyOf {
				if len(item.Required) != 1 {
					t.Errorf("TurnReceiptHeader allOf.if.anyOf 每项必须是一个 required 字段，实际 %v", item.Required)
					continue
				}
				seen = append(seen, item.Required[0])
			}
			assertExactStringValues(t, "TurnReceiptHeader allOf.if.anyOf.required", seen, recoveryFields)
		}
		assertExactStringValues(t, "TurnReceiptHeader allOf.then.required", condition.Then.Required, recoveryFields)
	}
	phase := doc.Components.Schemas["TurnReceiptPhase"]
	if !phase.ReadOnly {
		t.Error("TurnReceiptPhase 必须 readOnly/append-only")
	}
	assertExactStringValues(t, "TurnReceiptPhase.required", phase.Required,
		[]string{"turn_key", "phase_seq", "phase", "payload", "canonical_digest", "created_at"})

	repair := doc.Components.Schemas["ProjectionRepair"]
	requireContractProperties(t, "ProjectionRepair.properties", repair,
		"id", "goal_id", "status", "scope", "source_cursor", "replayed_event_count",
		"replayed_receipt_count", "error_code", "error_message", "client_key", "version",
		"started_at", "completed_at", "created_at", "updated_at")
	requireContractFields(t, "ProjectionRepair.required", repair.Required,
		"id", "goal_id", "status", "scope", "source_cursor", "replayed_event_count",
		"replayed_receipt_count", "version", "started_at", "created_at", "updated_at")
	if repair.Properties["id"].Pattern != "^projection_repair_[0-9A-HJKMNP-TV-Z]{26}$" {
		t.Errorf("ProjectionRepair.id 必须使用 projection_repair_ typed-id pattern，实际 %q", repair.Properties["id"].Pattern)
	}

	snapshot := doc.Components.Schemas["DeliveryBriefSnapshot"]
	requireContractProperties(t, "DeliveryBriefSnapshot.properties", snapshot,
		"id", "schema_version", "goal_id", "todo_id", "work_item_id", "snapshot_json", "canonical_digest",
		"as_of_event_seq", "source_versions", "freshness_state", "created_at", "client_key")
	requireContractFields(t, "DeliveryBriefSnapshot.required", snapshot.Required,
		"id", "schema_version", "goal_id", "todo_id", "work_item_id", "snapshot_json", "canonical_digest",
		"as_of_event_seq", "source_versions", "freshness_state", "created_at")
	if snapshot.Properties["schema_version"].Const != "delivery-brief-snapshot/v1" {
		t.Errorf("DeliveryBriefSnapshot.schema_version 必须固定为 delivery-brief-snapshot/v1，实际 %q", snapshot.Properties["schema_version"].Const)
	}
	if snapshot.Properties["id"].Pattern != "^brief_[0-9A-HJKMNP-TV-Z]{26}$" {
		t.Errorf("DeliveryBriefSnapshot.id 必须使用 brief_ typed-id pattern，实际 %q", snapshot.Properties["id"].Pattern)
	}

	workItemCreate := doc.Components.Schemas["WorkItemCreate"]
	if workItemCreate == nil || workItemCreate.Properties["acceptance_criteria"] == nil ||
		workItemCreate.Properties["acceptance_criteria"].MaxItems != 64 {
		t.Errorf("WorkItemCreate.acceptance_criteria 必须保留最多 64 条的闭集约束")
	}
	if workItemCreate == nil || len(workItemCreate.AllOf) != 1 ||
		!conditionContainsMinItems(workItemCreate.AllOf[0].Then, 1) {
		t.Errorf("WorkItemCreate 必须以条件 schema 强制根 Task 至少一条 acceptance_criteria（子 Task/Chat 可省略）")
	}

	reservation := doc.Components.Schemas["QuotaReservation"]
	requireContractFields(t, "QuotaReservation.required", reservation.Required,
		"goal_id", "todo_id", "turn_seq", "quota_kind", "status")
	resolution := doc.Components.Schemas["QuotaGapResolution"]
	requireContractFields(t, "QuotaGapResolution.required", resolution.Required,
		"id", "schema_version", "target", "amount", "evidence", "evidence_digest", "canonical_digest", "actor_id", "reason")
	if resolution.Properties["schema_version"].Const != domain.QuotaGapResolutionSchemaVersion {
		t.Errorf("QuotaGapResolution.schema_version=%q", resolution.Properties["schema_version"].Const)
	}
	usage := doc.Components.Schemas["CanonicalUsage"]
	requireContractProperties(t, "CanonicalUsage.properties", usage,
		"schema_version", "run_id", "usage_basis", "counters", "resolved_kinds",
		"unresolved_kinds", "provenance", "digest", "cost_microusd")
	requireContractFields(t, "CanonicalUsage.required", usage.Required,
		"schema_version", "run_id", "usage_basis", "counters", "resolved_kinds", "unresolved_kinds", "provenance", "digest")
	for _, flat := range []string{"input_tokens_total", "input_uncached_tokens", "cache_read_tokens", "cache_write_tokens", "output_tokens"} {
		if usage.Properties[flat] != nil {
			t.Errorf("CanonicalUsage 不得保留 flat counter %q；Go shape 是 counters.provenance nested", flat)
		}
	}
	price := doc.Components.Schemas["PriceSnapshotRef"]
	requireContractProperties(t, "PriceSnapshotRef.properties", price,
		"input_uncached_microusd_per_million", "cache_read_microusd_per_million",
		"cache_write_microusd_per_million", "output_microusd_per_million", "digest")
	if _, forbidden := price.Properties["input_tokens_total"]; forbidden {
		t.Error("PriceSnapshotRef 不得给 input_tokens_total 定价，避免 cached token 双计")
	}

	if liveEvidence := doc.Components.Schemas["EvidenceItem"]; liveEvidence == nil || liveEvidence.Lifecycle == "proposed" {
		t.Error("现有 live EvidenceItem 不得被治理 proposal 覆盖或改写生命周期")
	}
}

func contractPropertyNames(schema *contractSchema) []string {
	out := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		out = append(out, name)
	}
	return out
}

func collectContractRefs(node *yaml.Node, out map[string]bool) {
	if node == nil {
		return
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			if key.Value == "$ref" && value.Kind == yaml.ScalarNode {
				out[value.Value] = true
			}
			collectContractRefs(value, out)
		}
		return
	}
	for _, child := range node.Content {
		collectContractRefs(child, out)
	}
}

func contractSchemaIsClosed(schema *contractSchema) bool {
	return schema != nil && schema.AdditionalProperties.Kind == yaml.ScalarNode &&
		schema.AdditionalProperties.Value == "false"
}

func requireContractFields(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	set := make(map[string]bool, len(got))
	for _, field := range got {
		set[field] = true
	}
	for _, field := range want {
		if !set[field] {
			t.Errorf("%s 缺少 %q，实际 %v", what, field, got)
		}
	}
}

func requireContractProperties(t *testing.T, what string, schema *contractSchema, want ...string) {
	t.Helper()
	if schema == nil {
		t.Errorf("%s schema 缺失", what)
		return
	}
	for _, field := range want {
		if schema.Properties[field] == nil {
			t.Errorf("%s 缺少 %q", what, field)
		}
	}
}

// ── 门禁 B：SSE 事件名 ↔ asyncapi.yaml ───────────────────────────────

func TestContractGuardEventNamesMatchAsyncAPI(t *testing.T) {
	root := moduleRootForContracts(t)
	constants := extractEventNameConstants(t, root)
	codeEvents := make(map[string]bool, len(constants))
	for _, v := range constants {
		codeEvents[v] = true
	}

	path := filepath.Join(root, "contracts", "events", "asyncapi.yaml")
	var doc struct {
		Channels map[string]struct {
			Address  string               `yaml:"address"`
			Messages map[string]yaml.Node `yaml:"messages"`
		} `yaml:"channels"`
		Components struct {
			Schemas map[string]*contractSchema `yaml:"schemas"`
		} `yaml:"components"`
	}
	parseYAMLFile(t, path, &doc)

	envelope := doc.Components.Schemas["CanonicalEventEnvelope"]
	if envelope == nil || envelope.Properties["type"] == nil {
		t.Fatalf("%s 缺少 components.schemas.CanonicalEventEnvelope.type —— 该 enum 是事件门禁的数据源，不可缺失", path)
	}
	eventEnum := envelope.Properties["type"].Enum
	if len(eventEnum) == 0 {
		t.Fatalf("CanonicalEventEnvelope.type.enum 为空或不是字符串数组——事件白名单枚举不可被清空使门禁空转")
	}
	declaredEvents := make(map[string]bool, len(eventEnum))
	for _, v := range eventEnum {
		declaredEvents[v] = true
	}
	assertSetDiff(t, "事件名常量 ↔ asyncapi.yaml type.enum", codeEvents,
		"asyncapi.yaml CanonicalEventEnvelope.type.enum", declaredEvents)

	// aggregate 枚举必须与 domain.Aggregate* 常量一一对应。
	wantAggregates := map[string]bool{
		domain.AggregateWorkspace:             true,
		domain.AggregateAgentProfile:          true,
		domain.AggregateWorkItem:              true,
		domain.AggregatePlan:                  true,
		domain.AggregateExecutionRun:          true,
		domain.AggregateApproval:              true,
		domain.AggregateArtifact:              true,
		domain.AggregateRuntimeBinding:        true,
		domain.AggregateRunner:                true,
		domain.AggregateDispatch:              true,
		domain.AggregateDecision:              true,
		domain.AggregateTaskCoordinator:       true,
		domain.AggregateGoal:                  true,
		domain.AggregateTodo:                  true,
		domain.AggregateHandoff:               true,
		domain.AggregateValidationResult:      true,
		domain.AggregateGovernanceProjection:  true,
		domain.AggregateProjectionRepair:      true,
		domain.AggregateDeliveryBriefSnapshot: true,
		domain.AggregateQuotaGapResolution:    true,
		// 任务控制面补全（task-control-surface RFC §10）。
		domain.AggregateExecutionHost:     true,
		domain.AggregateWorkspaceLocation: true,
		domain.AggregateTaskComment:       true,
	}
	aggSchema := envelope.Properties["aggregate"]
	if aggSchema == nil || aggSchema.Properties["type"] == nil {
		t.Fatalf("CanonicalEventEnvelope.aggregate.type 缺失——aggregate 枚举对账无法进行")
	}
	gotAggregates := map[string]bool{}
	for _, v := range aggSchema.Properties["type"].Enum {
		gotAggregates[v] = true
	}
	assertSetDiff(t, "aggregate 类型 ↔ domain.Aggregate* 常量", wantAggregates,
		"asyncapi.yaml aggregate.type.enum", gotAggregates)

	// channel 地址必须是实际注册过的 SSE 路由（receive 操作 → GET）。
	registered := extractRegisteredRoutes(t, root)
	patterns := map[string]bool{}
	getPatterns := map[string]bool{}
	for key := range registered {
		mi := strings.IndexByte(key, ' ')
		pattern := key[mi+1:]
		patterns[pattern] = true
		if strings.HasPrefix(key, "GET ") {
			getPatterns[pattern] = true
		}
	}
	if len(doc.Channels) == 0 {
		t.Fatalf("%s 没有 channels 定义——SSE 契约入口不可为空", path)
	}
	names := make([]string, 0, len(doc.Channels))
	for name := range doc.Channels {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		ch := doc.Channels[name]
		addr := normalizeWildcardsOnly(ch.Address)
		if !patterns[addr] {
			t.Errorf("asyncapi channel %q 的 address %q 不是代码注册的路由模式（对比对象 internal/httpapi/server.go Routes()）", name, ch.Address)
			continue
		}
		if !getPatterns[addr] {
			t.Errorf("asyncapi channel %q 指向 %q 但代码未注册对应的 GET 路由（receive/SSE 要求 GET）", name, ch.Address)
		}
		if len(ch.Messages) == 0 {
			t.Errorf("asyncapi channel %q 未声明任何 message", name)
		}
	}
}

// TestContractGuardGovernanceEventLifecycle rejects any untracked proposal.
// All governance events currently have real producers and therefore live
// payload contracts; a future proposal must be explicitly reviewed before it
// can enter this file.
func TestContractGuardGovernanceEventLifecycle(t *testing.T) {
	type proposedMessage struct {
		Lifecycle        string    `yaml:"x-lifecycle"`
		EventName        string    `yaml:"x-event-name"`
		AggregateType    string    `yaml:"x-aggregate-type"`
		ProducerStatus   string    `yaml:"x-producer-status"`
		IdentityRequired *[]string `yaml:"x-identity-required"`
		Payload          struct {
			Ref string `yaml:"$ref"`
		} `yaml:"payload"`
	}
	type channel struct {
		Messages map[string]struct {
			Ref string `yaml:"$ref"`
		} `yaml:"messages"`
	}
	root := moduleRootForContracts(t)
	path := filepath.Join(root, "contracts", "events", "asyncapi.yaml")
	var doc struct {
		Channels   map[string]channel `yaml:"channels"`
		Components struct {
			Messages map[string]proposedMessage `yaml:"messages"`
			Schemas  map[string]*contractSchema `yaml:"schemas"`
		} `yaml:"components"`
	}
	parseYAMLFile(t, path, &doc)

	type expectedProposal struct {
		aggregate string
		schema    string
		identity  []string
	}
	want := map[string]expectedProposal{}

	liveEnvelope := doc.Components.Schemas["CanonicalEventEnvelope"]
	if liveEnvelope == nil || liveEnvelope.Properties["type"] == nil || liveEnvelope.Properties["aggregate"] == nil {
		t.Fatal("CanonicalEventEnvelope 必须保留 live event 与 aggregate enum")
	}
	liveEvents := map[string]bool{}
	for _, eventName := range liveEnvelope.Properties["type"].Enum {
		liveEvents[eventName] = true
	}
	liveMessageRefs := map[string]bool{}
	for _, ch := range doc.Channels {
		for _, message := range ch.Messages {
			liveMessageRefs[message.Ref] = true
		}
	}

	seen := map[string]bool{}
	for messageName, message := range doc.Components.Messages {
		if message.Lifecycle != "proposed" {
			continue
		}
		if message.EventName == "" {
			t.Errorf("components.messages.%s 缺少 x-event-name", messageName)
			continue
		}
		if seen[message.EventName] {
			t.Errorf("proposed event name 重复：%s", message.EventName)
		}
		seen[message.EventName] = true
		expected, ok := want[message.EventName]
		if !ok {
			t.Errorf("未裁决的 proposed governance event：%s", message.EventName)
			continue
		}
		if message.AggregateType != expected.aggregate {
			t.Errorf("%s aggregate=%q，want=%q", message.EventName, message.AggregateType, expected.aggregate)
		}
		if message.ProducerStatus != "absent" {
			t.Errorf("%s 在 producer 落地前必须 x-producer-status=absent", message.EventName)
		}
		if message.IdentityRequired == nil {
			t.Errorf("%s 必须显式声明 x-identity-required（即使仅依赖 envelope identity）", message.EventName)
			continue
		}
		if liveEvents[message.EventName] || domain.IsKnownEventName(message.EventName) {
			t.Errorf("%s 仍是 proposal，不得提前进入 live event whitelist", message.EventName)
		}
		messageRef := "#/components/messages/" + messageName
		if liveMessageRefs[messageRef] {
			t.Errorf("%s 不得挂载 live channel", messageRef)
		}
		schemaName := strings.TrimPrefix(message.Payload.Ref, "#/components/schemas/")
		if schemaName == message.Payload.Ref || schemaName != expected.schema {
			t.Errorf("%s payload ref=%q，want schema %s", message.EventName, message.Payload.Ref, expected.schema)
			continue
		}
		schema := doc.Components.Schemas[schemaName]
		if schema == nil {
			t.Errorf("%s payload schema %s 缺失", message.EventName, schemaName)
			continue
		}
		if schema.Lifecycle != "proposed" || schema.Type != "object" || !contractSchemaIsClosed(schema) {
			t.Errorf("%s payload 必须是 x-lifecycle=proposed、additionalProperties=false 的闭合 object", message.EventName)
		}
		assertExactStringValues(t, "components.messages."+messageName+".x-identity-required",
			*message.IdentityRequired, expected.identity)
		required := map[string]bool{}
		for _, field := range schema.Required {
			required[field] = true
		}
		for _, field := range *message.IdentityRequired {
			if !required[field] {
				t.Errorf("%s x-identity-required 字段 %q 未进入 %s.required", message.EventName, field, schemaName)
			}
		}
	}
	if len(seen) != len(want) {
		missing := []string{}
		for eventName := range want {
			if !seen[eventName] {
				missing = append(missing, eventName)
			}
		}
		sort.Strings(missing)
		t.Errorf("proposed governance event 不完整：missing=%v seen=%v", missing, seen)
	}
}

func TestContractGuardLiveGovernanceEventsHavePayloadSchemas(t *testing.T) {
	root := moduleRootForContracts(t)
	path := filepath.Join(root, "contracts", "events", "asyncapi.yaml")
	var doc struct {
		Components struct {
			Schemas map[string]*contractSchema `yaml:"schemas"`
		} `yaml:"components"`
	}
	parseYAMLFile(t, path, &doc)

	want := map[string]struct {
		schema   string
		required []string
	}{
		domain.EventGoalCreated:                  {schema: "GoalCreatedData", required: []string{"root_work_item_id", "state", "version"}},
		domain.EventGoalStateChanged:             {schema: "GoalStateChangedData", required: []string{"from_state", "to_state", "version"}},
		domain.EventTodoCreated:                  {schema: "TodoCreatedData", required: []string{"goal_id", "class", "state", "version"}},
		domain.EventTodoStateChanged:             {schema: "TodoStateChangedData", required: []string{"goal_id", "from_state", "to_state", "version"}},
		domain.EventTodoClaimChanged:             {schema: "TodoClaimChangedData", required: []string{"goal_id", "claim_version", "claim_state"}},
		domain.EventTurnReceiptAppended:          {schema: "TurnReceiptAppendedData", required: []string{"goal_id", "turn_seq", "record_kind", "digest"}},
		domain.EventHandoffCreated:               {schema: "HandoffCreatedData", required: []string{"goal_id", "handoff_id", "status"}},
		domain.EventHandoffStateChanged:          {schema: "HandoffStateChangedData", required: []string{"goal_id", "handoff_id", "from_state", "to_state", "status", "claim_transfer_state"}},
		domain.EventGoalEvidenceAdded:            {schema: "GoalEvidenceAddedData", required: []string{"goal_id", "source_kind", "source_id", "verification"}},
		domain.EventValidationRecorded:           {schema: "ValidationResultRecordedData", required: []string{"goal_id", "todo_id", "validation_result_id", "source_run_id", "status", "produced_by"}},
		domain.EventProjectionUpdated:            {schema: "ProjectionUpdatedData", required: []string{"goal_id", "digest", "version", "event_stream_seq", "through_turn_seq"}},
		domain.EventProjectionRepairChanged:      {schema: "ProjectionRepairStateChangedData", required: []string{"goal_id", "repair_id", "from_state", "to_state", "status"}},
		domain.EventQuotaReservationChanged:      {schema: "QuotaReservationChangedData", required: []string{"goal_id", "todo_id", "turn_seq", "quota_kind", "reservation_state", "amount", "reserved_amount", "committed_amount", "released_amount", "policy_limit", "policy_enforcement", "policy_digest", "reservation_version"}},
		domain.EventQuotaSpendRecorded:           {schema: "QuotaSpendRecordedData", required: []string{"goal_id", "todo_id", "turn_seq", "quota_kind", "run_id", "amount", "usage_basis", "usage_digest", "status"}},
		domain.EventDeliveryBriefSnapshotCreated: {schema: "DeliveryBriefSnapshotCreatedData", required: []string{"schema_version", "goal_id", "todo_id", "work_item_id", "canonical_digest", "as_of_event_seq", "source_versions", "freshness_state"}},
		domain.EventQuotaGapReconciled:           {schema: "QuotaGapReconciledData", required: []string{"schema_version", "resolution_id", "goal_id", "todo_id", "turn_seq", "quota_kind", "run_id", "original_usage_digest", "original_policy_digest", "status", "amount", "evidence_digest", "canonical_digest", "actor_kind", "actor_id", "reason"}},
	}
	seen := map[string]bool{}
	for eventName, expected := range want {
		if !domain.IsKnownEventName(eventName) {
			t.Errorf("live governance event %s 必须进入 domain whitelist", eventName)
		}
		schema := doc.Components.Schemas[expected.schema]
		if schema == nil {
			t.Errorf("%s 缺少 payload schema %s", eventName, expected.schema)
			continue
		}
		if schema.Lifecycle != "live" || schema.ProducerStatus != "present" || schema.EventName != eventName {
			t.Errorf("%s payload lifecycle/producer mapping 异常: %+v", eventName, schema)
		}
		if schema.Type != "object" || !contractSchemaIsClosed(schema) {
			t.Errorf("%s payload 必须是 additionalProperties=false 的 live object", eventName)
		}
		requireContractFields(t, expected.schema+".required", schema.Required, expected.required...)
		if eventName == domain.EventTurnReceiptAppended {
			requireContractProperties(t, expected.schema+".properties", schema,
				"outcome", "valid", "error_code", "path")
		}
		if eventName == domain.EventTodoStateChanged {
			requireContractProperties(t, expected.schema+".properties", schema,
				"completion_turn_key", "completion_evidence_id")
			completionRequired := false
			for _, condition := range schema.AllOf {
				toState := condition.If.Properties["to_state"]
				if toState != nil && toState.Const == string(domain.TodoCompleted) {
					required := map[string]bool{}
					for _, field := range condition.Then.Required {
						required[field] = true
					}
					completionRequired = required["completion_turn_key"] && required["completion_evidence_id"]
				}
			}
			if !completionRequired {
				t.Error("TodoStateChangedData completed transition must require completion identity")
			}
		}
		if eventName == domain.EventTodoClaimChanged {
			claimState := schema.Properties["claim_state"]
			if claimState == nil {
				t.Error("TodoClaimChangedData.claim_state is required")
			} else {
				states := map[string]bool{}
				for _, state := range claimState.Enum {
					states[state] = true
				}
				for _, state := range []string{"claimed", "renewed", "released", "expired"} {
					if !states[state] {
						t.Errorf("TodoClaimChangedData.claim_state must include %q", state)
					}
				}
			}
		}
		if seen[schema.EventName] {
			t.Errorf("live governance payload 重复声明 event %s", schema.EventName)
		}
		seen[schema.EventName] = true
	}
}

func TestContractGuardGovernanceVocabularyMatches(t *testing.T) {
	root := moduleRootForContracts(t)
	var openAPI struct {
		Components struct {
			Schemas map[string]*contractSchema `yaml:"schemas"`
		} `yaml:"components"`
	}
	parseYAMLFile(t, filepath.Join(root, "contracts", "web", "openapi.yaml"), &openAPI)
	var asyncAPI struct {
		Components struct {
			Schemas map[string]*contractSchema `yaml:"schemas"`
		} `yaml:"components"`
	}
	parseYAMLFile(t, filepath.Join(root, "contracts", "events", "asyncapi.yaml"), &asyncAPI)

	tests := []struct {
		name          string
		openSchema    string
		openProperty  string
		asyncSchema   string
		asyncProperty string
	}{
		{name: "goal status", openSchema: "GoalStatus", asyncSchema: "GoalCreatedData", asyncProperty: "state"},
		{name: "todo class", openSchema: "TodoClass", asyncSchema: "TodoCreatedData", asyncProperty: "class"},
		{name: "todo status", openSchema: "TodoStatus", asyncSchema: "TodoCreatedData", asyncProperty: "state"},
		{name: "receipt phase", openSchema: "TurnReceiptPhaseName", asyncSchema: "TurnReceiptAppendedData", asyncProperty: "phase"},
		{name: "quota kind", openSchema: "QuotaKind", asyncSchema: "QuotaReservationChangedData", asyncProperty: "quota_kind"},
		{name: "handoff status", openSchema: "HandoffStatus", asyncSchema: "HandoffStateChangedData", asyncProperty: "to_state"},
		{name: "evidence source kind", openSchema: "GovernanceEvidenceItem", openProperty: "source_kind", asyncSchema: "GoalEvidenceAddedData", asyncProperty: "source_kind"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			canonical := openAPI.Components.Schemas[tc.openSchema]
			eventData := asyncAPI.Components.Schemas[tc.asyncSchema]
			if canonical == nil || eventData == nil || eventData.Properties[tc.asyncProperty] == nil {
				t.Fatalf("governance vocabulary path 缺失：OpenAPI %s / AsyncAPI %s.%s",
					tc.openSchema, tc.asyncSchema, tc.asyncProperty)
			}
			want := canonical.Enum
			if tc.openProperty != "" {
				if canonical.Properties[tc.openProperty] == nil {
					t.Fatalf("OpenAPI %s.%s 缺失", tc.openSchema, tc.openProperty)
				}
				want = canonical.Properties[tc.openProperty].Enum
			}
			assertExactStringValues(t, tc.name, eventData.Properties[tc.asyncProperty].Enum, want)
		})
	}
}

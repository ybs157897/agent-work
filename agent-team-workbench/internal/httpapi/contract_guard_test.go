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

type contractSchema struct {
	Type       string                     `yaml:"type"`
	Enum       []string                   `yaml:"enum"`
	Properties map[string]*contractSchema `yaml:"properties"`
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
		domain.AggregateWorkspace:      true,
		domain.AggregateAgentProfile:   true,
		domain.AggregateWorkItem:       true,
		domain.AggregatePlan:           true,
		domain.AggregateExecutionRun:   true,
		domain.AggregateApproval:       true,
		domain.AggregateArtifact:       true,
		domain.AggregateRuntimeBinding: true,
		domain.AggregateRunner:         true,
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

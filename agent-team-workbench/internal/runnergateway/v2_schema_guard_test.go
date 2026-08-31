package runnergateway

// Runner protocol v2 契约门禁。
//
// Ownership：本文件归蜂群任务 W1-A「契约与红测先行」executor 所有，后续波次
// 不得改动；契约事实源为 contracts/runner/v2/schema.json（task-control-surface
// RFC §8，决策权威 agent-team-workbench-docs/architecture/
// task-control-surface-context-design.md）。
//
// I0 波只钉契约形状：v2 schema 可解析、关键 required 字段（host/epoch 身份、
// context_snapshot、lease/fencing framing、producer_seq dedup、按 Run 维度 ACK）
// 存在、snapshot_digest 计算公式入契、v1 契约成建制删除。帧级行为语义
//（ApplyRunnerEvent 原子性、旧 epoch 回 ACK 不落库等）接线后由 gateway 行为
// 测试覆盖（RFC §15.2），不在本门禁范围。

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// locateContractsRoot 从测试文件自身位置向上定位模块根（同时存在 go.mod 与
// contracts/runner/v2/schema.json 的目录）；go test 删除 cwd 依赖。
func locateContractsRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位测试文件路径，契约根目录不可得")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "contracts", "runner", "v2", "schema.json")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("无法定位模块根（需同时存在 go.mod 与 contracts/runner/v2/schema.json）")
		}
		dir = parent
	}
}

func loadV2Schema(t *testing.T) map[string]any {
	t.Helper()
	path := filepath.Join(locateContractsRoot(t), "contracts", "runner", "v2", "schema.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("解析 %s 失败（契约必须是合法 JSON）: %v", path, err)
	}
	return doc
}

func v2Def(t *testing.T, doc map[string]any, name string) map[string]any {
	t.Helper()
	defs, _ := doc["$defs"].(map[string]any)
	def, ok := defs[name].(map[string]any)
	if !ok {
		t.Fatalf("$defs.%s 缺失或形态异常——v2 消息定义不可缺角", name)
	}
	return def
}

// v2Required 返回 def 的 required 字符串切片。
func v2Required(t *testing.T, def map[string]any, what string) []string {
	t.Helper()
	raw, ok := def["required"].([]any)
	if !ok {
		t.Fatalf("%s 缺少 required 数组", what)
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("%s.required 含非字符串项: %v", what, v)
		}
		out = append(out, s)
	}
	return out
}

// requireFields 断言 required 覆盖全部 want 字段，缺任一即 Fatal 并列明细。
func requireFields(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	set := make(map[string]bool, len(got))
	for _, k := range got {
		set[k] = true
	}
	var missing []string
	for _, w := range want {
		if !set[w] {
			missing = append(missing, w)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%s.required 缺少字段 %v（实际 required=%v）", what, missing, got)
	}
}

// v2Messages 是 9 种 v2 envelope 的 method → $defs 名单（RFC §8 + heartbeat
// 租约续期/保活载体）。
var v2Messages = map[string]string{
	"runner.hello":   "RunnerHello",
	"server.welcome": "ServerWelcome",
	"run.offer":      "RunOffer",
	"run.accept":     "RunAccept",
	"run.reject":     "RunReject",
	"run.command":    "RunCommand",
	"run.event":      "RunEvent",
	"ack":            "RunAck",
	"heartbeat":      "Heartbeat",
}

// TestV2SchemaContractLoads 校验 v2 契约可解析，且 8 种消息 envelope 全部 v=2。
func TestV2SchemaContractLoads(t *testing.T) {
	doc := loadV2Schema(t)

	// 顶层 oneOf 必须恰好覆盖 8 种消息。
	oneOf, ok := doc["oneOf"].([]any)
	if !ok || len(oneOf) != len(v2Messages) {
		t.Fatalf("顶层 oneOf 应覆盖 %d 种消息，实际 %v", len(v2Messages), oneOf)
	}
	seen := map[string]bool{}
	for _, item := range oneOf {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("oneOf 项形态异常: %v", item)
		}
		ref, _ := m["$ref"].(string)
		seen[strings.TrimPrefix(ref, "#/$defs/")] = true
	}
	for method, defName := range v2Messages {
		if !seen[defName] {
			t.Errorf("顶层 oneOf 缺少消息 %s（$defs.%s）", method, defName)
		}
	}

	// 每个 envelope：required 含 v 且 properties.v.const == 2；method.const 白名单。
	for method, defName := range v2Messages {
		def := v2Def(t, doc, defName)
		requireFields(t, "$defs."+defName, v2Required(t, def, "$defs."+defName), "v", "method")
		props, _ := def["properties"].(map[string]any)
		if props == nil {
			t.Fatalf("$defs.%s 缺少 properties", defName)
		}
		vProp, _ := props["v"].(map[string]any)
		if vProp["const"] != float64(2) {
			t.Fatalf("$defs.%s.properties.v.const 必须为 2，实际 %v（不得出现 envelope v=1）", defName, vProp["const"])
		}
		methodProp, _ := props["method"].(map[string]any)
		if methodProp["const"] != method {
			t.Fatalf("$defs.%s.properties.method.const 必须为 %q，实际 %v", defName, method, methodProp["const"])
		}
	}
}

// TestV2SchemaHelloRequiresHostIdentity 钉住 hello 的宿主身份与 mount 广告契约。
func TestV2SchemaHelloRequiresHostIdentity(t *testing.T) {
	doc := loadV2Schema(t)

	hello := v2Def(t, doc, "RunnerHelloPayload")
	requireFields(t, "$defs.RunnerHelloPayload", v2Required(t, hello, "RunnerHelloPayload"),
		"host_id", "runner_id", "boot_id", "connection_epoch", "protocol_versions", "mounts")

	mount := v2Def(t, doc, "MountAdvertisement")
	requireFields(t, "$defs.MountAdvertisement", v2Required(t, mount, "MountAdvertisement"),
		"alias", "repository_identity", "registry_generation", "supported_ref_kinds", "checkouts")

	// 广告投影只含受信事实：宿主绝对路径（root/cwd）不得出现在 mount 契约里。
	props, _ := mount["properties"].(map[string]any)
	for banned := range props {
		if banned == "root" || banned == "cwd" || banned == "root_path" || banned == "absolute_path" {
			t.Fatalf("MountAdvertisement 不得携带宿主路径字段 %q——绝对路径只存在 Host 本地配置", banned)
		}
	}
}

// TestV2SchemaOfferRequiresContextSnapshot 钉住 run.offer 必携带不可变执行上下文。
func TestV2SchemaOfferRequiresContextSnapshot(t *testing.T) {
	doc := loadV2Schema(t)

	offer := v2Def(t, doc, "RunOfferPayload")
	requireFields(t, "$defs.RunOfferPayload", v2Required(t, offer, "RunOfferPayload"),
		"lease_id", "fencing_token", "run_spec")

	spec, ok := offer["properties"].(map[string]any)["run_spec"].(map[string]any)
	if !ok {
		t.Fatal("$defs.RunOfferPayload.properties.run_spec 缺失")
	}
	requireFields(t, "RunOfferPayload.run_spec", v2Required(t, spec, "RunOfferPayload.run_spec"),
		"run_id", "adapter_id", "context_snapshot")

	snap := v2Def(t, doc, "ContextSnapshot")
	requireFields(t, "$defs.ContextSnapshot", v2Required(t, snap, "ContextSnapshot"),
		"id", "schema_version", "execution_host_id", "workspace_location_id",
		"mount_generation", "repository_identity", "ref_kind", "context_generation",
		"snapshot_digest")
}

// TestV2SchemaFencingAndDedup 钉住 accept/reject/command/event 的 lease/fencing
// framing，以及 event 的稳定事件身份（event_id + producer_seq）。
func TestV2SchemaFencingAndDedup(t *testing.T) {
	doc := loadV2Schema(t)

	framing := []string{"run_id", "lease_id", "runner_id", "connection_epoch", "fencing_token"}
	for _, defName := range []string{"RunAcceptPayload", "RunRejectPayload", "RunCommandPayload", "RunEventPayload"} {
		def := v2Def(t, doc, defName)
		requireFields(t, "$defs."+defName, v2Required(t, def, defName), framing...)
	}

	event := v2Def(t, doc, "RunEventPayload")
	requireFields(t, "$defs.RunEventPayload", v2Required(t, event, "RunEventPayload"),
		"event_id", "producer_seq")

	// dedup 语义必须写入契约：key 与 producer_seq 单调性。
	if desc, _ := event["description"].(string); !strings.Contains(desc, "(run_id, lease_id, runner_id, producer_seq)") {
		t.Fatal("$defs.RunEventPayload.description 必须写明 dedup key = (run_id, lease_id, runner_id, producer_seq)")
	}
}

// TestV2SchemaAckPerRun 钉住 ACK 按 Run 维度确认，v1 全局 contiguous_seq 不得回流。
func TestV2SchemaAckPerRun(t *testing.T) {
	doc := loadV2Schema(t)

	ack := v2Def(t, doc, "RunAckPayload")
	requireFields(t, "$defs.RunAckPayload", v2Required(t, ack, "RunAckPayload"),
		"run_id", "lease_id", "runner_id", "fencing_token", "acked_producer_seq", "event_id")

	props, _ := ack["properties"].(map[string]any)
	if _, legacy := props["contiguous_seq"]; legacy {
		t.Fatal("$defs.RunAckPayload 不得包含 v1 的全局 contiguous_seq——ACK 不再跨 Run 共享水位")
	}
}

// TestV2SchemaDigestRecipe 钉住 snapshot_digest 计算公式进契约。
func TestV2SchemaDigestRecipe(t *testing.T) {
	doc := loadV2Schema(t)

	snap := v2Def(t, doc, "ContextSnapshot")
	props, _ := snap["properties"].(map[string]any)
	digest, ok := props["snapshot_digest"].(map[string]any)
	if !ok {
		t.Fatal("$defs.ContextSnapshot.properties.snapshot_digest 缺失")
	}
	desc, _ := digest["description"].(string)
	// 公式中的 "\n" 以字面转义写法出现在 description 里（hash 输入的分隔换行）。
	for _, want := range []string{"sha256", `execution-context/v1\n`, "RFC8785CanonicalJSON"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("snapshot_digest.description 缺少计算公式要素 %q；实际：%s", want, desc)
		}
	}
}

// TestV2SchemaHeartbeatIsConnectionLevel 钉住 heartbeat 连接级契约：它是 v1
// 租约续期的唯一线协议载体（Gateway 收到后按 runner_id 续租活跃 lease），
// 不得缺角，也不得携带 run/lease 维度字段。
func TestV2SchemaHeartbeatIsConnectionLevel(t *testing.T) {
	doc := loadV2Schema(t)

	hb := v2Def(t, doc, "Heartbeat")
	requireFields(t, "$defs.Heartbeat", v2Required(t, hb, "Heartbeat"),
		"v", "method", "runner_id", "connection_epoch")

	props, ok := hb["properties"].(map[string]any)
	if !ok {
		t.Fatal("$defs.Heartbeat 缺少 properties")
	}
	if v, _ := props["v"].(map[string]any); v["const"] != float64(2) {
		t.Fatalf("$defs.Heartbeat.properties.v.const 必须为 2，实际 %v", v["const"])
	}
	if m, _ := props["method"].(map[string]any); m["const"] != "heartbeat" {
		t.Fatalf("$defs.Heartbeat.properties.method.const 必须为 %q，实际 %v", "heartbeat", m["const"])
	}

	// 连接级帧：required 不得出现 run/lease 维度字段。
	req := v2Required(t, hb, "Heartbeat")
	for _, banned := range []string{"run_id", "lease_id", "fencing_token"} {
		for _, field := range req {
			if field == banned {
				t.Fatalf("heartbeat 是连接级帧，required 不得携带 %s（run 级确认走 run.ack）", banned)
			}
		}
	}

	// payload 允许为空对象（type=object、无 required）。
	payload, ok := props["payload"].(map[string]any)
	if !ok {
		t.Fatal("$defs.Heartbeat.properties.payload 缺失")
	}
	hbPayload := v2Def(t, doc, "HeartbeatPayload")
	if hbPayload["type"] != "object" {
		t.Fatalf("$defs.HeartbeatPayload.type 必须为 object（允许空对象），实际 %v", hbPayload["type"])
	}
	if _, hasReq := hbPayload["required"]; hasReq {
		t.Fatal("$defs.HeartbeatPayload 不得声明 required——心跳负载允许为空对象")
	}
	_ = payload

	// 续租语义必须写进契约：welcome 周期上报 + 按 runner_id 续租 + 旧 epoch 不续租。
	if desc, _ := hb["description"].(string); !strings.Contains(desc, "heartbeat_interval_seconds") ||
		!strings.Contains(desc, "续租") || !strings.Contains(desc, "旧 connection_epoch") {
		t.Fatalf("$defs.Heartbeat.description 必须写明周期上报/按 runner_id 续租/旧 epoch 不续租；实际：%s", desc)
	}

	// welcome 的租约/保活参数是 heartbeat 语义的另一半，不得从契约中丢失。
	welcome := v2Def(t, doc, "ServerWelcomePayload")
	requireFields(t, "$defs.ServerWelcomePayload", v2Required(t, welcome, "ServerWelcomePayload"),
		"heartbeat_interval_seconds")
	welcomeProps, _ := welcome["properties"].(map[string]any)
	if _, ok := welcomeProps["lease_policy"]; !ok {
		t.Fatal("$defs.ServerWelcomePayload 缺少 lease_policy——lease TTL/续租间隔没有线协议载体")
	}
}

// TestV2SchemaContextSnapshotCoversDigestIdentity 对账 ContextSnapshot 与
// digest 身份字段 13 项闭集（冻结事实源 internal/domain/execution_context.go
// 的 identityFieldNames/identityFields；本测试只读对账，不 import 生产包）。
// Runner 必须用本地事实 + offer 控制面字段重算 snapshot_digest：任何一个身份
// 字段被 additionalProperties:false 拦截，branch/worktree run 必然 digest 失配。
func TestV2SchemaContextSnapshotCoversDigestIdentity(t *testing.T) {
	doc := loadV2Schema(t)
	snap := v2Def(t, doc, "ContextSnapshot")
	props, ok := snap["properties"].(map[string]any)
	if !ok {
		t.Fatal("$defs.ContextSnapshot 缺少 properties")
	}

	// digest 键 → wire 属性名（唯一命名差异：digest 键 mount_alias 在 wire 上
	// 是 workspace_alias；其余 12 项同名）。
	digestIdentityFields := map[string]string{
		"base_revision":         "base_revision",
		"branch_name":           "branch_name",
		"checkout_ref":          "checkout_ref",
		"context_generation":    "context_generation",
		"execution_host_id":     "execution_host_id",
		"location_version":      "location_version",
		"mount_alias":           "workspace_alias",
		"mount_generation":      "mount_generation",
		"ref_kind":              "ref_kind",
		"repository_identity":   "repository_identity",
		"schema_version":        "schema_version",
		"workspace_location_id": "workspace_location_id",
		"worktree_ref":          "worktree_ref",
	}
	for digestKey, wireKey := range digestIdentityFields {
		if _, ok := props[wireKey]; !ok {
			t.Errorf("digest 身份字段 %q（wire 名 %q）不在 ContextSnapshot.properties——会被 additionalProperties:false 拦截，Runner 无法重算 snapshot_digest", digestKey, wireKey)
		}
	}

	// 补齐三字段的类型（optional/omitempty 语义，风格与既有字段一致）。
	for name, wantType := range map[string]string{
		"branch_name":      "string",
		"base_revision":    "string",
		"location_version": "integer",
	} {
		f, ok := props[name].(map[string]any)
		if !ok {
			t.Errorf("ContextSnapshot.%s 缺失", name)
			continue
		}
		if f["type"] != wantType {
			t.Errorf("ContextSnapshot.%s.type 必须为 %q，实际 %v", name, wantType, f["type"])
		}
	}

	// optional 语义：三字段不得进 required（omitempty；省略时以 "" 参与 digest）。
	req := v2Required(t, snap, "ContextSnapshot")
	for _, optional := range []string{"branch_name", "base_revision", "location_version"} {
		for _, field := range req {
			if field == optional {
				t.Errorf("digest 身份字段 %q 应为 optional（omitempty），不得进 required", optional)
			}
		}
	}

	// required ⊆ properties 自洽，防 required 拼写漂移。
	for _, field := range req {
		if _, ok := props[field]; !ok {
			t.Errorf("ContextSnapshot.required 字段 %q 无 properties 定义", field)
		}
	}
}

// TestV2SchemaV1Removed 断言 v1 契约已成建制删除（RFC §8：不留运行时兼容分支）。
func TestV2SchemaV1Removed(t *testing.T) {
	v1Dir := filepath.Join(locateContractsRoot(t), "contracts", "runner", "v1")
	if _, err := os.Stat(v1Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s 应已删除（v2 直接发布，不保留 v1 契约），stat err=%v", v1Dir, err)
	}
}

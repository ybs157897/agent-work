package knowledgelib

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// TaskKind identifies what a write task is for.
const (
	TaskInitialize   = "initialize"
	TaskIncremental  = "incremental"
	TaskRemove       = "remove"
	TaskRename       = "rename"
	TaskInvalidate   = "invalidate"
	TaskBranchView   = "branch_view"
	TaskLegacyImport = "legacy_import"
	TaskReindex      = "reindex"
)

// BriefInput is the trusted description of one write task.
type BriefInput struct {
	TaskID      string
	TaskKind    string
	LibraryRoot string
	StagingDir  string
	ViewID      string
	BaseRelease *ReleaseRef
	Sources     []BriefSource
	// Focus lists what changed for an incremental task.
	Focus Focus
	// ExistingDocuments is the published catalog the agent may extend.
	ExistingDocuments []ExistingDocument
	// EntityCatalog is the stable entity IDs the agent must reuse.
	EntityCatalog []EntitySpec
	// KnownEvidenceIDs are canonical evidence IDs already in the ledger.
	KnownEvidenceIDs []string
}

// ReleaseRef is a compact release reference for prompts.
type ReleaseRef struct {
	ID          string
	Seq         int
	PublishedAt string
}

// BriefSource is one frozen binding as presented to the agent. ReadPath is
// the exported snapshot tree, never the live repository path.
type BriefSource struct {
	Binding   string
	Name      string
	Kind      string
	RepoPath  string
	ReadPath  string
	GitRef    string
	CommitSHA string
	ObjectFmt string
	Dirty     bool
	Untracked []string
	Artifact  string
	// ArtifactResolution is 'declared' unless a real resolution produced the
	// version; the agent must not treat a registered value as the artifact a
	// service actually resolved.
	ArtifactResolution string
	Consumer           string
	Environment        string
}

// Focus narrows an incremental task.
type Focus struct {
	EventType    string
	ChangedPaths []string
	RemovedPaths []string
	RenamedPaths []string
	SourceNames  []string
	Notes        string
}

// ExistingDocument is one published document in the catalog digest.
type ExistingDocument struct {
	ID           string
	Path         string
	Title        string
	Kind         string
	AssertionIDs []string
}

// RenderBrief writes brief.md and task.json into the staging directory. The
// agent reads these files; the harness never relies on the model remembering
// a chat message.
func RenderBrief(in BriefInput) (string, error) {
	if strings.TrimSpace(in.StagingDir) == "" {
		return "", errf(ErrValidation, "staging directory is required")
	}
	if err := os.MkdirAll(filepath.Join(in.StagingDir, ContentZone), 0o755); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("# 资料整理任务\n\n")
	b.WriteString(fmt.Sprintf("- 任务 ID：`%s`\n- 任务类型：`%s`\n- 视图：`%s`\n", in.TaskID, in.TaskKind, in.ViewID))
	b.WriteString(fmt.Sprintf("- 资料库根目录：`%s`\n- 本次暂存目录：`%s`\n", in.LibraryRoot, in.StagingDir))
	if in.BaseRelease != nil {
		b.WriteString(fmt.Sprintf("- 当前已发布版本：`%s`（seq %d，%s）\n", in.BaseRelease.ID, in.BaseRelease.Seq, in.BaseRelease.PublishedAt))
	} else {
		b.WriteString("- 当前已发布版本：无（这是首次初始化）\n")
	}
	b.WriteString("\n## 一、职责边界\n\n")
	b.WriteString("你是资料库内部的资料整理智能体。你只做两件事：读取下面列出的固定来源，把结论写成本暂存目录里的 Markdown 知识文档。\n\n")
	b.WriteString("- 只读来源仓库，不要修改任何来源仓库的文件。\n")
	b.WriteString("- 只写本暂存目录（`content/`、`plan.json`、`evidence.yaml`、`entities.yaml`），不要写资料库根目录下的其他位置。\n")
	b.WriteString("- 不要自己写版本号、发布状态、批准状态或任何摘要/hash：这些由资料程序采集和校验。\n")
	b.WriteString("- 不要凭文件名或命名习惯猜业务关系；每条结论都要有可定位的证据。\n\n")
	b.WriteString("## 二、本次可用来源（已固定版本）\n\n")
	b.WriteString("| binding | 类型 | 只读路径（固定版本副本） | 分支 | commit | 工作树 | 依赖版本（登记声明值） | 使用方 | 环境 |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, s := range in.Sources {
		wt := "干净"
		if s.Dirty {
			wt = fmt.Sprintf("含未提交改动（%d 个文件）", len(s.Untracked))
		}
		b.WriteString(fmt.Sprintf("| %s | %s | `%s` | %s | `%s` | %s | %s | %s | %s |\n",
			s.Binding, s.Kind, s.ReadPath, orDash(s.GitRef), shortSHA(s.CommitSHA), wt,
			artifactLabel(s.Artifact, s.ArtifactResolution), orDash(s.Consumer), orDash(s.Environment)))
	}
	b.WriteString("\n**只读上面的副本目录**：那是本轮固定版本的实际内容，来源仓库路径会随时间变化，不要读它们。\n")
	b.WriteString("同一逻辑来源可能出现多个 binding：名字里带 `@使用方`、`#制品`、`~环境`，只要其中之一不同就是两个不同绑定，不要合并，也不要互相套用结论。\n")
	b.WriteString("依赖版本列写的是**登记声明值**，不代表构建时实际解析到的制品；没有解析证据时请在 coverage.gaps 中写明「实际制品版本未核实」。\n\n")

	if len(in.Sources) > 0 {
		b.WriteString("### 稳定实体 ID（`about` 与关系端点必须使用这些 ID）\n\n")
		b.WriteString("| 实体 ID | 类型 | 名称 |\n|---|---|---|\n")
		for _, e := range in.EntityCatalog {
			b.WriteString(fmt.Sprintf("| `%s` | %s | %s |\n", e.ID, e.Kind, e.Name))
		}
		b.WriteString("\n还需要别的实体（业务流程、事件、数据对象等）时，在 `entities.yaml` 里显式登记，不要临时编造 ID。\n\n")
	}

	if f := in.Focus; f.EventType != "" || len(f.ChangedPaths) > 0 || len(f.RemovedPaths) > 0 {
		b.WriteString("## 三、本次变更范围\n\n")
		if f.EventType != "" {
			b.WriteString(fmt.Sprintf("触发事件：`%s`\n\n", f.EventType))
		}
		if len(f.ChangedPaths) > 0 {
			b.WriteString("变更的文件：\n\n")
			for _, p := range clipList(f.ChangedPaths, 200) {
				b.WriteString("- `" + p + "`\n")
			}
			b.WriteString("\n")
		}
		if len(f.RemovedPaths) > 0 {
			b.WriteString("删除/改名的文件：\n\n")
			for _, p := range clipList(f.RemovedPaths, 200) {
				b.WriteString("- `" + p + "`\n")
			}
			b.WriteString("\n失去依据的条目要在新版本中撤下，不要直接宣布业务规则废止。\n\n")
		}
		if f.Notes != "" {
			b.WriteString("补充说明：" + f.Notes + "\n\n")
		}
		b.WriteString("增量要求：只重做受影响的知识单元，未受影响且依据仍有效的文档可以直接沿用；同时要主动扫描新增的接口、事件、调用和消费者，旧关系图里没有的边不会自己出现。\n\n")
	}

	if len(in.ExistingDocuments) > 0 {
		b.WriteString("## 四、当前已发布文档目录\n\n")
		b.WriteString("| 文档 ID | 路径 | 标题 | 已有条目 ID |\n|---|---|---|---|\n")
		for _, d := range clipDocs(in.ExistingDocuments, 300) {
			b.WriteString(fmt.Sprintf("| `%s` | `%s` | %s | %s |\n", d.ID, d.Path, d.Title, orDash(strings.Join(clipList(d.AssertionIDs, 12), ", "))))
		}
		b.WriteString("\n已发布正文可以直接读取：`" + filepath.Join(in.LibraryRoot, ContentZone) + "/`。沿用某个条目时保留它的稳定 ID，只更新内容。\n\n")
	}

	b.WriteString("## 五、必须产出的文件\n\n")
	b.WriteString("```text\n")
	b.WriteString("content/<主类>/.../<主题>.md    正式知识文档（至少一篇，除非本轮只做删除）\n")
	b.WriteString("plan.json                       本轮计划与覆盖情况\n")
	b.WriteString("evidence.yaml                   你引用到的每一条证据的定位请求\n")
	b.WriteString("entities.yaml                   你新登记的实体（没有就写空列表）\n")
	b.WriteString("```\n\n")

	b.WriteString("### 5.1 知识文档格式\n\n")
	b.WriteString("文档以 YAML frontmatter 开始，正文用标题块承载知识条目。每个条目块 = 标题 + ```yaml 元数据 + `#### 陈述` 正文小节（可选 `#### 说明与未知`）。\n\n")
	b.WriteString("主类目录：`content/business/rules/`、`content/business/flows/`、`content/components/`、`content/contracts/apis/`、`content/contracts/events/`、`content/contracts/data/`、`content/operations/`、`content/decisions/`。\n\n")
	b.WriteString("完整样例：\n\n")
	b.WriteString(briefSample)
	b.WriteString("\n\n硬性规则：\n\n")
	b.WriteString("- `schema_version` 必须是 `" + RecordSyntaxVersion + "`，`id` 以 `doc:` 开头并保持稳定（文档移动不改 ID）。\n")
	b.WriteString("- Assertion 的 `id` 以 `assertion:` 开头；一条知识只有一个主编写位置，其他文档用 ID 引用。\n")
	b.WriteString("- 陈述只写在 `#### 陈述` 正文里，**不要**在 YAML 里再写一遍 `statement`。\n")
	b.WriteString("- 元数据里出现 `statement`、`digest`、`approved`、`confirmed`、`review_state`、`release_id`、`version` 或任何未列出的字段，整篇会被拒绝。\n")
	b.WriteString("- `perspective` 只能是 `normative`／`descriptive`；`basis` 只能是 `source_statement`／`code_static`／`runtime_observed`／`inferred`。\n")
	b.WriteString("- `scope.conditions` 为空表示“没有记录到限定信息”，不表示无条件适用；明确的无条件声明要写出来。\n")
	b.WriteString("- 关系块用 `kind: relation`，`from`/`to` 是 `{kind, id}`，`predicate` 取自受控词表：" + predicateList() + "。\n\n")

	b.WriteString("### 5.2 `evidence.yaml`：证据定位请求\n\n")
	b.WriteString("你只声明“去哪个来源、哪个文件的哪一段找证据”，内容、摘要和证据 ID 由程序采集。**不要**写 digest、hash、批准或校验结果。\n\n")
	b.WriteString("```yaml\n")
	b.WriteString("evidence:\n")
	b.WriteString("  - key: ev-cancel-release          # 本轮本地别名，供文档里的 evidence_id 引用\n")
	b.WriteString("    binding: order-service          # 上面的来源名\n")
	b.WriteString("    path: src/main/java/com/example/order/OrderService.java\n")
	b.WriteString("    locator:\n")
	b.WriteString("      kind: source_text\n")
	b.WriteString("      interval: half_open\n")
	b.WriteString("      start: {line: 40, column: 0}   # 0-based\n")
	b.WriteString("      end: {line: 58, column: 0}     # 结束行不含\n")
	b.WriteString("    note: 取消分支调用发布方法\n")
	b.WriteString("```\n\n")
	b.WriteString("定位必须在固定版本里真实命中。行号越界、引句在文件里出现多次却不给范围、或者指向不存在的文件，本次任务会被判不合格。\n\n")

	b.WriteString("### 5.3 `plan.json`\n\n")
	b.WriteString("```json\n")
	b.WriteString("{\n")
	b.WriteString("  \"summary\": \"本轮做了什么\",\n")
	b.WriteString("  \"documents\": [\"content/components/order-service.md\"],\n")
	b.WriteString("  \"removals\": [],\n")
	b.WriteString("  \"renames\": [],\n")
	b.WriteString("  \"coverage\": {\n")
	b.WriteString("    \"sources_read\": [\"order-service\"],\n")
	b.WriteString("    \"sources_missed\": [],\n")
	b.WriteString("    \"gaps\": [\"未核实生产环境是否真的投递成功\"],\n")
	b.WriteString("    \"notes\": \"\"\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")
	b.WriteString("```\n\n")
	b.WriteString("`coverage.gaps` 要如实写出你不知道的部分；说不确定不算失败，装作确定会被验收拒绝。\n")

	if err := os.WriteFile(filepath.Join(in.StagingDir, "brief.md"), []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	contract := map[string]any{
		"task_id":        in.TaskID,
		"task_kind":      in.TaskKind,
		"library_root":   in.LibraryRoot,
		"staging_dir":    in.StagingDir,
		"view_id":        in.ViewID,
		"schema_version": RecordSyntaxVersion,
		"sources":        in.Sources,
		"entities":       in.EntityCatalog,
		"focus":          in.Focus,
		"required_files": []string{
			"content/**/*.md", "plan.json", "evidence.yaml", "entities.yaml",
		},
	}
	raw, err := json.MarshalIndent(contract, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(in.StagingDir, "task.json"), raw, 0o644); err != nil {
		return "", err
	}
	return b.String(), nil
}

func predicateList() string {
	out := make([]string, 0, len(RelationPredicates))
	for p := range RelationPredicates {
		out = append(out, "`"+p+"`")
	}
	sort.Strings(out)
	return strings.Join(out, "、")
}

func clipList(in []string, max int) []string {
	if len(in) <= max {
		return in
	}
	return append(append([]string(nil), in[:max]...), fmt.Sprintf("…（共 %d 项，已截断）", len(in)))
}

func clipDocs(in []ExistingDocument, max int) []ExistingDocument {
	if len(in) <= max {
		return in
	}
	return in[:max]
}

// artifactLabel marks an artifact version as declared or resolved so a
// registered value is never presented as the actually resolved artifact.
func artifactLabel(artifact, resolution string) string {
	if strings.TrimSpace(artifact) == "" {
		return "未核实"
	}
	switch resolution {
	case "resolved":
		return artifact + "（已解析）"
	case "unknown":
		return artifact + "（未核实）"
	default:
		return artifact + "（登记声明值）"
	}
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

const briefSample = "````markdown\n" + `---
schema_version: kb-note/0.2-draft
id: doc:order-cancel
kind: business.rule
title: 订单取消与设备占用
summary: 取消订单时设备占用的处理要求。
about: [entity:service:order-service]
domains: [order]
---

# 订单取消与设备占用

## 知识条目

### 设备占用释放要求

` + "```yaml" + `
kind: assertion
id: assertion:cancel-release-policy
about: [entity:service:order-service]
perspective: normative
basis: source_statement
scope:
  conditions:
    - 订单取消成功，且该订单存在关联的设备占用。
  environments: []
evidence:
  - evidence_id: ev-cancel-release
    role: supports
` + "```" + `

#### 陈述

系统应释放该订单关联的设备占用。

#### 说明与未知

原需求没有明确释放时限。
` + "\n````"

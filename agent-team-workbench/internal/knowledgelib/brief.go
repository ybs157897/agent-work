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
	TaskLegacyImport = "legacy_import"
	TaskReindex      = "reindex"
)

// DefaultViewID is the one active view a library writes into. Views exist in
// the record syntax so a branch overlay can be added later; this build accepts
// only the default view and rejects a task that names another one.
const DefaultViewID = "baseline"

// BriefInput is the trusted description of one write task.
type BriefInput struct {
	TaskID      string
	TaskKind    string
	LibraryRoot string
	StagingDir  string
	ViewID      string
	BaseRelease *ReleaseRef
	Sources     []BriefSource
	// Requirement is the external requirement text this task was opened for.
	// It is declared input, not repository evidence.
	Requirement *Requirement
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
	// WorktreeNote explains why the working tree was excluded from this input.
	WorktreeNote string
	Artifact     string
	// ArtifactResolution is 'declared' unless a real resolution produced the
	// version; the agent must not treat a registered value as the artifact a
	// service actually resolved.
	ArtifactResolution string
	Consumer           string
	Environment        string
}

// RequirementFileName is the staging file holding the full requirement text.
// The text is also inlined in brief.md; the file exists so a long requirement
// can be read in full without truncating the brief.
const RequirementFileName = "requirement.md"

// Requirement is the external requirement text that opened this task. It is
// declared input: the library records what was asked for, never that the code
// implements it.
type Requirement struct {
	RequirementID string `json:"requirement_id,omitempty"`
	Title         string `json:"title,omitempty"`
	Version       string `json:"version,omitempty"`
	Source        string `json:"source,omitempty"`
	ContentRef    string `json:"content_ref,omitempty"`
	Digest        string `json:"digest,omitempty"`
	Text          string `json:"text,omitempty"`
	Path          string `json:"path,omitempty"`
	// Binding is the frozen snapshot binding that holds this text. The agent
	// cites it, not the staging path, so the requirement's own bytes are
	// collected as evidence.
	Binding string `json:"binding,omitempty"`
	// Origin says where the text came from: the referenced document or the
	// event's inline payload. An inline copy must never be described as the
	// referenced original.
	Origin string `json:"origin,omitempty"`
	// Unresolved records why the referenced text could not be read. A task
	// with an unresolved requirement must say so instead of inventing text.
	Unresolved string            `json:"unresolved,omitempty"`
	Extra      map[string]string `json:"extra,omitempty"`
}

// Focus narrows an incremental task.
type Focus struct {
	EventType    string
	ChangedPaths []string
	RemovedPaths []string
	RenamedPaths []string
	SourceNames  []string
	// Branch and the commit SHAs are the observed revision of the reported
	// change. They tell the agent which line to keep reading, and are never
	// presented as something the library verified.
	Branch      string
	HeadSHA     string
	PreviousSHA string
	// The requirement this task was opened for. When no body could be
	// accepted, RequirementUnresolved carries the reason so the brief reports
	// the gap instead of silently having no requirement section.
	RequirementID         string
	RequirementVersion    string
	RequirementTitle      string
	RequirementUnresolved string
	Notes                 string
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
	// The requirement text is written before the brief so the path the brief
	// advertises always exists by the time the agent can read anything.
	if r := in.Requirement; r != nil && r.Unresolved == "" {
		body := "# " + firstNonEmpty(r.Title, r.RequirementID, "本次需求原文") + "\n\n" + r.Text + "\n"
		if err := os.WriteFile(filepath.Join(in.StagingDir, RequirementFileName), []byte(body), 0o644); err != nil {
			return "", err
		}
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
		if s.WorktreeNote != "" {
			wt = s.WorktreeNote
		} else if s.Dirty {
			wt = fmt.Sprintf("含未提交改动（%d 个文件）", len(s.Untracked))
		}
		b.WriteString(fmt.Sprintf("| %s | %s | `%s` | %s | `%s` | %s | %s | %s | %s |\n",
			s.Binding, s.Kind, s.ReadPath, orDash(s.GitRef), shortSHA(s.CommitSHA), wt,
			artifactLabel(s.Artifact, s.ArtifactResolution), orDash(s.Consumer), orDash(s.Environment)))
	}
	b.WriteString("\n分支列写的是本次固定版本实际来自的 ref（登记 ref 优先于工作树当前分支；工作树列写明未提交改动是否被算作输入）。\n")
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

	if r := in.Requirement; r != nil {
		b.WriteString("## 三、本次需求原文（外部声明输入）\n\n")
		b.WriteString("下面这段文本是本次任务的直接依据，由外部系统在事件里提交，**不是你从来源仓库读到的**。\n")
		b.WriteString("它只说明「被要求做什么」，不证明代码、配置或线上行为已经做到。\n\n")
		if r.RequirementID != "" {
			b.WriteString("- 需求 ID：`" + r.RequirementID + "`（同一需求 ID 的后续版本要沿用已发布条目里同一个 `assertion:` ID，只更新陈述，不要新建第二条）\n")
		}
		if r.Title != "" {
			b.WriteString("- 标题：" + r.Title + "\n")
		}
		if r.Version != "" {
			b.WriteString("- 需求版本：" + r.Version + "\n")
		}
		if r.Source != "" {
			b.WriteString("- 提交方：" + r.Source + "\n")
		}
		if r.ContentRef != "" {
			b.WriteString("- 原文引用：" + r.ContentRef + "\n")
		}
		if r.Origin == "inline_payload" {
			b.WriteString("- 正文来源：事件 payload 内联文本（不是引用文档的内容）\n")
		} else if r.Origin == "content_ref" {
			b.WriteString("- 正文来源：引用文档（受理时已冻结）\n")
		}
		if r.Digest != "" {
			b.WriteString("- 文本摘要（sha256）：`" + r.Digest + "`\n")
		}
		for _, key := range requirementExtraKeys(r.Extra) {
			b.WriteString("- " + key + "：" + r.Extra[key] + "\n")
		}
		b.WriteString("\n")
		switch {
		case r.Unresolved != "" && strings.TrimSpace(r.Text) != "":
			// A declared reference failed, but an inline copy was accepted.
			// Both facts are stated: the gap stays visible and the text is
			// never presented as the referenced original.
			b.WriteString("**引用的需求文档没有取到**：" + r.Unresolved + "\n\n")
			b.WriteString("下面的正文来自**事件 payload 的内联文本**，不是那份文档的内容；请在 coverage.gaps 里写明「引用文档未取到，本轮依据内联副本」。\n\n")
			if r.Path != "" {
				b.WriteString("内联副本另存为：`" + r.Path + "`（仅供阅读；证据必须引用下面的冻结绑定）。\n\n")
			}
			b.WriteString("```text\n" + r.Text + "\n```\n\n")
			b.WriteString("处理要求：\n\n")
			b.WriteString("- 把这段内联正文作为 `basis: source_statement`、`perspective: normative` 的条目记录，并在陈述里写明依据是内联副本而非引用文档。\n")
			b.WriteString("- 需求是「要求」，不是「现状」：不要写「已实现」「已上线」，也不要写审批或发布状态字段。\n")
			b.WriteString("- **需求条目必须登记证据**：")
			if r.Binding != "" {
				b.WriteString("binding 写 `" + r.Binding + "`")
			} else {
				b.WriteString("binding 写来源表里 `requirement-inline:` 开头的那一行")
			}
			b.WriteString("，`path` 写 `requirement.md`，locator 用 `source_text` 引用这份冻结的内联副本。\n\n")
		case r.Unresolved != "":
			b.WriteString("**这段需求的原文没有取到**：" + r.Unresolved + "\n")
			b.WriteString("本轮不要凭标题猜测需求内容；按现有来源照常整理，并在 plan.json 的 coverage.gaps 里写明「需求原文缺失，无法核对」。\n\n")
		default:
			if r.Path != "" {
				b.WriteString("完整原文另存为：`" + r.Path + "`（仅供阅读；证据必须引用下面的冻结绑定，不要引用这个暂存路径）。\n\n")
			}
			b.WriteString("```text\n" + r.Text + "\n```\n\n")
			b.WriteString("处理要求：\n\n")
			b.WriteString("- 把需求正文作为 `basis: source_statement`、`perspective: normative` 的条目记进对应的业务规则/流程文档，陈述里保留可核对的原文关键值（阈值、时限、状态名、字段名）。\n")
			b.WriteString("- 需求是「要求」，不是「现状」：不要写「已实现」「已上线」「代码中已生效」，也不要写 `approved`／`review_state`／任何审批或发布状态——这些字段会被整篇拒绝。\n")
			b.WriteString("- 来源仓库里能核对的实现要单独用 `basis: code_static` 或 `runtime_observed` 的条目写，并明确说明它与需求是否一致；找不到对应实现就写进「说明与未知」或 coverage.gaps。\n")
			b.WriteString("- **需求条目必须登记证据**：需求原文已作为下面的冻结来源绑定进本轮快照，")
			if r.Binding != "" {
				b.WriteString("binding 写 `" + r.Binding + "`")
			} else {
				b.WriteString("binding 写上面来源表里 `requirement:` 开头的那一行")
			}
			b.WriteString("，`path` 写 `requirement.md`，locator 用 `source_text` + 行区间引用冻结原文；不要引用暂存目录里的 requirement.md（它不是证据来源）。\n\n")
			b.WriteString("需求证据的 evidence.yaml 样例：\n\n```yaml\n")
			b.WriteString("evidence:\n")
			b.WriteString("  - key: ev-requirement-time-limit\n")
			if r.Binding != "" {
				b.WriteString("    binding: " + r.Binding + "\n")
			} else {
				b.WriteString("    binding: requirement:<需求 ID>\n")
			}
			b.WriteString("    path: requirement.md\n")
			b.WriteString("    locator:\n      kind: source_text\n      interval: half_open\n")
			b.WriteString("      start: {line: 0, column: 0}\n      end: {line: 3, column: 0}\n")
			b.WriteString("    note: 需求正文中的时限条款\n```\n\n")
		}
	}

	if f := in.Focus; f.EventType != "" || len(f.ChangedPaths) > 0 || len(f.RemovedPaths) > 0 {
		b.WriteString("## 四、本次变更范围\n\n")
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
		if f.Branch != "" {
			b.WriteString("报告的分支：" + f.Branch + "\n\n")
		}
		if f.HeadSHA != "" {
			b.WriteString("报告的提交：" + shortSHA(f.HeadSHA))
			if f.PreviousSHA != "" {
				b.WriteString("（前一版本 " + shortSHA(f.PreviousSHA) + "）")
			}
			b.WriteString("\n\n")
		}
		if f.Notes != "" {
			b.WriteString("补充说明：" + f.Notes + "\n\n")
		}
		b.WriteString("增量要求：只重做受影响的知识单元，未受影响且依据仍有效的文档可以直接沿用；同时要主动扫描新增的接口、事件、调用和消费者，旧关系图里没有的边不会自己出现。\n\n")
	}

	if len(in.ExistingDocuments) > 0 {
		b.WriteString("## 五、当前已发布文档目录\n\n")
		b.WriteString("| 文档 ID | 路径 | 标题 | 已有条目 ID |\n|---|---|---|---|\n")
		for _, d := range clipDocs(in.ExistingDocuments, 300) {
			b.WriteString(fmt.Sprintf("| `%s` | `%s` | %s | %s |\n", d.ID, d.Path, d.Title, orDash(strings.Join(clipList(d.AssertionIDs, 12), ", "))))
		}
		b.WriteString("\n已发布正文可以直接读取：`" + filepath.Join(in.LibraryRoot, ContentZone) + "/`。沿用某个条目时保留它的稳定 ID，只更新内容。\n\n")
	}

	b.WriteString("## 六、必须产出的文件\n\n")
	b.WriteString("```text\n")
	b.WriteString("content/<主类>/.../<主题>.md    正式知识文档（至少一篇，除非本轮只做删除）\n")
	b.WriteString("plan.json                       本轮计划与覆盖情况\n")
	b.WriteString("evidence.yaml                   你引用到的每一条证据的定位请求\n")
	b.WriteString("entities.yaml                   你新登记的实体（没有就写空列表）\n")
	b.WriteString("```\n\n")

	b.WriteString("### 6.1 知识文档格式\n\n")
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

	b.WriteString("关系块同样有必须有可无的字段，完整样例：\n\n")
	b.WriteString(briefRelationSample)
	b.WriteString("\n\n关系块的元数据**只有** `kind/id/from/predicate/to/perspective/basis/scope/evidence`：\n")
	b.WriteString("`from` 与 `to` 已经指明了两个对象，所以关系块里**没有** `about`；解释写在「陈述」小节。\n")
	b.WriteString("需要描述单个对象的结论（包括「某事件被某服务消费」这类对单个对象的判断）时改用 `kind: assertion` 并用 `about`。\n\n")
	b.WriteString("### 6.2 `evidence.yaml`：证据定位请求\n\n")
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

	b.WriteString("### 6.3 `plan.json`\n\n")
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
	if resolution == "not_applicable" {
		// A requirement document is not a build artifact; saying "unverified"
		// here would imply a dependency claim that was never made.
		return "不适用"
	}
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

// briefRelationSample is the other half of the record grammar. Without it the
// writer has to infer the relation block's required fields, and a single
// omitted field costs a whole repair turn.
const briefRelationSample = "````markdown\n" + `---
schema_version: kb-note/0.2-draft
id: doc:order-cancel-flow
kind: business.flow
title: 订单取消到设备释放
summary: 取消事件从订单服务到设备服务的连接。
about: [entity:service:order-service, entity:service:device-service]
---

# 订单取消到设备释放

## 知识条目

### 订单服务发布取消事件

` + "```yaml" + `
kind: relation
id: relation:order-publishes-cancel
from: {kind: entity, id: entity:service:order-service}
predicate: publishes
to: {kind: entity, id: entity:event:order-cancelled-event}
perspective: descriptive
basis: code_static
scope:
  conditions:
    - 源码中取消成功分支调用了事件发布方法。
  environments: []
evidence:
  - evidence_id: ev-order-publish-call
    role: supports
` + "```" + `

#### 陈述

order-service 的取消分支构造 OrderCancelledEvent 并调用发布方法。
` + "\n````"

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

// requirementExtraKeys lists declared requirement metadata in a stable order
// so the brief does not reshuffle itself between runs.
func requirementExtraKeys(extra map[string]string) []string {
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

复核完成。change-and-correction.md 已读；客户端源码已实际核实：原 `normal-jdtls` 推断（仅凭 package.json 断言 Web 端无 LSP/保存能力）确属不充分——`lsp/session.ts` 是完整的本地 JavaLspSession 实现（WebSocket JSON-RPC、didOpen/didChange/didSave、references/definition/completion、ServiceReady 等待），`api/client.ts:186` 的 `writeFile` 经 PUT 写盘，`App.tsx:245` 有 dirty 跟踪的 `saveFile` 及保存失败重试条。外部依赖侧：现有代码对 workspace 外 URI 已有 "external" 徽标并禁用打开，但无来源路径提示，definition 流程直接丢弃外部目标。以下仅输出改变/新增对象，未受影响事项与问题走 preserve。

```atw-analysis
{
  "version": "chat-analysis/v1",
  "summary": "本轮按 change-and-correction.md 复核两项：其一，实际读取 apps/web 客户端源码（api/client.ts、lsp/session.ts、App.tsx、ReferencesPeek.tsx、lsp/usageHits.ts、lsp/gtdu.ts，SHA-256 均已实测），确认并纠正上一版 normal-jdtls 的不充分推断——Web 端存在本地 JavaLspSession 实现（WebSocket JSON-RPC、文档同步、references/definition/completion、ServiceReady 索引等待）与经 Gateway PUT /fs/file 的保存链路（saveFile 带 dirty 跟踪与失败重试），原“无 LSP/写盘能力”推断撤回；范围是否开放写入仍属产品裁决，不因代码存在而自动批准。其二，外部依赖只读约束不变，变更提议新增“定义/引用结果来自外部依赖时展示只读标识与来源路径提示”；代码复核显示现有交互仅对 workspace 外 URI（jdt://）显示 external 徽标并禁用打开、无来源路径提示，definition 流程（gtdu.ts locToTarget）直接丢弃外部目标，新增部分为未确认提议，与既有回填结论方向一致。JDK 降级规则未变，未重开。已保存产品结论独立保留，未写入任何 item 正文。",
  "sources": [
    {
      "id": "s-change",
      "kind": "attachment",
      "ref": "src_01M22FTB44CZQ2RZ6AZZCBS3PG",
      "sha256": "a40d0974bf36faade9fa060b887ba7e5a25f84ec2ee59532e2c8281d4d635806",
      "locator": "全文 20 行",
      "read_status": "read",
      "coverage": "全文读取：外部依赖只读约束保留 + 新增只读标识/来源路径提示提议；开发对 package.json 推断的纠正及所指源码位置",
      "limitations": "变更提议自声明不代表产品确认，不授权改代码或发布任务",
      "quote": "在定义/引用结果来自外部依赖时，新增明确的只读标识与来源路径提示，避免误以为可以直接编辑。"
    },
    {
      "id": "s-conv-u05",
      "kind": "conversation",
      "ref": "conversation:sha256:9edf50f71bb34f9d70c0752ce1ef8dfe31180d10e85b77aeb7609f66e6f65b97",
      "sha256": "9edf50f71bb34f9d70c0752ce1ef8dfe31180d10e85b77aeb7609f66e6f65b97",
      "locator": "用户本轮变更复核指令",
      "read_status": "read",
      "coverage": "复核 change-and-correction.md、纠正 normal-jdtls 推断、preserve 未受影响对象的要求",
      "limitations": "仅含本 Run 上下文内提供的对话内容"
    },
    {
      "id": "s-client",
      "kind": "code",
      "ref": "apps/web/src/api/client.ts",
      "sha256": "c2d57f495d457b7a9fd5be2bbdff859ef0f528dc401b81ef5a4073056d2f5e22",
      "locator": "全文 215 行（writeFile 在第 186 行）",
      "read_status": "read",
      "coverage": "全文读取：GatewayClient 含 readFile/writeFile（PUT /fs/file, text/plain）、tree/stat/search/files/project 等",
      "quote": "async writeFile(workspaceId: string, path: string, content: string): Promise<void> {"
    },
    {
      "id": "s-session",
      "kind": "code",
      "ref": "apps/web/src/lsp/session.ts",
      "sha256": "49918225ac98098ac59fe7785defba41a9fdc01be7b2269652628d7b6b6cce75",
      "locator": "全文 517 行",
      "read_status": "read",
      "coverage": "全文读取：本地 JavaLspSession——WebSocket JsonRpcClient、initialize、didOpen/didChange/didSave/didClose、references/definition/completion、waitUntilReady（ServiceReady 与索引进度）",
      "quote": "export class JavaLspSession {"
    },
    {
      "id": "s-app",
      "kind": "code",
      "ref": "apps/web/src/App.tsx",
      "sha256": "e63332f2622607eedcdd71166ce7dc05505d0b8652cc42a95565c083d6d2af6a",
      "locator": "全文 938 行（引用点 12/207/245/259/585/689/845）",
      "read_status": "read",
      "coverage": "关键段落与全部匹配行读取：导入并实例化 JavaLspSession、saveFile 调 client.writeFile、references/definition 调用、保存失败显示 Retry save 错误条",
      "quote": "await current.client.writeFile(current.workspaceId, path, content)"
    },
    {
      "id": "s-peek",
      "kind": "code",
      "ref": "apps/web/src/components/ReferencesPeek.tsx",
      "sha256": "57639fefcaff2904bbe0e1b07cff68f7ab9ab1d2b615acbe3487194a01d2124d",
      "locator": "第 195-264 行",
      "read_status": "read",
      "coverage": "结果列表渲染读取：对 workspace 外 URI 的命中（hit.path == null）禁用打开、tooltip 'External / jdt:// not openable in P2'、显示 external 徽标",
      "quote": "title={external ? 'External / jdt:// not openable in P2' : hit.path ?? hit.uri}"
    },
    {
      "id": "s-usagehits",
      "kind": "code",
      "ref": "apps/web/src/lsp/usageHits.ts",
      "sha256": "92594d079686a3b85ed3dcaaac37eb7e7a88ccdef2d3865ebba80c2bce483c51",
      "locator": "全文 111 行",
      "read_status": "read",
      "coverage": "全文读取：buildUsageHits 用 pathFromClientUri 将无法映射到 workspace 的 URI 记为 path=null（外部依赖命中），不读取其内容",
      "quote": "const path = pathFromClientUri(workspaceId, loc.uri)"
    },
    {
      "id": "s-gtdu",
      "kind": "code",
      "ref": "apps/web/src/lsp/gtdu.ts",
      "sha256": "1332731d1eac28e03095551865be2bf588835395042fdb6ca9265cb071a113e1",
      "locator": "全文 107 行",
      "read_status": "read",
      "coverage": "全文读取：decideGtdu/locToTarget 丢弃 pathFromClientUri 为 null 的定义目标（外部依赖），全部外部时返回 nowhere",
      "quote": "const path = pathFromClientUri(workspaceId, loc.uri)\n  if (path == null || path === '') return null"
    }
  ],
  "items": [
    {
      "id": "normal-jdtls",
      "kind": "normal",
      "title": "代码现状（已纠正）：Web 端已有本地 LSP 会话与经 Gateway 的保存链路",
      "detail": "纠正上一版推断：原结论仅凭 package.json 无第三方 LSP 依赖即断言 Web 端无 LSP/保存能力，被产品回填驳回，经实际读码证实不充分并撤回。实际实现：lsp/session.ts 为本地 JavaLspSession（WebSocket JSON-RPC 经 Gateway /lsp 端点、initialize、didOpen/didChange/didSave/didClose、references/definition/completion、ServiceReady 等待与索引进度上报）；api/client.ts:186 writeFile 经 PUT /fs/file 写盘；App.tsx:245 saveFile 带 dirty 跟踪、失败时显示含 Retry save 的错误条，并在多处导航/打开前自动保存。服务端 manager.go 的 jdtls 生命周期与此前观察一致。",
      "source_ids": ["s-session", "s-client", "s-app", "s-change", "s-pkg"],
      "basis": "observed",
      "impact": "原“缺口在 Web 端协议接入与编辑写盘链路”的判断不再成立；首期是否开放写入回到产品范围裁决，代码已具备保存能力不等于产品批准开放编辑。",
      "recommendation": "以实际观察替换原推断；scope-choice 与 write-confirm 的产品结论由服务端独立保留，不在本 item 记录；不在分析阶段实现或扩大写盘范围。"
    },
    {
      "id": "exc-deps",
      "kind": "exception",
      "title": "外部依赖只读展示：新增只读标识与来源路径提示（变更提议）",
      "detail": "外部依赖按只读展示、不提供写入入口的约束保留。change-and-correction.md 提议新增：当定义/引用结果来自外部依赖时，展示明确的只读标识与来源路径提示，避免误以为可直接编辑；该新增部分为未确认提议，变更材料自声明不代表产品批准。代码复核现状：ReferencesPeek 对 workspace 之外的 URI（pathFromClientUri 返回 null，如 jdt://）已显示 external 徽标、禁用打开并以 tooltip 说明不可打开；但无来源路径提示；definition 流程（gtdu.ts locToTarget）直接丢弃外部目标，全部目标为外部时提示 'Cannot find declaration to go to'。变更方向与只读约束一致，仅扩展展示/跳转交互。",
      "source_ids": ["s-change", "s-peek", "s-usagehits", "s-gtdu"],
      "basis": "proposed",
      "impact": "影响定义/引用结果中外部依赖条目的展示与跳转交互；不涉及写入路径，与 Gateway 唯一信任边界一致。",
      "recommendation": "将新增只读标识与来源路径提示的展示形式提交产品确认（见 deps-badge 问题），确认后回填；未确认前不实现。"
    }
  ],
  "questions": [
    {
      "id": "deps-badge",
      "prompt": "外部依赖结果的新增只读标识与来源路径提示，按什么形式展示？",
      "selection": "single",
      "options": [
        {
          "id": "a",
          "label": "在现有 external 徽标上补充来源信息（如 jar 坐标或路径），其余交互不变"
        },
        {
          "id": "b",
          "label": "只读标识 + 来源提示 + 定义跳转也列出外部目标（仅展示、不可打开），替代当前直接丢弃"
        },
        {
          "id": "c",
          "label": "其他展示形式（请在回填时说明）"
        }
      ],
      "item_ids": ["exc-deps"]
    }
  ],
  "preserve_item_ids": [
    "workflow-confirmed",
    "scope-readonly",
    "conflict-scope",
    "normal-read",
    "boundary",
    "exc-index",
    "exc-jdk",
    "exc-jump",
    "unknown-write-details",
    "unknown-jdk-version"
  ],
  "preserve_question_ids": [
    "scope-choice",
    "exception-interaction",
    "write-confirm"
  ]
}
```
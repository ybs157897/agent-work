# 产品/开发双智能体一等入口 + Chat 自动检索资料库

Status: implemented

## 决策与理由

团队名册收口为两个高频智能体：产品智能体（atlas，role=pm）与开发智能体（forge，role=developer）。
nova/pixel/sentinel 三个 bundle 从 `agents/` 真相源删除，seed 演示团队同步收口；资料库管理员作为系统
内置身份保留在配置页（它的 runtime/model 仍需配置入口）。左侧导航新增两个直达入口，按 **slug** 定位
agent、点击即进 `/chat?agent=<id>`——显示名可以改，slug 才是稳定身份。

Chat 模式补齐知识通路：普通智能体的 Chat Run 创建时自动预取本工作空间资料库**已发布 release** 的
检索结果（上限 5 条），以 `knowledge_context` 键冻结进 run.Input，运行时在每轮输入拼装点
（`appendSourceContext`）以「[资料库检索]」节拼在 source/analysis 上下文之后。冻结语义与
source_context 一致：retry/redo/续跑复用持久化 input，不重新检索；检索失败只记日志、不阻塞对话。
任务模式的 `consult_knowledge` 通道不变，两条通路读的都是同一已发布 release，且引用条目都报出
release 固定的真实版本号（同期修复了 SearchRelease 命中项不填 Version 的读端缺口——此前
knowledgeAppendix 会拼出并不存在的 v0，「引用与关联」元数据也因 version_id 为空整行缺失）。

## 放弃了什么

- **UI 过滤代替删除**：配置页隐藏三个智能体、定义留在盘上，是垫片。`agents/` 是配置真相源，
  真相源说没有就是没有；既有 DB 残留的 profile 不再收到配置回写，随库重建自然消亡。
- **Chat 内给 agent 实时知识查询工具**：工具面意味着 runtime 适配层逐个接协议、管凭据、防越权；
  创建时预取用既有检索端口一次定型，且对模型透明（声明头明确「参考资料，不授予权限」）。
  负向保证：普通智能体永远只读已发布 release，写库仍是资料库管理员独占通道。
- **预取失败阻断对话**：知识是增强不是前置条件，资料库未发布/检索失败时 Chat 必须照常可用。
- **按显示名定位侧栏入口**：名字是用户可编辑的展示字段，slug 才是目录真相源的键。

## 复活条件

- 需要 Chat 内实时/多轮检索（预取快照跟不上对话推进）时：先评估把 knowledge_context 从「创建时
  冻结」改为「每轮拼装时按当轮 instruction 重取」——预埋点是 `appendSourceContext` 本就是每轮
  拼装点，只需把取值从 run.Input 换成检索调用；实时工具面仍不复活，除非预取语义被证伪。
- 团队需要第三个业务角色时：在 `agents/<slug>/` 落 bundle 即恢复导入，不需要任何代码改动；
  侧栏入口要扩到第三个角色时再抽象「slug → 入口」配置表，当前两个入口不提前泛化。

## 验证

2026-09-13 按 [pre-merge ego 配方](../process/2026-09-13-pre-merge-ego-verification.md) 在 `:8090`
隔离实例（主库一致快照 + 0066 迁移 + 隔离仓 registry + 真实凭据）完成真实浏览器验证，主树 8080 全程未动：

1. 侧栏渲染「产品智能体/开发智能体」两个入口（slug 定位），点击直达 `/chat?agent=<atlas id>`，
   仅该入口 `aria-current="page"`，「对话」不双高亮；dashboard 速览只列三个启用成员。
2. 配置页主列表恰为产品/开发/知识库管理员三卡（importer 已将 atlas/forge 改名），「已停用（3）」
   折叠区默认收起，内为 Nova/Pixel/Sentinel（0066 迁移置停用）；chat chips 不含停用成员。
3. 知识注入真实链路：隔离仓内嵌套小 git 仓造「星轨罗盘」来源文档 → 注册来源 → initialize →
   资料管理员真实模型 run 完成并发布 release（4 条断言）→ UI 向产品智能体提问 → 新 run 的
   `input.knowledge_context` 含两条断言全文与「（v1）」真实版本号（SearchRelease 修复的生产路径证据），
   run succeeded，回答引用 42 小时/极光钟摆、88 µT/静默领航并复述未知项纪律。
4. 负向：资料库管理员自己的 chat run `knowledge_context IS NULL`（注入范围不含系统 agent）。

排障事实（后续使用者参考）：来源 repo_path 必须指向 **git 仓库目录**（文件路径报 "not readable"）、
必须在 workspace root 之内（"outside the workspace root"）、且树导出不得超 freeze 上限
（整仓 clone 超限，嵌套小仓解决）；macOS 下 registry root 用 `/tmp/...` 与 EvalSymlinks 后的
`/private/tmp/...` 不一致时也会不可读。

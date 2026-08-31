# 知识检索器：文件系统直读 + 词频打分

Status: implemented

## 决策与理由

M3 的 KnowledgeRetriever 落在 `agent-team-workbench/internal/knowledge`：`Retriever` 接口 + `FileRetriever` 实现，直读 knowledge 根目录下 `<corpus>/*.md`，frontmatter（gopkg.in/yaml.v3，go.mod 已有依赖）与正文二级节解析后就地打分返回。选文件系统直读：MVP 语料是人工 review 的法典条目（几十条量级），每次全量遍历解析的重建成本为零，换来零索引失效面、零额外存储概念。

打分规则固定为词频加权：term 与 ID 全等（大小写不敏感）+10、标题每次命中 +3、正文每次命中 +1；零分不进结果；Score 降序、同分 ID 升序保证确定性；Limit 缺省 5、上限 20。Snippet 取首个命中段落截 200 rune（rune 口径切断，不破坏多字节字符）。

Supersede 感知：默认只返回 status=effective（Query.Status 可指定其他档或 any 全量）；同 ID 多版本只取最高 version，次序为「先按状态过滤、再按版本去重」——默认查询下「v1 effective + v2 draft」返回 v1，各状态切片内部自洽。corpus 目录不存在返回空结果而非错误（knowledge 层可选部署）；条目文件解析失败（frontmatter 缺失/未闭合、YAML 非法或含未知键、id/title/status 空、version<1）返回带路径的错误，不静默跳过——坏条目混进法典比一次显式检索失败更贵，语料质量由人工 review 兜底。

## 放弃了什么

- **索引库（bleve / SQLite FTS 等）**：语料几十条，遍历打分亚毫秒；索引引入构建时机、写后失效、额外存储三个新概念，M3 阶段没有一个换得回。
- **向量检索（embedding 召回）**：法典条目短且术语受宪章约束（三值用词 + 术语不造同义词，见 [product-agent-charter.md](../../../docs/product/product-agent-charter.md) §3.4），精确词命中已够召回；embedding 依赖外部服务、打分不可解释，与「法典是决策真相源」的可审计要求冲突。
- **TF-IDF / BM25 统计排序**：语料规模下 IDF 无意义（高频领域词几乎每条都含），固定权重更可预测、可解释、可手工验证。
- **坏条目静默跳过 + 告警**：检索侧静默降级会掩盖语料腐化（字段名 typo 落默认值混进结果），与 [resume 探测永不静默降级](../architecture/2026-08-23-resume-never-silent-degrade.md) 是同一红线。

## 复活条件

- 语料条目 >500 或单条正文显著变长、检索延迟进入 agent 可感知区间 → 重估索引/缓存层；返工点在 `FileRetriever` 的装载实现，`Retriever` 接口与打分契约不动。
- agent 反馈「排序差」或「术语改写后查不到」成规模 → 先考虑同义词表（仍非向量）再考虑 BM25；届时 revisit 本文的固定权重决策。

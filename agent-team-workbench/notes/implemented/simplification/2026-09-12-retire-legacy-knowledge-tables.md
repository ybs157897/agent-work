# 退役旧资料管理员：删除遗留表，管理页改挂 `/library`

Status: implemented

## 决策与理由

迁移 0057 建统一知识库时，对旧「逐条资料管理员」的 13 张业务表 + 旧 FTS 索引 + 索引状态表
选择了「原样保留、只做代码级退役」，理由是可恢复性。2026-09-12 产品决定改为彻底移除：
旧功能与其存量数据都不再需要，留着只会让人误以为还有第二条写入路径。

- 迁移 `0065_retire_legacy_knowledge_tables.sql` DROP 这 15 个对象。删除是安全的：新库用自己的
  命名空间（`knowledge_libraries` / `knowledge_library_*` / `knowledge_document*` /
  `knowledge_assertion*` / `knowledge_release*` / `knowledge_write_tasks` /
  `knowledge_search_index`），Go 源码零引用（`internal/persistence/sqlstore/knowledge_library_retirement_test.go`
  的 `TestLegacyKnowledgeTablesAreNotReferencedByGoSource`），没有任何外键指向它们，保留表上也没有
  触发器读它们。`knowledge_index` 是 FTS5 虚拟表，DROP 会一并带走它的 shadow 表。
- 只有 4 张表仍有数据（共 39 行）：`knowledge_jobs` 6、`knowledge_job_actions` 19、
  `knowledge_query_snapshots` 13、`knowledge_librarian_configs` 1。删除前两库都已全量落盘：
  `.acceptance/backups/2026-09-12-legacy-knowledge-tables/`（整库 `.backup` + DDL/INSERT `.sql` +
  JSON + `manifest.txt` 里的 sha256）。旧 FTS shadow 表的内容只存在于整库备份中。
- 守卫测试随之反向：`TestLegacyKnowledgeTablesStillExist` 删除，换成
  `TestLegacyKnowledgeObjectsAreDropped`（15 个对象都不存在，且新库五张核心表仍在，避免空 schema 假通过）。
- 前端管理页 URL `/knowledge` → `/library`，与后端 `/library` 命名空间对齐。不加重定向：
  旧地址现在走 `NotFoundPage`。路由、导航、面包屑、Chat 侧栏入口、`web/DESIGN.md` 同步更新；
  页面组件与 `KnowledgeTab` 等域内标识符保持不动（它们描述的是知识库本身，不是旧实现）。

## 放弃了什么

- **沿用 0057 的「原样保留」**：它是上一个决策的默认选项，保留数据不需要任何新工作。否决理由是产品
  判断变了——没有读取入口的历史数据不是资产而是误导，且备份已经承担了可恢复性。
- **只删空表、保留有数据的 4 张**：看似最保守，实际最糟——留下的是「退役了一半」的 schema，谁也不知道
  剩下的表算不算真相源。
- **保留 `/knowledge` 或加 301/前端重定向**：会长期存在两套入口命名，违背删除优先于垫片；本仓库未对外
  承诺兼容性。
- **重命名页面模块与组件（`knowledge.page.tsx` / `KnowledgePage` / `KnowledgeTab`）**：域内词汇本就该叫
  knowledge，改名只是噪音；URL 才是对外的入口契约。

## 复活条件

需要找回旧记录时：`.acceptance/backups/2026-09-12-legacy-knowledge-tables/` 里的整库 `.backup` 可直接挂起读取，
`*-legacy.sql` 可按需只恢复某张表。恢复是数据操作，不需要恢复任何代码——旧代码已按「删除优先于垫片」
成建制删除，仓库历史里可查。

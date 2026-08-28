# Obsidian · 个人便携、不内置分发

Status: proposed

索引：
[obsidian-portable-personal-use.md](../../../../agent-team-workbench-docs/待实现/obsidian-portable-personal-use.md)

## 决策与理由

用户问能否把 Obsidian「不安装、二进制放项目里当内部工具」。澄清场景为**本人使用**后约定：

1. **个人便携可以**：从官网取得官方构建，放本机任意路径运行；不必装到系统 Applications。
2. **仓内只放数据**：Markdown vault 可以进 git；Obsidian 二进制**不进仓、不进制品**。
3. **启动用本机路径**：例如 `open -a /path/to/Obsidian.app /path/to/vault`，helper 最多写「打开」，不写「下载/vendoring」。

依据 [Obsidian Terms](https://obsidian.md/terms)：禁止 license / sell / transfer / **distribute or share** Software。团队内「项目内置免安装」等于再分发，个人本机副本不属于该路径。

## 放弃了什么

- **把官方二进制 vendoring 进仓库当内置工具**：EULA 明确禁止再分发；与 FreeBSD 无法打包同一原因。
- **当作 workbench 产品/CI 依赖**：产品链不应绑定专有闭源桌面 App。
- **此时选型开源替代（Logseq / SilverBullet 等）**：当前需求是个人笔记便携，不是团队可再分发查看器；若需求变成「仓内自带可分发知识库 UI」再另开提案。

## 负向保证（落地时）

- 任何 helper / 文档示例**不得**指示 `git add` Obsidian 二进制，或从制品服务器分发官方包。
- 不把 Obsidian 写进 CI、Dockerfile、或「克隆即用」的 onboarding 必装闭源二进制步骤。
- `agent-team-workbench/notes/` 决策留痕目录与 Obsidian vault **物理分离**，避免 lifecycle 目录被 vault 插件改写。

## 复活 / 落地条件

【需要仓内可复制的个人约定：gitignore 路径 + 可选 open-vault 脚本 + vault 根路径】
→ 在 docs「待实现」对应条目落地上述三项，并将本 note 迁到 `notes/implemented/process/`；
若需求变为「团队免安装共用查看器」→ **先 reject 或归档本 note 的「仅个人」边界**，另开可再分发方案的 proposed note，不得在本约定下偷塞官方二进制。

# Obsidian 个人便携用法（索引）

> 结论与落地约定。用户 2026-08-29 确认场景为**本人使用**（非团队内置分发）。**尚未落脚本 / gitignore / vault 约定到仓内。**

| 产物 | 路径 |
| --- | --- |
| 本索引 | [obsidian-portable-personal-use.md](./obsidian-portable-personal-use.md) |
| 决策 note（proposed） | [../../agent-team-workbench/notes/proposed/process/2026-08-29-obsidian-portable-personal-use.md](../../agent-team-workbench/notes/proposed/process/2026-08-29-obsidian-portable-personal-use.md) |
| 待实现总览 | [README.md](./README.md) |

## 一句话

本人可把官方 Obsidian 二进制放本机任意目录便携跑；**vault（Markdown）可进仓，二进制永不进仓、永不随项目分发。**

## 结论速查

| 做法 | 是否可行 |
| --- | --- |
| 官网下载，本机目录便携启动（不装到 `/Applications`） | ✅ 个人用 |
| 仓库只存 vault（`.md` / `.obsidian` 配置按需） | ✅ |
| `open -a /path/to/Obsidian.app /path/to/vault` | ✅ |
| 把 `Obsidian.app` / AppImage 提交进 git 或制品 | ❌（再分发，EULA 禁止） |
| 当「项目内置工具」随仓给同事免安装用 | ❌ |

条款依据：[Obsidian Terms](https://obsidian.md/terms)——软件许可非卖断；禁止 distribute / share 给第三方。FreeBSD 等因「不许再分发」也无法做官方预编译包，与 vendoring 同属一类。

## 待落地（本条目）

1. 仓内 `.gitignore` 明确忽略本机 Obsidian 二进制路径约定（若采用项目旁目录）。
2. 可选：个人 helper 脚本（打开 vault；不打包、不下载官方二进制）。
3. 可选：vault 根目录约定（例如 `notes/vault/` 或独立知识库路径）——与 `agent-team-workbench/notes/` 决策 note 生命周期目录区分清楚，避免混用。

## 非目标

- 不把 Obsidian 做成 workbench 产品依赖或 CI 工具链。
- 不替团队做可再分发的「内置知识库 App」；若将来要团队共用查看器，另选可再分发的开源方案（另开 note）。

# 内置 Runtime 二进制

仓库内置 Codex / Kimi CLI，克隆后无需再下载。control-plane 与 runnerd 会按当前 OS/CPU 自动选用对应路径；仍可用环境变量覆盖。

| Runtime | 版本 | 平台目录 |
|---------|------|----------|
| Codex | [v0.149.0](https://github.com/openai/codex/releases/tag/rust-v0.149.0) | `codex/darwin-arm64`, `codex/windows-amd64` |
| Kimi | [v0.38.0](https://github.com/MoonshotAI/kimi-code/releases/tag/%40moonshot-ai%2Fkimi-code%400.38.0) | `kimi/darwin-arm64`, `kimi/windows-amd64` |

> ⚠️ **本机 `kimi/darwin-arm64/kimi` 是本地打补丁的构建，不是官方发布包。**
>
> 基于官方 `@moonshot-ai/kimi-code@0.38.0`（commit `0999454bd`）源码加了「结构化最终输出」补丁：
> `kimi -p --json` / `--output-schema`，以及 kap-server 的 `output_contract` 请求字段。
> 版本号仍是 `0.38.0`（刻意与官方 tag 一致）。
>
> - SHA-256：`7afed09696e83f07b029413f93523a0f10ce951c455b5a0a854e7eba790ebe94`
> - 构建与补丁说明：`/Users/yin/Documents/ybs/code/kimi-structured-output-build/`（见 `DELIVERY.md`、`/Users/yin/Documents/ybs/code/kimi-code/LOCAL-MODIFICATIONS.md`）
> - 回滚原件：`/Users/yin/Documents/ybs/code/kimi-structured-output-build/artifacts/rollback/kimi-0.38.0-official-darwin-arm64.bak`
>   （SHA-256 `92bf3b4b6643e7c4cc12c82e5680cc5b54a5a6768a301de815e5e9a02d2184bb`）
>
> **注意**：该路径由 Git LFS 跟踪（见根 `.gitattributes`）。执行 `git lfs pull` 或重新 checkout 会**静默还原为官方二进制**，本地补丁丢失；要恢复需从上面的产物目录重新复制。

## 目录布局

```text
runtimes/
  codex/
    darwin-arm64/codex
    windows-amd64/codex.exe
    windows-amd64/codex-command-runner.exe
    windows-amd64/codex-windows-sandbox-setup.exe
  kimi/
    darwin-arm64/kimi
    windows-amd64/kimi.exe
```

## 默认行为

启动时自动解析（无需设置 `ATW_CODEX_BIN` / `ATW_KIMI_BIN`）：

- macOS Apple Silicon → `runtimes/*/darwin-arm64/`
- Windows x64 → `runtimes/*/windows-amd64/`
- Codex app-server → 始终启用稳定 `multi_agent`；原生 OpenAI/Codex provider 额外启用 `multi_agent_v2`，第三方 Responses provider 显式关闭 v2；未配置 effort 时默认 `ultra`，显式 effort 保留。
- Kimi app-server → 每轮 profile 默认 `permission_mode=yolo`、`swarm_mode=true`；显式 manual 权限仍保留。

覆盖示例：

```bash
ATW_CODEX_BIN=/path/to/codex make run-control-plane
```

## Git LFS

单文件超过 GitHub 100MB 限制，这些二进制通过 **Git LFS** 跟踪。克隆后若二进制缺失：

```bash
git lfs install
git lfs pull
```

## 升级版本

1. 从上述 Release 页下载对应平台包并解压到 `runtimes/<name>/<platform>/`
2. 更新本文件版本表
3. 本地验证：`make run-control-plane` 日志应出现 `codexapp: 已注册` / `kimi: 已注册`

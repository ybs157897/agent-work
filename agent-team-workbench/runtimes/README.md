# 内置 Runtime 二进制

仓库内置 Codex / Kimi CLI，克隆后无需再下载。control-plane 与 runnerd 会按当前 OS/CPU 自动选用对应路径；仍可用环境变量覆盖。

| Runtime | 版本 | 平台目录 |
|---------|------|----------|
| Codex | [v0.149.0](https://github.com/openai/codex/releases/tag/rust-v0.149.0) | `codex/darwin-arm64`, `codex/windows-amd64` |
| Kimi | [v0.38.0](https://github.com/MoonshotAI/kimi-code/releases/tag/%40moonshot-ai%2Fkimi-code%400.38.0) | `kimi/darwin-arm64`, `kimi/windows-amd64` |

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

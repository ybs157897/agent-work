# web-idea 工作指令

## 纪律

本仓库遵循 `dsh-dev-workflow`：一任务一分支、分刀提交、删除优先于垫片、证据匹配表面、决策留痕。

## 范围红线（负向保证）

1. **MVP 不做完整 IDE**：不做调试器 UI、重构写盘、终端复刻、插件市场。
2. **语义能力只接 Java（jdtls）**：其他语言最多语法高亮，不上第二套 language server，除非新开 ADR。
3. **Gateway 是唯一信任边界**：浏览器永不直连 jdtls；一切路径必须经 Workspace Root 沙箱。
4. **不嵌入 Theia / code-server 整机**：复用 Monaco + LSP 协议，不嵌整站 IDE 产品。
5. **文档与实现同寿**：改组件边界或协议时，同一提交更新 `docs/architecture/` 与相关 ADR / notes。

## 文档权威

- 产品范围：`docs/product/vision.md`
- 架构事实源：`docs/architecture/`
- 决策：`docs/decisions/`（ADR）+ `notes/{lifecycle}/architecture/`

## 验证（触面）

- Gateway：`cd apps/gateway && go test ./...`
- Web：`cd apps/web && pnpm exec tsc -b`（或 `pnpm build`）
- 契约：改 API 时同步 `contracts/openapi/gateway-v1.yaml`
- 一键：`bash scripts/verify-scaffold.sh`

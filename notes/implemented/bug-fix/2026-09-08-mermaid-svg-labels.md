# Mermaid 节点文字保留在 SVG 清洗边界内

Status: implemented

## 决策与理由

产品画布真实验收发现流程图只剩方框，`.chat-mermaid svg .node` 的文本均为空。Mermaid 默认输出 foreignObject HTML 标签，而已有 DOMPurify 仅允许 SVG profile，导致标签被清除。

在共享 [MermaidDiagram](../../../agent-team-workbench/web/src/components/chat/mermaid-diagram.tsx) 初始化中设置根级 `htmlLabels: false`，生成原生 SVG 文字。保留 `securityLevel: strict` 和原有 SVG 清洗。明暗模式实际渲染均保留 6 个节点标签，回归断言见[验收记录](../../../docs/product/product-agent-canvas-acceptance.md#mermaid-回归断言)。

## 放弃了什么

不扩大 HTML/foreignObject 的允许范围，不绕过 DOMPurify，也不把只有方框的截图视为图表验收通过。

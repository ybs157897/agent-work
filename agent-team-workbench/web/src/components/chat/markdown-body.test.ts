// 只钉可离线验证的纯函数（hasHanText / tableToTsv）。
// ErrorBoundary 与 TableCard 是组件：本仓库 vitest 跑 node 环境（vite.config.ts
// test.environment: 'node'）无 DOM/渲染器，不在此测组件行为。
import { describe, expect, it } from "vitest";
import {
  isSafeMarkdownImageSource,
  normalizeCalloutType,
  remarkCallouts,
  stripThinkTags,
  type MdNode,
} from "./markdown-body";
import { tableToTsv } from "./table-card";
import { hashCode, mermaidCacheKey } from "./mermaid-diagram";

describe("Markdown image source safety", () => {
  it("allows ordinary URLs and relative file names", () => {
    for (const source of [
      "https://example.com/a.png",
      "image.png",
      "../a.png",
      "blob:https://example/a",
    ]) {
      expect(isSafeMarkdownImageSource(source)).toBe(true);
    }
  });
  it("rejects empty values and executable schemes", () => {
    for (const source of [
      undefined,
      "",
      "javascript:alert(1)",
      "vbscript:msgbox(1)",
      "data:text/html,bad",
      "file:///tmp/a",
    ]) {
      expect(isSafeMarkdownImageSource(source)).toBe(false);
    }
  });
});

describe("Markdown safety and callout contracts", () => {
  it("strips private reasoning tags before markdown parsing", () => {
    expect(
      stripThinkTags("before\n<think>secret **reasoning**</think>\nafter"),
    ).toBe("before\n\nafter");
  });
  it("normalizes supported and unknown callout labels", () => {
    expect(normalizeCalloutType("WARNING")).toBe("warning");
    expect(normalizeCalloutType("unknown")).toBe("note");
  });
  it("rewrites GitHub alerts and preserves markdown body nodes", () => {
    const tree: MdNode = {
      type: "root",
      children: [
        {
          type: "blockquote",
          children: [
            {
              type: "paragraph",
              children: [{ type: "text", value: "[!TIP] Helpful **hint**" }],
            },
          ],
        },
      ],
    };
    remarkCallouts()(tree);
    expect(tree.children![0]?.data?.hProperties?.className).toEqual([
      "chat-callout",
      "chat-callout-tip",
    ]);
    expect(tree.children![0]?.children?.[1]?.children?.[0]?.value).toBe(
      "Helpful **hint**",
    );
  });
  it("rewrites :::warning containers", () => {
    const tree: MdNode = {
      type: "root",
      children: [
        {
          type: "paragraph",
          children: [{ type: "text", value: ":::warning\nBe careful" }],
        },
        { type: "paragraph", children: [{ type: "text", value: ":::" }] },
      ],
    };
    remarkCallouts()(tree);
    expect(tree.children!).toHaveLength(1);
    expect(tree.children![0]?.data?.hProperties?.className).toEqual([
      "chat-callout",
      "chat-callout-warning",
    ]);
  });
});

describe("Mermaid render helpers", () => {
  it("creates stable ids and separates cache themes", () => {
    expect(hashCode("graph TD; A-->B")).toBe(hashCode("graph TD; A-->B"));
    expect(hashCode("graph TD; A-->B")).not.toBe(hashCode("graph TD; A-->C"));
    expect(mermaidCacheKey("A", "default")).not.toBe(
      mermaidCacheKey("A", "dark"),
    );
  });
});

describe("tableToTsv（表格复制内容的拼装）", () => {
  it("基本转换：\\t 拼列、\\n 拼行", () => {
    expect(
      tableToTsv([
        ["a", "b"],
        ["c", "d"],
      ]),
    ).toBe("a\tb\nc\td");
  });

  it("空单元格保留占位（连续分隔符不塌缩）", () => {
    expect(
      tableToTsv([
        ["a", ""],
        ["", ""],
      ]),
    ).toBe("a\t\n\t");
  });

  it("单行单格与空表", () => {
    expect(tableToTsv([["only"]])).toBe("only");
    expect(tableToTsv([])).toBe("");
  });
});

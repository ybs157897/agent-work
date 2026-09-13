import { renderToStaticMarkup } from 'react-dom/server';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { ContentBlockDocument } from '../../utils/content-blocks';
import { AssistantTurn } from './assistant-turn';
import { downloadDocumentMarkdown } from './delivered-document-panel';

/** 与真实样例同形的最小前言：过程 + 结论先行 + 分隔线。 */
const INTRO = [
  '我先核对仓库现状，再分派只读取证。**结论先行**：在册入口只有一个本地过滤框。',
  '',
  '---',
  '',
  '',
].join('\n');

/** H1 + 5 个 H2 + 12 条 AC + 末尾 languagegui 表格。 */
function acceptanceDocument(): string {
  const criteria = Array.from({ length: 12 }, (_, index) => {
    const id = String(index + 1).padStart(2, '0');
    return [
      `- [ ] **AC-${id} 命中必现**：命中项**应当**保留在看板中。`,
      '',
      `  判定：构造标题含唯一词 AC-${id} 的根任务，断言卡片仍在。`,
      '',
    ].join('\n');
  });
  return [
    '# 《任务看板标题搜索》验收条件',
    '',
    '日期：2026-09-13。状态：草案。',
    '',
    '## 适用范围',
    '',
    '- 入口：`/tasks` 头部搜索框。',
    '',
    '## 验收条件',
    '',
    ...criteria,
    '## 待裁决',
    '',
    '- 是否保留 FTS 检索面板。',
    '',
    '## 非目标',
    '',
    '- 不覆盖第二套解析器。',
    '',
    '## 判定环境与证据要求',
    '',
    '- 现有测试只覆盖本地过滤。',
    '',
    '```languagegui',
    '{"version":"languagegui/v1","blocks":[{"type":"table","title":"三处计数口径"}]}',
    '```',
    '',
  ].join('\n');
}

const DELIVERED = INTRO + acceptanceDocument();

const canonical: ContentBlockDocument = {
  version: 'languagegui/v1',
  blocks: [
    {
      type: 'metric',
      title: '完成态指标',
      items: [{ label: '质量', value: '通过', tone: 'neutral' }],
    },
  ],
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('AssistantTurn · 正文交付文档阅读面', () => {
  it('切出独立文档面板：前言留在原位，文档整体进面板', () => {
    const html = renderToStaticMarkup(<AssistantTurn text={DELIVERED} runId="run_1" messageId="msg_1" />);
    const introIndex = html.indexOf('结论先行');
    const panelIndex = html.indexOf('data-delivered-document');
    const firstCriterion = html.indexOf('AC-01 命中必现');
    const lastSection = html.indexOf('判定环境与证据要求');

    expect(introIndex).toBeGreaterThan(-1);
    expect(panelIndex).toBeGreaterThan(introIndex);
    expect(firstCriterion).toBeGreaterThan(panelIndex);
    expect(lastSection).toBeGreaterThan(firstCriterion);
    expect(html.match(/data-delivered-document/g)).toHaveLength(1);
  });

  it('文档正文只出现一次：标题、AC 与文后小节都不丢失不重复', () => {
    const html = renderToStaticMarkup(<AssistantTurn text={DELIVERED} />);
    expect(html.match(/<h1>/g)).toHaveLength(1);
    expect(html.match(/AC-12 命中必现/g)).toHaveLength(1);
    expect(html.match(/判定环境与证据要求/g)).toHaveLength(1);
    expect(html).toContain('class="chat-document-title"');
    expect(html).toContain('《任务看板标题搜索》验收条件');
  });

  it('提供复制原文 Markdown、下载 .md 与单独阅读三个入口', () => {
    const html = renderToStaticMarkup(<AssistantTurn text={DELIVERED} />);
    expect(html).toContain('aria-label="复制文档 Markdown"');
    expect(html).toContain('aria-label="下载文档 .md"');
    expect(html).toContain('aria-label="单独阅读文档"');
    expect(html).toContain('aria-haspopup="dialog"');
    // 服务端渲染没有 document，Drawer 浮层不在静态标记里。
    expect(html).not.toContain('role="dialog"');
  });

  it('普通短答与只有小标题的长回答维持整篇直出', () => {
    const short = renderToStaticMarkup(<AssistantTurn text="好的，已完成。" />);
    expect(short).not.toContain('data-delivered-document');
    expect(short).toContain('<div class="chat-prose">');

    const headingsOnly = renderToStaticMarkup(
      <AssistantTurn text={`# 标题\n\n${'内容段落。'.repeat(80)}\n\n末尾标记`} />,
    );
    expect(headingsOnly).not.toContain('data-delivered-document');
    expect(headingsOnly).toContain('末尾标记');
  });

  it('canonical content block 留在文档原位，不重复也不丢顺序', () => {
    const html = renderToStaticMarkup(
      <AssistantTurn text={DELIVERED} contentBlocks={canonical} runId="run_1" messageId="msg_1" />,
    );
    const panelIndex = html.indexOf('data-delivered-document');
    const blockIndex = html.indexOf('data-content-block="metric"');
    const lastSection = html.indexOf('判定环境与证据要求');
    expect(html.match(/data-content-block="metric"/g)).toHaveLength(1);
    expect(blockIndex).toBeGreaterThan(panelIndex);
    expect(blockIndex).toBeGreaterThan(lastSection);
    expect(html).toContain('完成态指标');
    expect(html).not.toContain('三处计数口径');
  });

  it('流式期间面板稳定：只保留一个光标且不重复文档', () => {
    const html = renderToStaticMarkup(<AssistantTurn text={DELIVERED} streaming />);
    expect(html.match(/data-delivered-document/g)).toHaveLength(1);
    expect(html.match(/chat-stream-caret/g)).toHaveLength(1);
  });

  it('前置解释为空时只渲染文档面板', () => {
    const html = renderToStaticMarkup(<AssistantTurn text={acceptanceDocument()} />);
    expect(html).toContain('data-delivered-document');
    expect(html).not.toContain('结论先行');
  });
});

describe('downloadDocumentMarkdown', () => {
  it('用标题派生的 .md 文件名下载原文 Markdown', async () => {
    const click = vi.fn();
    const anchor = { href: '', download: '', click };
    let captured: Blob | undefined;
    vi.stubGlobal('document', { createElement: vi.fn(() => anchor) });
    vi.stubGlobal('URL', {
      createObjectURL: vi.fn((blob: Blob) => {
        captured = blob;
        return 'blob:test';
      }),
      revokeObjectURL: vi.fn(),
    });

    downloadDocumentMarkdown(acceptanceDocument(), '《任务看板标题搜索》验收条件');

    expect(anchor.download).toBe('《任务看板标题搜索》验收条件.md');
    expect(click).toHaveBeenCalledOnce();
    expect(captured?.type).toBe('text/markdown;charset=utf-8');
    await expect(captured?.text()).resolves.toBe(acceptanceDocument());
  });

  it('没有 document 时静默跳过', () => {
    expect(() => downloadDocumentMarkdown('# 文档', '标题')).not.toThrow();
  });
});

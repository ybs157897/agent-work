import { describe, expect, it } from 'vitest';
import { splitStreamingMarkdown, splitStreamingMarkdownBlocks } from './streaming-markdown';

describe('splitStreamingMarkdown', () => {
  it('passes ordinary Markdown and complete fences through unchanged', () => {
    const ordinary = '# 标题\n\n- **完成**\n- 进行中';
    expect(splitStreamingMarkdown(ordinary)).toEqual({ visibleText: ordinary, pendingText: '' });

    const complete = '前缀\n\n```ts\nconst answer = 42;\n```\n\n后缀';
    expect(splitStreamingMarkdown(complete)).toEqual({ visibleText: complete, pendingText: '' });
  });

  it('buffers an incomplete fence while keeping its completed prefix visible', () => {
    const text = '已完成 **正文**。\n\n```ts\nconst answer =';
    expect(splitStreamingMarkdown(text)).toEqual({
      visibleText: '已完成 **正文**。\n\n',
      pendingText: '```ts\nconst answer =',
      pendingKind: 'code',
    });
  });

  it('classifies LanguageGUI, Mermaid and tilde fences', () => {
    expect(splitStreamingMarkdown('```languagegui\n{"version":')).toMatchObject({
      visibleText: '',
      pendingKind: 'languagegui',
    });
    expect(splitStreamingMarkdown('```mermaid\ngraph TD;')).toMatchObject({
      visibleText: '',
      pendingKind: 'mermaid',
    });
    expect(splitStreamingMarkdown('~~~tsx\nconst value = 1')).toMatchObject({
      visibleText: '',
      pendingKind: 'code',
    });
  });

  it('does not close a fence with a shorter or mismatched marker', () => {
    const text = '````ts\nconst ticks = "```";\n```\n~~~';
    expect(splitStreamingMarkdown(text)).toMatchObject({
      visibleText: '',
      pendingText: text,
      pendingKind: 'code',
    });
  });

  it('buffers incomplete block math but ignores escaped and inline-code dollars', () => {
    const text = '正文\n\n$$\nE = mc^2';
    expect(splitStreamingMarkdown(text)).toEqual({
      visibleText: '正文\n\n',
      pendingText: '$$\nE = mc^2',
      pendingKind: 'math',
    });
    const complete = '价格 \\$$5，代码 `$$`。\n\n$$x+y$$';
    expect(splitStreamingMarkdown(complete)).toEqual({ visibleText: complete, pendingText: '' });
  });

  it('buffers an incomplete callout container and releases it after closing', () => {
    const incomplete = '说明\n\n:::warning\n请先备份';
    expect(splitStreamingMarkdown(incomplete)).toEqual({
      visibleText: '说明\n\n',
      pendingText: ':::warning\n请先备份',
      pendingKind: 'callout',
    });
    const complete = `${incomplete}\n:::`;
    expect(splitStreamingMarkdown(complete)).toEqual({ visibleText: complete, pendingText: '' });
  });
});

describe('splitStreamingMarkdownBlocks', () => {
  it('commits completed paragraphs while keeping the latest block mutable', () => {
    const text = '第一段 **已完成**。\n\n第二段正在输出';
    const result = splitStreamingMarkdownBlocks(text);
    expect(result.completedBlocks).toEqual(['第一段 **已完成**。\n\n']);
    expect(result.currentBlock).toBe('第二段正在输出');
    expect(result.unsafePending).toBe('');
    expect(result.completedBlocks.join('') + result.currentBlock).toBe(text);
  });

  it('keeps a list in one Markdown tree across blank lines', () => {
    const text = '- one\n- two\n\n- three\n\n正文';
    const result = splitStreamingMarkdownBlocks(text);
    expect(result.completedBlocks).toEqual(['- one\n- two\n\n- three\n\n']);
    expect(result.currentBlock).toBe('正文');
  });

  it('does not expose an incomplete complex block and preserves exact text', () => {
    const text = '说明\n\n```tsx\nconst answer = 42;';
    const result = splitStreamingMarkdownBlocks(text);
    expect(result.completedBlocks).toEqual(['说明\n\n']);
    expect(result.currentBlock).toBe('');
    expect(result.unsafePending).toBe('```tsx\nconst answer = 42;');
    expect(result.completedBlocks.join('') + result.currentBlock + result.unsafePending).toBe(text);
  });

  it('releases a fence atomically, then starts a new mutable block', () => {
    const text = '前文\n\n```ts\nconst answer = 42;\n```\n\n后文';
    const result = splitStreamingMarkdownBlocks(text);
    expect(result.completedBlocks).toEqual(['前文\n\n', '```ts\nconst answer = 42;\n```\n\n']);
    expect(result.currentBlock).toBe('后文');
    expect(result.unsafePending).toBe('');
  });

  it('retains math and callout pending tails byte-for-byte', () => {
    for (const text of ['前文\n\n$$\nx + y', '前文\n\n:::warning\n小心']) {
      const result = splitStreamingMarkdownBlocks(text);
      expect(result.unsafePending).toBe(text.slice(text.indexOf('\n\n') + 2));
      expect(result.completedBlocks.join('') + result.currentBlock + result.unsafePending).toBe(text);
    }
  });
});

import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { MarkdownBody } from './markdown-body';

function render(markdown: string): string {
  return renderToStaticMarkup(<MarkdownBody text={markdown} />);
}

describe('MarkdownBody · LeAgent 内容覆盖', () => {
  it('渲染 GFM 正文标签、代码面板、表格和图片', () => {
    const html = render([
      '# 一级标题',
      '## 二级标题',
      '**粗体** *斜体* ~~删除线~~ `inline` [链接](https://example.com)',
      '- 列表项\n- [x] 已完成',
      '> 引用正文',
      '```ts\nconst answer = 42;\n```',
      '| 名称 | 值 |\n| --- | ---: |\n| answer | 42 |',
      '![示例](image.png)',
    ].join('\n\n'));

    expect(html).toContain('<h1>一级标题</h1>');
    expect(html).toContain('<h2>二级标题</h2>');
    expect(html).toContain('<strong>粗体</strong>');
    expect(html).toContain('<del>删除线</del>');
    expect(html).toContain('class="chat-code-panel my-3"');
    expect(html).toContain('class="chat-table-wrap group"');
    expect(html).toContain('type="checkbox"');
    expect(html).toContain('src="image.png"');
  });

  it('渲染 Callout、KaTeX，并为 Mermaid 保留异步占位', () => {
    const html = render([
      '> [!TIP] 保留 **Markdown** 内容',
      '行内公式 $E=mc^2$',
      '```mermaid\ngraph TD; A-->B\n```',
    ].join('\n\n'));

    expect(html).toContain('chat-callout chat-callout-tip');
    expect(html).toContain('chat-callout-title');
    expect(html).toContain('class="katex"');
    expect(html).toContain('class="chat-mermaid-loading"');
    expect(html).toContain('正在渲染图表');
  });
});

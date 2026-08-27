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

  it('将落定的 languagegui/v1 fence 渲染为结构化块并保留前后正文', () => {
    const html = render([
      '上方说明。',
      '```languagegui',
      JSON.stringify({
        version: 'languagegui/v1',
        blocks: [{ type: 'metric', title: '预算', items: [{ label: '可用额度', value: '$2,480.58' }] }],
      }),
      '```',
      '下方说明。',
    ].join('\n\n'));
    expect(html).toContain('<p>上方说明。</p>');
    expect(html).toContain('data-content-block="metric"');
    expect(html).toContain('可用额度');
    expect(html).toContain('<p>下方说明。</p>');
    expect(html).not.toContain('language-languagegui');
  });

  it('坏 JSON 回落代码面板；流式阶段不提前生成 Widget', () => {
    const invalid = render('```languagegui\n{bad json}\n```');
    expect(invalid).toContain('chat-code-panel');
    expect(invalid).toContain('{bad json}');

    const streaming = renderToStaticMarkup(
      <MarkdownBody
        streaming
        text={'```languagegui\n{"version":"languagegui/v1","blocks":[{"type":"metric","items":[{"label":"x","value":1}]}]}\n```'}
      />,
    );
    expect(streaming).not.toContain('data-content-block="metric"');
    expect(streaming).toContain('language-languagegui');
  });

  it('读取 fence meta，展示文件名、行号与选中行', () => {
    const html = render([
      '```tsx filename=App.tsx {2,4-5}',
      'const one = 1;\nconst two = 2;\nconst three = 3;\nconst four = 4;\nconst five = 5;',
      '```',
    ].join('\n'));
    expect(html).toContain('App.tsx');
    expect(html).toContain('复制代码');
    expect(html).toContain('导出');
    expect(html).toContain('aria-haspopup="menu"');
    expect(html).toContain('>1</span>');
    expect(html).toContain('>5</span>');
    expect(html).toContain('bg-brand-primary/10');
  });

  it('does not emit executable Markdown links', () => {
    const html = render('[unsafe](javascript:alert(1)) [safe](https://example.com)');
    expect(html).not.toContain('javascript:');
    expect(html).toContain('href="https://example.com"');
  });
});

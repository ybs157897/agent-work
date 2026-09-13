import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { MarkdownBody, normalizeKimiMathDelimiters } from './markdown-body';

function render(markdown: string): string {
  return renderToStaticMarkup(<MarkdownBody text={markdown} />);
}

describe('MarkdownBody · LeAgent 内容覆盖', () => {
  it('规范化 Kimi 数学分隔符但保留 fenced 与 inline code 原文', () => {
    const source = [
      String.raw`行内 \(x^2\)。`,
      '',
      String.raw`\[`,
      '  y = mx + b',
      String.raw`\]`,
      '',
      '`\\(not math\\)`',
      String.raw`Escaped \\(not math\\).`,
      '',
      '```text',
      String.raw`\[keep this source\]`,
      '```',
      '',
      '~~~text title=sample',
      String.raw`\(keep tilde source\)`,
      '~~~',
    ].join('\n');
    const normalized = normalizeKimiMathDelimiters(source);
    expect(normalized).toContain('行内 $x^2$');
    expect(normalized).toContain('$$\n  y = mx + b\n$$');
    expect(normalized).toContain('`\\(not math\\)`');
    expect(normalized).toContain(String.raw`Escaped \\(not math\\).`);
    expect(normalized).toContain('\\[keep this source\\]');
    expect(normalized).toContain('\\(keep tilde source\\)');
    expect(render(source)).toContain('class="katex"');
  });
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

  it('完整表格缺少外层闭合符时最终渲染为表格，流式仍保留源码', () => {
    const source = JSON.stringify({
      version: 'languagegui/v1',
      blocks: [{
        type: 'table', title: '已整理文档清单',
        columns: [{ key: 'path', label: '文档' }, { key: 'scope', label: '覆盖内容' }],
        rows: [{ path: 'content/business/flows/order-cancel-to-device-release.md', scope: '订单取消 → 设备占用释放' }],
      }],
    }).slice(0, -2);
    const text = `上方说明。\n\n\`\`\`languagegui\n${source}\n\`\`\`\n\n下方说明。`;
    const html = render(text);
    expect(html).toContain('data-content-block="table"');
    expect(html).toContain('order-cancel-to-device-release.md');
    expect(html).toContain('订单取消 → 设备占用释放');
    expect(html).toContain('<p>上方说明。</p>');
    expect(html).toContain('<p>下方说明。</p>');
    expect(html).not.toContain('chat-code-panel');
    const streaming = renderToStaticMarkup(<MarkdownBody text={text} streaming />);
    expect(streaming).not.toContain('data-content-block="table"');
    expect(streaming).toContain('chat-code-panel');
    expect(render(text.replace('```languagegui', '```json'))).toContain('chat-code-panel');
  });

  it('坏 JSON 回落代码面板；完整的流式 fence 直接生成 Widget', () => {
    const invalid = render('```languagegui\n{bad json}\n```');
    expect(invalid).toContain('chat-code-panel');
    expect(invalid).toContain('{bad json}');

    const streaming = renderToStaticMarkup(
      <MarkdownBody
        streaming
        text={'```languagegui\n{"version":"languagegui/v1","blocks":[{"type":"metric","items":[{"label":"x","value":1}]}]}\n```'}
      />,
    );
    expect(streaming).toContain('data-content-block="metric"');
    expect(streaming).not.toContain('language-languagegui');
  });

  it.each([false, true])('将正文里的裸协议 JSON 原位渲染为指标与表格（streaming=%s）', (streaming) => {
    const source = JSON.stringify({
      version: 'languagegui/v1',
      blocks: [
        { type: 'metric', title: '需求拆解速览', items: [{ label: '需求条目', value: 6, tone: 'positive' }] },
        { type: 'table', title: '需求点与现状对照', columns: [{ key: 'req', label: '需求点' }], rows: [{ req: '只搜标题' }] },
        { type: 'table', title: '验收条件（草案）', columns: [{ key: 'check', label: '检查' }], rows: [{ check: '清空恢复列表' }] },
      ],
    });
    const html = renderToStaticMarkup(
      <MarkdownBody streaming={streaming} text={`需求本身不大。\n\n${source}\n\n待你拍板。`} />,
    );
    expect(html).toContain('<p>需求本身不大。</p>');
    expect(html.match(/data-content-block="metric"/g)).toHaveLength(1);
    expect(html.match(/data-content-block="table"/g)).toHaveLength(2);
    expect(html).toContain('需求拆解速览');
    expect(html).toContain('只搜标题');
    expect(html).not.toContain('&quot;version&quot;');
    expect(html).not.toContain('chat-code-panel');
    expect(html.indexOf('需求本身不大')).toBeLessThan(html.indexOf('data-content-block="metric"'));
    expect(html.indexOf('待你拍板')).toBeGreaterThan(html.lastIndexOf('data-content-block="table"'));
  });

  it('裸协议输出在流式未完成时缓冲，最终失败时保留可读源文', () => {
    const text = '可见 **前缀**。\n\n{"version":"languagegui/v1","blocks":[';
    const streaming = renderToStaticMarkup(<MarkdownBody streaming text={text} />);
    expect(streaming).toContain('<strong>前缀</strong>');
    expect(streaming).not.toContain('version');
    expect(streaming).not.toContain('chat-code-panel');

    const final = render(text);
    expect(final).toContain('<strong>前缀</strong>');
    expect(final).toContain('chat-code-panel');
    expect(final).toContain('languagegui/v1');
  });

  it('流式阶段始终走 Markdown 树，并隐藏未闭合复杂尾部', () => {
    const ordinary = renderToStaticMarkup(
      <MarkdownBody
        streaming
        text={'# 已渲染标题\n\n**加粗正文**\n\n```ts\nconst answer = 42;\n```\n\n$$E=mc^2$$'}
      />,
    );
    expect(ordinary).toContain('<h1>已渲染标题</h1>');
    expect(ordinary).toContain('<strong>加粗正文</strong>');
    expect(ordinary).toContain('chat-code-panel');
    expect(ordinary).toContain('class="katex"');

    const incomplete = renderToStaticMarkup(
      <MarkdownBody
        streaming
        text={'可见 **前缀**。\n\n```languagegui\n{"version":"languagegui/v1","blocks":['}
      />,
    );
    expect(incomplete).toContain('<strong>前缀</strong>');
    expect(incomplete).not.toContain('language-languagegui');
    expect(incomplete).not.toContain('"version"');
    expect(incomplete).not.toContain('chat-code-panel');
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

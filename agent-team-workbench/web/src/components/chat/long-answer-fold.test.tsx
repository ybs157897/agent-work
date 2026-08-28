import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { AssistantTurn } from './assistant-turn';
import { splitLongAnswer } from './long-answer-fold';

/** 落定长文夹具：首个二级标题出现在 300 字符之后，尾部带唯一标记。 */
const LONG_MARKDOWN = [
  '这是一份评审报告的开头概述。'.repeat(24),
  '## 详细发现',
  '正文段落内容。'.repeat(200),
  'TAIL-MARKER 结尾段落。',
].join('\n\n');

describe('splitLongAnswer · 截断点纯函数', () => {
  it('短文本与恰好等于阈值不折叠', () => {
    expect(splitLongAnswer('短回答正文')).toBeNull();
    // 阈值语义是「> 1600 才折叠」：补齐到恰好 1600 仍不折叠。
    const exact = `${'a'.repeat(400)}\n\n${'b'.repeat(1_198)}`;
    expect(exact.length).toBe(1_600);
    expect(splitLongAnswer(exact)).toBeNull();
  });

  it('优先在 300 字符之后的第一个二级/三级标题处截断', () => {
    const h2 = `${'x'.repeat(400)}\n## 小节标题\n${'y'.repeat(1_400)}`;
    expect(splitLongAnswer(h2)).toEqual({
      preview: 'x'.repeat(400),
      truncated: true,
    });

    const h3 = `${'x'.repeat(320)}\n### 三级标题\n${'y'.repeat(1_400)}`;
    expect(splitLongAnswer(h3)?.preview).toBe('x'.repeat(320));
  });

  it('四级及更深标题不充当截断点；无段落边界时不硬折', () => {
    const deep = `${'x'.repeat(320)}\n#### 四级标题\n${'y'.repeat(1_400)}`;
    expect(splitLongAnswer(deep)).toBeNull();
  });

  it('早于 300 字符的标题被否决，回落段落边界', () => {
    // 无段落边界：整个答案不折叠。
    const earlyNoBoundary = `${'a'.repeat(120)}\n## 过早的标题\n${'b'.repeat(1_600)}`;
    expect(splitLongAnswer(earlyNoBoundary)).toBeNull();
    // 有段落边界：按段落规则截断，标题之后的内容进折叠区。
    // （b 段收在 292 字符处，保证标题仍落在 300 之前被否决。）
    const earlyWithBoundary = `${'a'.repeat(200)}\n\n${'b'.repeat(90)}\n## 过早标题\n${'c'.repeat(1_400)}`;
    expect(splitLongAnswer(earlyWithBoundary)?.preview).toBe('a'.repeat(200));
  });

  it('无标题时在不超过 800 字符的最后一个段落边界截断', () => {
    const paragraphs = `${'a'.repeat(500)}\n\n${'b'.repeat(500)}\n\n${'c'.repeat(700)}`;
    expect(splitLongAnswer(paragraphs)).toEqual({
      preview: 'a'.repeat(500),
      truncated: true,
    });
  });

  it('截断点落在未闭合围栏内时回退到围栏之前（段落规则）', () => {
    // 800 字符内最后一个段落边界位于打开的代码围栏内部。
    const fenced = `${'i'.repeat(100)}\n\n\`\`\`ts\n${'x'.repeat(300)}\n\n${'x'.repeat(500)}\n\`\`\`\n\n${'t'.repeat(1_200)}`;
    const split = splitLongAnswer(fenced);
    expect(split?.preview).toBe('i'.repeat(100));
    expect(split?.preview).not.toContain('```');
    expect(fenced.startsWith(split!.preview)).toBe(true);
  });

  it('截断点落在未闭合围栏内时回退到围栏之前（标题规则）', () => {
    // 300 字符后的第一个「标题」其实是围栏内注释行，必须连同围栏一起让位。
    const headingInFence = `${'h'.repeat(340)}\n\n\`\`\`md\n## 假标题\n\`\`\`\n\n${'x'.repeat(1_300)}`;
    const split = splitLongAnswer(headingInFence);
    expect(split?.preview).toBe('h'.repeat(340));
    expect(split?.preview).not.toContain('假标题');
    expect(split?.preview).not.toContain('```');
  });
});

describe('AssistantTurn · 长回答渐进披露', () => {
  it('长文落定态默认折叠：预览 + 渐变淡出 + 展开按钮带剩余体量', () => {
    const html = renderToStaticMarkup(<AssistantTurn text={LONG_MARKDOWN} />);

    // 折叠按钮：幽灵风格 + aria-expanded，剩余体量用「约 N 字」表达。
    expect(html).toContain('chat-long-answer-toggle');
    expect(html).toContain('chat-long-answer-fade');
    expect(html).toMatch(/展开全文（约 \d+ 字）/);
    expect(html).toContain('aria-expanded="false"');
    // 折叠态全文不在 DOM，aria-controls 不得悬空。
    expect(html).not.toContain('aria-controls');
    // 预览只含开头，不含折叠区尾部内容。
    expect(html).toContain('这是一份评审报告的开头概述');
    expect(html).toContain('开头概述');
    expect(html).not.toContain('TAIL-MARKER');
    expect(html).not.toContain('详细发现');
  });

  it('流式态永不折叠：全文直出且无展开按钮', () => {
    const html = renderToStaticMarkup(<AssistantTurn text={LONG_MARKDOWN} streaming />);

    expect(html).toContain('TAIL-MARKER');
    expect(html).toContain('chat-stream-caret');
    expect(html).not.toContain('展开全文');
    expect(html).not.toContain('chat-long-answer-toggle');
  });

  it('短回答与既有的消息操作不受影响', () => {
    const html = renderToStaticMarkup(<AssistantTurn text="短回答正文" />);
    expect(html).toContain('短回答正文');
    expect(html).not.toContain('chat-long-answer-toggle');
    expect(html).not.toContain('aria-expanded');
  });

  it('contentBlocks 始终完整渲染，折叠只作用于 markdown 正文', () => {
    const html = renderToStaticMarkup(
      <AssistantTurn
        text={LONG_MARKDOWN}
        contentBlocks={{
          version: 'languagegui/v1',
          blocks: [
            {
              type: 'metric',
              title: '预算',
              items: [{ label: '可用额度', value: '$2,480.58', tone: 'neutral' }],
            },
          ],
        }}
      />,
    );

    // 正文折叠照常，块不因折叠而消失。
    expect(html).toContain('chat-long-answer-toggle');
    expect(html).not.toContain('TAIL-MARKER');
    expect(html).toContain('data-content-block="metric"');
    expect(html).toContain('可用额度');
  });
});

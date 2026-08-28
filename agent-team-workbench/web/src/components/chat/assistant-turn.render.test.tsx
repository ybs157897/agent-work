import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { ContentBlockDocument } from '../../utils/content-blocks';
import { AssistantTurn } from './assistant-turn';

const document: ContentBlockDocument = {
  version: 'languagegui/v1',
  blocks: [
    {
      type: 'metric',
      title: '完成态指标',
      items: [{ label: '质量', value: '通过', tone: 'neutral' }],
    },
  ],
};

describe('AssistantTurn · canonical ContentBlock placement', () => {
  it('长正文默认完整渲染，不显示隐藏或展开控制', () => {
    const tail = '正文末尾 TAIL-MARKER';
    const html = renderToStaticMarkup(
      <AssistantTurn text={`# 标题\n\n${'内容段落。'.repeat(80)}\n\n${tail}`} />,
    );
    expect(html).toContain(tail);
    expect(html).not.toContain('展开全文');
    expect(html).not.toContain('收起全文');
    expect(html).not.toContain('chat-long-answer-toggle');
  });

  it('在同源 fence 的原位置渲染 canonical block，不移动到回答末尾', () => {
    const html = renderToStaticMarkup(
      <AssistantTurn
        text={'上方正文。\n\n```languagegui\n{"bad":"stream copy"}\n```\n\n下方正文。'}
        contentBlocks={document}
      />,
    );
    const before = html.indexOf('上方正文');
    const block = html.indexOf('data-content-block="metric"');
    const after = html.indexOf('下方正文');
    expect(before).toBeGreaterThanOrEqual(0);
    expect(block).toBeGreaterThan(before);
    expect(after).toBeGreaterThan(block);
    expect(html.match(/data-content-block="metric"/g)).toHaveLength(1);
    expect(html).not.toContain('stream copy');
  });

  it('没有同源 fence 时把 canonical block 追加在正文之后', () => {
    const html = renderToStaticMarkup(
      <AssistantTurn text="正文。" contentBlocks={document} />,
    );
    expect(html.indexOf('data-content-block="metric"')).toBeGreaterThan(html.indexOf('正文。'));
  });

  it('支持 tilde fence，并在多个 fence 违反契约时保留原始顺序', () => {
    const tilde = renderToStaticMarkup(
      <AssistantTurn
        text={'上方。\n\n~~~languagegui\n{"bad":true}\n~~~\n\n下方。'}
        contentBlocks={document}
      />,
    );
    expect(tilde.match(/data-content-block="metric"/g)).toHaveLength(1);
    expect(tilde.indexOf('data-content-block="metric"')).toBeGreaterThan(tilde.indexOf('上方。'));
    expect(tilde.indexOf('下方。')).toBeGreaterThan(tilde.indexOf('data-content-block="metric"'));

    const first = JSON.stringify({
      version: 'languagegui/v1',
      blocks: [{ type: 'metric', title: '原始 A', items: [{ label: 'A', value: '1' }] }],
    });
    const second = JSON.stringify({
      version: 'languagegui/v1',
      blocks: [{ type: 'metric', title: '原始 B', items: [{ label: 'B', value: '2' }] }],
    });
    const multiple = renderToStaticMarkup(
      <AssistantTurn
        text={`前。\n\n\`\`\`languagegui\n${first}\n\`\`\`\n\n中。\n\n\`\`\`languagegui\n${second}\n\`\`\`\n\n后。`}
        contentBlocks={document}
      />,
    );
    expect(multiple).toContain('原始 A');
    expect(multiple).toContain('原始 B');
    expect(multiple).not.toContain('完成态指标');
    expect(multiple.indexOf('原始 A')).toBeLessThan(multiple.indexOf('原始 B'));
  });
});

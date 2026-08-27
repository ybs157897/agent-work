import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { TranscriptView } from './transcript-view';
import type { TranscriptSegment } from '../../utils/chronological-transcript';

describe('production chat LanguageGUI skin', () => {
  it('keeps real user and assistant content/actions while removing duplicate role headers', () => {
    const segments: TranscriptSegment[] = [
      {
        kind: 'user',
        msg: { key: 'run-user', runId: 'run-1', kind: 'user', text: '请总结这次运行。', at: '2026-08-27T12:00:00Z' },
      },
      {
        kind: 'assistant',
        msg: { key: 'run-assistant', runId: 'run-1', kind: 'assistant', text: '# 运行摘要\n\n结果 **已完成**。', at: '2026-08-27T12:00:01Z' },
      },
    ];
    const html = renderToStaticMarkup(
      <div className="tx-scope chat-languagegui-skin">
        <div className="chat-thread">
          <TranscriptView segments={segments} onFork={() => undefined} agent={{ name: 'Atlas' }} />
        </div>
        <div className="chat-composer-stack" />
      </div>,
    );

    expect(html).toContain('tx-scope chat-languagegui-skin');
    expect(html).toContain('chat-composer-stack');
    expect(html).toContain('chat-user-turn');
    expect(html).toContain('chat-assistant-turn');
    expect(html).toContain('请总结这次运行。');
    expect(html).toContain('<div class="chat-prose"><h1>运行摘要</h1>');
    expect(html).toContain('chat-msg-actions');
    expect(html).toContain('aria-label="复制"');
    expect(html).toContain('aria-label="分叉对话"');
    expect(html).not.toContain('chat-role-label');
    expect(html).not.toContain('chat-msg-time');
    expect(html).not.toContain('2026-08-27');
  });

  it('renders canonical ContentBlocks once and removes a duplicate LanguageGUI fence', () => {
    const document = {
      version: 'languagegui/v1' as const,
      blocks: [{
        type: 'metric' as const,
        title: '项目概况',
        items: [{ label: '完成度', value: '68%', tone: 'positive' as const }],
      }],
    };
    const fenced = [
      '这里是摘要。',
      '```languagegui',
      JSON.stringify(document),
      '```',
    ].join('\n');
    const segments: TranscriptSegment[] = [{
      kind: 'assistant',
      msg: {
        key: 'blocks',
        runId: 'run-1',
        kind: 'assistant',
        text: fenced,
        at: '2026-08-27T12:00:00Z',
        contentBlocks: document,
      },
    }];
    const html = renderToStaticMarkup(<TranscriptView segments={segments} agent={{ name: 'Atlas' }} />);
    expect(html).toContain('<p>这里是摘要。</p>');
    expect(html.match(/data-content-block="metric"/g)).toHaveLength(1);
    expect(html).not.toContain('language-languagegui');
  });
});

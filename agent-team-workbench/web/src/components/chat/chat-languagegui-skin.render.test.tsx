import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { TranscriptView } from './transcript-view';
import { shouldAutoCollapseReasoning, WorkActivityTimeline } from './work-activity-timeline';
import type { PresentedTranscriptSegment, WorkTimelineSegment } from '../../utils/work-activity-timeline';

describe('production chat LanguageGUI skin', () => {
  it('auto-collapses a newly settled reasoning phase unless the user interacted', () => {
    expect(shouldAutoCollapseReasoning(null, 'phase-1:settled', false)).toBe(true);
    expect(shouldAutoCollapseReasoning(null, 'phase-1:settled', true)).toBe(false);
    expect(shouldAutoCollapseReasoning('phase-1:settled', 'phase-1:settled', false)).toBe(false);
    expect(shouldAutoCollapseReasoning('phase-1:settled', null, false)).toBe(false);
  });

  it('keeps real user and assistant content/actions while removing duplicate role headers', () => {
    const segments: PresentedTranscriptSegment[] = [
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
    expect(html).toContain('<h1>运行摘要</h1>');
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
    const segments: PresentedTranscriptSegment[] = [{
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

  it('collapses successful work by default but keeps failed evidence expanded', () => {
    const successful: WorkTimelineSegment = {
      kind: 'work-timeline',
      runId: 'run-success',
      status: 'succeeded',
      createdAt: '2026-08-27T12:00:00Z',
      updatedAt: '2026-08-27T12:01:25Z',
      items: [
        {
          kind: 'thinking',
          msg: { key: 'thinking-success', runId: 'run-success', kind: 'thinking', text: '成功过程应折叠', at: '2026-08-27T12:00:20Z' },
        },
        {
          kind: 'assistant',
          msg: { key: 'interim-success', runId: 'run-success', kind: 'assistant', text: '过程正文也应折叠', at: '2026-08-27T12:00:30Z' },
        },
      ],
    };
    const failed: WorkTimelineSegment = {
      kind: 'work-timeline',
      runId: 'run-failed',
      status: 'failed',
      createdAt: '2026-08-27T12:02:00Z',
      updatedAt: '2026-08-27T12:02:05Z',
      items: [{
        kind: 'meta',
        msg: { key: 'failure', runId: 'run-failed', kind: 'error', text: 'provider.api_error', at: '2026-08-27T12:02:05Z' },
      }],
    };
    const unknown: WorkTimelineSegment = {
      kind: 'work-timeline',
      runId: 'run-unknown',
      createdAt: '',
      updatedAt: '',
      items: [],
    };

    const finalAnswer: PresentedTranscriptSegment = {
      kind: 'assistant',
      msg: { key: 'final-success', runId: 'run-success', kind: 'assistant', text: '真正的 final answer', at: '2026-08-27T12:01:25Z' },
    };
    const successHtml = renderToStaticMarkup(<TranscriptView segments={[successful, finalAnswer]} />);
    const failureHtml = renderToStaticMarkup(<TranscriptView segments={[failed]} />);
    const unknownHtml = renderToStaticMarkup(<TranscriptView segments={[unknown]} />);

    expect(successHtml).toContain('已工作 1 分 25 秒');
    expect(successHtml).toContain('aria-expanded="false"');
    expect(successHtml).not.toContain('成功过程应折叠');
    expect(successHtml).not.toContain('过程正文也应折叠');
    expect(successHtml).toContain('真正的 final answer');
    expect(failureHtml).toContain('运行失败 · 5 秒');
    expect(failureHtml).toContain('aria-expanded="true"');
    expect(failureHtml).toContain('provider.api_error');
    expect(unknownHtml).toContain('工作过程');
    expect(unknownHtml).not.toContain('工作中');
  });

  it('running work keeps thinking/tools collapsed while interim output renders as ordinary Markdown sibling', () => {
    const running: WorkTimelineSegment = {
      kind: 'work-timeline',
      runId: 'run-live',
      status: 'running',
      createdAt: '2026-08-27T12:00:00Z',
      updatedAt: '2026-08-27T12:00:05Z',
      items: [
        {
          kind: 'thinking',
          streaming: false,
          renderKey: 'thinking:run-live:phase-1',
          msg: {
            key: 'thinking-live',
            runId: 'run-live',
            kind: 'thinking',
            text: '先分析项目结构，再寻找最脆弱的耦合点',
            at: '2026-08-27T12:00:02Z',
            startedAt: '2026-08-27T12:00:01Z',
          },
        },
        {
          kind: 'assistant',
          renderKey: 'assistant:run-live:0',
          msg: {
            key: 'interim-live',
            runId: 'run-live',
            kind: 'assistant',
            text: '好的，我先全面了解项目架构。',
            at: '2026-08-27T12:00:03Z',
          },
        },
        {
          kind: 'activity',
          runId: 'run-live',
          items: [{
            key: 'tool-live',
            runId: 'run-live',
            kind: 'tool',
            text: '调用工具 Read：src/app.ts',
            at: '2026-08-27T12:00:04Z',
            tool: 'Read',
            argsSummary: 'src/app.ts',
            toolStatus: 'running',
            startedAt: '2026-08-27T12:00:04Z',
          }],
        },
      ],
    };

    const html = renderToStaticMarkup(<TranscriptView segments={[running]} />);
    const thinkingIndex = html.indexOf('先分析项目结构');
    const interimIndex = html.indexOf('aria-label="过程正文"');
    const toolIndex = html.indexOf('data-variant="timeline"');

    expect(html).toContain('aria-label="收起工作过程：工作中');
    expect(html).toContain('aria-label="展开思考 · 持续了');
    expect(html).toContain('：先分析项目结构，再寻找最脆弱的耦合点"');
    expect(html).toContain('aria-label="过程正文"><div class="chat-prose"><p>好的，我先全面了解项目架构。</p>');
    expect(html).toContain('data-variant="timeline"');
    expect(html).toContain('aria-expanded="false"');
    expect(thinkingIndex).toBeGreaterThan(-1);
    expect(interimIndex).toBeGreaterThan(thinkingIndex);
    expect(toolIndex).toBeGreaterThan(interimIndex);
  });

  it('uses the final reasoning delta for duration instead of the next tool boundary', () => {
    const timeline: WorkTimelineSegment = {
      kind: 'work-timeline',
      runId: 'run-reasoning-gap',
      status: 'failed',
      createdAt: '2026-08-28T15:50:01.400673Z',
      updatedAt: '2026-08-28T15:50:54.368966Z',
      items: [
        {
          kind: 'thinking',
          msg: {
            key: 'thinking-gap',
            runId: 'run-reasoning-gap',
            kind: 'thinking',
            text: '准备写入正式文档',
            at: '2026-08-28T15:50:54.368966Z',
            startedAt: '2026-08-28T15:50:01.400673Z',
            completedAt: '2026-08-28T15:50:01.973207Z',
          },
        },
        {
          kind: 'activity',
          runId: 'run-reasoning-gap',
          items: [{
            key: 'write-start',
            runId: 'run-reasoning-gap',
            kind: 'tool',
            text: '调用工具 Write',
            tool: 'Write',
            toolStatus: 'success',
            startedAt: '2026-08-28T15:50:54.368966Z',
            completedAt: '2026-08-28T15:50:55.000000Z',
            at: '2026-08-28T15:50:55.000000Z',
          }],
        },
      ],
    };

    const html = renderToStaticMarkup(<WorkActivityTimeline segment={timeline} />);
    expect(html).toContain('思考 · 持续了 &lt;1 秒');
    expect(html).not.toContain('持续了 52 秒');
  });

  it('settles a quiet live reasoning row and appends the accessible output loader', () => {
    const timeline: WorkTimelineSegment = {
      kind: 'work-timeline',
      runId: 'run-idle-reasoning',
      status: 'running',
      createdAt: '2026-08-28T00:00:00Z',
      updatedAt: '2026-08-28T00:00:01Z',
      items: [{
        kind: 'thinking',
        streaming: true,
        renderKey: 'thinking:run-idle-reasoning:phase-1',
        msg: {
          key: 'thinking-idle',
          runId: 'run-idle-reasoning',
          kind: 'thinking',
          text: '已经输出完这一段思考',
          startedAt: '2026-08-28T00:00:00Z',
          at: '2026-08-28T00:00:01Z',
        },
      }],
    };

    const html = renderToStaticMarkup(<WorkActivityTimeline segment={timeline} />);
    expect(html).toContain('展开思考 · 持续了 1 秒');
    expect(html).not.toContain('正在思考');
    expect(html).toContain('role="status" aria-live="polite"');
    expect(html).toContain('正在输出');
  });

  it('renders the live final draft outside the work timeline with a compact output loader', () => {
    const live: PresentedTranscriptSegment[] = [
      {
        kind: 'work-timeline',
        runId: 'run-live-final',
        status: 'running',
        createdAt: '2026-08-27T12:00:00Z',
        updatedAt: '2026-08-27T12:00:05Z',
        items: [],
      },
      {
        kind: 'assistant',
        streaming: true,
        renderKey: 'assistant:run-live-final:stream',
        msg: {
          key: 'live-final',
          runId: 'run-live-final',
          kind: 'assistant',
          text: '第一段正文。\n\n第二段正在输出',
          at: '2026-08-27T12:00:05Z',
        },
      },
      { kind: 'thinking-placeholder', runId: 'run-live-final' },
    ];

    const html = renderToStaticMarkup(<TranscriptView segments={live} />);
    expect(html).toContain('第一段正文。');
    expect(html).toContain('第二段正在输出');
    expect(html).toContain('role="status" aria-live="polite"');
    expect(html).toContain('正在输出');
    expect(html.indexOf('第一段正文。')).toBeLessThan(html.indexOf('正在输出'));
  });

  it('uses the last non-empty reasoning line as ticker and suppresses an empty streaming row', () => {
    const liveAt = new Date().toISOString();
    const segment: WorkTimelineSegment = {
      kind: 'work-timeline',
      runId: 'run-ticker',
      status: 'running',
      createdAt: liveAt,
      updatedAt: liveAt,
      items: [
        {
          kind: 'thinking',
          streaming: true,
          renderKey: 'thinking:run-ticker:phase-1',
          msg: {
            key: 'thinking-ticker',
            runId: 'run-ticker',
            kind: 'thinking',
            text: '上一行不应进入 ticker\n\n当前最后一行',
            at: liveAt,
          },
        },
        {
          kind: 'thinking',
          streaming: true,
          renderKey: 'thinking:run-ticker:phase-empty',
          msg: {
            key: 'thinking-empty',
            runId: 'run-ticker',
            kind: 'thinking',
            text: '   ',
            at: liveAt,
          },
        },
      ],
    };

    const html = renderToStaticMarkup(<TranscriptView segments={[segment]} />);
    expect(html).toContain('当前最后一行');
    expect(html).not.toContain('上一行不应进入 ticker');
    expect(html).toContain('1 段思考');
    expect(html).not.toContain('2 段思考');
    expect(html).toContain('data-streaming="true"');
  });
});

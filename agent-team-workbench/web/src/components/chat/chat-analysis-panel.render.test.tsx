import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { ChatAnalysisPanel } from './chat-analysis-panel';
import type { ChatAnalysisProjection } from '../../api/chat-analysis';

const baseProjection: ChatAnalysisProjection = {
  workspace_id: 'ws_1', chat_id: 'wi_1', agent_id: 'agent_1', version: 2, revision: 1,
  status: 'needs_answer', pending_count: 2, answered_count: 0, deferred_count: 0,
  document: {
    version: 'chat-analysis/v1', summary: '请确认当前 Java 能力范围。',
    sources: [{ id: 's1', kind: 'code', ref: 'src/main/App.java', sha256: 'a'.repeat(64), read_status: 'partial', limitations: '测试范围未覆盖' }],
    items: [{ id: 'item_1', kind: 'requirement', title: 'Java 能力范围', detail: '需要确认优先级', source_ids: ['s1'], basis: 'observed', impact: '影响第一阶段范围', recommendation: '先确认只读和编辑边界' }],
    questions: [
      { id: 'q1', prompt: '第一阶段包含哪些能力？', selection: 'multiple', options: [{ id: 'read', label: '代码阅读' }, { id: 'edit', label: '代码编辑' }], item_ids: ['item_1'] },
      { id: 'q2', prompt: '后续问题', selection: 'single', options: [{ id: 'yes', label: '是' }, { id: 'no', label: '否' }], item_ids: ['item_1'] },
    ],
  },
  current_question: { id: 'q1', prompt: '第一阶段包含哪些能力？', selection: 'multiple', options: [{ id: 'read', label: '代码阅读' }, { id: 'edit', label: '代码编辑' }], item_ids: ['item_1'] },
  answers: [],
  decisions: [],
};

describe('ChatAnalysisPanel', () => {
  it('renders one current question with checkbox and reviewable items/source details', () => {
    const html = renderToStaticMarkup(
      <ChatAnalysisPanel
        projection={baseProjection}
        draft={null}
        loading={false}
        submitting={false}
        error={null}
        conversationSources={[]}
        onSelection={() => undefined}
        onText={() => undefined}
        onSubmit={() => undefined}
        onRestart={() => undefined}
        onRefresh={() => undefined}
        onRestoreDraftText={() => undefined}
        onDiscardDraft={() => undefined}
      />,
    );
    expect(html).toContain('需求分析');
    expect(html).toContain('type="checkbox"');
    expect(html).toContain('保存回答并继续');
    expect(html).toContain('暂不确定');
    expect(html).toContain('Java 能力范围');
    expect(html).toContain('src/main/App.java');
    expect(html).not.toContain('后续问题');
    expect(html).not.toContain('q2');
  });

  it('renders server failure projection and error even without a document', () => {
    const html = renderToStaticMarkup(
      <ChatAnalysisPanel
        projection={{ workspace_id: 'ws_1', chat_id: 'wi_1', agent_id: 'agent_1', version: 2, revision: 0, status: 'failed', error: 'analysis 输出无效: invalid source version', pending_count: 0, answered_count: 0, deferred_count: 0, answers: [], decisions: [] }}
        draft={null}
        loading={false}
        submitting={false}
        error={null}
        conversationSources={[]}
        onSelection={() => undefined}
        onText={() => undefined}
        onSubmit={() => undefined}
        onRestart={() => undefined}
        onRefresh={() => undefined}
        onRestoreDraftText={() => undefined}
        onDiscardDraft={() => undefined}
      />,
    );
    expect(html).toContain('整理失败');
    expect(html).toContain('invalid source version');
    expect(html).toContain('重新整理');
  });

  it('keeps loading and fetch errors visible before a projection exists', () => {
    const loadingHtml = renderToStaticMarkup(
      <ChatAnalysisPanel projection={null} draft={null} loading submitting={false} error={null} conversationSources={[]} onSelection={() => undefined} onText={() => undefined} onSubmit={() => undefined} onRestart={() => undefined} onRefresh={() => undefined} onRestoreDraftText={() => undefined} onDiscardDraft={() => undefined} />,
    );
    expect(loadingHtml).toContain('需求分析加载中');
    const errorHtml = renderToStaticMarkup(
      <ChatAnalysisPanel projection={null} draft={null} loading={false} submitting={false} error="状态读取失败" conversationSources={[]} onSelection={() => undefined} onText={() => undefined} onSubmit={() => undefined} onRestart={() => undefined} onRefresh={() => undefined} onRestoreDraftText={() => undefined} onDiscardDraft={() => undefined} />,
    );
    expect(errorHtml).toContain('状态读取失败');
  });

  it('marks a same-question old-revision draft stale instead of checking old options', () => {
    const html = renderToStaticMarkup(
      <ChatAnalysisPanel
        projection={baseProjection}
        draft={{ version: 1, revision: 0, questionId: 'q1', selectedOptionIds: ['read'], text: '旧补充' }}
        loading={false}
        submitting={false}
        error={null}
        conversationSources={[]}
        onSelection={() => undefined}
        onText={() => undefined}
        onSubmit={() => undefined}
        onRestart={() => undefined}
        onRefresh={() => undefined}
        onRestoreDraftText={() => undefined}
        onDiscardDraft={() => undefined}
      />,
    );
    expect(html).toContain('旧选项不会自动套用');
    expect(html).toContain('保留补充文字并重新填写');
    expect(html).not.toContain('checked=""');
  });
});

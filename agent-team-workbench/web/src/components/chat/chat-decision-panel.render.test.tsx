import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { ChatDecisionPanel } from './chat-decision-panel';
import type { ChatAnalysisDecision, ChatAnalysisItem } from '../../api/chat-analysis';

const item: ChatAnalysisItem = {
  id: 'item_scope',
  kind: 'requirement',
  title: 'Java 能力范围',
  detail: '需要确认第一阶段覆盖哪些能力。',
  source_ids: ['s1'],
  basis: 'observed',
};

const secondItem: ChatAnalysisItem = {
  ...item,
  id: 'item_exception',
  title: '异常处理',
};

const decision: ChatAnalysisDecision = {
  id: 'decision_1',
  revision: 2,
  item_id: item.id,
  item_fingerprint: 'fingerprint-1',
  outcome: 'confirmed',
  conclusion: '确认代码阅读。',
  basis: '产品同步记录。',
  product_version: '2026-Q3',
  created_at: '2026-09-09T00:02:00Z',
  status: 'needs_reconfirmation',
};

const handlers = {
  onOutcome: () => undefined,
  onConclusion: () => undefined,
  onBasis: () => undefined,
  onProductVersion: () => undefined,
  onSubmit: () => undefined,
  onRecheck: () => undefined,
  onRestoreDraftText: () => undefined,
  onDiscardDraft: () => undefined,
  onReviewChanges: () => undefined,
};

describe('ChatDecisionPanel', () => {
  it('shows one item, all three explicit outcomes, change review and collapsed history', () => {
    const html = renderToStaticMarkup(
      <ChatDecisionPanel
        item={item}
        items={[item, secondItem]}
        selectedItemId={item.id}
        decision={decision}
        revision={2}
        recheckReason="代码来源发生变化"
        history={[{ id: 'history-1', kind: 'decision', revision: 1, created_at: '2026-09-08T00:00:00Z', outcome: 'rejected', conclusion: '旧结论', reason: 'source_changed' }]}
        draft={{ version: 3, revision: 2, itemId: item.id, outcome: 'needs_clarification', conclusion: '', basis: '', productVersion: '' }}
        loading={false}
        historyLoading={false}
        submitting={false}
        error={null}
        {...handlers}
        onSelectItem={() => undefined}
      />,
    );

    expect(html).toContain('产品结论回填');
    expect(html).toContain('需重新确认');
    expect(html).toContain('需要复核：代码来源发生变化');
    expect(html).toContain('确认');
    expect(html).toContain('否决');
    expect(html).toContain('待澄清');
    expect(html).toContain('补充变更并复核');
    expect(html).toContain('查看历史与已答记录（1）');
    expect(html).toContain('异常处理');
    expect(html).toContain('<details');
    expect(html).not.toContain('<details open');
  });

  it('keeps an old-revision draft visibly stale and does not bind it to the current form', () => {
    const html = renderToStaticMarkup(
      <ChatDecisionPanel
        item={item}
        items={[item]}
        selectedItemId={item.id}
        decision={null}
        revision={3}
        history={[]}
        draft={{ version: 3, revision: 2, itemId: item.id, outcome: 'confirmed', conclusion: '旧结论文字', basis: '旧依据', productVersion: 'v1' }}
        loading={false}
        historyLoading={false}
        submitting={false}
        error={null}
        {...handlers}
        onSelectItem={() => undefined}
      />,
    );

    expect(html).toContain('旧结论草稿不会自动套用');
    expect(html).toContain('旧结论文字');
    expect(html).toContain('保留结论文字并重新填写');
    expect(html).not.toContain('value="旧结论文字"');
    expect(html).not.toContain('checked=""');
  });
});

import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { DecisionEntry } from '../../api/types';
import { latestSegmentSeq, TaskLedgerView } from './task-ledger';

const entry = (over: Partial<DecisionEntry> = {}): DecisionEntry => ({
  id: 'dec_1',
  work_item_id: 'wi_1',
  quote: '上线窗口定在下周四，逾期不候。',
  created_at: '2026-08-30T02:00:00Z',
  ...over,
});

describe('TaskLedgerView', () => {
  it('加载中按加载文案呈现', () => {
    const html = renderToStaticMarkup(<TaskLedgerView digest={null} />);
    expect(html).toContain('台账加载中…');
    expect(html).toContain('任务台账');
  });

  it('rolling_digest 以多行预格式化文本展示（保留换行）', () => {
    const html = renderToStaticMarkup(
      <TaskLedgerView digest={'状态：进行中\n最近轮次：初稿完成'} />,
    );
    expect(html).toContain('任务台账');
    expect(html).toContain('状态：进行中\n最近轮次：初稿完成');
    expect(html).toContain('bg-surface-sunken');
  });

  it('digest 为空走 EmptyState（说明生成时机）', () => {
    const html = renderToStaticMarkup(<TaskLedgerView digest="" />);
    expect(html).toContain('尚无台账摘要');
    expect(html).toContain('会话轮次收尾后自动生成');
  });

  it('决策原话按引文样式展示：quote 原文 + 时间 + 来源轮次', () => {
    const html = renderToStaticMarkup(
      <TaskLedgerView digest="" decisions={[entry({ source_run_id: 'run_1' })]} />,
    );
    expect(html).toContain('上线窗口定在下周四，逾期不候。');
    expect(html).toContain('border-l-2'); // 引文左线，区别于普通文本
    expect(html).toContain('来自 run_1');
  });

  it('决策列表为空给出钉取入口提示，未拉取时不渲染提示', () => {
    const empty = renderToStaticMarkup(<TaskLedgerView digest="" decisions={[]} />);
    expect(empty).toContain('暂无决策原话');

    const unloaded = renderToStaticMarkup(<TaskLedgerView digest="" />);
    expect(unloaded).not.toContain('暂无决策原话');
  });

  it('会话段序非空时在标题展示「第 N 段」', () => {
    const html = renderToStaticMarkup(
      <TaskLedgerView digest="x" segmentSeq={3} />,
    );
    expect(html).toContain('第 3 段');
  });
});

describe('latestSegmentSeq', () => {
  const session = (taskKey: string, segmentSeq: number) => ({
    id: `ts_${segmentSeq}`,
    agent_profile_id: 'agent_1',
    adapter_id: 'codex',
    task_key: taskKey,
    runs_count: 1,
    input_tokens_cum: 0,
    segment_seq: segmentSeq,
    created_at: '',
    updated_at: '',
  });

  it('取同任务锚点的最大段序（轮换代际）', () => {
    expect(latestSegmentSeq([session('wi_1', 1), session('wi_1', 3), session('wi_2', 2)], 'wi_1')).toBe(3);
  });

  it('无匹配锚点或其他任务不串号', () => {
    expect(latestSegmentSeq([session('wi_2', 2)], 'wi_1')).toBeNull();
    expect(latestSegmentSeq([], 'wi_1')).toBeNull();
  });
});

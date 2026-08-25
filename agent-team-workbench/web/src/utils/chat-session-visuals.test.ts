import { describe, expect, it } from 'vitest';
import { conversationStatusDotClass, suggestedPrompts } from './chat-session-visuals';
import { conversationLabel } from '../stores/chat.store';

describe('conversationStatusDotClass', () => {
  const item = (latestRunId?: string) => ({ latest_run_id: latestRunId });

  it('执行中状态走品牌色并带脉冲', () => {
    for (const status of ['queued', 'starting', 'running']) {
      expect(conversationStatusDotClass(item('r1'), { r1: { status } })).toBe(
        'bg-brand-accent status-pulse',
      );
    }
  });

  it('终态映射语义色：成功绿/失败红/中断灰', () => {
    expect(conversationStatusDotClass(item('r'), { r: { status: 'succeeded' } })).toBe('bg-status-success');
    expect(conversationStatusDotClass(item('r'), { r: { status: 'failed' } })).toBe('bg-status-error');
    expect(conversationStatusDotClass(item('r'), { r: { status: 'lost' } })).toBe('bg-status-error');
    expect(conversationStatusDotClass(item('r'), { r: { status: 'interrupted' } })).toBe('bg-status-standby');
  });

  it('等待审批用警示黄——优先级高于执行中集合（waiting_approval ∈ ACTIVE）', () => {
    expect(conversationStatusDotClass(item('r'), { r: { status: 'waiting_approval' } })).toBe(
      'bg-status-warning',
    );
  });

  it('无 run 或未知 run 回落待机灰', () => {
    expect(conversationStatusDotClass(item(undefined), {})).toBe('bg-status-standby');
    expect(conversationStatusDotClass(item('missing'), {})).toBe('bg-status-standby');
  });
});

describe('conversationLabel 防回归', () => {
  it('waiting_approval 显示「待审批」而非「思考中…」（判定顺序先于 ACTIVE）', () => {
    const item = { latest_run_id: 'r', phase: 'execution', status: 'in_progress' };
    expect(conversationLabel(item, { r: { status: 'waiting_approval' } })).toBe('待审批');
    expect(conversationLabel(item, { r: { status: 'running' } })).toBe('思考中…');
    expect(conversationLabel(item, { r: { status: 'succeeded' } })).toBe('已回复');
  });
});

describe('suggestedPrompts', () => {
  it('已知角色给角色化提示词', () => {
    expect(suggestedPrompts('pm')).toHaveLength(2);
    expect(suggestedPrompts('architect')[0]).toContain('架构');
    expect(suggestedPrompts('developer')[0]).toContain('审查');
    expect(suggestedPrompts('ui')[0]).toContain('界面');
    expect(suggestedPrompts('reviewer')[0]).toContain('评审');
  });

  it('未知与空角色回落通用提示词', () => {
    expect(suggestedPrompts('unknown-role')).toEqual(['帮我了解当前项目的现状', '列出当前待办并给出优先级建议']);
    expect(suggestedPrompts(undefined)).toHaveLength(2);
  });
});

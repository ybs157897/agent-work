import { describe, expect, it } from 'vitest';
import type { ApprovalRequest } from '../../api/types';
import { resolvedApprovalLine } from './approval-card';

const base: ApprovalRequest = {
  id: 'ap_1',
  run_id: 'run_1',
  work_item_id: 'wi_1',
  kind: 'bash',
  risk: 'high',
  status: 'pending',
  summary: 'rm -rf /tmp/scratch',
};

describe('resolvedApprovalLine', () => {
  it('pending 无完成态行（走交互卡）', () => {
    expect(resolvedApprovalLine(base)).toBeNull();
  });

  it('approved/rejected 行携带 kind·risk 与 resolved_at；拒绝行标 deny', () => {
    const approved = resolvedApprovalLine({
      ...base,
      status: 'approved',
      resolved_at: '2026-08-22T00:00:00Z',
    });
    expect(approved?.text).toBe('✓ 审批已批准 · bash · high');
    expect(approved?.deny).toBe(false);
    expect(approved?.at).toBe('2026-08-22T00:00:00Z');

    const rejected = resolvedApprovalLine({ ...base, status: 'rejected' });
    expect(rejected?.text).toBe('✕ 审批已拒绝 · bash · high');
    expect(rejected?.deny).toBe(true);
    expect(rejected?.at).toBeUndefined();
  });

  it('expired 渲染中性过期行，不带决议标记', () => {
    const line = resolvedApprovalLine({ ...base, status: 'expired' });
    expect(line?.text).toBe('审批已过期 · bash · high');
    expect(line?.deny).toBe(false);
  });
});

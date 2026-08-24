import { describe, expect, it } from 'vitest';
import type { ApprovalRequest } from '../../api/types';
import { allowChoices, approvalDetailText, approvalHeadline, cardAllowChoices, resolvedApprovalLine } from './approval-card';

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

describe('allowChoices（三级授权）', () => {
  it('顺序 once → thread → workspace，仅 workspace 级标警示（次级位置）', () => {
    const choices = allowChoices();
    expect(choices.map((c) => c.scope)).toEqual(['once', 'thread', 'workspace']);
    expect(choices.filter((c) => c.danger)).toHaveLength(1);
    expect(choices[2].danger).toBe(true);
  });

  it('非 once 选项文案明示授权记忆语义（本会话/工作区）', () => {
    const choices = allowChoices();
    expect(choices[0].label).toBe('允许一次');
    expect(choices[1].label).toContain('本会话');
    expect(choices[2].label).toContain('工作区');
    for (const c of choices) {
      expect(c.toast).not.toHaveLength(0);
    }
  });

  it('非可授权 kind（tool/question 等）只保留「允许」，不展示会被 422 的授权项', () => {
    expect(cardAllowChoices('command').map((c) => c.scope)).toEqual(['once', 'thread', 'workspace']);
    expect(cardAllowChoices('file_change')).toHaveLength(3);
    expect(cardAllowChoices('permissions')).toHaveLength(3);
    expect(cardAllowChoices('tool').map((c) => c.scope)).toEqual(['once']);
    expect(cardAllowChoices('question').map((c) => c.scope)).toEqual(['once']);
  });
});

describe('approvalHeadline', () => {
  it('maps command kind to human headline', () => {
    expect(approvalHeadline('command')).toBe('允许执行此命令？');
  });
});

describe('approvalDetailText', () => {
  it('strips codex command prefix from summary', () => {
    expect(approvalDetailText("Codex 请求执行命令: go test ./...")).toBe('go test ./...');
  });
});

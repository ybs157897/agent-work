import { describe, expect, it } from 'vitest';
import type { ApprovalRequest } from '../../api/types';
import {
  allowChoices,
  approvalDetailText,
  approvalHeadline,
  approvalReceipt,
  autoApproveEligible,
  cardAllowChoices,
} from './approval-card';

const base: ApprovalRequest = {
  id: 'ap_1',
  run_id: 'run_1',
  work_item_id: 'wi_1',
  kind: 'bash',
  risk: 'high',
  status: 'pending',
  summary: 'rm -rf /tmp/scratch',
};

describe('approvalReceipt', () => {
  it('pending 无回执（走交互卡）', () => {
    expect(approvalReceipt(base)).toBeNull();
  });

  it('approved/rejected 回执携带 kind·risk 与 resolved_at；图标态区分', () => {
    const approved = approvalReceipt({
      ...base,
      status: 'approved',
      resolved_at: '2026-08-22T00:00:00Z',
    });
    expect(approved?.label).toBe('已批准 · bash · 高风险');
    expect(approved?.icon).toBe('approved');
    expect(approved?.at).toBe('2026-08-22T00:00:00Z');

    const rejected = approvalReceipt({ ...base, status: 'rejected' });
    expect(rejected?.label).toBe('已拒绝 · bash · 高风险');
    expect(rejected?.icon).toBe('rejected');
    expect(rejected?.at).toBeUndefined();
  });

  it('expired 渲染中性过期回执；低风险不标风险后缀', () => {
    const line = approvalReceipt({ ...base, status: 'expired' });
    expect(line?.label).toBe('已过期 · bash · 高风险');
    expect(line?.icon).toBe('expired');

    const low = approvalReceipt({ ...base, risk: 'low', status: 'approved' });
    expect(low?.label).toBe('已批准 · bash');
  });
});

describe('autoApproveEligible（低风险倒计时资格）', () => {
  it('低风险 + 可授权 kind + pending 才有资格', () => {
    expect(autoApproveEligible({ ...base, risk: 'low', kind: 'command' })).toBe(true);
    expect(autoApproveEligible({ ...base, risk: 'low', kind: 'file_change' })).toBe(true);
    expect(autoApproveEligible({ ...base, risk: 'low', kind: 'permissions' })).toBe(true);
  });

  it('中高风险不自动批准（风险不对称）', () => {
    expect(autoApproveEligible({ ...base, risk: 'medium', kind: 'command' })).toBe(false);
    expect(autoApproveEligible({ ...base, risk: 'high', kind: 'command' })).toBe(false);
  });

  it('非可授权 kind 与非 pending 状态无资格', () => {
    expect(autoApproveEligible({ ...base, risk: 'low', kind: 'tool' })).toBe(false);
    expect(autoApproveEligible({ ...base, risk: 'low', kind: 'question' })).toBe(false);
    expect(autoApproveEligible({ ...base, risk: 'low', kind: 'command', status: 'approved' })).toBe(false);
    expect(autoApproveEligible({ ...base, risk: 'low', kind: 'command', status: 'expired' })).toBe(false);
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

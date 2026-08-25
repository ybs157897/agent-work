/**
 * 对话页会话视觉辅助（纯函数）：会话列表状态点配色与空会话建议提示词。
 * 状态点色板对齐 DESIGN.md frontmatter `components.session-dot`。
 */
import { ACTIVE } from '../stores/chat.store';
import type { WorkItem } from '../api/types';

/** 会话状态点：最新 run 状态 → 语义色类。waiting_approval 先于 ACTIVE 判定——"需要你"比"在跑"更值得看见。 */
export function conversationStatusDotClass(
  item: Pick<WorkItem, 'latest_run_id'>,
  runs: Readonly<Record<string, { status: string }>>,
): string {
  const status = item.latest_run_id ? runs[item.latest_run_id]?.status : undefined;
  if (status === 'waiting_approval') return 'bg-status-warning';
  if (status && ACTIVE.has(status)) return 'bg-brand-accent status-pulse';
  switch (status) {
    case 'succeeded':
      return 'bg-status-success';
    case 'failed':
    case 'lost':
      return 'bg-status-error';
    default:
      return 'bg-status-standby';
  }
}

/** 按角色给空会话建议首条提示词；未知角色回落通用两条。 */
export function suggestedPrompts(role?: string): string[] {
  const r = (role ?? '').toLowerCase();
  if (r.includes('pm') || r.includes('product')) {
    return ['评估当前项目质量，指出最需要改进的三点', '把当前阶段的目标拆成可执行的任务清单'];
  }
  if (r.includes('architect')) {
    return ['评审当前架构，找出最脆弱的耦合点', '给出下一阶段的演进路线建议'];
  }
  if (r.includes('develop') || r.includes('dev')) {
    return ['审查最近的改动，指出潜在缺陷', '为关键路径补上缺失的测试'];
  }
  if (r.includes('ui') || r.includes('design')) {
    return ['走查当前界面，列出可用性问题', '为对话页提出一版视觉精修建议'];
  }
  if (r.includes('review')) {
    return ['对当前代码做一轮评审，按严重程度分级', '检查验证门禁是否有绕过路径'];
  }
  return ['帮我了解当前项目的现状', '列出当前待办并给出优先级建议'];
}

import { ChevronDown, ShieldAlert } from 'lucide-react';
import { useState } from 'react';
import { ApiError } from '../../api/client';
import { resolveApproval } from '../../api/endpoints';
import type { ApprovalRequest } from '../../api/types';
import { useRunsStore } from '../../stores/runs.store';
import { toast } from '../../stores/toast.store';
import { formatTime } from '../../utils/format';

export interface ApprovalLine {
  text: string;
  /** 拒绝行用错误色；批准/过期保持辅助色。 */
  deny: boolean;
  at?: string;
}

/** 允许选项的三级授权（对照 codex 桌面端「本次允许/本会话总是允许/总是允许」）。 */
export interface AllowChoice {
  scope: 'once' | 'thread' | 'workspace';
  label: string;
  /** workspace 级全局授权：次级位置 + 警示样式（影响面超出本会话）。 */
  danger: boolean;
  toast: string;
}

/** allowChoices 渲染顺序：允许（本次）→ 本会话总是允许 → 工作区总是允许（警示）。 */
export function allowChoices(): AllowChoice[] {
  return [
    { scope: 'once', label: '允许一次', danger: false, toast: '已允许' },
    { scope: 'thread', label: '本会话总是允许', danger: false, toast: '已允许，本会话同类请求将自动批准' },
    { scope: 'workspace', label: '整个工作区总是允许', danger: true, toast: '已允许，工作区内同类请求将自动批准' },
  ];
}

/** 可授权 kind 闭集（对齐服务端 approval_grants CHECK 约束）。 */
const GRANTABLE_KINDS = new Set(['command', 'file_change', 'permissions']);

/** cardAllowChoices：非可授权 kind（tool/question 等）只保留「允许」，不展示会被
 * 服务端 422 拒绝的授权选项。 */
export function cardAllowChoices(kind: string): AllowChoice[] {
  const choices = allowChoices();
  if (GRANTABLE_KINDS.has(kind)) return choices;
  return choices.filter((c) => c.scope === 'once');
}

const KIND_LABEL: Record<string, string> = {
  command: '终端命令',
  file_change: '文件修改',
  permissions: '权限变更',
  tool: '工具调用',
  question: '确认问题',
};

const RISK_LABEL: Record<string, string> = {
  low: '低风险',
  medium: '中风险',
  high: '高风险',
};

/** 审批卡主标题（按 kind 人话化）。 */
export function approvalHeadline(kind: string): string {
  switch (kind) {
    case 'command':
      return '允许执行此命令？';
    case 'file_change':
      return '允许修改文件？';
    case 'permissions':
      return '允许变更权限？';
    default:
      return '需要你的批准';
  }
}

/** 从 summary 提取可展示的命令/摘要正文。 */
export function approvalDetailText(summary: string): string {
  const trimmed = summary.trim();
  const prefixes = [
    /^Codex 请求执行命令:\s*/i,
    /^请求执行命令:\s*/i,
    /^Allow .* to run this command:\s*/i,
  ];
  for (const re of prefixes) {
    if (re.test(trimmed)) return trimmed.replace(re, '').trim();
  }
  return trimmed;
}

/** 已决议审批的消息流完成态行（保留可见）；pending 无行，返回 null 走交互卡。 */
export function resolvedApprovalLine(a: ApprovalRequest): ApprovalLine | null {
  const label = `${a.kind} · ${a.risk}`;
  if (a.status === 'approved') return { text: `✓ 审批已批准 · ${label}`, deny: false, at: a.resolved_at };
  if (a.status === 'rejected') return { text: `✕ 审批已拒绝 · ${label}`, deny: true, at: a.resolved_at };
  if (a.status === 'expired') return { text: `审批已过期 · ${label}`, deny: false, at: a.resolved_at };
  return null;
}

/**
 * 消息流内的审批卡：pending 渲染交互卡，决议后转完成态行。
 * 决议状态以 runs store 的 listApprovals 投影为权威——本地不缓存结果，重取成功即切换形态。
 */
export function ApprovalCard({ approval }: { approval: ApprovalRequest }) {
  const line = resolvedApprovalLine(approval);
  if (line) {
    return (
      <div className="py-0.5">
        <div className={`text-center text-caption ${line.deny ? 'text-status-error' : 'text-text-tertiary'}`}>
          {line.text}
          {line.at && <span className="ml-1 tabular-nums">{formatTime(line.at)}</span>}
        </div>
      </div>
    );
  }
  return <PendingApprovalCard approval={approval} />;
}

function PendingApprovalCard({ approval }: { approval: ApprovalRequest }) {
  const fetchApprovals = useRunsStore((s) => s.fetchApprovals);
  const [rejecting, setRejecting] = useState(false);
  const [showMore, setShowMore] = useState(false);
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);

  const choices = cardAllowChoices(approval.kind);
  const onceChoice = choices.find((c) => c.scope === 'once')!;
  const memoryChoices = choices.filter((c) => c.scope !== 'once');
  const detail = approvalDetailText(approval.summary);
  const kindLabel = KIND_LABEL[approval.kind] ?? approval.kind;
  const riskLabel = RISK_LABEL[approval.risk] ?? approval.risk;

  const decide = async (
    decision: 'approved' | 'rejected',
    why: string,
    scope: 'once' | 'thread' | 'workspace' = 'once',
    successToast?: string,
  ) => {
    if (busy) return;
    setBusy(true);
    try {
      await resolveApproval(approval.id, approval.run_id, decision, why, scope);
      await fetchApprovals(approval.run_id);
      toast.success(successToast ?? (decision === 'approved' ? '已允许' : '已拒绝'));
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : '操作失败');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="chat-approval-card" data-codex-approval-surface>
      <div className="chat-approval-head">
        <ShieldAlert className="h-4 w-4 shrink-0 text-status-warning" aria-hidden />
        <div className="min-w-0 flex-1">
          <p className="text-body font-semibold text-text-primary">{approvalHeadline(approval.kind)}</p>
          <div className="mt-1 flex flex-wrap items-center gap-1.5">
            <span className="chat-approval-badge">{kindLabel}</span>
            <span className="chat-approval-badge chat-approval-badge-risk">{riskLabel}</span>
          </div>
        </div>
      </div>

      <pre className="chat-approval-detail">{detail}</pre>

      {rejecting ? (
        <div className="chat-approval-reject">
          <label className="text-caption font-medium text-text-secondary">拒绝理由（可选）</label>
          <input
            autoFocus
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault();
                void decide('rejected', reason.trim());
              }
              if (e.key === 'Escape') setRejecting(false);
            }}
            placeholder="说明拒绝原因…"
            className="w-full rounded-lg border border-border-strong bg-surface-base px-3 py-2 text-body outline-none focus:ring-2 focus:ring-brand-primary/30"
          />
          <div className="chat-approval-actions">
            <button
              type="button"
              onClick={() => void decide('rejected', reason.trim())}
              disabled={busy}
              className="chat-approval-btn chat-approval-btn-danger"
            >
              {busy ? '提交中…' : '确认拒绝'}
            </button>
            <button
              type="button"
              onClick={() => setRejecting(false)}
              disabled={busy}
              className="chat-approval-btn chat-approval-btn-ghost"
            >
              取消
            </button>
          </div>
        </div>
      ) : (
        <>
          <div className="chat-approval-actions chat-approval-actions-primary">
            <button
              type="button"
              onClick={() => void decide('approved', '', onceChoice.scope, onceChoice.toast)}
              disabled={busy}
              className="chat-approval-btn chat-approval-btn-primary"
            >
              {busy ? '处理中…' : onceChoice.label}
            </button>
            <button
              type="button"
              onClick={() => setRejecting(true)}
              disabled={busy}
              className="chat-approval-btn chat-approval-btn-danger-outline"
            >
              拒绝
            </button>
          </div>

          {memoryChoices.length > 0 && (
            <div className="chat-approval-memory">
              <button
                type="button"
                className="chat-approval-memory-toggle"
                onClick={() => setShowMore((v) => !v)}
                aria-expanded={showMore}
              >
                <span>记住选择，不再询问</span>
                <ChevronDown className={`h-3.5 w-3.5 transition-transform ${showMore ? 'rotate-180' : ''}`} />
              </button>
              {showMore && (
                <div className="chat-approval-memory-options">
                  {memoryChoices.map((choice) => (
                    <button
                      key={choice.scope}
                      type="button"
                      onClick={() => void decide('approved', '', choice.scope, choice.toast)}
                      disabled={busy}
                      className={
                        choice.danger
                          ? 'chat-approval-btn chat-approval-btn-warning-outline w-full'
                          : 'chat-approval-btn chat-approval-btn-ghost w-full'
                      }
                    >
                      {choice.label}
                    </button>
                  ))}
                </div>
              )}
            </div>
          )}
        </>
      )}
    </div>
  );
}

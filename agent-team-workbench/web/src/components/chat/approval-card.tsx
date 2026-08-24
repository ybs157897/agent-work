import { useState } from 'react';
import { ShieldAlert } from 'lucide-react';
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
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);

  const decide = async (decision: 'approved' | 'rejected', why: string) => {
    if (busy) return;
    setBusy(true);
    try {
      await resolveApproval(approval.id, approval.run_id, decision, why);
      await fetchApprovals(approval.run_id);
      toast.success(decision === 'approved' ? '已批准' : '已拒绝');
    } catch (err) {
      // 失败保持卡片原状（含已输入的理由），可重试。
      toast.error(err instanceof ApiError ? err.message : '操作失败');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="rounded-lg border border-status-warning/30 bg-status-warning/5 p-snug">
      <div className="flex items-center gap-1.5 text-body font-medium text-text-primary">
        <ShieldAlert className="w-4 h-4 text-status-warning" />
        审批请求 · {approval.kind} · {approval.risk}
      </div>
      <p className="text-caption text-text-secondary mt-1">{approval.summary}</p>
      {rejecting ? (
        <div className="mt-2">
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
            placeholder="拒绝原因（可选）"
            className="w-full rounded-input border border-border-strong bg-surface-raised px-snug py-tight text-body outline-none focus:ring-2 focus:ring-brand-primary/30"
          />
          <div className="mt-2 flex gap-2">
            <button
              onClick={() => void decide('rejected', reason.trim())}
              disabled={busy}
              className="text-caption rounded-button px-2 py-1 border border-status-error/40 text-status-error disabled:opacity-50"
            >
              {busy ? '提交中' : '确认拒绝'}
            </button>
            <button
              onClick={() => setRejecting(false)}
              disabled={busy}
              className="text-caption rounded-button px-2 py-1 bg-transparent border border-border-strong text-text-secondary hover:bg-surface-base transition-colors disabled:opacity-50"
            >
              取消
            </button>
          </div>
        </div>
      ) : (
        <div className="flex gap-2 mt-2">
          <button
            onClick={() => void decide('approved', '')}
            disabled={busy}
            className="text-caption rounded-button px-2 py-1 bg-status-success text-white disabled:opacity-50"
          >
            {busy ? '处理中' : '批准'}
          </button>
          <button
            onClick={() => setRejecting(true)}
            disabled={busy}
            className="text-caption rounded-button px-2 py-1 border border-status-error/40 text-status-error disabled:opacity-50"
          >
            拒绝
          </button>
        </div>
      )}
    </div>
  );
}

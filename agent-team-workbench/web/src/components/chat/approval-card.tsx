import { motion, AnimatePresence, useReducedMotion } from 'motion/react';
import {
  Check,
  ChevronDown,
  Clock,
  MessageCircleQuestion,
  PenLine,
  ShieldAlert,
  Terminal,
  TriangleAlert,
  Wrench,
  X,
  type LucideIcon,
} from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { ApiError } from '../../api/client';
import { resolveApproval } from '../../api/endpoints';
import type { ApprovalRequest } from '../../api/types';
import { useRunsStore } from '../../stores/runs.store';
import { toast } from '../../stores/toast.store';
import { formatTime } from '../../utils/format';
import { Button } from '../ui';

/** 低风险可授权请求的自动批准窗口（秒）；归零即按「允许一次」决议。 */
export const AUTO_APPROVE_SECS = 30;

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

const KIND_ICON: Record<string, LucideIcon> = {
  command: Terminal,
  file_change: PenLine,
  permissions: ShieldAlert,
  tool: Wrench,
  question: MessageCircleQuestion,
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
    case 'question':
      return 'Agent 请求确认';
    default:
      return '需要你的批准';
  }
}

/** 倒计时资格：低风险且可授权且待决议。风险不对称——自动批准不越权到中高风险。 */
export function autoApproveEligible(a: Pick<ApprovalRequest, 'kind' | 'risk' | 'status'>): boolean {
  return a.status === 'pending' && a.risk === 'low' && GRANTABLE_KINDS.has(a.kind);
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

/** 决议回执的稳定语义模型：icon 驱动渲染，label 供测试与展示共享。 */
export interface ApprovalReceipt {
  icon: 'approved' | 'rejected' | 'expired';
  label: string;
  at?: string;
}

/** 已决议审批的回执行模型；pending 返回 null（走交互卡）。 */
export function approvalReceipt(a: ApprovalRequest): ApprovalReceipt | null {
  const kind = KIND_LABEL[a.kind] ?? a.kind;
  const risk = a.risk && a.risk !== 'low' ? ` · ${RISK_LABEL[a.risk] ?? a.risk}` : '';
  const label = `${kind}${risk}`;
  if (a.status === 'approved') return { icon: 'approved', label: `已批准 · ${label}`, at: a.resolved_at };
  if (a.status === 'rejected') return { icon: 'rejected', label: `已拒绝 · ${label}`, at: a.resolved_at };
  if (a.status === 'expired') return { icon: 'expired', label: `已过期 · ${label}`, at: a.resolved_at };
  return null;
}

/**
 * 消息流内的审批卡（tx 重设计）：pending 渲染交互卡（kind 图标芯片 + 变体正文 +
 * 低风险倒计时），决议后转左对齐回执行。决议状态以 runs store 的 listApprovals
 * 投影为权威——本地不缓存结果，重取成功即切换形态。
 */
export function ApprovalCard({ approval }: { approval: ApprovalRequest }) {
  const receipt = approvalReceipt(approval);
  if (receipt) return <ResolvedReceipt receipt={receipt} />;
  return <PendingApprovalCard approval={approval} />;
}

function ResolvedReceipt({ receipt }: { receipt: ApprovalReceipt }) {
  const iconTone =
    receipt.icon === 'approved'
      ? 'text-status-success'
      : receipt.icon === 'rejected'
        ? 'text-status-error'
        : 'text-text-tertiary';
  return (
    <div className="chat-approval-receipt" data-codex-approval-surface>
      {receipt.icon === 'approved' && <Check className={`chat-approval-receipt-icon ${iconTone}`} aria-hidden />}
      {receipt.icon === 'rejected' && <X className={`chat-approval-receipt-icon ${iconTone}`} aria-hidden />}
      {receipt.icon === 'expired' && <Clock className={`chat-approval-receipt-icon ${iconTone}`} aria-hidden />}
      <span className="min-w-0 flex-1 truncate text-text-secondary">{receipt.label}</span>
      {receipt.at && <span className="shrink-0 tabular-nums text-text-tertiary">{formatTime(receipt.at)}</span>}
    </div>
  );
}

/** 滚动数字：数值变化时旧值上滑出、新值下滑入；reduced-motion 退化为静态数字。 */
function RollingValue({ value }: { value: number }) {
  const reduceMotion = useReducedMotion();
  return (
    <span className="relative inline-block h-4 w-4 overflow-hidden align-[-2px]">
      <AnimatePresence initial={false} mode="popLayout">
        <motion.span
          key={value}
          className="tabular-nums"
          initial={reduceMotion ? false : { y: 10, opacity: 0 }}
          animate={{ y: 0, opacity: 1 }}
          exit={reduceMotion ? undefined : { y: -10, opacity: 0 }}
          transition={reduceMotion ? { duration: 0 } : { duration: 0.22, ease: [0.16, 1, 0.3, 1] }}
        >
          {value}
        </motion.span>
      </AnimatePresence>
    </span>
  );
}

/** 环形进度：剩余时间的微缩表达；静态底环 + 行动色进度弧。 */
function CountdownRing({ remaining }: { remaining: number }) {
  const reduceMotion = useReducedMotion();
  const r = 6.5;
  const c = 2 * Math.PI * r;
  const fraction = remaining / AUTO_APPROVE_SECS;
  return (
    <svg viewBox="0 0 16 16" className="h-4 w-4 -rotate-90" aria-hidden>
      <circle cx="8" cy="8" r={r} fill="none" className="chat-approval-ring-track" strokeWidth="2" />
      <circle
        cx="8"
        cy="8"
        r={r}
        fill="none"
        className="chat-approval-ring-value"
        strokeWidth="2"
        strokeLinecap="round"
        strokeDasharray={c}
        strokeDashoffset={c * (1 - fraction)}
        style={reduceMotion ? undefined : { transition: 'stroke-dashoffset 1s linear' }}
      />
    </svg>
  );
}

function PendingApprovalCard({ approval }: { approval: ApprovalRequest }) {
  const fetchApprovals = useRunsStore((s) => s.fetchApprovals);
  const [rejecting, setRejecting] = useState(false);
  const [showMore, setShowMore] = useState(false);
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [secs, setSecs] = useState(AUTO_APPROVE_SECS);
  const autoFiredRef = useRef(false);

  const choices = cardAllowChoices(approval.kind);
  const onceChoice = choices.find((c) => c.scope === 'once')!;
  const memoryChoices = choices.filter((c) => c.scope !== 'once');
  const kindLabel = KIND_LABEL[approval.kind] ?? approval.kind;
  const riskLabel = RISK_LABEL[approval.risk] ?? approval.risk;
  const detail = approvalDetailText(approval.summary);
  const isCommand = approval.kind === 'command';
  const Icon = KIND_ICON[approval.kind] ?? Wrench;
  const auto = autoApproveEligible(approval);

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

  // 低风险倒计时：归零按「允许一次」自动决议；busy/重复触发由 firedRef 收口。
  useEffect(() => {
    if (!auto) return;
    const timer = setInterval(() => setSecs((v) => (v > 0 ? v - 1 : 0)), 1000);
    return () => clearInterval(timer);
  }, [auto]);
  useEffect(() => {
    if (!auto || secs > 0 || autoFiredRef.current) return;
    autoFiredRef.current = true;
    void decide('approved', '', 'once', '已自动批准（低风险）');
    // decide 闭包随渲染重建，firedRef 已防重入；busy 内部再兜一层。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [auto, secs]);

  return (
    <div className="chat-approval-card" data-codex-approval-surface data-risk={approval.risk}>
      <div className="chat-approval-head">
        <span className="chat-approval-chip" data-risk={approval.risk} aria-hidden>
          <Icon />
        </span>
        <div className="min-w-0 flex-1">
          <p className="chat-approval-title">{approvalHeadline(approval.kind)}</p>
          <div className="chat-approval-sub">
            <span>{kindLabel}</span>
            {approval.risk !== 'low' && (
              <span className="chat-approval-sub-risk">
                <TriangleAlert aria-hidden className="h-3 w-3" />
                {riskLabel}
              </span>
            )}
          </div>
        </div>
      </div>

      {isCommand ? (
        <pre className="chat-approval-code">
          <span className="chat-approval-prompt" aria-hidden>
            $
          </span>
          {detail}
        </pre>
      ) : (
        <p className="chat-approval-summary">{detail}</p>
      )}

      {rejecting ? (
        <div className="chat-approval-reject">
          <label className="text-caption font-medium text-text-secondary" htmlFor={`reject-${approval.id}`}>
            拒绝理由（可选）
          </label>
          <textarea
            id={`reject-${approval.id}`}
            autoFocus
            rows={2}
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault();
                void decide('rejected', reason.trim());
              }
              if (e.key === 'Escape') setRejecting(false);
            }}
            placeholder="说明拒绝原因…"
            className="chat-approval-reject-input"
          />
          <div className="chat-approval-actions !px-0 !pt-0">
            <Button variant="danger" type="button" onClick={() => void decide('rejected', reason.trim())} disabled={busy}>
              {busy ? '提交中…' : '确认拒绝'}
            </Button>
            <Button variant="secondary" type="button" onClick={() => setRejecting(false)} disabled={busy}>
              取消
            </Button>
          </div>
        </div>
      ) : (
        <>
          <div className="chat-approval-actions">
            {auto ? (
              <span className="chat-approval-countdown" role="timer" aria-label={`${secs} 秒后自动批准`}>
                <CountdownRing remaining={secs} />
                <span className="hidden sm:inline">低风险 ·</span>
                <RollingValue value={secs} />
                <span>秒后自动批准</span>
              </span>
            ) : (
              <span className="flex-1" />
            )}
            <Button variant="ghost" type="button" onClick={() => setRejecting(true)} disabled={busy}>
              拒绝
            </Button>
            <Button
              variant="success"
              type="button"
              onClick={() => void decide('approved', '', onceChoice.scope, onceChoice.toast)}
              disabled={busy}
            >
              {busy ? '处理中…' : onceChoice.label}
            </Button>
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
                    <Button
                      key={choice.scope}
                      variant={choice.danger ? 'warning-outline' : 'secondary'}
                      className="w-full"
                      type="button"
                      onClick={() => void decide('approved', '', choice.scope, choice.toast)}
                      disabled={busy}
                    >
                      {choice.label}
                    </Button>
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

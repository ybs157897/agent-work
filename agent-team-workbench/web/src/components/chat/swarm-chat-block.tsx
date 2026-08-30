import {
  AlertCircle,
  Check,
  PanelRightOpen,
  Clock3,
  LoaderCircle,
  PauseCircle,
  UsersRound,
  XCircle,
} from 'lucide-react';
import type { SwarmMemberProjection, SwarmProjection } from '../../stores/chat.store';
import css from './swarm-chat-block.module.css';

export type { SwarmMemberProjection, SwarmProjection } from '../../stores/chat.store';
type SwarmMemberStatus = SwarmMemberProjection['status'];

const statusText: Record<SwarmMemberStatus, string> = {
  queued: '排队中',
  running: '执行中',
  waiting: '等待中',
  completed: '已完成',
  failed: '失败',
  stopped: '已停止',
};

function StatusIcon({ status }: { status: SwarmMemberStatus }) {
  const props = { size: 14, strokeWidth: 2, 'aria-hidden': true } as const;
  if (status === 'running') return <LoaderCircle {...props} className={css.runningIcon} />;
  if (status === 'completed') return <Check {...props} />;
  if (status === 'failed') return <AlertCircle {...props} />;
  if (status === 'stopped') return <XCircle {...props} />;
  if (status === 'waiting') return <PauseCircle {...props} />;
  return <Clock3 {...props} />;
}

export function swarmMemberPreview(value: string, maxLength = 96): string {
  const plain = value
    .replace(/\\(?:\(|\)|\[|\])/g, '')
    .replace(/\\(?:boxed|text)\{([^{}]*)\}/g, '$1')
    .replace(/\\times/g, '×')
    .replace(/\\div/g, '÷')
    .replace(/\\sqrt/g, '√')
    .replace(/\\(?:quad|,)/g, ' ')
    .replace(/\\begin\{[^}]+\}|\\end\{[^}]+\}/g, '')
    .replace(/\\\\/g, ' ')
    .replace(/[`*_>#]/g, '')
    .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1')
    .replace(/\s*-{3,}\s*/g, ' · ')
    .replace(/\s+/g, ' ')
    .trim();
  return plain.length > maxLength ? `${plain.slice(0, Math.max(0, maxLength - 1))}…` : plain;
}

function SwarmMemberCell({ member, selected, onSelect, enabled }: { member: SwarmMemberProjection; selected: boolean; onSelect: () => void; enabled: boolean }) {
  const rawPreview = member.summary || member.description ||
    (member.status === 'waiting' && member.reason) || '等待结果';
  const preview = swarmMemberPreview(rawPreview);

  return (
    <article className={`${css.cell} ${selected ? css.cellSelected : ''} ${css[`status_${member.status}`]}`}>
      <button
        type="button"
        className={css.cellButton}
        aria-pressed={selected}
        aria-disabled={!enabled}
        aria-label={`第 ${member.index} 项，${member.name}，${statusText[member.status]}，${preview}`}
        onClick={onSelect}
        disabled={!enabled}
      >
        <span className={css.memberIdentity}>
          <span className={css.memberIndex} aria-hidden="true">{member.index}</span>
          <span className={css.memberMark} data-identity={(((member.index - 1) % 8 + 8) % 8) + 1} aria-hidden="true"><UsersRound size={15} /></span>
          <span className={css.memberName}>{member.name}</span>
        </span>
        <span className={css.status}>
          <StatusIcon status={member.status} />
          <span>{statusText[member.status]}</span>
        </span>
        <span className={css.ticker}>{preview}</span>
        <PanelRightOpen size={15} aria-hidden="true" className={css.panelIcon} />
      </button>
    </article>
  );
}

export function SwarmChatBlock({ projection, selectedMemberKey, selectionPrefix = '', onSelectMember }: { projection: SwarmProjection; selectedMemberKey?: string; selectionPrefix?: string; onSelectMember?: (member: SwarmMemberProjection) => void }) {
  const settled = projection.members.filter((member) =>
    member.status === 'completed' || member.status === 'failed' || member.status === 'stopped',
  ).length;
  const failed = projection.members.filter((member) => member.status === 'failed').length;
  const stopped = projection.members.filter((member) => member.status === 'stopped').length;
  const total = Math.max(projection.total, projection.members.length);
  const remaining = Math.max(0, total - settled);
  const percent = total > 0 ? Math.min(100, Math.round((settled / total) * 100)) : 0;
  const allCompleted = projection.members.length === total && projection.members.every((member) => member.status === 'completed');
  const mergeLabel = failed > 0
    ? remaining > 0
      ? `已有 ${failed} 个失败，仍等待 ${remaining} 个结果`
      : `结果已收齐，其中 ${failed} 个失败`
    : projection.status === 'failed'
      ? '合流失败：Kimi 蜂群调用未完成'
    : stopped > 0
      ? remaining > 0
        ? `已有 ${stopped} 个中断，仍等待 ${remaining} 个结果`
        : `结果已收齐，其中 ${stopped} 个已中断`
      : projection.status === 'stopped'
        ? '合流中断：Kimi 蜂群已停止'
      : allCompleted
        ? '结果已齐套，可供巢头合流'
        : `仍在运行，等待 ${remaining} 个结果`;
  const mergeClass = failed > 0 || projection.status === 'failed'
    ? css.mergeError
    : stopped > 0 || projection.status === 'stopped'
      ? css.mergeStopped
      : allCompleted
        ? css.mergeComplete
        : '';
  const MergeIcon = failed > 0 || projection.status === 'failed'
    ? AlertCircle
    : stopped > 0 || projection.status === 'stopped'
      ? XCircle
      : allCompleted
        ? Check
        : Clock3;

  return (
    <section className={css.block} aria-label={`Kimi 蜂群：${projection.title}`}>
      <span className={css.srOnly} role="status" aria-live="polite" aria-atomic="true">Kimi 蜂群：{projection.title}，已结束 {settled}/{total}</span>
      <header className={css.header}>
        <span className={css.hiveIcon} aria-hidden="true"><UsersRound size={18} /></span>
        <div className={css.heading}>
          <span className={css.kicker}>Kimi 蜂群</span>
          <h3 className={css.title}>{projection.title}</h3>
        </div>
        <span className={css.count}>{settled}/{total}<span className={css.countLabel}> 已结束</span></span>
      </header>
      <div className={css.progressTrack} role="progressbar" aria-label="蜂群结束进度" aria-valuemin={0} aria-valuemax={total} aria-valuenow={settled}>
        <span className={css.progressValue} style={{ width: `${percent}%` }} />
      </div>
      <div className={css.grid}>
        {projection.members.map((member) => <SwarmMemberCell key={member.id} member={member} selected={selectedMemberKey === `${selectionPrefix}:${projection.id}:${member.id}`} enabled={Boolean(onSelectMember)} onSelect={() => onSelectMember?.(member)} />)}
      </div>
      <footer className={`${css.mergeBar} ${mergeClass}`}>
        <span className={css.mergeIcon} aria-hidden="true"><MergeIcon size={14} /></span>
        <span>{mergeLabel}</span>
      </footer>
    </section>
  );
}

/** Codex 的子 Agent 是普通工作流卡片，不伪装成 Kimi AgentSwarm；详情仍走统一 workspace。 */
export function CodexAgentCard({ agent, selected, onSelect }: { agent: SwarmMemberProjection; selected?: boolean; onSelect?: (agent: SwarmMemberProjection) => void }) {
  const preview = swarmMemberPreview(agent.summary || agent.description || agent.error || agent.reason || '等待结果');
  const enabled = Boolean(onSelect);
  return (
    <article className="rounded-container border border-border-subtle bg-surface-raised shadow-card">
      <button
        type="button"
        disabled={!enabled}
        aria-pressed={selected}
        aria-label={`Codex 子 Agent ${agent.name}，${statusText[agent.status]}，${preview}`}
        onClick={() => onSelect?.(agent)}
        className={`flex w-full items-start gap-3 rounded-container p-3 text-left transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand-primary ${selected ? 'bg-brand-muted' : 'hover:bg-surface-sunken'} disabled:cursor-default`}
      >
        <span className="mt-0.5 grid h-7 w-7 shrink-0 place-items-center rounded-button bg-surface-sunken text-text-secondary" aria-hidden="true"><UsersRound size={15} /></span>
        <span className="min-w-0 flex-1">
          <span className="flex items-center gap-2 text-caption font-medium text-text-primary"><span className="truncate">{agent.name || 'Codex 子 Agent'}</span><span className="shrink-0 text-text-tertiary">·</span><span className="inline-flex shrink-0 items-center gap-1"><StatusIcon status={agent.status} />{statusText[agent.status]}</span></span>
          <span className="mt-1 block truncate text-caption text-text-tertiary">{preview}</span>
        </span>
        <PanelRightOpen size={15} className="mt-1 shrink-0 text-text-tertiary" aria-hidden="true" />
      </button>
    </article>
  );
}

export default SwarmChatBlock;

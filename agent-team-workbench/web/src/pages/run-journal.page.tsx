import { Anchor, CheckCircle2, ChevronRight, CircleOff, Handshake, Loader2, MessageSquare, Radio, RefreshCw, Scale, Send, SkipForward, AlertCircle, Terminal } from 'lucide-react';
import { useEffect, useState, type ReactNode } from 'react';
import { Link, useParams } from 'react-router-dom';
import { ApiError } from '../api/client';
import { getRunJournal } from '../api/endpoints';
import type { RunJournal, RunJournalPhase } from '../api/types';
import { ErrorState } from '../components/async-state';
import { Button, EmptyState, ListSkeleton, buttonClassName } from '../components/ui';
import { formatDateTime, formatTime } from '../utils/format';

/**
 * Run 环节时间线（/runs/:runId/journal）：七段相位只读投影的排障视图。
 * 排障口径：最后一个 failed 或未闭合（outcome=null）的相位就是故障环节，
 * 失败证据默认展开，detail 渐进披露。
 */

export type RunJournalViewState =
  | { kind: 'loading' }
  | { kind: 'not-found' }
  | { kind: 'error'; message: string }
  | { kind: 'ready'; journal: RunJournal };

/** 404 = 该 run 没有 journal（尚未产生或已清理），是稳定事实，不当连接错误展示。 */
export function toJournalViewState(error: unknown): RunJournalViewState {
  if (error instanceof ApiError && error.status === 404) return { kind: 'not-found' };
  return {
    kind: 'error',
    message: error instanceof ApiError ? error.message : '环节时间线加载失败',
  };
}

const PHASE_META: Record<string, { label: string; icon: ReactNode }> = {
  dispatch: { label: '派发', icon: <Send className="h-3.5 w-3.5" /> },
  spawn: { label: '拉起进程', icon: <Terminal className="h-3.5 w-3.5" /> },
  handshake: { label: '握手 / resume', icon: <Handshake className="h-3.5 w-3.5" /> },
  first_event: { label: '等待首帧', icon: <Radio className="h-3.5 w-3.5" /> },
  streaming: { label: '对话流', icon: <MessageSquare className="h-3.5 w-3.5" /> },
  settle: { label: '终态裁决', icon: <Scale className="h-3.5 w-3.5" /> },
  post: { label: '终态钩子', icon: <Anchor className="h-3.5 w-3.5" /> },
};

function phaseMeta(phase: string): { label: string; icon: ReactNode } {
  return PHASE_META[phase] ?? { label: phase, icon: <CircleOff className="h-3.5 w-3.5" /> };
}

interface OutcomeVisual {
  text: string;
  icon: ReactNode;
  /** 行卡与徽章共用的语义色调。 */
  tone: 'ok' | 'failed' | 'skipped' | 'running' | 'interrupted';
}

/**
 * outcome 视觉语义：failed 与未闭合必须一眼可辨——
 * outcome=null 且 closed_at=null → 进行中；closed_at 有值但没落到 outcome → 中断。
 */
function outcomeVisual(entry: RunJournalPhase): OutcomeVisual {
  if (entry.outcome === 'ok') {
    return { text: '正常', icon: <CheckCircle2 className="h-3.5 w-3.5" />, tone: 'ok' };
  }
  if (entry.outcome === 'failed') {
    return { text: '失败', icon: <AlertCircle className="h-3.5 w-3.5" />, tone: 'failed' };
  }
  if (entry.outcome === 'skipped') {
    return { text: '跳过', icon: <SkipForward className="h-3.5 w-3.5" />, tone: 'skipped' };
  }
  if (entry.closed_at === null) {
    return { text: '进行中', icon: <Loader2 className="h-3.5 w-3.5 animate-spin" />, tone: 'running' };
  }
  return { text: '中断', icon: <CircleOff className="h-3.5 w-3.5" />, tone: 'interrupted' };
}

const TONE_ROW_CLASSES: Record<OutcomeVisual['tone'], string> = {
  ok: 'border-border-subtle bg-surface-raised',
  skipped: 'border-border-subtle bg-surface-raised',
  failed: 'border-status-error/45 bg-status-error/5',
  running: 'border-status-info/40 bg-status-info/5',
  interrupted: 'border-status-warning/45 bg-status-warning/5',
};

const TONE_BADGE_CLASSES: Record<OutcomeVisual['tone'], string> = {
  ok: 'border-status-success/30 bg-status-success/10 text-status-success',
  skipped: 'border-border-subtle bg-surface-base text-text-tertiary',
  failed: 'border-status-error/35 bg-status-error/10 text-status-error',
  running: 'border-status-info/35 bg-status-info/10 text-status-info',
  interrupted: 'border-status-warning/40 bg-status-warning/10 text-status-warning',
};

const TONE_RAIL_CLASSES: Record<OutcomeVisual['tone'], string> = {
  ok: 'border-border-subtle text-text-tertiary',
  skipped: 'border-border-subtle text-text-tertiary',
  failed: 'border-status-error/45 text-status-error',
  running: 'border-status-info/45 text-status-info',
  interrupted: 'border-status-warning/50 text-status-warning',
};

export function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(ms < 10_000 ? 2 : 1)}s`;
  const minutes = Math.floor(ms / 60_000);
  const seconds = Math.round((ms % 60_000) / 1000);
  return seconds > 0 ? `${minutes}m${seconds}s` : `${minutes}m`;
}

function formatDetailValue(value: unknown): string {
  if (typeof value === 'string') return value;
  return JSON.stringify(value) ?? String(value);
}

/** 连续同相位条目归为一组：post 多钩子（每钩子一对）与重试同列在一个相位头下。 */
interface PhaseGroup {
  name: string;
  entries: RunJournalPhase[];
}

export function groupJournalPhases(phases: RunJournalPhase[]): PhaseGroup[] {
  const groups: PhaseGroup[] = [];
  for (const entry of phases) {
    const last = groups[groups.length - 1];
    if (last && last.name === entry.phase) last.entries.push(entry);
    else groups.push({ name: entry.phase, entries: [entry] });
  }
  return groups;
}

export default function RunJournalPage() {
  const { runId } = useParams<{ runId: string }>();
  const [state, setState] = useState<RunJournalViewState>({ kind: 'loading' });
  const [reloadToken, setReloadToken] = useState(0);

  useEffect(() => {
    if (!runId) return;
    let active = true;
    setState({ kind: 'loading' });
    getRunJournal(runId)
      .then((journal) => {
        if (active) setState({ kind: 'ready', journal });
      })
      .catch((error: unknown) => {
        if (active) setState(toJournalViewState(error));
      });
    return () => {
      active = false;
    };
  }, [runId, reloadToken]);

  const refresh = () => setReloadToken((token) => token + 1);

  return (
    <main className="page-shell">
      <header className="page-header">
        <div>
          <p className="text-caption font-medium uppercase tracking-wider text-brand-primary">运行案牍</p>
          <h2 className="page-title">运行环节</h2>
          {runId && <p className="mt-1 break-all font-mono text-caption text-text-tertiary">{runId}</p>}
        </div>
        <Button onClick={refresh}>
          <RefreshCw className="h-4 w-4" aria-hidden="true" />
          刷新
        </Button>
      </header>
      <RunJournalView state={state} onRetry={refresh} />
    </main>
  );
}

/** 纯投影视图：状态机驱动，供页面与渲染测试共用。 */
export function RunJournalView({ state, onRetry }: { state: RunJournalViewState; onRetry: () => void }) {
  if (state.kind === 'loading') {
    return (
      <section className="ink-paper-panel overflow-hidden rounded-card" aria-label="环节时间线加载中">
        <ListSkeleton padded={false} />
      </section>
    );
  }
  if (state.kind === 'not-found') {
    return (
      <section className="ink-paper-panel rounded-card p-comfortable">
        <EmptyState
          icon={<CircleOff className="h-5 w-5" />}
          title="没有该运行的环节记录"
          description="运行可能尚未产生 journal，或记录已被清理"
          action={(
            <Link to="/chat" className={buttonClassName('secondary', 'md')}>
              返回对话
            </Link>
          )}
        />
      </section>
    );
  }
  if (state.kind === 'error') {
    return <ErrorState message={state.message} onRetry={onRetry} />;
  }

  const { journal } = state;
  return (
    <div className="space-y-base">
      {journal.phases.length === 0 ? (
        <section className="ink-paper-panel rounded-card p-comfortable">
          <EmptyState
            icon={<CircleOff className="h-5 w-5" />}
            title="该运行还没有环节记录"
            description="运行开始推进后，派发到终态钩子的各环节会出现在这里"
          />
        </section>
      ) : (
        <TroubleStrip phases={journal.phases} />
      )}

      {journal.phases.length > 0 && (
        <section className="ink-paper-panel rounded-card p-comfortable" aria-label="运行环节时间线">
          <ol className="relative ml-3 space-y-snug border-l border-border-subtle pl-comfortable">
            {groupJournalPhases(journal.phases).map((group, groupIndex) => {
              const meta = phaseMeta(group.name);
              return (
                <li key={`${group.name}:${groupIndex}`} className="relative">
                  <span
                    className={`absolute -left-9 top-0 flex h-6 w-6 items-center justify-center rounded-full border bg-surface-raised ${TONE_RAIL_CLASSES[outcomeVisual(group.entries[group.entries.length - 1]).tone]}`}
                    aria-hidden="true"
                  >
                    {meta.icon}
                  </span>
                  <div className="flex items-baseline gap-snug">
                    <h3 className="text-body font-medium text-text-primary">{meta.label}</h3>
                    <span className="font-mono text-caption text-text-tertiary">{group.name}</span>
                    {group.entries.length > 1 && (
                      <span className="text-caption text-text-tertiary">共 {group.entries.length} 段</span>
                    )}
                  </div>
                  <div className="mt-tight space-y-tight">
                    {group.entries.map((entry, entryIndex) => (
                      <JournalEntryRow
                        key={`${group.name}:${entry.attempt}:${entryIndex}`}
                        entry={entry}
                      />
                    ))}
                  </div>
                </li>
              );
            })}
          </ol>
          <p className="mt-base text-caption text-text-tertiary">
            生成于 <time dateTime={journal.generated_at}>{formatDateTime(journal.generated_at)}</time>
          </p>
        </section>
      )}

      <div className="grid gap-snug sm:grid-cols-2">
        <section className="ink-paper-panel rounded-card p-comfortable" aria-label="进程输出摘要">
          <h3 className="text-h3 text-text-primary">进程输出</h3>
          <p className="mt-tight text-body text-text-secondary">
            共 {journal.log.chunks} 条
            {journal.log.truncated ? ' · 已截断，仅保留尾部' : ''}
          </p>
        </section>
        {journal.governance && (
          <section className="ink-paper-panel rounded-card p-comfortable" aria-label="治理回合">
            <h3 className="text-h3 text-text-primary">治理回合</h3>
            <dl className="mt-tight space-y-micro text-caption">
              <div className="flex gap-snug">
                <dt className="shrink-0 text-text-tertiary">目标</dt>
                <dd className="min-w-0 break-all font-mono text-text-secondary">{journal.governance.goal_id}</dd>
              </div>
              <div className="flex gap-snug">
                <dt className="shrink-0 text-text-tertiary">待办</dt>
                <dd className="min-w-0 break-all font-mono text-text-secondary">{journal.governance.todo_id}</dd>
              </div>
              <div className="flex gap-snug">
                <dt className="shrink-0 text-text-tertiary">回合</dt>
                <dd className="font-mono tabular-nums text-text-secondary">第 {journal.governance.turn_seq} 轮</dd>
              </div>
              <div className="flex gap-snug">
                <dt className="shrink-0 text-text-tertiary">摘要</dt>
                <dd className="min-w-0 break-all font-mono text-text-secondary">{journal.governance.digest}</dd>
              </div>
            </dl>
          </section>
        )}
      </div>
    </div>
  );
}

/** 故障指向条：最后一个 failed 或未闭合的相位，就是排障的起点。 */
function TroubleStrip({ phases }: { phases: RunJournalPhase[] }) {
  const trouble = [...phases].reverse().find((entry) => entry.outcome === 'failed' || entry.outcome === null);
  if (!trouble) return null;
  const meta = phaseMeta(trouble.phase);
  const visual = outcomeVisual(trouble);
  const stripClasses =
    visual.tone === 'failed'
      ? 'border-status-error/45 bg-status-error/5 text-status-error'
      : visual.tone === 'interrupted'
        ? 'border-status-warning/45 bg-status-warning/5 text-status-warning'
        : 'border-status-info/40 bg-status-info/5 text-status-info';
  const suffix =
    visual.tone === 'failed'
      ? trouble.failure
        ? ` · ${trouble.failure.code}`
        : ''
      : visual.tone === 'interrupted'
        ? '（已闭合但未落到终态）'
        : '（相位尚未闭合）';
  return (
    <div className={`rounded-card border px-comfortable py-snug text-body ${stripClasses}`} role="status">
      {visual.tone === 'failed' ? '故障环节' : visual.tone === 'interrupted' ? '中断环节' : '进行中环节'}
      ：{meta.label} · 第 {trouble.attempt} 次{suffix}
    </div>
  );
}

function JournalEntryRow({ entry }: { entry: RunJournalPhase }) {
  const visual = outcomeVisual(entry);
  const hook = typeof entry.detail?.hook === 'string' ? entry.detail.hook : null;
  return (
    <div className={`rounded-card border px-snug py-tight ${TONE_ROW_CLASSES[visual.tone]}`}>
      <div className="flex flex-wrap items-center gap-2">
        <span
          className={`inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-caption font-medium ${TONE_BADGE_CLASSES[visual.tone]}`}
        >
          {visual.icon}
          {visual.text}
        </span>
        {hook && <span className="font-mono text-caption text-text-secondary">{hook}</span>}
        <span className="text-caption text-text-tertiary">第 {entry.attempt} 次</span>
        <time
          dateTime={entry.entered_at}
          className="font-mono text-caption tabular-nums text-text-tertiary"
          title={`进入 ${formatDateTime(entry.entered_at)}`}
        >
          {formatTime(entry.entered_at)}
          {entry.closed_at ? ` → ${formatTime(entry.closed_at)}` : ''}
        </time>
        {entry.duration_ms != null && (
          <span className="font-mono text-caption tabular-nums text-text-secondary">
            {formatDuration(entry.duration_ms)}
          </span>
        )}
      </div>
      {entry.failure && <FailureBlock failure={entry.failure} />}
      {entry.detail && <DetailDisclosure detail={entry.detail} />}
    </div>
  );
}

/** 失败证据默认展开：code/family/retryable 一行，message 独立成段。 */
function FailureBlock({ failure }: { failure: NonNullable<RunJournalPhase['failure']> }) {
  return (
    <div className="mt-tight rounded-button border border-status-error/30 bg-surface-base/60 px-snug py-tight">
      <div className="flex flex-wrap items-center gap-2 font-mono text-caption">
        <span className="font-medium text-status-error">{failure.code}</span>
        <span className="text-text-tertiary">family={failure.family}</span>
        <span className={failure.retryable ? 'text-status-warning' : 'text-text-tertiary'}>
          {failure.retryable ? '可重试' : '不可重试'}
        </span>
      </div>
      <p className="mt-micro text-caption leading-5 text-text-secondary">{failure.message}</p>
    </div>
  );
}

/** detail 渐进披露：默认折叠，点开看 JSON 键值。 */
function DetailDisclosure({ detail }: { detail: Record<string, unknown> }) {
  const [open, setOpen] = useState(false);
  const entries = Object.entries(detail);
  if (entries.length === 0) return null;
  return (
    <div className="mt-tight">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
        className="inline-flex items-center gap-1 rounded-button px-1 py-0.5 text-caption text-text-tertiary transition-colors hover:text-text-secondary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
      >
        <ChevronRight
          className={`h-3 w-3 transition-transform duration-inkFast ${open ? 'rotate-90' : ''}`}
          aria-hidden="true"
        />
        详情（{entries.length} 项）
      </button>
      {open && (
        <dl className="mt-micro space-y-micro rounded-button bg-surface-sunken/50 px-snug py-tight">
          {entries.map(([key, value]) => (
            <div key={key} className="flex gap-snug text-caption">
              <dt className="shrink-0 font-mono text-text-tertiary">{key}</dt>
              <dd className="min-w-0 break-all font-mono text-text-secondary">{formatDetailValue(value)}</dd>
            </div>
          ))}
        </dl>
      )}
    </div>
  );
}

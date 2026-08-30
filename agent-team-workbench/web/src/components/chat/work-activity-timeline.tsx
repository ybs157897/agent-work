import { Check, ChevronDown, CircleStop, CircleX, LoaderCircle, Wrench } from 'lucide-react';
import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react';
import { AgentOutput } from './agent-output';
import { ActivityGroup } from './tool-card';
import { ApprovalCard } from './approval-card';
import { CodexAgentCard, SwarmChatBlock } from './swarm-chat-block';
import { ACTIVE } from '../../stores/chat.store';
import type { SwarmMemberProjection } from '../../stores/chat.store';
import type { WorkActivityItem, WorkTimelineSegment } from '../../utils/work-activity-timeline';
import { shouldShowOutputLoading } from '../../utils/chronological-transcript';
import css from './work-activity-timeline.module.css';

const OUTPUT_LOADING_IDLE_MS = 700;

export function workElapsed(createdAt: string, updatedAt: string, now: number, live: boolean): string | undefined {
  const start = Date.parse(createdAt);
  const end = Date.parse(updatedAt);
  if (!Number.isFinite(start)) return undefined;
  const stop = live ? now : Number.isFinite(end) ? Math.max(start, end) : start;
  const ms = Math.max(0, stop - start);
  if (ms < 1000) return '<1 秒';
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds} 秒`;
  const minutes = Math.floor(seconds / 60);
  const rest = seconds % 60;
  return rest ? `${minutes} 分 ${rest} 秒` : `${minutes} 分钟`;
}

function withDuration(label: string, duration?: string, separator = ' · '): string {
  return duration ? `${label}${separator}${duration}` : label;
}

function statusCopy(status: string | undefined, duration?: string): { label: string; tone: string } {
  if (status === 'failed') return { label: withDuration('运行失败', duration), tone: 'error' };
  if (status === 'interrupted' || status === 'cancelled' || status === 'lost') {
    return { label: withDuration('已中断', duration), tone: 'stopped' };
  }
  if (status === 'succeeded') return { label: withDuration('已工作', duration, ' '), tone: 'success' };
  if (!status) return { label: withDuration('工作过程', duration), tone: 'neutral' };
  return { label: withDuration('工作中', duration, ' '), tone: 'running' };
}

function screenReaderStatus(status: string | undefined): string {
  if (status === 'failed') return '工作运行失败';
  if (status === 'interrupted' || status === 'cancelled' || status === 'lost') return '工作已中断';
  if (status === 'succeeded') return '工作已完成';
  if (status && ACTIVE.has(status)) return '工作正在进行';
  return '工作状态未知';
}

function itemStartedAt(item: WorkActivityItem): string | undefined {
  if (item.kind === 'thinking' || item.kind === 'assistant' || item.kind === 'meta') {
    return item.msg.startedAt ?? item.msg.at;
  }
  if (item.kind === 'activity') return item.items[0]?.startedAt ?? item.items[0]?.at;
  if (item.kind === 'swarm') return item.msg.swarm?.startedAt ?? item.msg.at;
  if (item.kind === 'subagent') return item.msg.at;
  if (item.kind === 'approval') return item.approval.resolved_at;
  return undefined;
}

function workSummary(items: readonly WorkActivityItem[]): string {
  const thinking = items.filter((item) => item.kind === 'thinking').length;
  const tools = items.reduce((total, item) => total + (item.kind === 'activity' ? item.items.length : 0), 0);
  const narration = items.filter((item) => item.kind === 'assistant').length;
  const swarms = items.filter((item) => item.kind === 'swarm').length;
  return [
    thinking ? `${thinking} 段思考` : '',
    tools ? `${tools} 次工具` : '',
    swarms ? `${swarms} 个蜂群` : '',
    narration ? `${narration} 段过程正文` : '',
  ].filter(Boolean).join(' · ');
}

function workItemKey(item: WorkActivityItem, index: number): string {
  if (item.kind === 'activity') return `activity:${item.runId}:${item.items[0]?.key ?? index}`;
  if (item.kind === 'swarm') return `swarm:${item.runId}:${item.msg.swarm?.id ?? item.msg.key}`;
  if (item.kind === 'subagent') return `subagent:${item.runId}:${item.msg.childAgent?.id ?? item.msg.key}`;
  if (item.kind === 'thinking-placeholder') return `placeholder:${item.runId}`;
  if (item.kind === 'approval') return `approval:${item.approval.id}`;
  if (item.kind === 'thinking' || item.kind === 'assistant') return item.renderKey ?? item.msg.key;
  return item.msg.key;
}

function isVisibleWorkItem(item: WorkActivityItem): boolean {
  return item.kind !== 'thinking' || !item.streaming || item.msg.text.trim().length > 0;
}

function thinkingPreview(text: string): string {
  const lastLine = text.split(/\r?\n/).map((line) => line.trim()).filter(Boolean).at(-1) ?? '';
  return lastLine;
}

export function OutputLoadingIndicator() {
  return (
    <div className={css.outputLoading} role="status" aria-live="polite">
      <LoaderCircle className={css.outputLoadingIcon} aria-hidden="true" />
      <span className="sr-only">正在输出</span>
    </div>
  );
}

export function shouldAutoCollapseReasoning(
  previousKey: string | null,
  currentKey: string | null,
  userInteracted: boolean,
): boolean {
  return currentKey !== null && previousKey !== currentKey && !userInteracted;
}

function ThinkingDisclosure({
  item,
  duration,
}: {
  item: Extract<WorkActivityItem, { kind: 'thinking' }>;
  duration?: string;
}) {
  const bodyId = useId();
  const [expanded, setExpanded] = useState(false);
  const [renderContent, setRenderContent] = useState(false);
  const [scrollMask, setScrollMask] = useState<'none' | 'top' | 'bottom' | 'both'>('none');
  const previewRef = useRef<HTMLSpanElement>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  const userInteractedRef = useRef(false);
  const autoCollapseKey = item.streaming
    ? null
    : `${item.renderKey ?? item.msg.phaseId ?? item.msg.key}:settled`;
  const previousAutoCollapseKeyRef = useRef<string | null>(autoCollapseKey);
  const stickToBottomRef = useRef(true);
  const scrollFrameRef = useRef(0);
  const label = duration ? `思考 · 持续了 ${duration}` : item.streaming ? '正在思考' : '思考';
  const preview = thinkingPreview(item.msg.text);

  useEffect(() => {
    if (expanded) {
      setRenderContent(true);
      return;
    }
    if (!renderContent) return;
    const timer = window.setTimeout(() => setRenderContent(false), 300);
    return () => window.clearTimeout(timer);
  }, [expanded, renderContent]);

  useEffect(() => {
    const previous = previousAutoCollapseKeyRef.current;
    previousAutoCollapseKeyRef.current = autoCollapseKey;
    if (shouldAutoCollapseReasoning(previous, autoCollapseKey, userInteractedRef.current)) {
      setExpanded(false);
    }
  }, [autoCollapseKey]);

  const updateScrollMask = useCallback(() => {
    const element = bodyRef.current;
    if (!element) return;
    const top = element.scrollTop > 2;
    const bottom = element.scrollHeight - element.scrollTop - element.clientHeight > 2;
    setScrollMask(top ? (bottom ? 'both' : 'top') : bottom ? 'bottom' : 'none');
    stickToBottomRef.current = !bottom;
  }, []);

  const scheduleScrollMask = useCallback(() => {
    if (scrollFrameRef.current) return;
    scrollFrameRef.current = requestAnimationFrame(() => {
      scrollFrameRef.current = 0;
      updateScrollMask();
    });
  }, [updateScrollMask]);

  useEffect(() => {
    if (expanded || !previewRef.current) return;
    const element = previewRef.current;
    const frame = requestAnimationFrame(() => {
      element.scrollLeft = element.scrollWidth;
    });
    return () => cancelAnimationFrame(frame);
  }, [expanded, preview]);

  useEffect(() => {
    if (!expanded || !item.streaming || !bodyRef.current) return;
    const element = bodyRef.current;
    if (!stickToBottomRef.current) return;
    const frame = requestAnimationFrame(() => {
      element.scrollTop = element.scrollHeight;
      scheduleScrollMask();
    });
    return () => cancelAnimationFrame(frame);
  }, [expanded, item.msg.text, item.streaming, scheduleScrollMask]);

  useEffect(() => {
    if (expanded) scheduleScrollMask();
    return () => {
      if (scrollFrameRef.current) cancelAnimationFrame(scrollFrameRef.current);
      scrollFrameRef.current = 0;
    };
  }, [expanded, item.msg.text, scheduleScrollMask]);

  return (
    <div className={css.thinking} data-kind="thinking" data-streaming={item.streaming || undefined}>
      <button
        type="button"
        className={css.thinkingHeader}
        aria-expanded={expanded}
        aria-controls={expanded ? bodyId : undefined}
        onClick={() => {
          userInteractedRef.current = true;
          setExpanded((value) => !value);
        }}
        aria-label={`${expanded ? '收起' : '展开'}${label}${preview ? `：${preview}` : ''}`}
      >
        <span className={css.thinkingLabel}>{label}</span>
        {!expanded && <span ref={previewRef} className={css.thinkingPreview} data-preview-key={preview}>
          <span key={preview} className={css.thinkingPreviewText}>{preview}</span>
        </span>}
        <ChevronDown className={css.thinkingChevron} aria-hidden />
      </button>
      {renderContent && <div
        id={bodyId}
        ref={bodyRef}
        className={css.thinkingText}
        data-expanded={expanded}
        data-scroll-mask={scrollMask}
        aria-hidden={!expanded}
        onScroll={scheduleScrollMask}
      >{item.msg.text}</div>}
    </div>
  );
}

function Item({
  item,
  nextItem,
  timelineUpdatedAt,
  now,
  running,
  stoppedRuns,
  onSelectSwarmMember,
  selectedSwarmMemberKey,
}: {
  item: WorkActivityItem;
  nextItem?: WorkActivityItem;
  timelineUpdatedAt: string;
  now: number;
  running: boolean;
  stoppedRuns?: ReadonlySet<string>;
  onSelectSwarmMember?: (runId: string, swarmId: string, member: SwarmMemberProjection) => void;
  selectedSwarmMemberKey?: string;
}) {
  switch (item.kind) {
    case 'activity':
      return <ActivityGroup items={item.items} stoppedRuns={stoppedRuns} variant="timeline" defaultCollapsed />;
    case 'swarm':
      return item.msg.swarm ? <SwarmChatBlock projection={item.msg.swarm} selectedMemberKey={selectedSwarmMemberKey} selectionPrefix={item.runId} onSelectMember={(member) => onSelectSwarmMember?.(item.runId, item.msg.swarm!.id, member)} /> : null;
    case 'subagent':
      return item.msg.childAgent ? <CodexAgentCard agent={item.msg.childAgent} selected={selectedSwarmMemberKey === `${item.runId}:${item.msg.childAgent.parentThreadId ?? 'codex'}:${item.msg.childAgent.id}`} onSelect={(agent) => onSelectSwarmMember?.(item.runId, agent.parentThreadId ?? 'codex', agent)} /> : null;
    case 'thinking': {
      const start = item.msg.startedAt ?? item.msg.at;
      const nextStartedAt = nextItem ? itemStartedAt(nextItem) : undefined;
      const end = item.msg.completedAt
        ?? (item.streaming && running ? new Date(now).toISOString() : nextStartedAt ?? timelineUpdatedAt);
      const duration = start === end ? undefined : workElapsed(start, end, now, false);
      return <ThinkingDisclosure item={item} duration={duration} />;
    }
    case 'thinking-placeholder':
      return <OutputLoadingIndicator />;
    case 'assistant':
      return (
        <article className={`chat-assistant-turn ${css.assistant}`} aria-label="过程正文">
          <AgentOutput
            text={item.msg.text}
            streaming={item.streaming}
            contentBlocks={item.msg.contentBlocks}
            runId={item.msg.runId}
            messageId={item.renderKey ?? item.msg.key}
          />
        </article>
      );
    case 'approval':
      return <ApprovalCard approval={item.approval} />;
    case 'meta':
      return <div className={css.meta} data-error={item.msg.kind === 'error' || undefined}>{item.msg.text}</div>;
  }
}

export function WorkActivityTimeline({ segment, onSelectSwarmMember, selectedSwarmMemberKey }: { segment: WorkTimelineSegment; onSelectSwarmMember?: (runId: string, swarmId: string, member: SwarmMemberProjection) => void; selectedSwarmMemberKey?: string }) {
  const headingId = useId();
  const bodyId = useId();
  const visibleItems = useMemo(() => segment.items.filter(isVisibleWorkItem), [segment.items]);
  const inferredRunning = segment.status === undefined && visibleItems.some((item) =>
    (item.kind === 'thinking' && item.streaming)
    || (item.kind === 'assistant' && item.streaming)
    || item.kind === 'thinking-placeholder'
    || (item.kind === 'activity' && item.items.some((tool) => tool.toolStatus === 'running'))
    || (item.kind === 'swarm' && item.msg.swarm?.status === 'running')
    || (item.kind === 'subagent' && item.msg.childAgent?.status === 'running')
    || (item.kind === 'approval' && item.approval.status === 'pending'),
  );
  const running = (segment.status !== undefined && ACTIVE.has(segment.status)) || inferredRunning;
  const effectiveStatus = inferredRunning ? 'running' : segment.status;
  const [expanded, setExpanded] = useState(
    running
    || segment.status === 'failed'
    || segment.status === 'interrupted'
    || segment.status === 'cancelled'
    || segment.status === 'lost',
  );
  const previousStatusRef = useRef(segment.status);
  const previousRunningRef = useRef(running);
  const [now, setNow] = useState(() => Date.now());
  const renderedItems = useMemo(() => {
    if (!running || visibleItems.some((item) => item.kind === 'thinking-placeholder')) return visibleItems;
    const hasPendingApproval = segment.status === 'waiting_approval' || visibleItems.some((item) => item.kind === 'approval');
    const hasRunningTool = visibleItems.some((item) =>
      (item.kind === 'activity' && item.items.some((tool) => tool.toolStatus === 'running'))
      || (item.kind === 'swarm' && item.msg.swarm?.status === 'running')
      || (item.kind === 'subagent' && item.msg.childAgent?.status === 'running'),
    );
    const latestThinking = [...visibleItems].reverse().find((item) => item.kind === 'thinking' && item.streaming);
    if (!latestThinking) return visibleItems;
    const lastDeltaAt = latestThinking?.kind === 'thinking'
      ? Date.parse(latestThinking.msg.at)
      : Number.NaN;
    const reasoningIdle = Boolean(
      latestThinking
      && Number.isFinite(lastDeltaAt)
      && now - lastDeltaAt >= OUTPUT_LOADING_IDLE_MS,
    );
    if (!reasoningIdle || !shouldShowOutputLoading({
      runActive: true,
      hasPendingApproval,
      hasRunningTool,
      streamingReasoning: true,
      reasoningIdle,
      streamingAnswer: false,
    })) return visibleItems;
    const settledItems = reasoningIdle && latestThinking
      ? visibleItems.map((item) => item.kind === 'thinking' && item === latestThinking
        ? { ...item, streaming: false, msg: { ...item.msg, completedAt: item.msg.at } }
        : item)
      : visibleItems;
    return [...settledItems, { kind: 'thinking-placeholder' as const, runId: segment.runId }];
  }, [now, running, segment.runId, segment.status, visibleItems]);
  useEffect(() => {
    if (!running) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [running]);
  useEffect(() => {
    const previous = previousStatusRef.current;
    const wasRunning = previousRunningRef.current;
    previousStatusRef.current = segment.status;
    previousRunningRef.current = running;
    if (!wasRunning && running) setExpanded(true);
    if (previous === segment.status) return;
    if (segment.status === 'succeeded') setExpanded(false);
    if (segment.status === 'failed' || segment.status === 'interrupted' || segment.status === 'cancelled' || segment.status === 'lost') {
      setExpanded(true);
    }
    if (!previous && segment.status && ACTIVE.has(segment.status)) setExpanded(true);
  }, [running, segment.status]);
  const duration = useMemo(() => workElapsed(segment.createdAt, segment.updatedAt, now, running), [segment.createdAt, segment.updatedAt, now, running]);
  const status = statusCopy(effectiveStatus, duration);
  const summary = workSummary(visibleItems);
  const stoppedRuns = useMemo<ReadonlySet<string> | undefined>(
    () => running ? undefined : new Set([segment.runId]),
    [running, segment.runId],
  );
  return (
    <section className={css.timeline} aria-labelledby={headingId} aria-busy={running} data-status={effectiveStatus}>
      <button
        type="button"
        className={css.header}
        data-expanded={expanded}
        onClick={() => setExpanded((value) => !value)}
        aria-expanded={expanded}
        aria-controls={expanded ? bodyId : undefined}
        aria-label={`${expanded ? '收起' : '展开'}工作过程：${status.label}`}
      >
        <span className={css.mark} aria-hidden>
          {running ? <LoaderCircle /> : status.tone === 'success' ? <Check /> : status.tone === 'stopped' ? <CircleStop /> : status.tone === 'error' ? <CircleX /> : <Wrench />}
        </span>
        <span className={css.heading}>
          <span id={headingId} className={css.title} data-tone={status.tone}>{status.label}</span>
          <span className="sr-only" role="status" aria-atomic="true">{screenReaderStatus(effectiveStatus)}</span>
          {summary && <span className={css.summary}>{summary}</span>}
        </span>
        <ChevronDown className={css.chevron} aria-hidden />
      </button>
      {expanded && (
        <div
          id={bodyId}
          className={css.body}
          role="list"
          aria-label="按顺序排列的工作过程"
        >
          {renderedItems.map((item, index) => (
            <div className={css.item} role="listitem" key={workItemKey(item, index)}>
              <Item
                item={item}
                nextItem={renderedItems[index + 1]}
                timelineUpdatedAt={segment.updatedAt}
                now={now}
                running={running}
                stoppedRuns={stoppedRuns}
                onSelectSwarmMember={onSelectSwarmMember}
                selectedSwarmMemberKey={selectedSwarmMemberKey}
              />
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

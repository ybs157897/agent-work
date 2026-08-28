import { create } from 'zustand';
import { getRun, listApprovals, listArtifacts, listRunEvents } from '../api/endpoints';
import type { ApprovalRequest, Artifact, CanonicalEvent, ExecutionRun } from '../api/types';
import { extractDeltaChunk } from './delta-chunk';
import { isOutputTraceEnabled, outputTraceEventText, traceOutput } from '../utils/output-trace';

/** Run 时间线条目：来自 SSE 信封（协议 §6.2）或 run_seq 历史回放。 */
export interface TimelineEntry {
  event_id: string;
  stream_seq: number;
  run_seq?: number;
  type: string;
  occurred_at: string;
  role?: string;
  text?: string;
  data?: Record<string, unknown>;
}

const TIMELINE_CAP = 500;
/** 推理折叠预算：每个 message.completed 锚点上保留的逐出推理文本尾部上限（字符）。 */
const REASONING_FOLD_TAIL_CHARS = 4000;
const TIMELINE_STICKY_GROUPS: ReadonlyArray<ReadonlySet<string>> = [
  new Set(['run.plan_updated']),
  new Set(['goal.updated', 'goal.create', 'goal.clear', 'session.goal.updated']),
];

/** text-delta 在时间线 cap 前被逐帧淘汰时仍需保留完整的用户可见过程正文。 */
const liveTextFoldBuffers = new Map<string, { text: string; startedAt: string; phaseId: string }>();
const liveReasoningFoldBuffers = new Map<string, { text: string; startedAt: string; completedAt: string; phaseId: string }>();
/** 已见 run 事件身份；防历史/SSE 重放重复渲染或把 delta 再次拼进折叠缓冲。 */
const liveEventKeys = new Map<string, Set<string>>();
/** run 快照 single-flight：长历史重放期间未知 run 只允许一个在途请求。 */
const runSnapshotFetches = new Map<string, Promise<void>>();
const runTerminalRefreshQueued = new Set<string>();
const RUN_TERMINAL_STATUSES = new Set<ExecutionRun['status']>([
  'succeeded', 'failed', 'cancelled', 'interrupted', 'lost',
]);
/** SSE 已观察到但快照可能尚未包含的终态；保护旧在途响应不倒退状态。 */
const observedRunTerminals = new Map<string, { status: ExecutionRun['status']; occurredAt: string }>();

interface RunsStore {
  runs: Record<string, ExecutionRun>;
  timelines: Record<string, TimelineEntry[]>;
  approvals: Record<string, ApprovalRequest[]>;
  artifacts: Record<string, Artifact[]>;
  /** 正在展示的 run（详情抽屉/对话页引用计数）；watched run 会拉取快照与历史。 */
  watching: Record<string, number>;
  /** 已加载过历史回放的 run。 */
  historyLoaded: Record<string, boolean>;

  watchRun: (runId: string) => void;
  unwatchRun: (runId: string) => void;
  fetchRun: (runId: string) => Promise<void>;
  fetchApprovals: (runId: string) => Promise<void>;
  fetchArtifacts: (runId: string) => Promise<void>;
  /** 加载 run_events 历史并按 run_seq 与 SSE 增量合并去重。 */
  loadHistory: (runId: string) => Promise<void>;
  /** 处理 run 域 SSE 事件；返回是否已消费。 */
  applyEvent: (ev: CanonicalEvent) => boolean;
}

function entryText(ev: CanonicalEvent): string | undefined {
  const v = ev.data?.text;
  return typeof v === 'string' ? v : undefined;
}

function entryRole(ev: CanonicalEvent): string | undefined {
  const v = ev.data?.role;
  return typeof v === 'string' ? v : undefined;
}

/**
 * usage.updated → 快照用量四字段 patch（run 进行中用量就地更新，不等终态
 * fetchRun）。契约上四件齐全；防御式只取类型匹配项，避免半帧清除已知值。
 */
function parseUsagePatch(
  data?: Record<string, unknown>,
): Partial<Pick<ExecutionRun, 'usage_in' | 'usage_out' | 'usage_cached' | 'usage_basis'>> | undefined {
  if (!data) return undefined;
  const patch: Partial<Pick<ExecutionRun, 'usage_in' | 'usage_out' | 'usage_cached' | 'usage_basis'>> = {};
  if (typeof data.usage_in === 'number') patch.usage_in = data.usage_in;
  if (typeof data.usage_out === 'number') patch.usage_out = data.usage_out;
  if (typeof data.usage_cached === 'number') patch.usage_cached = data.usage_cached;
  if (typeof data.usage_basis === 'string') patch.usage_basis = data.usage_basis;
  return Object.keys(patch).length > 0 ? patch : undefined;
}

/** 历史与 SSE 增量按 run_seq 去重合并（SSE 条目优先，run_seq 缺省的历史条目保留）。 */
function mergeTimeline(history: TimelineEntry[], live: TimelineEntry[]): TimelineEntry[] {
  const bySeq = new Map<number, TimelineEntry>();
  const noSeq: TimelineEntry[] = [];
  for (const e of history) {
    if (e.run_seq !== undefined) bySeq.set(e.run_seq, e);
    else noSeq.push(e);
  }
  for (const e of live) {
    if (e.run_seq !== undefined) bySeq.set(e.run_seq, e);
    else noSeq.push(e);
  }
  const merged = [...bySeq.values()].sort((a, b) => (a.run_seq ?? 0) - (b.run_seq ?? 0));
  return capTimeline([...merged, ...noSeq]);
}

const TOOL_EVENT_TYPES = new Set(['tool.started', 'tool.progress', 'tool.completed', 'tool.failed']);
const TOOL_TERMINAL_TYPES = new Set(['tool.completed', 'tool.failed']);
const STRUCTURAL_EVENT_TYPES = new Set(['run.created', 'message.completed']);

function toolCallId(entry: TimelineEntry): string | undefined {
  const value = entry.data?.call_id;
  return typeof value === 'string' && value.length > 0 ? value : undefined;
}

function entryIdentity(entry: TimelineEntry): string {
  return entry.run_seq === undefined ? entry.event_id : `run:${entry.run_seq}`;
}

function entryPhaseId(entry: TimelineEntry): string {
  return entry.run_seq === undefined ? entry.event_id : `run-seq-${entry.run_seq}`;
}

function eventIdentity(event: Pick<CanonicalEvent, 'event_id' | 'run_seq'>): string {
  return event.run_seq === undefined ? event.event_id : `run:${event.run_seq}`;
}

function latestEntry(entries: TimelineEntry[], predicate: (entry: TimelineEntry) => boolean): TimelineEntry | undefined {
  for (let index = entries.length - 1; index >= 0; index -= 1) {
    if (predicate(entries[index])) return entries[index];
  }
  return undefined;
}

/**
 * Select complete tool call bundles without allowing a terminal event to outlive
 * its started event. A completed call needs started + terminal; a live call needs
 * started + its latest progress only. This is deliberately event based so history
 * replay and SSE use the same retention semantics.
 */
function selectToolBundles(entries: TimelineEntry[], budget: number): Set<string> {
  const calls = new Map<string, TimelineEntry[]>();
  for (const entry of entries) {
    if (!TOOL_EVENT_TYPES.has(entry.type)) continue;
    const callId = toolCallId(entry);
    if (!callId) continue;
    const call = calls.get(callId) ?? [];
    call.push(entry);
    calls.set(callId, call);
  }

  const bundles = [...calls.entries()]
    .map(([callId, callEntries]) => {
      const started = callEntries.find((entry) => entry.type === 'tool.started');
      if (!started) return undefined;
      const terminal = latestEntry(callEntries, (entry) => TOOL_TERMINAL_TYPES.has(entry.type));
      const latestProgress = latestEntry(callEntries, (entry) => entry.type === 'tool.progress');
      const retained = terminal ? [started, terminal] : [started, ...(latestProgress ? [latestProgress] : [])];
      const lastIndex = Math.max(...retained.map((entry) => entries.indexOf(entry)));
      return { callId, retained, lastIndex };
    })
    .filter((bundle): bundle is { callId: string; retained: TimelineEntry[]; lastIndex: number } => Boolean(bundle))
    .sort((a, b) => b.lastIndex - a.lastIndex);

  const selected = new Set<string>();
  let used = 0;
  for (const bundle of bundles) {
    if (used + bundle.retained.length > budget) continue;
    for (const entry of bundle.retained) selected.add(entryIdentity(entry));
    used += bundle.retained.length;
  }
  return selected;
}

/**
 * 推理/过程正文折叠：reasoning-delta 与正文帧同池按尾填充，而推理永远先于正文产生——
 * 长 run 超帽后推理帧必被整段淘汰，历史「思考过程」面板因此整层消失。
 * 截断落定后，把每个 tool.started / message.completed 阶段边界前未全程存活的
 * 推理聚合挂到该边界 data 上（`reasoning_folded`，合成投影字段、不上线），过程正文
 * 同样挂到 `text_folded`。这样历史回放仍保持 thinking → 正文 → tool → thinking，
 * 而不是把过程正文挪到最终正文前。推理有尾部预算，正文不截断：它是用户可见内容，
 * 还需要支持 final 前缀去重；块内帧全部存活时不折叠（buildMessages 走原缓冲）。
 */
function foldEvictedReasoning(
  entries: readonly TimelineEntry[],
  retained: TimelineEntry[],
): TimelineEntry[] {
  const retainedIds = new Set(retained.map(entryIdentity));
  const folds = new Map<string, {
    reasoning?: { text: string; truncated: boolean; startedAt?: string; completedAt?: string; phaseId?: string };
    text?: { text: string; startedAt?: string; phaseId?: string };
  }>();
  const buffers: Record<'reasoning' | 'text', { text: string; sawDelta: boolean; sawEvicted: boolean; startedAt: string; completedAt: string; phaseId: string }> = {
    reasoning: { text: '', sawDelta: false, sawEvicted: false, startedAt: '', completedAt: '', phaseId: '' },
    text: { text: '', sawDelta: false, sawEvicted: false, startedAt: '', completedAt: '', phaseId: '' },
  };
  for (const entry of entries) {
    if (entry.type === 'message.delta') {
      const text = extractDeltaChunk(entry.data);
      const deltaKind = text?.type === 'reasoning-delta' ? 'reasoning' : text?.type === 'text-delta' ? 'text' : undefined;
      if (deltaKind && text?.text) {
        const buffer = buffers[deltaKind];
        if (!buffer.sawDelta) {
          buffer.startedAt = entry.occurred_at;
          buffer.phaseId = entryPhaseId(entry);
        }
        buffer.text += text.text;
        if (deltaKind === 'reasoning') buffer.completedAt = entry.occurred_at;
        buffer.sawDelta = true;
        if (!retainedIds.has(entryIdentity(entry))) buffer.sawEvicted = true;
      }
      continue;
    }
    if (entry.type === 'tool.started' || entry.type === 'message.completed') {
      // 未保留的工具边界在 UI 中也不可见：把它之前的逐出推理继续带到
      // 下一个可见边界，避免旧阶段完全丢失；可见边界才真正关闭 fold。
      if (!retainedIds.has(entryIdentity(entry))) continue;
      // 折叠粘滞：锚点已携带折叠时绝不重算覆盖——增量合并每来一个事件就重截断，
      // 扫描窗口随尾淘汰收窄，重算会让折叠越合越短（测试钉：部分逐出场景）。
      // 折叠在锚点定稿时生成，此后该块的推理不可能再有新增，保持原值即完整。
      if (retainedIds.has(entryIdentity(entry))) {
        const fold: { reasoning?: { text: string; truncated: boolean; startedAt?: string; completedAt?: string; phaseId?: string }; text?: { text: string; startedAt?: string; phaseId?: string } } = {};
        const reasoning = buffers.reasoning;
        if (typeof entry.data?.reasoning_folded !== 'string' && reasoning.sawDelta && reasoning.sawEvicted) {
          const truncated = reasoning.text.length > REASONING_FOLD_TAIL_CHARS;
          fold.reasoning = {
            text: truncated ? reasoning.text.slice(-REASONING_FOLD_TAIL_CHARS) : reasoning.text,
            truncated,
            ...(reasoning.startedAt ? { startedAt: reasoning.startedAt } : {}),
            ...(reasoning.completedAt ? { completedAt: reasoning.completedAt } : {}),
            ...(reasoning.phaseId ? { phaseId: reasoning.phaseId } : {}),
          };
        }
        const textBuffer = buffers.text;
        if (typeof entry.data?.text_folded !== 'string' && textBuffer.sawDelta && textBuffer.sawEvicted) {
          fold.text = {
            text: textBuffer.text,
            ...(textBuffer.startedAt ? { startedAt: textBuffer.startedAt } : {}),
            ...(textBuffer.phaseId ? { phaseId: textBuffer.phaseId } : {}),
          };
        }
        if (fold.reasoning || fold.text) folds.set(entryIdentity(entry), fold);
      }
      if (retainedIds.has(entryIdentity(entry))) {
        for (const buffer of Object.values(buffers)) {
          buffer.text = '';
          buffer.sawDelta = false;
          buffer.sawEvicted = false;
          buffer.startedAt = '';
          buffer.completedAt = '';
          buffer.phaseId = '';
        }
      }
    }
  }
  if (folds.size === 0) return retained;
  return retained.map((entry) => {
    const fold = folds.get(entryIdentity(entry));
    if (!fold) return entry;
    return {
      ...entry,
      data: {
        ...entry.data,
          ...(fold.reasoning ? {
            reasoning_folded: fold.reasoning.text,
            ...(fold.reasoning.startedAt ? { reasoning_folded_started_at: fold.reasoning.startedAt } : {}),
            ...(fold.reasoning.completedAt ? { reasoning_folded_completed_at: fold.reasoning.completedAt } : {}),
          ...(fold.reasoning.phaseId ? { reasoning_folded_phase_id: fold.reasoning.phaseId } : {}),
          ...(fold.reasoning.truncated ? { reasoning_folded_truncated: true } : {}),
        } : {}),
        ...(fold.text ? {
          text_folded: fold.text.text,
          ...(fold.text.startedAt ? { text_folded_started_at: fold.text.startedAt } : {}),
          ...(fold.text.phaseId ? { text_folded_phase_id: fold.text.phaseId } : {}),
        } : {}),
      },
    };
  });
}

/**
 * 高频 delta 只保留尾部，但结构事件、Plan/Goal 快照和工具生命周期有更高保留优先级。
 * 工具按完整 call bundle 从新到旧选择，绝不保留没有 started 的 terminal。
 */
function capTimeline(entries: TimelineEntry[]): TimelineEntry[] {
  if (entries.length <= TIMELINE_CAP) return entries;
  const selected = new Set<string>();
  const identity = entryIdentity;

  // These are the durable transcript anchors. Preserve them before noisy delta
  // frames; the common case has only a handful of these entries.
  for (const entry of entries) {
    if (STRUCTURAL_EVENT_TYPES.has(entry.type)) selected.add(identity(entry));
  }

  // Plan and Goal are current-state snapshots. Keep the latest entry in each
  // family, including an explicit empty/clear payload.
  for (const group of TIMELINE_STICKY_GROUPS) {
    const latest = latestEntry(entries, (entry) => group.has(entry.type));
    if (latest) selected.add(identity(latest));
  }

  // If an unusually large transcript contains more anchors than the cap, keep
  // the newest anchors first while retaining run.created when possible.
  if (selected.size > TIMELINE_CAP) {
    const anchors = entries.filter((entry) => selected.has(identity(entry)));
    const newest = anchors.slice(-TIMELINE_CAP);
    return foldEvictedReasoning(entries, newest.sort((a, b) => entries.indexOf(a) - entries.indexOf(b)));
  }

  const toolBudget = TIMELINE_CAP - selected.size;
  const toolEntries = entries.filter((entry) => TOOL_EVENT_TYPES.has(entry.type));
  const toolSelected = selectToolBundles(toolEntries, toolBudget);
  for (const id of toolSelected) selected.add(id);

  // Some adapters emit tool.started before a call id is available. Preserve
  // each such event as its own live bundle; otherwise it is excluded from
  // both bundle selection and the non-tool tail.
  const unboundStarts = toolEntries
    .filter((entry) => entry.type === 'tool.started' && !toolCallId(entry))
    .sort((a, b) => entries.indexOf(b) - entries.indexOf(a));
  const unboundBudget = Math.max(0, toolBudget - toolSelected.size);
  for (const entry of unboundStarts.slice(0, unboundBudget)) selected.add(identity(entry));

  // Fill remaining capacity with newest non-structural, non-tool frames. This
  // preserves the visible tail without allowing it to evict a call lifecycle.
  const remaining = TIMELINE_CAP - selected.size;
  if (remaining > 0) {
    const tail = entries.filter((entry) => !selected.has(identity(entry)) && !TOOL_EVENT_TYPES.has(entry.type));
    for (const entry of tail.slice(-remaining)) selected.add(identity(entry));
  }

  return foldEvictedReasoning(entries, entries.filter((entry) => selected.has(identity(entry))));
}

export const useRunsStore = create<RunsStore>()((set, get) => ({
  runs: {},
  timelines: {},
  approvals: {},
  artifacts: {},
  watching: {},
  historyLoaded: {},

  watchRun: (runId) => {
    set((s) => ({ watching: { ...s.watching, [runId]: (s.watching[runId] ?? 0) + 1 } }));
    void get().fetchRun(runId);
    void get().fetchApprovals(runId);
    void get().fetchArtifacts(runId);
    void get().loadHistory(runId);
  },

  unwatchRun: (runId) =>
    set((s) => {
      const n = (s.watching[runId] ?? 0) - 1;
      const watching = { ...s.watching };
      if (n <= 0) delete watching[runId];
      else watching[runId] = n;
      if (n <= 0) liveTextFoldBuffers.delete(runId);
      if (n <= 0) liveReasoningFoldBuffers.delete(runId);
      if (n <= 0) liveEventKeys.delete(runId);
      return { watching };
    }),

  fetchRun: async (runId) => {
    const existing = runSnapshotFetches.get(runId);
    if (existing) return existing;
    const request = getRun(runId)
      .then((run) => {
        const observed = observedRunTerminals.get(runId);
        const protectedRun = observed && !RUN_TERMINAL_STATUSES.has(run.status)
          ? { ...run, status: observed.status, updated_at: observed.occurredAt }
          : run;
        set((s) => ({ runs: { ...s.runs, [runId]: protectedRun } }));
        if (observed && RUN_TERMINAL_STATUSES.has(run.status)) observedRunTerminals.delete(runId);
      })
      .finally(() => runSnapshotFetches.delete(runId));
    runSnapshotFetches.set(runId, request);
    return request;
  },

  fetchApprovals: async (runId) => {
    const { items } = await listApprovals(runId);
    set((s) => ({ approvals: { ...s.approvals, [runId]: items } }));
  },

  fetchArtifacts: async (runId) => {
    const { items } = await listArtifacts(runId);
    set((s) => ({ artifacts: { ...s.artifacts, [runId]: items } }));
  },

  loadHistory: async (runId) => {
    if (get().historyLoaded[runId]) return;
    set((s) => ({ historyLoaded: { ...s.historyLoaded, [runId]: true } }));
    try {
      const { items } = await listRunEvents(runId);
      const history: TimelineEntry[] = items.map((e) => ({
        event_id: `hist-${runId}-${e.run_seq}`,
        stream_seq: 0,
        run_seq: e.run_seq,
        type: e.event_type,
        occurred_at: e.occurred_at,
        role: typeof e.payload?.role === 'string' ? e.payload.role : undefined,
        text: typeof e.payload?.text === 'string' ? e.payload.text : undefined,
        data: e.payload,
      }));
      const seenEvents = liveEventKeys.get(runId) ?? new Set<string>();
      for (const entry of history) seenEvents.add(entryIdentity(entry));
      liveEventKeys.set(runId, seenEvents);
      set((s) => ({
        timelines: { ...s.timelines, [runId]: mergeTimeline(history, s.timelines[runId] ?? []) },
      }));
    } catch {
      // 历史加载失败不阻塞实时流；下次 watch 重试。
      set((s) => ({ historyLoaded: { ...s.historyLoaded, [runId]: false } }));
    }
  },

  applyEvent: (ev) => {
    const aggType = ev.aggregate.type;
    const runId = aggType === 'execution_run' ? ev.aggregate.id : undefined;

    if (runId) {
      const beforeTimeline = get().timelines[runId] ?? [];
      const existingEntry = beforeTimeline.find((entry) =>
        ev.run_seq !== undefined && entry.run_seq !== undefined
          ? entry.run_seq === ev.run_seq
          : entry.event_id === ev.event_id,
      );
      const eventKey = eventIdentity(ev);
      const seenEvents = liveEventKeys.get(runId) ?? new Set<string>();
      const duplicate = existingEntry !== undefined || seenEvents.has(eventKey);
      if (ev.type === 'run.created' && !duplicate) {
        liveTextFoldBuffers.delete(runId);
        liveReasoningFoldBuffers.delete(runId);
        liveEventKeys.delete(runId);
        observedRunTerminals.delete(runId);
        runTerminalRefreshQueued.delete(runId);
      }
      const activeSeenEvents = liveEventKeys.get(runId) ?? new Set<string>();
      activeSeenEvents.add(eventKey);
      liveEventKeys.set(runId, activeSeenEvents);
      const delta = ev.type === 'message.delta' ? extractDeltaChunk(ev.data) : null;
      const duplicateDelta = Boolean(delta && duplicate);
      if (!duplicateDelta && delta?.type === 'text-delta' && delta.text) {
        const previous = liveTextFoldBuffers.get(runId);
        liveTextFoldBuffers.set(runId, {
          text: `${previous?.text ?? ''}${delta.text}`,
          startedAt: previous?.startedAt ?? ev.occurred_at,
          phaseId: previous?.phaseId ?? entryPhaseId({
            event_id: ev.event_id,
            stream_seq: ev.stream_seq,
            run_seq: ev.run_seq,
            type: ev.type,
            occurred_at: ev.occurred_at,
          }),
        });
      }
      if (!duplicateDelta && delta?.type === 'reasoning-delta' && delta.text) {
        const previous = liveReasoningFoldBuffers.get(runId);
        liveReasoningFoldBuffers.set(runId, {
          text: `${previous?.text ?? ''}${delta.text}`,
          startedAt: previous?.startedAt ?? ev.occurred_at,
          completedAt: ev.occurred_at,
          phaseId: previous?.phaseId ?? entryPhaseId({
            event_id: ev.event_id,
            stream_seq: ev.stream_seq,
            run_seq: ev.run_seq,
            type: ev.type,
            occurred_at: ev.occurred_at,
          }),
        });
      }
      const liveTextFold = (ev.type === 'tool.started' || ev.type === 'message.completed')
        ? liveTextFoldBuffers.get(runId)
        : undefined;
      const liveReasoningFold = (ev.type === 'tool.started' || ev.type === 'message.completed')
        ? liveReasoningFoldBuffers.get(runId)
        : undefined;
      const eventData = (liveTextFold || liveReasoningFold) && beforeTimeline.length >= TIMELINE_CAP
        ? {
            ...ev.data,
            ...(liveReasoningFold ? {
              reasoning_folded: liveReasoningFold.text.slice(-REASONING_FOLD_TAIL_CHARS),
              ...(liveReasoningFold.text.length > REASONING_FOLD_TAIL_CHARS ? { reasoning_folded_truncated: true } : {}),
              reasoning_folded_started_at: liveReasoningFold.startedAt,
              reasoning_folded_completed_at: liveReasoningFold.completedAt,
              reasoning_folded_phase_id: liveReasoningFold.phaseId,
            } : {}),
            ...(liveTextFold ? {
              text_folded: liveTextFold.text,
              text_folded_started_at: liveTextFold.startedAt,
              text_folded_phase_id: liveTextFold.phaseId,
            } : {}),
          }
        : ev.data;
      const terminalEvent = ev.type === 'run.completed' || ev.type === 'run.failed' ||
        (ev.type === 'run.status_changed' && ['succeeded', 'failed', 'cancelled', 'interrupted', 'lost'].includes(String(ev.data?.status)));
      // A replay of the same boundary must not erase client-side sticky fold
      // fields that were synthesized after the raw event first arrived.
      const retainedEventData = existingEntry?.data
        ? { ...eventData, ...existingEntry.data }
        : eventData;
      // 已由历史或实时流见过的事件不再制造同内容 store 更新；否则长 Run
      // 会因数百次重复 React 投影冻结页面。首次事件才进入时间线。
      if (!duplicate) {
        set((s) => ({
          timelines: {
            ...s.timelines,
            [runId]: mergeTimeline(s.timelines[runId] ?? [], [
              {
                event_id: ev.event_id,
                stream_seq: ev.stream_seq,
                run_seq: ev.run_seq,
                type: ev.type,
                occurred_at: ev.occurred_at,
                role: entryRole(ev),
                text: entryText(ev),
                data: retainedEventData,
              },
            ]),
          },
        }));
      }
      const appliedTimeline = get().timelines[runId] ?? [];
      const retained = appliedTimeline.some((entry) =>
        ev.run_seq !== undefined && entry.run_seq !== undefined
          ? entry.run_seq === ev.run_seq
          : entry.event_id === ev.event_id,
      );
      if (!duplicate && (ev.type === 'tool.started' || ev.type === 'message.completed') && liveTextFold) {
        if (retained || beforeTimeline.length < TIMELINE_CAP) liveTextFoldBuffers.delete(runId);
      }
      if (!duplicate && (ev.type === 'tool.started' || ev.type === 'message.completed') && liveReasoningFold) {
        if (retained || beforeTimeline.length < TIMELINE_CAP) liveReasoningFoldBuffers.delete(runId);
      }
      if (terminalEvent && !duplicate) {
        liveTextFoldBuffers.delete(runId);
        liveReasoningFoldBuffers.delete(runId);
      }
      if (isOutputTraceEnabled()) {
        traceOutput({
          stage: 'timeline.applied',
          mode: ev.type === 'message.completed' ? 'final' : 'streaming',
          source: 'timeline',
          runId,
          messageId: typeof ev.data?.message_id === 'string'
            ? ev.data.message_id
            : typeof ev.data?.item_id === 'string'
              ? ev.data.item_id
              : undefined,
          eventId: ev.event_id,
          correlationId: ev.correlation_id,
          callId: typeof ev.data?.call_id === 'string' ? ev.data.call_id : undefined,
          streamSeq: ev.stream_seq,
          runSeq: ev.run_seq,
          eventType: ev.type,
          serverOccurredAt: ev.occurred_at,
          text: outputTraceEventText(ev.type, ev.data),
          projection: { timelineEntries: appliedTimeline.length },
          status: typeof ev.data?.status === 'string' ? ev.data.status : undefined,
          duplicate,
          retained,
          metadata: ev.type === 'tool.completed' || ev.type === 'tool.failed'
            ? { lifecycle: 'tool-terminal' }
            : undefined,
        });
      }

      // 已加载的 run 快照做就地状态/进度/用量更新；watched 但未知时拉取快照。
      const cached = get().runs[runId];
      const lifecycleStatus: ExecutionRun['status'] | undefined = ev.type === 'run.completed'
        ? 'succeeded'
        : ev.type === 'run.failed'
          ? 'failed'
          : undefined;
      const status = typeof ev.data?.status === 'string'
        ? (ev.data.status as ExecutionRun['status'])
        : lifecycleStatus;
      const progress = typeof ev.data?.progress === 'number' ? ev.data.progress : undefined;
      const usage = ev.type === 'usage.updated' ? parseUsagePatch(ev.data) : undefined;
      if (
        !duplicate && terminalEvent && status && RUN_TERMINAL_STATUSES.has(status)
        && (Boolean(get().watching[runId]) || Boolean(cached) || runSnapshotFetches.has(runId))
      ) {
        observedRunTerminals.set(runId, { status, occurredAt: ev.occurred_at });
      }
      if (!duplicate && cached && (status || progress !== undefined || usage)) {
        set((s) => ({
          runs: {
            ...s.runs,
            [runId]: {
              ...cached,
              status: status ?? cached.status,
              progress: progress ?? cached.progress,
              ...(lifecycleStatus ? { updated_at: ev.occurred_at } : {}),
              ...usage,
            },
          },
        }));
      } else if (!cached && get().watching[runId]) {
        void get().fetchRun(runId);
      }

      if (!duplicate && ev.type === 'run.status_changed' && status === 'waiting_approval' && get().watching[runId]) {
        void get().fetchApprovals(runId);
      }
      if (!duplicate && terminalEvent && (get().watching[runId] || get().runs[runId])) {
        const pending = runSnapshotFetches.get(runId);
        if (!pending) {
          void get().fetchRun(runId);
        } else if (!runTerminalRefreshQueued.has(runId)) {
          runTerminalRefreshQueued.add(runId);
          void pending
            .catch(() => undefined)
            .then(() => get().fetchRun(runId))
            .catch(() => undefined)
            .finally(() => runTerminalRefreshQueued.delete(runId));
        }
      }
      if (terminalEvent && !get().watching[runId]) liveEventKeys.delete(runId);
      return true;
    }

    // approval / artifact 事件的信封不含 run_id（M1）：刷新所有 watched run 的对应数据。
    if (aggType === 'approval' || aggType === 'artifact') {
      for (const watchedId of Object.keys(get().watching)) {
        if (aggType === 'approval') void get().fetchApprovals(watchedId);
        else void get().fetchArtifacts(watchedId);
      }
      return true;
    }
    return false;
  },
}));

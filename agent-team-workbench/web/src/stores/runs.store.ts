import { create } from 'zustand';
import { getRun, listApprovals, listArtifacts, listRunEvents } from '../api/endpoints';
import type { ApprovalRequest, Artifact, CanonicalEvent, ExecutionRun } from '../api/types';
import { extractDeltaChunk } from './delta-chunk';

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
 * 推理折叠：reasoning-delta 与正文帧同池按尾填充，而推理永远先于正文产生——
 * 长 run 超帽后推理帧必被整段淘汰，历史「思考过程」面板因此整层消失。
 * 截断落定后，把每个 message.completed 锚点之前未全程存活的推理文本聚合挂到
 * 锚点 data 上（`reasoning_folded`，合成投影字段、不上线），思考内容随结构锚点
 * 存活。尾部预算截断防内存回流；块内帧全部存活时不折叠（buildMessages 走原
 * reasoningBuf 路径，双源不重复出卡）。
 */
function foldEvictedReasoning(
  entries: readonly TimelineEntry[],
  retained: TimelineEntry[],
): TimelineEntry[] {
  const retainedIds = new Set(retained.map(entryIdentity));
  const folds = new Map<string, { text: string; truncated: boolean }>();
  let buf = '';
  let sawDelta = false;
  let sawEvicted = false;
  for (const entry of entries) {
    if (entry.type === 'message.delta') {
      const text = extractDeltaChunk(entry.data);
      if (text?.type === 'reasoning-delta' && text.text) {
        buf += text.text;
        sawDelta = true;
        if (!retainedIds.has(entryIdentity(entry))) sawEvicted = true;
      }
      continue;
    }
    if (entry.type === 'message.completed') {
      // 折叠粘滞：锚点已携带折叠时绝不重算覆盖——增量合并每来一个事件就重截断，
      // 扫描窗口随尾淘汰收窄，重算会让折叠越合越短（测试钉：部分逐出场景）。
      // 折叠在锚点定稿时生成，此后该块的推理不可能再有新增，保持原值即完整。
      const alreadyFolded = typeof entry.data?.reasoning_folded === 'string';
      if (!alreadyFolded && sawDelta && sawEvicted && retainedIds.has(entryIdentity(entry))) {
        const truncated = buf.length > REASONING_FOLD_TAIL_CHARS;
        folds.set(entryIdentity(entry), {
          text: truncated ? buf.slice(-REASONING_FOLD_TAIL_CHARS) : buf,
          truncated,
        });
      }
      buf = '';
      sawDelta = false;
      sawEvicted = false;
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
        reasoning_folded: fold.text,
        ...(fold.truncated ? { reasoning_folded_truncated: true } : {}),
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
      return { watching };
    }),

  fetchRun: async (runId) => {
    const run = await getRun(runId);
    set((s) => ({ runs: { ...s.runs, [runId]: run } }));
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
      // 时间线追加（run.* / message.* / tool.* 都以 execution_run 聚合出现）。
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
              data: ev.data,
            },
          ]),
        },
      }));

      // 已加载的 run 快照做就地状态/进度/用量更新；watched 但未知时拉取快照。
      const cached = get().runs[runId];
      const status = typeof ev.data?.status === 'string' ? (ev.data.status as ExecutionRun['status']) : undefined;
      const progress = typeof ev.data?.progress === 'number' ? ev.data.progress : undefined;
      const usage = ev.type === 'usage.updated' ? parseUsagePatch(ev.data) : undefined;
      if (cached && (status || progress !== undefined || usage)) {
        set((s) => ({
          runs: {
            ...s.runs,
            [runId]: {
              ...cached,
              status: status ?? cached.status,
              progress: progress ?? cached.progress,
              ...usage,
            },
          },
        }));
      } else if (!cached && get().watching[runId]) {
        void get().fetchRun(runId);
      }

      if (ev.type === 'run.status_changed' && status === 'waiting_approval' && get().watching[runId]) {
        void get().fetchApprovals(runId);
      }
      if ((ev.type === 'run.completed' || ev.type === 'run.failed') && (get().watching[runId] || get().runs[runId])) {
        void get().fetchRun(runId);
      }
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

import { create } from 'zustand';
import { getRun, listApprovals, listArtifacts, listRunEvents } from '../api/endpoints';
import type { ApprovalRequest, Artifact, CanonicalEvent, ExecutionRun } from '../api/types';

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

function appendTimeline(entries: TimelineEntry[] | undefined, entry: TimelineEntry): TimelineEntry[] {
  const list = entries ? [...entries, entry] : [entry];
  return list.length > TIMELINE_CAP ? list.slice(list.length - TIMELINE_CAP) : list;
}

function entryText(ev: CanonicalEvent): string | undefined {
  const v = ev.data?.text;
  return typeof v === 'string' ? v : undefined;
}

function entryRole(ev: CanonicalEvent): string | undefined {
  const v = ev.data?.role;
  return typeof v === 'string' ? v : undefined;
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
  return [...merged, ...noSeq].slice(-TIMELINE_CAP);
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
          [runId]: appendTimeline(s.timelines[runId], {
            event_id: ev.event_id,
            stream_seq: ev.stream_seq,
            run_seq: ev.run_seq,
            type: ev.type,
            occurred_at: ev.occurred_at,
            role: entryRole(ev),
            text: entryText(ev),
            data: ev.data,
          }),
        },
      }));

      // 已加载的 run 快照做就地状态/进度更新；watched 但未知时拉取快照。
      const cached = get().runs[runId];
      const status = typeof ev.data?.status === 'string' ? (ev.data.status as ExecutionRun['status']) : undefined;
      const progress = typeof ev.data?.progress === 'number' ? ev.data.progress : undefined;
      if (cached && (status || progress !== undefined)) {
        set((s) => ({
          runs: {
            ...s.runs,
            [runId]: {
              ...cached,
              status: status ?? cached.status,
              progress: progress ?? cached.progress,
            },
          },
        }));
      } else if (!cached && get().watching[runId]) {
        void get().fetchRun(runId);
      }

      if (ev.type === 'run.status_changed' && status === 'waiting_approval' && get().watching[runId]) {
        void get().fetchApprovals(runId);
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

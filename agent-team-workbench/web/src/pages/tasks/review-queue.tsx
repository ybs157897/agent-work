import { ChevronDown, ChevronUp, Inbox, RefreshCw, ShieldCheck } from 'lucide-react';
import { useEffect } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import type { ReviewQueueItem } from '../../api/types';
import { PriorityBadge } from '../../components/priority-badge';
import { Button, Select } from '../../components/ui';
import { useReviewQueueStore, type ReviewQueuePhaseFilter } from '../../stores/review-queue.store';
import { coordinatorStatusText } from './coordinator-panel';

const PHASE_TEXT: Record<string, string> = {
  execution: '执行阶段',
  review: '评审中',
  acceptance: '待验收',
};

/**
 * Review Queue 顶部摘要（任务控制面 RFC §12.4）：
 * - badge 只显示服务端 total_count（唯一权威值），不显示当前页长度；
 * - 展开列表走 URL ?queue=review（view 参数继续只表示 kanban|list）；
 * - Row 点击/回车导航 /tasks/:taskId，复用任务详情全页。
 */

/** pending_since 距今的可读等待时长（服务端固定排序的第一行即全局最早等待）。 */
export function pendingDurationText(pendingSince: string, now: number = Date.now()): string {
  const start = new Date(pendingSince).getTime();
  if (Number.isNaN(start)) return '';
  const minutes = Math.max(0, Math.floor((now - start) / 60000));
  if (minutes < 1) return '刚刚进入';
  if (minutes < 60) return `等待 ${minutes} 分钟`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `等待 ${hours} 小时 ${minutes % 60} 分`;
  return `等待 ${Math.floor(hours / 24)} 天 ${hours % 24} 小时`;
}

export function ReviewQueueSummary() {
  const totalCount = useReviewQueueStore((s) => s.totalCount);
  const loaded = useReviewQueueStore((s) => s.loaded);
  const items = useReviewQueueStore((s) => s.items);
  const refresh = useReviewQueueStore((s) => s.refresh);
  const [searchParams, setSearchParams] = useSearchParams();

  // 页面挂载即拉一次（badge 常驻 /tasks 顶部；事件失效重取由 SSE 路由承担）。
  useEffect(() => {
    void refresh();
  }, [refresh]);

  return (
    <ReviewQueueSummaryView
      loaded={loaded}
      totalCount={totalCount}
      earliest={items[0]}
      queueOpen={searchParams.get('queue') === 'review'}
      onRefresh={() => void refresh()}
      onToggle={() => {
        const next = new URLSearchParams(searchParams);
        if (searchParams.get('queue') === 'review') next.delete('queue');
        else next.set('queue', 'review');
        setSearchParams(next, { replace: true });
      }}
    />
  );
}

/** 纯展示摘要行（props 驱动；容器负责 store/URL 接线）。 */
export function ReviewQueueSummaryView({
  loaded,
  totalCount,
  earliest,
  queueOpen,
  onRefresh,
  onToggle,
}: {
  loaded: boolean;
  totalCount: number;
  earliest?: ReviewQueueItem;
  queueOpen: boolean;
  onRefresh: () => void;
  onToggle: () => void;
}) {
  return (
    <section aria-label="复审队列" className="rounded-card border border-border-subtle bg-surface-raised shadow-level-1">
      <div className="flex flex-wrap items-center gap-snug px-base py-snug">
        <div className="flex items-center gap-snug">
          <ShieldCheck className="h-5 w-5 text-brand-primary" aria-hidden />
          <h3 className="font-display text-h3 text-text-primary">复审队列</h3>
          <span
            title="服务端权威计数：进入评审/待验收的任务总数"
            aria-label={`复审队列共 ${loaded ? totalCount : '…'} 个任务`}
            className="inline-flex min-w-7 items-center justify-center rounded-full border border-brand-primary/25 bg-brand-primary/10 px-2 py-0.5 text-caption font-medium text-brand-accent tabular-nums"
          >
            {loaded ? totalCount : '…'}
          </span>
        </div>
        {queueOpen && earliest && (
          <span className="text-caption text-text-tertiary" role="status">
            最早：{earliest.work_item.title} · {pendingDurationText(earliest.pending_since)}
          </span>
        )}
        <div className="ml-auto flex items-center gap-snug">
          <button
            type="button"
            onClick={onRefresh}
            title="刷新复审队列"
            aria-label="刷新复审队列"
            className="inline-flex h-8 w-8 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-surface-base hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
          >
            <RefreshCw className="h-4 w-4" aria-hidden />
          </button>
          <Button size="sm" variant="ghost" onClick={onToggle} aria-expanded={queueOpen}>
            <span className="inline-flex items-center gap-tight">
              {queueOpen ? <ChevronUp className="h-4 w-4" aria-hidden /> : <ChevronDown className="h-4 w-4" aria-hidden />}
              {queueOpen ? '收起队列' : '查看队列'}
            </span>
          </Button>
        </div>
      </div>
      {queueOpen && <ReviewQueueListEmbedded />}
    </section>
  );
}

/** Summary 内嵌的列表挂载点：真实实现由容器注入（避免容器↔视图循环引用）。 */
function ReviewQueueListEmbedded() {
  return <ReviewQueueList />;
}

/** 队列列表容器：store 接线 + 行导航。 */
export function ReviewQueueList() {
  const items = useReviewQueueStore((s) => s.items);
  const loaded = useReviewQueueStore((s) => s.loaded);
  const loading = useReviewQueueStore((s) => s.loading);
  const error = useReviewQueueStore((s) => s.error);
  const nextCursor = useReviewQueueStore((s) => s.nextCursor);
  const phaseFilter = useReviewQueueStore((s) => s.phaseFilter);
  const setPhaseFilter = useReviewQueueStore((s) => s.setPhaseFilter);
  const refresh = useReviewQueueStore((s) => s.refresh);
  const loadMore = useReviewQueueStore((s) => s.loadMore);
  const navigate = useNavigate();

  return (
    <ReviewQueueListView
      items={items}
      loaded={loaded}
      loading={loading}
      error={error}
      nextCursor={nextCursor}
      phaseFilter={phaseFilter}
      onPhaseFilter={(phase) => setPhaseFilter(phase)}
      onRefresh={() => void refresh()}
      onLoadMore={() => void loadMore()}
      onOpen={(taskId) => navigate(`/tasks/${taskId}`)}
    />
  );
}

/** 纯展示队列列表（props 驱动）。 */
export function ReviewQueueListView({
  items,
  loaded,
  loading,
  error,
  nextCursor,
  phaseFilter,
  onPhaseFilter,
  onRefresh,
  onLoadMore,
  onOpen,
}: {
  items: ReviewQueueItem[];
  loaded: boolean;
  loading: boolean;
  error: string | null;
  nextCursor: string | null;
  phaseFilter: ReviewQueuePhaseFilter;
  onPhaseFilter: (phase: ReviewQueuePhaseFilter) => void;
  onRefresh: () => void;
  onLoadMore: () => void;
  onOpen: (taskId: string) => void;
}) {
  return (
    <div className="border-t border-border-subtle px-base py-snug">
      <div className="mb-snug flex items-center justify-between gap-snug">
        <p className="text-caption text-text-tertiary">
          仅统计进行中且进入评审/待验收的任务；排序按等待时间
        </p>
        <label className="flex items-center gap-tight text-caption text-text-secondary">
          阶段
          <Select
            aria-label="按阶段过滤复审队列"
            value={phaseFilter}
            onChange={(e) => onPhaseFilter(e.target.value as ReviewQueuePhaseFilter)}
            wrapperClassName="mt-0"
            className="w-auto"
          >
            <option value="">全部</option>
            <option value="review">评审中</option>
            <option value="acceptance">待验收</option>
          </Select>
        </label>
      </div>

      {error && (
        <div role="alert" className="mb-snug flex items-center justify-between gap-snug rounded-button border border-status-error/20 bg-status-error/5 px-snug py-tight">
          <p className="text-caption text-status-error">{error}</p>
          <Button size="sm" variant="danger-outline" onClick={onRefresh}>
            重试
          </Button>
        </div>
      )}

      {!loaded && !error ? (
        <p className="py-tight text-caption text-text-tertiary" role="status">
          复审队列加载中…
        </p>
      ) : items.length === 0 && !error ? (
        <div className="py-comfortable text-center">
          <Inbox className="mx-auto h-6 w-6 text-text-tertiary" aria-hidden />
          <p className="mt-1 text-body text-text-secondary">没有等待复审的任务</p>
          <p className="text-caption text-text-tertiary">任务进入评审或待验收阶段后会出现在这里</p>
        </div>
      ) : (
        <ul className="space-y-tight">
          {items.map((item) => (
            <ReviewQueueRow key={item.work_item.id} item={item} onOpen={() => onOpen(item.work_item.id)} />
          ))}
        </ul>
      )}

      {nextCursor && (
        <div className="mt-snug flex justify-center">
          <Button size="sm" variant="secondary" onClick={onLoadMore} disabled={loading}>
            {loading ? '加载中…' : '加载更多'}
          </Button>
        </div>
      )}
    </div>
  );
}

function ReviewQueueRow({ item, onOpen }: { item: ReviewQueueItem; onOpen: () => void }) {
  const wi = item.work_item;
  return (
    <li>
      <button
        type="button"
        onClick={onOpen}
        aria-label={`打开任务 ${wi.title}`}
        className="flex w-full cursor-pointer items-center justify-between gap-snug rounded-button border border-border-subtle bg-surface-base px-snug py-tight text-left transition-colors hover:bg-surface-raised focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
      >
        <div className="flex min-w-0 items-center gap-snug">
          <PriorityBadge priority={wi.priority} />
          <span className="truncate text-body font-medium text-text-primary">{wi.title}</span>
        </div>
        <div className="flex shrink-0 items-center gap-base text-caption text-text-secondary">
          <span className="rounded-sm border border-brand-primary/20 bg-brand-primary/10 px-1.5 py-0.5 font-medium text-brand-accent">
            {PHASE_TEXT[wi.phase ?? ''] ?? wi.phase}
          </span>
          {item.coordinator && (
            <span title={`Coordinator：${coordinatorStatusText(item.coordinator.status)}`}>
              {coordinatorStatusText(item.coordinator.status)}
            </span>
          )}
          <span className="tabular-nums text-text-tertiary">{pendingDurationText(item.pending_since)}</span>
        </div>
      </button>
    </li>
  );
}

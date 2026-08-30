import { SearchX } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import type { SearchItem, SearchKind } from '../../api/types';
import { EmptyState } from '../../components/ui';
import { useTaskSearchStore } from '../../stores/task-search.store';
import { parseSnippet } from '../../utils/search-snippet';

const KIND_TEXT: Record<SearchKind, string> = {
  segment_summary: '片段摘要',
  decision: '决策',
  artifact: '产物',
};

const KIND_ORDER: readonly SearchKind[] = ['segment_summary', 'decision', 'artifact'];

/**
 * tasks 页 Header 的 FTS 检索入口（会话元模型 S4）：
 * 输入防抖调后端 search API；结果面板按 kind 分组，点击打开对应 TaskDetail。
 */
export function TaskSearch({ onOpenTask }: { onOpenTask: (workItemId: string) => void }) {
  const query = useTaskSearchStore((s) => s.query);
  const phase = useTaskSearchStore((s) => s.phase);
  const items = useTaskSearchStore((s) => s.items);
  const source = useTaskSearchStore((s) => s.source);
  const setQuery = useTaskSearchStore((s) => s.setQuery);
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  // 面板开启时的外点关闭与 Escape（与 PromptBox Library 弹层同款节奏）。
  useEffect(() => {
    if (!open) return;
    const closeOnOutside = (event: MouseEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
    };
    const closeOnEscape = (event: globalThis.KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false);
    };
    document.addEventListener('mousedown', closeOnOutside);
    document.addEventListener('keydown', closeOnEscape);
    return () => {
      document.removeEventListener('mousedown', closeOnOutside);
      document.removeEventListener('keydown', closeOnEscape);
    };
  }, [open]);

  const openResult = (workItemId: string) => {
    setOpen(false);
    onOpenTask(workItemId);
  };

  return (
    <div ref={rootRef} className="relative">
      <input
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        onFocus={() => setOpen(true)}
        placeholder="检索任务、决策、产物"
        aria-label="检索任务台账"
        className="w-56 rounded-input border border-border-strong bg-surface-raised px-snug py-tight text-body text-text-primary outline-none transition-colors placeholder:text-text-tertiary focus:border-brand-primary/40 focus:ring-2 focus:ring-brand-primary/20"
      />
      {open && query.trim() !== '' && phase !== 'idle' && (
        <TaskSearchPanel phase={phase === 'loading' ? 'loading' : 'done'} items={items} source={source} onOpenTask={openResult} />
      )}
    </div>
  );
}

/** 结果列表：kind 固定顺序分组 + 徽标；snippet 的 [] 命中段做视觉强调不外露方括号。 */
export function SearchResults({
  items,
  onOpenTask,
}: {
  items: readonly SearchItem[];
  onOpenTask: (workItemId: string) => void;
}) {
  return (
    <ul className="max-h-96 overflow-y-auto py-micro" aria-label="检索命中列表">
      {KIND_ORDER.map((kind) => {
        const group = items.filter((i) => i.kind === kind);
        if (group.length === 0) return null;
        return (
          <li key={kind}>
            <p className="px-snug pb-micro pt-tight text-caption font-medium uppercase tracking-widest text-text-tertiary">
              {KIND_TEXT[kind]}
              <span className="ml-1 tabular-nums">{group.length}</span>
            </p>
            <ul>
              {group.map((item) => (
                <li key={`${kind}:${item.source_id}`}>
                  <button
                    type="button"
                    data-work-item-id={item.work_item_id}
                    aria-label={`打开任务 ${item.title}`}
                    title={item.work_item_id}
                    onClick={() => onOpenTask(item.work_item_id)}
                    className="block w-full px-snug py-tight text-left transition-colors hover:bg-surface-base focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-brand-primary/40"
                  >
                    <span className="block truncate text-body text-text-primary">{item.title}</span>
                    {item.snippet && (
                      <span className="mt-0.5 block truncate text-caption text-text-tertiary">
                        <SnippetText text={item.snippet} />
                      </span>
                    )}
                  </button>
                </li>
              ))}
            </ul>
          </li>
        );
      })}
    </ul>
  );
}

/** 结果面板：三态（loading / 空命中 / 分组结果）+ 本地回退标注；由 TaskSearch 组合。 */
export function TaskSearchPanel({
  phase,
  items,
  source,
  onOpenTask,
}: {
  phase: 'loading' | 'done';
  items: readonly SearchItem[];
  source: 'remote' | 'local' | null;
  onOpenTask: (workItemId: string) => void;
}) {
  return (
    <div
      role="dialog"
      aria-label="检索结果"
      className="absolute right-0 top-full z-40 mt-2 w-[26rem] overflow-hidden rounded-card border border-border-subtle bg-surface-raised shadow-level-3"
    >
      {phase === 'loading' ? (
        <p role="status" className="px-snug py-tight text-caption text-text-tertiary">
          检索中…
        </p>
      ) : items.length === 0 ? (
        <EmptyState
          icon={<SearchX className="h-5 w-5" aria-hidden />}
          title="无检索结果"
          description="换个关键词试试；检索覆盖片段摘要、决策台账与产物标题。"
          className="py-loose"
        />
      ) : (
        <SearchResults items={items} onOpenTask={onOpenTask} />
      )}
      {source === 'local' && (
        <p className="border-t border-border-subtle bg-surface-sunken/55 px-snug py-micro text-caption text-text-tertiary">
          检索服务暂不可用，以上为本地标题匹配
        </p>
      )}
    </div>
  );
}

/** snippet 渲染：[命中词] → 强调底色段；`…` 截断符随普通文本保留。 */
export function SnippetText({ text }: { text: string }) {
  return (
    <>
      {parseSnippet(text).map((part, index) =>
        part.hit ? (
          <mark key={index} className="rounded-sm bg-brand-primary/10 px-0.5 text-brand-accent">
            {part.text}
          </mark>
        ) : (
          <span key={index}>{part.text}</span>
        ),
      )}
    </>
  );
}

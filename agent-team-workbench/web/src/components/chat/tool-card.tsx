import { ChevronDown, ChevronRight, FilePen, FileText, Plug, Search, Terminal, Wrench, type LucideIcon } from 'lucide-react';
import { createElement, useState } from 'react';
import { toolDuration, type ChatMessage, type ToolStatus } from '../../stores/chat.store';
import { DiffCard, looksLikeUnifiedDiff, stripControlChars } from './diff-card';

/**
 * 工具名 → 类别图标（宽松子串匹配，覆盖各 runtime 的命名习惯：Bash/Read/Write/Grep/
 * mcp__server__tool 等；未知落 Wrench）。纯函数：同输入同输出。
 */
export function toolIcon(tool?: string): LucideIcon {
  const t = (tool ?? '').toLowerCase();
  if (/(shell|bash|zsh|exec|terminal|command)/.test(t)) return Terminal;
  if (/(^|[^a-z])(read|view|cat)([^a-z]|$)/.test(t)) return FileText;
  if (/(write|edit|patch|save)/.test(t)) return FilePen;
  if (/(search|grep|glob|find|query|list)/.test(t)) return Search;
  if (t.includes('mcp')) return Plug;
  return Wrench;
}

/** 活动段：同 run 连续工具行折叠为 activity 组，其余消息原样透传（保持时间线顺序）。 */
export type ActivitySegment =
  | { kind: 'activity'; runId: string; items: ChatMessage[] }
  | { kind: 'single'; item: ChatMessage };

/**
 * 把消息流切段：连续且同 run 的工具行（toolStatus 已定义）合成一个活动组；
 * assistant/error(system)/user 等消息切段边界——工具跨轮次分属各自的组。
 */
export function groupActivity(messages: ChatMessage[]): ActivitySegment[] {
  const segments: ActivitySegment[] = [];
  let buffer: ChatMessage[] = [];
  const flush = () => {
    if (buffer.length) {
      segments.push({ kind: 'activity', runId: buffer[0].runId, items: buffer });
      buffer = [];
    }
  };
  for (const m of messages) {
    if (m.toolStatus !== undefined) {
      if (buffer.length && buffer[0].runId !== m.runId) flush();
      buffer.push(m);
      continue;
    }
    flush();
    segments.push({ kind: 'single', item: m });
  }
  flush();
  return segments;
}

/** 组级 worked-for：组内最早 started 到最晚 completed（occurred_at 为 RFC3339 UTC，字符串可比）。 */
export function groupWorkedFor(items: ChatMessage[]): string | null {
  const starts = items.flatMap((m) => (m.startedAt ? [m.startedAt] : []));
  const ends = items.flatMap((m) => (m.completedAt ? [m.completedAt] : []));
  if (!starts.length || !ends.length) return null;
  const earliest = starts.reduce((a, b) => (a <= b ? a : b));
  const latest = ends.reduce((a, b) => (a >= b ? a : b));
  return toolDuration(earliest, latest);
}

function toolStatusClass(status: ToolStatus | undefined): string {
  if (status === 'failed') return 'text-status-error';
  if (status === 'running') return 'text-text-secondary';
  return 'text-text-tertiary'; // 成功后弱化
}

/**
 * 同 run 工具行的可折叠「活动」组：组头 worked-for 计时；有进行中工具时强制展开
 * （完成后可由用户折叠）。collapsed 是本地 state——刷新回默认展开。
 */
export function ActivityGroup({ items }: { items: ChatMessage[] }) {
  const [collapsed, setCollapsed] = useState(false);
  const running = items.some((m) => m.toolStatus === 'running');
  const open = !collapsed || running;
  const workedFor = groupWorkedFor(items);
  return (
    <div className="chat-activity">
      <button className="chat-activity-head" onClick={() => setCollapsed((v) => !v)}>
        {open ? <ChevronDown className="h-3.5 w-3.5 shrink-0" /> : <ChevronRight className="h-3.5 w-3.5 shrink-0" />}
        <span className="font-medium">活动</span>
        <span className="text-text-tertiary">{items.length} 次调用</span>
        {running && <span className="text-status-info">进行中</span>}
        {workedFor && <span className="ml-auto tabular-nums text-text-tertiary">{workedFor}</span>}
      </button>
      {open && (
        <div className="chat-activity-body">
          {items.map((m) => (
            <ToolRow key={m.key} msg={m} />
          ))}
        </div>
      )}
    </div>
  );
}

/**
 * 单次工具调用的行卡片：类别图标 + 摘要 + 状态色 + 耗时徽章；点击行展开输出。
 * 失败行默认展开（错误要响亮），成功行默认收起。
 */
export function ToolRow({ msg }: { msg: ChatMessage }) {
  const [open, setOpen] = useState(msg.toolStatus === 'failed');
  const duration = toolDuration(msg.startedAt, msg.completedAt);
  // diff 输出走专用卡（刀2）；等宽 pre 仍是其余输出的默认形态。
  const detail = msg.detail;
  const isDiff = detail !== undefined && looksLikeUnifiedDiff(stripControlChars(detail));
  const hasDetail = detail !== undefined;
  // createElement 渲染图标引用：避免本地变量承接组件触发 static-components 规则。
  const icon = (className: string) => createElement(toolIcon(msg.tool), { className });
  return (
    <div className="min-w-0">
      <button
        className={`chat-tool-row ${toolStatusClass(msg.toolStatus)}`}
        onClick={() => hasDetail && setOpen((v) => !v)}
        aria-expanded={hasDetail ? open : undefined}
      >
        {icon(msg.toolStatus === 'running' ? 'h-3.5 w-3.5 shrink-0 animate-pulse' : 'h-3.5 w-3.5 shrink-0')}
        <span className="min-w-0 flex-1 truncate">{msg.text}</span>
        {duration && (
          <span className="shrink-0 rounded bg-surface-sunken px-1 tabular-nums text-[11px] text-text-tertiary">
            {duration}
          </span>
        )}
      </button>
      {open &&
        (isDiff ? (
          <div className="mx-1 mt-0.5 mb-1">
            <DiffCard text={msg.detail} />
          </div>
        ) : (
          <pre className="mx-1 mt-0.5 mb-1 max-h-48 overflow-y-auto whitespace-pre-wrap break-words rounded-md bg-surface-base px-3 py-2 text-left font-mono text-[11px] leading-4 text-text-secondary">
            {msg.detail}
          </pre>
        ))}
    </div>
  );
}

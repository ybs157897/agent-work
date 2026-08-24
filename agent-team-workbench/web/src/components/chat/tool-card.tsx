import { ChevronDown, ChevronRight, FilePen, FileText, Plug, Search, SquareCode, Terminal, Wrench, type LucideIcon } from 'lucide-react';
import { createElement, useState, type ReactNode } from 'react';
import { toolDuration, type ChatMessage } from '../../stores/chat.store';
import { activityWorkedFor } from '../../utils/activity-worked-for';
import type { ReasoningSlot } from '../../utils/transcript-layout';
import { ReasoningProcessPanel } from './reasoning-activity-row';
import { DiffCard, looksLikeUnifiedDiff, stripControlChars } from './diff-card';
import { DisclosureRow } from './blocks/DisclosureRow';
import { StateDot } from './blocks/StateDot';
import { TerminalBlock } from './blocks/TerminalBlock';
import { ReadBlock } from './blocks/ReadBlock';
import { SearchBlock } from './blocks/SearchBlock';
import { cx } from './blocks/cx';
import { toolRowModel, classifyTool, type ToolRowModel, type ToolRowState } from './tool-model';
import { readBlockFromOutput } from './read-model';
import { parseGrepOutput, parsePathList } from './search-parse';
import css from './tool-row.module.css';

/**
 * 工具名 → 类别图标：mcp 优先特判，其余走 tool-model 的族分类
 * （bash→Terminal、read→FileText、write/edit→FilePen、search→Search、code→SquareCode、未知→Wrench）。
 */
export function toolIcon(tool?: string): LucideIcon {
  const t = (tool ?? '').toLowerCase();
  if (t.includes('mcp')) return Plug;
  switch (classifyTool(tool)) {
    case 'bash':
      return Terminal;
    case 'read':
      return FileText;
    case 'write':
    case 'edit':
      return FilePen;
    case 'search':
      return Search;
    case 'code':
      return SquareCode;
    default:
      return Wrench;
  }
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

/**
 * 同 run 工具行的可折叠「活动」组：组头 worked-for 计时；有进行中工具时强制展开
 * （完成后可由用户折叠）。collapsed 是本地 state——刷新回默认展开。
 * stoppedRuns：已终态但未成功（中断/取消/丢失/失败）的 run 集，组内仍 running 的行按中断态展示。
 */
export function ActivityGroup({
  items,
  reasoning,
  assistantAt,
  stoppedRuns,
  defaultCollapsed = false,
  suppressDiff = false,
}: {
  items: ChatMessage[];
  reasoning?: ReasoningSlot;
  /** assistant 落定时间，用于组级 worked-for 上界 */
  assistantAt?: string;
  stoppedRuns?: ReadonlySet<string>;
  /** run 终态且无 running 工具时默认折叠 */
  defaultCollapsed?: boolean;
  /** turn 级 diff 汇总存在时隐藏工具行内 diff */
  suppressDiff?: boolean;
}) {
  const [collapsed, setCollapsed] = useState(defaultCollapsed);
  const running =
    Boolean(reasoning?.streaming) ||
    items.some((m) => m.toolStatus === 'running' && !stoppedRuns?.has(m.runId));
  const open = !collapsed || running;
  const workedFor = activityWorkedFor(items, reasoning, assistantAt);

  const headLabel = running
    ? '工作中…'
    : workedFor
      ? `已工作 ${workedFor}`
      : '已完成';

  return (
    <div className="chat-activity">
      <button type="button" className="chat-activity-head" onClick={() => setCollapsed((v) => !v)} aria-expanded={open}>
        {open ? <ChevronDown className="h-3.5 w-3.5 shrink-0" /> : <ChevronRight className="h-3.5 w-3.5 shrink-0" />}
        <span className="min-w-0 flex-1 truncate font-medium text-text-secondary">{headLabel}</span>
      </button>
      {open && (
        <div className="chat-activity-body">
          {reasoning && (
            <ReasoningProcessPanel
              key={reasoning.key}
              panelKey={reasoning.key}
              text={reasoning.text}
              streaming={reasoning.streaming}
            />
          )}
          {items.map((m) => (
            <ToolRow
              key={m.key}
              msg={m}
              stopped={stoppedRuns?.has(m.runId) && m.toolStatus === 'running'}
              suppressDiff={suppressDiff}
            />
          ))}
        </div>
      )}
    </div>
  );
}

/** 展开体卡片分派：按工具族 + 输出形态选终端/diff/read/search 卡，均不适用时落 IN/OUT 通用卡。 */
function ExpandedBody({
  model,
  state,
  suppressDiff = false,
}: {
  model: ToolRowModel;
  state: ToolRowState;
  suppressDiff?: boolean;
}) {
  const output = model.output;
  switch (model.family) {
    case 'bash':
      return (
        <TerminalBlock
          className={css.cardBody}
          command={model.summary}
          output={output ?? undefined}
          exitCode={model.exitCode}
          running={state === 'running'}
        />
      );
    case 'write':
    case 'edit': {
      if (suppressDiff) break;
      const detail = output !== null ? stripControlChars(output) : '';
      if (detail !== '' && looksLikeUnifiedDiff(detail)) {
        return (
          <div className={css.cardBody}>
            <DiffCard text={output ?? ''} />
          </div>
        );
      }
      break;
    }
    case 'read': {
      if (state !== 'running' && output !== null) {
        const read = readBlockFromOutput(output, model.filePath);
        if (read !== null) return <ReadBlock {...read} className={css.cardBody} />;
      }
      break;
    }
    case 'search': {
      if (output !== null) {
        const grep = parseGrepOutput(output);
        if (grep !== null) {
          return (
            <SearchBlock
              kind="matches"
              className={css.cardBody}
              files={grep.files}
              truncated={grep.truncated}
              total={grep.files.reduce((n, f) => n + f.matches.length, 0)}
            />
          );
        }
        const paths = parsePathList(output);
        if (paths !== null) {
          return (
            <SearchBlock
              kind="paths"
              className={css.cardBody}
              paths={paths}
              truncated={output.length >= 1900}
              total={paths.length}
            />
          );
        }
      }
      break;
    }
    default:
      break;
  }
  // 通用 IN/OUT 卡：单文件工具不暴露 IN（路径已是摘要；对齐 DSH single-file 取舍）。
  const singleFile = model.filePath !== undefined;
  const body = singleFile ? null : model.body;
  if (body === null && output === null) return null;
  return (
    <div className={cx(css.ioCard, css.cardBody)}>
      {body !== null && (
        <div className={css.ioSection}>
          <span className={css.ioLabel}>IN</span>
          <span className={css.ioText}>{body}</span>
        </div>
      )}
      {body !== null && output !== null && <span className={css.ioDivider} aria-hidden />}
      {output !== null && (
        <div className={css.ioSection}>
          <span className={css.ioLabel}>OUT</span>
          <span className={css.ioText} data-error={state === 'error' || undefined}>
            {output}
          </span>
        </div>
      )}
    </div>
  );
}

/** 运行态的无障碍文本：扫光与状态点是纯视觉，AT 需要文字承载状态。 */
function stateStatus(state: ToolRowState): string | null {
  switch (state) {
    case 'running':
      return '进行中';
    case 'error':
      return '失败';
    case 'stopped':
      return '已中断';
    default:
      return null;
  }
}

/** 前导槽：error/stopped 换成状态点（红/琥珀），running 保持图标（行扫光承载进行中信号）。 */
function leadingFor(state: ToolRowState, icon: ReactNode): ReactNode {
  switch (state) {
    case 'error':
      return <StateDot state="error" />;
    case 'stopped':
      return <StateDot state="warning" />;
    default:
      return icon;
  }
}

/**
 * 单次工具调用的摘要行（DSH ToolRow chrome）：16px 前导槽（图标/状态点，悬停换
 * chevron 预览）+ 族标题 + 圆点 + 截断摘要 + 耗时徽章；整行点击展开（Enter/Space
 * 同效），展开体按族分派终端/diff/read/search/IN-OUT 卡。错误行折叠摘要直接是
 * 失败首行红字；中断 run 的挂起行按 stopped（琥珀点、无扫光）展示。
 */
export function ToolRow({
  msg,
  stopped = false,
  suppressDiff = false,
}: {
  msg: ChatMessage;
  stopped?: boolean;
  suppressDiff?: boolean;
}) {
  const model = toolRowModel(msg);
  const state: ToolRowState = stopped && model.state === 'running' ? 'stopped' : model.state;
  const [expanded, setExpanded] = useState(false);
  const duration = toolDuration(msg.startedAt, msg.completedAt);
  // createElement 渲染图标引用：避免本地变量承接组件触发 static-components 规则。
  const icon = createElement(toolIcon(msg.tool), { className: 'h-3.5 w-3.5' });
  // 卡片存在性判定与 ExpandedBody 的分派保持一致（行能否展开取决于有无任何展开体）。
  const singleFile = model.filePath !== undefined;
  const hasIoBody = (!singleFile && model.body !== null) || model.output !== null;
  const hasCard =
    model.family === 'bash' ||
    (!suppressDiff &&
      (model.family === 'write' || model.family === 'edit') &&
      model.output !== null &&
      looksLikeUnifiedDiff(stripControlChars(model.output))) ||
    (model.family === 'read' && state !== 'running' && model.output !== null) ||
    (model.family === 'search' && model.output !== null);
  const expandable = hasCard || hasIoBody;
  const open = expanded && expandable;
  const failureLine = state === 'error' ? model.errorSummary : null;
  const summaryText = failureLine ?? model.summary;
  const status = stateStatus(state);

  return (
    <div className={css.root} data-tool={msg.tool} data-state={state}>
      {status !== null && <span className={css.visuallyHidden}>{status}</span>}
      <DisclosureRow
        rowClassName={css.row}
        leadingClassName={css.leading}
        titleClassName={css.title}
        chevronClassName={css.chevron}
        icon={leadingFor(state, icon)}
        title={model.title}
        open={open}
        expandable={expandable}
        expandOnRowClick
        keepContentWhenOpen
        onToggle={() => setExpanded((v) => !v)}
        collapsedContent={
          summaryText !== '' || duration !== null ? (
            <>
              {summaryText !== '' && <span className={css.sep} aria-hidden />}
              {summaryText !== '' && (
                <span className={cx(css.summary, failureLine !== null && css.errorSummary)}>{summaryText}</span>
              )}
              {duration !== null && <span className={css.summarySuffix}>{duration}</span>}
            </>
          ) : undefined
        }
      >
        <div className={css.bodyWrap}>
          <ExpandedBody model={model} state={state} suppressDiff={suppressDiff} />
        </div>
      </DisclosureRow>
    </div>
  );
}

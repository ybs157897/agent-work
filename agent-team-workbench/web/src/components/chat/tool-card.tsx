import { Check, CircleStop, CircleX, ChevronDown, ChevronRight, FilePen, FileText, LoaderCircle, Plug, Search, SquareCode, Terminal, Wrench, type LucideIcon } from 'lucide-react';
import { createElement, useEffect, useId, useRef, useState, type ReactNode } from 'react';
import { AnimatePresence, motion, useReducedMotion } from 'motion/react';
import { toolDuration, type ChatMessage } from '../../stores/chat.store';
import { DiffCard, looksLikeUnifiedDiff, stripControlChars } from './diff-card';
import { TerminalBlock } from './blocks/TerminalBlock';
import { ReadBlock } from './blocks/ReadBlock';
import { SearchBlock } from './blocks/SearchBlock';
import { cx } from './blocks/cx';
import { FAMILY_TITLES, toolRowModel, toolRowTitleForState, classifyTool, type ToolFamily, type ToolRowModel, type ToolRowState } from './tool-model';
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

/** ToolCallStrip 标题：工具名转为可扫描的人类标题，缺失时回退到工具族标题。 */
export function humanizeToolName(tool: string | undefined, family?: ToolFamily): string {
  const raw = tool?.trim();
  if (!raw) return FAMILY_TITLES[family ?? 'others'];
  return raw
    .split(/[_-]+/)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1).toLowerCase())
    .join(' ');
}

/** 活动段：同 run 连续工具行折叠为 activity 组，其余消息原样透传（保持时间线顺序）。 */
export type ActivitySegment =
  | { kind: 'activity'; runId: string; items: ChatMessage[] }
  | { kind: 'single'; item: ChatMessage };

type ActivityKind = 'explore' | 'execute' | 'changes' | 'cua' | 'agent' | 'tool';

export interface GroupActivityOptions {
  /** ZCode 默认将探索类连续调用合并为一个 Explore 组。 */
  groupExplore?: boolean;
  /** ZCode 默认将终端/代码类连续调用合并为一个 Execute 组。 */
  groupExecute?: boolean;
  /** ZCode 默认不合并变更类调用，保持单文件操作可追溯。 */
  groupChanges?: boolean;
}

function activityKind(item: ChatMessage): ActivityKind {
  const name = (item.tool ?? '').toLowerCase();
  if (/(^|[_-])(computer(?:_use)?|cua|browser|playwright|screenshot|click|type)([_-]|$)/.test(name)) return 'cua';
  if (/(^|[_-])(agent|task|subagent)([_-]|$)/.test(name)) return 'agent';
  switch (toolRowModel(item).family) {
    case 'read':
    case 'search':
      return 'explore';
    case 'bash':
    case 'code':
      return 'execute';
    case 'write':
    case 'edit':
      return 'changes';
    default:
      return 'tool';
  }
}

const ACTIVITY_KIND_TITLES: Record<ActivityKind, string> = {
  explore: '探索',
  execute: '执行',
  changes: '变更',
  cua: '电脑操作',
  agent: 'Agent',
  tool: '工具',
};

/**
 * 把消息流切段：连续且同 run 的工具行（toolStatus 已定义）合成一个活动组；
 * assistant/error(system)/user 等消息切段边界——工具跨轮次分属各自的组。
 */
export function groupActivity(messages: ChatMessage[], options: GroupActivityOptions = {}): ActivitySegment[] {
  const segments: ActivitySegment[] = [];
  let buffer: ChatMessage[] = [];
  let bufferKind: ActivityKind | undefined;
  const groupExplore = options.groupExplore ?? true;
  const groupExecute = options.groupExecute ?? true;
  const groupChanges = options.groupChanges ?? false;
  const flush = () => {
    if (buffer.length) {
      segments.push({ kind: 'activity', runId: buffer[0].runId, items: buffer });
      buffer = [];
    }
    bufferKind = undefined;
  };
  for (const m of messages) {
    if (m.toolStatus !== undefined) {
      const kind = activityKind(m);
      const shouldGroup = kind === 'explore'
        ? groupExplore
        : kind === 'execute'
          ? groupExecute
          : kind === 'changes'
            ? groupChanges
            : true;
      if (buffer.length && (buffer[0].runId !== m.runId || bufferKind !== kind || !shouldGroup)) flush();
      buffer.push(m);
      bufferKind = kind;
      continue;
    }
    flush();
    segments.push({ kind: 'single', item: m });
  }
  flush();
  return segments;
}

/** ToolCallStrip 的稳定语义模型，供渲染和纯函数测试共享。 */
export function toolChipModel(msg: ChatMessage, stopped = false) {
  const model = toolRowModel(msg);
  const state: ToolRowState = stopped && model.state === 'running' ? 'stopped' : model.state;
  return {
    title: humanizeToolName(msg.tool, model.family),
    summary: toolRowTitleForState(model, state),
    state,
    duration: toolDuration(msg.startedAt, msg.completedAt),
  } as const;
}

export type ActivityGroupState = ToolRowState | 'empty';

export function shouldAutoCollapseActivity(
  previous: ActivityGroupState | null,
  current: ActivityGroupState,
): boolean {
  return previous === 'running' && current !== 'running';
}

export interface ActivityGroupModel {
  total: number;
  completed: number;
  running: number;
  failed: number;
  stopped: number;
  state: ActivityGroupState;
  summary: string;
  status: string;
  toolSummary: string;
}

/** 同一 run 工具组的可见汇总；只消费真实工具消息，不从正文推断。 */
export function activityGroupModel(
  items: ChatMessage[],
  stoppedRuns?: ReadonlySet<string>,
): ActivityGroupModel {
  let completed = 0;
  let running = 0;
  let failed = 0;
  let stopped = 0;
  for (const item of items) {
    const visuallyStopped = stoppedRuns?.has(item.runId) && item.toolStatus === 'running';
    const state = toolChipModel(item, visuallyStopped).state;
    if (state === 'ok') completed += 1;
    else if (state === 'running') running += 1;
    else if (state === 'error') failed += 1;
    else stopped += 1;
  }
  const total = items.length;
  const state: ActivityGroupState = total === 0
    ? 'empty'
    : running > 0
      ? 'running'
      : failed > 0
        ? 'error'
        : stopped > 0
          ? 'stopped'
          : 'ok';
  const summary = total === 0
    ? '暂无工具调用'
    : [
        completed ? `${completed} 完成` : '',
        running ? `${running} 进行中` : '',
        failed ? `${failed} 失败` : '',
        stopped ? `${stopped} 中断` : '',
      ].filter(Boolean).join(' · ');
  const status = state === 'running'
    ? '正在执行'
    : state === 'error'
      ? '执行失败'
      : state === 'stopped'
        ? '执行中断'
        : state === 'ok'
          ? '执行完成'
          : '等待调用';
  const familyCounts = new Map<ToolFamily, number>();
  for (const item of items) {
    const family = toolRowModel(item).family;
    familyCounts.set(family, (familyCounts.get(family) ?? 0) + 1);
  }
  const toolSummary = [...familyCounts.entries()]
    .map(([family, count]) => `${FAMILY_TITLES[family]} × ${count}`)
    .join(' · ');
  return { total, completed, running, failed, stopped, state, summary, status, toolSummary };
}

function ActivityStatusIcon({ state }: { state: ActivityGroupState }) {
  if (state === 'running') return <LoaderCircle className={css.runningIcon} aria-hidden />;
  if (state === 'error') return <CircleX className={css.errorIcon} aria-hidden />;
  if (state === 'stopped') return <CircleStop className={css.stoppedIcon} aria-hidden />;
  if (state === 'ok') return <Check className={css.okIcon} aria-hidden />;
  return <Wrench aria-hidden />;
}

/**
 * 同一阶段的连续工具调用收敛成一条活动摘要；正文会切断批次。
 * 展开后按事件顺序纵向列出工具日志，单条详情就地渐进披露。
 */
export function ActivityGroup({
  items,
  stoppedRuns,
  defaultCollapsed = true,
  defaultSelectedKey,
  suppressDiff = false,
  variant = 'default',
}: {
  items: ChatMessage[];
  stoppedRuns?: ReadonlySet<string>;
  /** 工具组默认只展示组级汇总；仅演示或明确交互场景可显式展开。 */
  defaultCollapsed?: boolean;
  /** Demo/验收可指定首个展开项；生产默认不抢占用户注意力。 */
  defaultSelectedKey?: string;
  /** turn 级 diff 汇总存在时隐藏工具行内 diff */
  suppressDiff?: boolean;
  /** 嵌入工作时间线时使用紧凑的命令行视觉。 */
  variant?: 'default' | 'timeline';
}) {
  const headingId = useId();
  const bodyId = useId();
  const [collapsed, setCollapsed] = useState(defaultCollapsed);
  const previousState = useRef<ActivityGroupState | null>(null);
  const [selectedKey, setSelectedKey] = useState<string | null>(() =>
    defaultSelectedKey && items.some((item) => item.key === defaultSelectedKey)
      ? defaultSelectedKey
      : null,
  );
  const reduceMotion = useReducedMotion();
  const latestSummaryRef = useRef<HTMLSpanElement>(null);
  const group = activityGroupModel(items, stoppedRuns);
  const latestItem = items[items.length - 1];
  const latestModel = latestItem ? toolRowModel(latestItem) : undefined;
  const latestChangeStats = latestModel?.changeStats;
  const latestSummary = latestChangeStats
    ? changeSummaryText(latestChangeStats)
    : latestItem
      ? toolChipModel(latestItem, Boolean(stoppedRuns?.has(latestItem.runId) && latestItem.toolStatus === 'running')).summary
      : '';

  useEffect(() => {
    const previous = previousState.current;
    if (shouldAutoCollapseActivity(previous, group.state)) setCollapsed(true);
    previousState.current = group.state;
  }, [group.state]);

  useEffect(() => {
    if (selectedKey && !items.some((item) => item.key === selectedKey)) setSelectedKey(null);
  }, [items, selectedKey]);

  useEffect(() => {
    if (variant !== 'timeline' || !collapsed || !latestSummaryRef.current) return;
    latestSummaryRef.current.scrollLeft = latestSummaryRef.current.scrollWidth;
  }, [collapsed, variant, latestItem?.key, latestSummary]);

  const kind = items.length > 0 ? activityKind(items[0]) : 'tool';
  const runningItem = items.find((item) => toolChipModel(
    item,
    Boolean(stoppedRuns?.has(item.runId) && item.toolStatus === 'running'),
  ).state === 'running');
  const currentAction = runningItem ? toolChipModel(runningItem).summary : undefined;

  return (
    <section
      className={`${css.activity} ${variant === 'timeline' ? css.timelineActivity : ''}`}
      data-state={group.state}
      data-variant={variant}
      aria-labelledby={headingId}
    >
      {items.length > 0 ? (
        <button
          type="button"
          className={css.activityHeader}
          onClick={() => setCollapsed((value) => !value)}
          aria-expanded={!collapsed}
          aria-controls={collapsed ? undefined : bodyId}
          aria-label={`${collapsed ? '展开' : '收起'}工具调用：共 ${group.total} 次；${group.toolSummary}；${variant === 'timeline' && latestSummary ? `${latestSummary}；` : ''}${group.summary}`}
        >
          <span className={css.activityMark} aria-hidden><Wrench /></span>
          <span className={css.activityHeading}>
            <span className={css.activityTitle}>
              <span className={css.activityLabel} id={headingId} role="heading" aria-level={3}>
                <span className="sr-only">工具调用</span>
              </span>
              <span className={css.activityKind}>{ACTIVITY_KIND_TITLES[kind]}</span>
              <span className={css.activityCount}>{group.total} 次调用</span>
            </span>
            <span className={css.activityToolSummary}>{group.toolSummary}</span>
            {variant === 'timeline' && latestSummary && !currentAction && (
              <span ref={latestSummaryRef} className={css.timelineLatestSummary} title={latestSummary}>
                {latestChangeStats ? <ChangeSummary stats={latestChangeStats} /> : latestSummary}
              </span>
            )}
            <span className={css.activitySummary}>{group.summary}</span>
            {currentAction && (
              <span className={css.activityTicker} aria-label={`当前动作：${currentAction}`}>
                <span className={css.runningSweep}>{currentAction}</span>
              </span>
            )}
          </span>
          <span
            className={css.groupStatus}
            role={group.state === 'running' ? 'status' : undefined}
            aria-atomic={group.state === 'running' ? 'true' : undefined}
          >
            <ActivityStatusIcon state={group.state} />
            <span>{group.status}</span>
          </span>
          <span className={css.collapseButton} aria-hidden>
            {collapsed ? <ChevronRight /> : <ChevronDown />}
          </span>
        </button>
      ) : (
        <div className={css.activityHeader}>
          <span className={css.activityMark} aria-hidden><Wrench /></span>
          <div className={css.activityHeading}>
            <h3 id={headingId} className="sr-only">工具调用</h3>
            <p>{group.summary}</p>
          </div>
          <span className={css.groupStatus}>
            <ActivityStatusIcon state={group.state} />
            <span>{group.status}</span>
          </span>
        </div>
      )}

      {items.length === 0 ? (
        <div className={css.emptyState}>当前回合没有工具调用</div>
      ) : !collapsed ? (
        <div id={bodyId} className={css.activityBody}>
          <div className={css.strip} role="list" aria-label="工具调用">
            {items.map((m, index) => {
              const stopped = Boolean(stoppedRuns?.has(m.runId) && m.toolStatus === 'running');
              const selected = selectedKey === m.key;
              const model = toolRowModel(m);
              const state = toolChipModel(m, stopped).state;
              const detailsId = `${bodyId}-tool-${index + 1}`;
              return (
                <div key={m.key} className={css.timelineItem} role="listitem">
                  <ToolRow
                    msg={m}
                    index={index + 1}
                    stopped={stopped}
                    selected={selected}
                    detailsId={detailsId}
                    onToggle={() => setSelectedKey((key) => key === m.key ? null : m.key)}
                  />
                  <AnimatePresence initial={false}>
                    {selected && (
                      <motion.div
                        id={detailsId}
                        className={css.details}
                        role="region"
                        aria-label={`${toolChipModel(m, stopped).title} 工具详情`}
                        initial={reduceMotion ? false : { opacity: 0, y: -4 }}
                        animate={{ opacity: 1, y: 0 }}
                        exit={reduceMotion ? { opacity: 0 } : { opacity: 0, y: -4 }}
                        transition={reduceMotion ? { duration: 0 } : { duration: 0.18, ease: [0.16, 1, 0.3, 1] }}
                      >
                        <ExpandedBody model={model} state={state} suppressDiff={suppressDiff} />
                      </motion.div>
                    )}
                  </AnimatePresence>
                </div>
              );
            })}
          </div>
        </div>
      ) : null}
    </section>
  );
}

/**
 * 工具族展开体渲染器注册表：族 → 专用渲染函数，返回 null 表示不适用、
 * 落通用 IN/OUT 卡。新增族的专用渲染只需在此加一条注册，分派逻辑不动。
 */
type FamilyBodyRenderer = (
  model: ToolRowModel,
  state: ToolRowState,
  ctx: { suppressDiff: boolean },
) => ReactNode | null;

/** write/edit 共用：输出是 unified diff 时渲染 diff 卡，否则落通用卡。 */
const renderDiffBody: FamilyBodyRenderer = (model, _state, ctx) => {
  if (ctx.suppressDiff) return null;
  const detail = model.output !== null ? stripControlChars(model.output) : '';
  if (detail !== '' && looksLikeUnifiedDiff(detail)) {
    return (
      <div className={css.cardBody}>
        <DiffCard text={model.output ?? ''} />
      </div>
    );
  }
  return null;
};

/** bash/code 共用：命令 + 输出 + 退出码的终端语义卡。 */
const renderTerminalBody: FamilyBodyRenderer = (model, state) => (
  <TerminalBlock
    className={css.cardBody}
    command={model.summary}
    output={model.output ?? undefined}
    exitCode={model.exitCode}
    running={state === 'running'}
  />
);

const TOOL_BODY_RENDERERS: Partial<Record<ToolFamily, FamilyBodyRenderer>> = {
  bash: renderTerminalBody,
  code: renderTerminalBody,
  write: renderDiffBody,
  edit: renderDiffBody,
  read: (model, state) => {
    if (state !== 'running' && model.output !== null) {
      const read = readBlockFromOutput(model.output, model.filePath);
      if (read !== null) return <ReadBlock {...read} className={css.cardBody} />;
    }
    return null;
  },
  search: (model) => {
    if (model.output !== null) {
      const grep = parseGrepOutput(model.output);
      if (grep !== null) {
        const shown = grep.files.reduce((count, file) => count + file.matches.length, 0);
        return (
          <SearchBlock
            kind="matches"
            className={css.cardBody}
            files={grep.files}
            truncated={grep.truncated}
            total={grep.truncated ? undefined : shown}
          />
        );
      }
      const paths = parsePathList(model.output);
      if (paths !== null) {
        const truncated = model.output.length >= 1900;
        return (
          <SearchBlock
            kind="paths"
            className={css.cardBody}
            paths={paths}
            truncated={truncated}
            total={truncated ? undefined : paths.length}
          />
        );
      }
    }
    return null;
  },
};

/** 展开体卡片分派：注册表命中则用族专用渲染器，未命中/不适用落 IN/OUT 通用卡。 */
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
  if (
    suppressDiff
    && (model.family === 'write' || model.family === 'edit')
    && output !== null
    && looksLikeUnifiedDiff(output)
  ) {
    return <div className={css.emptyDetails}>变更内容已汇总到本轮 Diff</div>;
  }
  const specialized = TOOL_BODY_RENDERERS[model.family]?.(model, state, { suppressDiff });
  if (specialized !== undefined && specialized !== null) return <>{specialized}</>;

  // 通用 IN/OUT 卡：单文件工具不暴露 IN（路径已是摘要；对齐 DSH single-file 取舍）。
  const singleFile = model.filePath !== undefined;
  const body = singleFile ? null : model.body;
  if (body === null && output === null) {
    return (
      <div className={css.emptyDetails}>
        {state === 'running' ? '等待工具输出…' : '工具未返回可展示详情'}
      </div>
    );
  }
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

/** 工具状态同时用于可见文案和无障碍名称，不能只靠颜色或动画。 */
function stateStatus(state: ToolRowState): string {
  switch (state) {
    case 'running':
      return '进行中';
    case 'error':
      return '失败';
    case 'stopped':
      return '已中断';
    default:
      return '已完成';
  }
}

function formatWrittenBytes(bytes: number): string {
  if (bytes < 1_000) return `${bytes} B`;
  if (bytes < 1_000_000) return `${(bytes / 1_000).toFixed(1)} KB`;
  return `${(bytes / 1_000_000).toFixed(1)} MB`;
}

function changeSummaryText(stats: NonNullable<ToolRowModel['changeStats']>): string {
  const parts = [`${stats.files} 个文件已更改`];
  if (stats.additions !== undefined) parts.push(`+${stats.additions}`);
  if (stats.deletions !== undefined) parts.push(`−${stats.deletions}`);
  if (stats.bytes !== undefined && stats.additions === undefined && stats.deletions === undefined) {
    parts.push(`· ${formatWrittenBytes(stats.bytes)}`);
  }
  return parts.join(' ');
}

function ChangeSummary({ stats }: { stats: NonNullable<ToolRowModel['changeStats']> }) {
  if (stats.operation === 'written') {
    return <><span>{stats.files} 个文件已更改</span>{stats.bytes !== undefined && <span className={css.changeBytes}> · {formatWrittenBytes(stats.bytes)}</span>}</>;
  }
  return (
    <>
      <span>{stats.files} 个文件已更改</span>
      {stats.additions !== undefined && <span className={css.changeAddition}> +{stats.additions}</span>}
      {stats.deletions !== undefined && <span className={css.changeDeletion}> −{stats.deletions}</span>}
    </>
  );
}

/** 纵向工具日志行；详情由 ActivityGroup 紧跟当前行单选展开。 */
export function ToolRow({
  msg,
  index,
  stopped = false,
  selected = false,
  detailsId,
  onToggle,
}: {
  msg: ChatMessage;
  index?: number;
  stopped?: boolean;
  selected?: boolean;
  detailsId?: string;
  onToggle?: () => void;
}) {
  const chip = toolChipModel(msg, stopped);
  const state = chip.state;
  const duration = chip.duration;
  const status = stateStatus(state);
  const title = chip.title;
  const model = toolRowModel(msg);
  // createElement 渲染图标引用：避免本地变量承接组件触发 static-components 规则。
  const icon = createElement(toolIcon(msg.tool), { 'aria-hidden': true });
  const statusIcon = state === 'running'
    ? <LoaderCircle className={css.runningIcon} aria-hidden />
    : state === 'error'
      ? <CircleX className={css.errorIcon} aria-hidden />
      : state === 'stopped'
        ? <CircleStop className={css.stoppedIcon} aria-hidden />
        : <Check className={css.okIcon} aria-hidden />;
  const accessibleName = [
    `Action ${index ?? 1}`,
    title,
    model.changeStats ? changeSummaryText(model.changeStats) : chip.summary,
    status,
    duration,
  ].filter(Boolean).join('，');
  return (
    <div className={css.root} data-tool={msg.tool} data-state={state}>
      <button
        type="button"
        className={css.chip}
        data-selected={selected || undefined}
        onClick={onToggle}
        aria-expanded={selected}
        aria-controls={selected ? detailsId : undefined}
        aria-label={accessibleName}
      >
        <span className={css.chipMeta}>
          <span className={css.index}>Action {index ?? 1}</span>
          <span className={css.toolBadge}>
            <span className={css.icon}>{icon}</span>
            <span className={css.title}>{title}</span>
          </span>
        </span>
        <span className={css.chipSummary} title={model.changeStats ? changeSummaryText(model.changeStats) : chip.summary}>
          {model.changeStats ? <ChangeSummary stats={model.changeStats} /> : chip.summary}
        </span>
        <span className={css.chipFooter}>
          <span className={css.duration}>{duration ?? '—'}</span>
          <span className={css.status}>{statusIcon}<span>{status}</span></span>
          <ChevronRight className={css.detailChevron} data-open={selected || undefined} aria-hidden />
        </span>
      </button>
    </div>
  );
}

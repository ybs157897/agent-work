import { Check, CircleStop, CircleX, ChevronDown, ChevronRight, FilePen, FileText, LoaderCircle, Plug, Search, SquareCode, Terminal, Wrench, type LucideIcon } from 'lucide-react';
import { createElement, useId, useState, type ReactNode } from 'react';
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

export interface ActivityGroupModel {
  total: number;
  completed: number;
  running: number;
  failed: number;
  stopped: number;
  state: ActivityGroupState;
  summary: string;
  status: string;
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
  return { total, completed, running, failed, stopped, state, summary, status };
}

function ActivityStatusIcon({ state }: { state: ActivityGroupState }) {
  if (state === 'running') return <LoaderCircle className={css.runningIcon} aria-hidden />;
  if (state === 'error') return <CircleX className={css.errorIcon} aria-hidden />;
  if (state === 'stopped') return <CircleStop className={css.stoppedIcon} aria-hidden />;
  if (state === 'ok') return <Check className={css.okIcon} aria-hidden />;
  return <Wrench aria-hidden />;
}

/**
 * 同 run 工具调用收敛成一个 LanguageGUI 活动卡；组级折叠控制扫描密度，
 * 单选 Action 卡在下方渐进披露真实工具详情。
 */
export function ActivityGroup({
  items,
  stoppedRuns,
  defaultCollapsed = false,
  defaultSelectedKey,
  suppressDiff = false,
}: {
  items: ChatMessage[];
  stoppedRuns?: ReadonlySet<string>;
  /** 历史终态 run 默认只展示组级汇总。 */
  defaultCollapsed?: boolean;
  /** Demo/验收可指定首个展开项；生产默认不抢占用户注意力。 */
  defaultSelectedKey?: string;
  /** turn 级 diff 汇总存在时隐藏工具行内 diff */
  suppressDiff?: boolean;
}) {
  const headingId = useId();
  const bodyId = useId();
  const [collapsed, setCollapsed] = useState(defaultCollapsed);
  const [selectedKey, setSelectedKey] = useState<string | null>(() =>
    defaultSelectedKey && items.some((item) => item.key === defaultSelectedKey)
      ? defaultSelectedKey
      : null,
  );
  const reduceMotion = useReducedMotion();
  const group = activityGroupModel(items, stoppedRuns);
  const selected = selectedKey ? items.find((item) => item.key === selectedKey) : undefined;
  const selectedStopped = selected ? stoppedRuns?.has(selected.runId) && selected.toolStatus === 'running' : false;
  const selectedModel = selected ? toolRowModel(selected) : undefined;
  const selectedState = selected ? toolChipModel(selected, selectedStopped).state : undefined;
  const showDetails = Boolean(!collapsed && selectedModel && selectedState);

  return (
    <section
      className={css.activity}
      data-state={group.state}
      aria-labelledby={headingId}
    >
      <header className={css.activityHeader}>
        <span className={css.activityMark} aria-hidden><Wrench /></span>
        <div className={css.activityHeading}>
          <h3 id={headingId}>工具执行</h3>
          <p>{group.summary}</p>
        </div>
        <span
          className={css.groupStatus}
          role={group.state === 'running' ? 'status' : undefined}
          aria-atomic={group.state === 'running' ? 'true' : undefined}
        >
          <ActivityStatusIcon state={group.state} />
          <span>{group.status}</span>
        </span>
        {items.length > 0 && (
          <button
            type="button"
            className={css.collapseButton}
            onClick={() => setCollapsed((value) => !value)}
            aria-expanded={!collapsed}
            aria-controls={bodyId}
            aria-label={collapsed ? `展开 ${group.total} 个工具调用` : '收起工具调用'}
          >
            {collapsed ? <ChevronRight aria-hidden /> : <ChevronDown aria-hidden />}
          </button>
        )}
      </header>

      {items.length === 0 ? (
        <div className={css.emptyState}>当前回合没有工具调用</div>
      ) : !collapsed ? (
        <div id={bodyId} className={css.activityBody}>
          <div className={css.strip} role="list" aria-label="工具调用">
            {items.map((m, index) => (
              <ToolRow
                key={m.key}
                msg={m}
                index={index + 1}
                stopped={stoppedRuns?.has(m.runId) && m.toolStatus === 'running'}
                selected={selectedKey === m.key}
                onToggle={() => setSelectedKey((key) => key === m.key ? null : m.key)}
              />
            ))}
          </div>
          <AnimatePresence initial={false}>
            {showDetails && selectedModel && selectedState && selected && (
              <motion.div
                className={css.details}
                role="region"
                aria-label={`${toolChipModel(selected, selectedStopped).title} 工具详情`}
                initial={reduceMotion ? false : { opacity: 0, y: -4 }}
                animate={{ opacity: 1, y: 0 }}
                exit={reduceMotion ? { opacity: 0 } : { opacity: 0, y: -4 }}
                transition={reduceMotion ? { duration: 0 } : { duration: 0.2, ease: [0.16, 1, 0.3, 1] }}
              >
                <ExpandedBody model={selectedModel} state={selectedState} suppressDiff={suppressDiff} />
              </motion.div>
            )}
          </AnimatePresence>
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

/** LanguageGUI Action 卡；详情由 ActivityGroup 在轨道外单选展开。 */
export function ToolRow({
  msg,
  index,
  stopped = false,
  selected = false,
  onToggle,
}: {
  msg: ChatMessage;
  index?: number;
  stopped?: boolean;
  selected?: boolean;
  onToggle?: () => void;
}) {
  const chip = toolChipModel(msg, stopped);
  const state = chip.state;
  const duration = chip.duration;
  const status = stateStatus(state);
  const title = chip.title;
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
    chip.summary,
    status,
    duration,
  ].filter(Boolean).join('，');
  return (
    <div className={css.root} data-tool={msg.tool} data-state={state} role="listitem">
      <button
        type="button"
        className={css.chip}
        data-selected={selected || undefined}
        onClick={onToggle}
        aria-expanded={selected}
        aria-pressed={selected}
        aria-label={accessibleName}
      >
        <span className={css.chipMeta}>
          <span className={css.index}>Action {index ?? 1}</span>
          <span className={css.toolBadge}>
            <span className={css.icon}>{icon}</span>
            <span className={css.title}>{title}</span>
          </span>
        </span>
        <span className={css.chipSummary} title={chip.summary}>{chip.summary}</span>
        <span className={css.chipFooter}>
          <span className={css.duration}>{duration ?? '—'}</span>
          <span className={css.status}>{statusIcon}<span>{status}</span></span>
        </span>
      </button>
    </div>
  );
}

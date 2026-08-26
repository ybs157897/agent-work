import { Check, CircleX, ChevronDown, ChevronRight, FilePen, FileText, LoaderCircle, Plug, Search, SquareCode, Terminal, Wrench, type LucideIcon } from 'lucide-react';
import { createElement, useState, type ReactNode } from 'react';
import { AnimatePresence, motion, useReducedMotion } from 'motion/react';
import { toolDuration, type ChatMessage } from '../../stores/chat.store';
import { DiffCard, looksLikeUnifiedDiff, stripControlChars } from './diff-card';
import { StateDot } from './blocks/StateDot';
import { TerminalBlock } from './blocks/TerminalBlock';
import { ReadBlock } from './blocks/ReadBlock';
import { SearchBlock } from './blocks/SearchBlock';
import { cx } from './blocks/cx';
import { FAMILY_TITLES, toolRowModel, classifyTool, type ToolFamily, type ToolRowModel, type ToolRowState } from './tool-model';
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
    state,
    duration: toolDuration(msg.startedAt, msg.completedAt),
  } as const;
}

/**
 * 同 run 工具调用按 LeAgent 的横向 chip 轨展示；单选 chip 后在轨道下方展开本地专用详情体。
 * stoppedRuns 把终态 run 遗留的 running 帧投影为中断态，避免继续显示运行动画。
 */
export function ActivityGroup({
  items,
  stoppedRuns,
  defaultCollapsed = false,
  suppressDiff = false,
}: {
  items: ChatMessage[];
  stoppedRuns?: ReadonlySet<string>;
  /** run 终态且无 running 工具时默认折叠 */
  defaultCollapsed?: boolean;
  /** turn 级 diff 汇总存在时隐藏工具行内 diff */
  suppressDiff?: boolean;
}) {
  const [panelExpanded, setPanelExpanded] = useState(!defaultCollapsed);
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const reduceMotion = useReducedMotion();
  const selected = selectedKey ? items.find((item) => item.key === selectedKey) : undefined;
  const selectedStopped = selected ? stoppedRuns?.has(selected.runId) && selected.toolStatus === 'running' : false;
  const selectedModel = selected ? toolRowModel(selected) : undefined;
  const selectedState = selected ? toolChipModel(selected, selectedStopped).state : undefined;
  const showDetails = Boolean(panelExpanded && selectedModel && selectedState);

  return (
    <div className={css.activityBody}>
      <div className={css.stripRow}>
        <button
          type="button"
          className={css.detailsToggle}
          disabled={!selected}
          onClick={() => setPanelExpanded((value) => !value)}
          aria-expanded={showDetails}
          aria-label="切换工具详情"
        >
          {showDetails ? <ChevronDown aria-hidden /> : <ChevronRight aria-hidden />}
        </button>
        <div className={css.strip} role="list" aria-label="工具调用">
          {items.map((m, index) => (
            <ToolRow
              key={m.key}
              msg={m}
              index={index + 1}
              stopped={stoppedRuns?.has(m.runId) && m.toolStatus === 'running'}
              selected={selectedKey === m.key}
              onToggle={() => {
                setSelectedKey((key) => key === m.key ? null : m.key);
                setPanelExpanded(true);
              }}
            />
          ))}
        </div>
      </div>
      <AnimatePresence initial={false}>
        {showDetails && selectedModel && selectedState && (
          <motion.div
            className={css.details}
            initial={reduceMotion ? false : { opacity: 0, height: 0, y: -4 }}
            animate={{ opacity: 1, height: 'auto', y: 0 }}
            exit={reduceMotion ? { opacity: 0 } : { opacity: 0, height: 0, y: -4 }}
            transition={reduceMotion ? { duration: 0 } : { duration: 0.24, ease: [0.16, 1, 0.3, 1] }}
          >
            <ExpandedBody model={selectedModel} state={selectedState} suppressDiff={suppressDiff} />
          </motion.div>
        )}
      </AnimatePresence>
    </div>
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
      const paths = parsePathList(model.output);
      if (paths !== null) {
        return (
          <SearchBlock
            kind="paths"
            className={css.cardBody}
            paths={paths}
            truncated={model.output.length >= 1900}
            total={paths.length}
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
  const specialized = TOOL_BODY_RENDERERS[model.family]?.(model, state, { suppressDiff });
  if (specialized !== undefined && specialized !== null) return <>{specialized}</>;

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

/** ToolCallStrip 中的单次调用 chip；详情由 ActivityGroup 在 strip 外统一渲染。 */
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
  // createElement 渲染图标引用：避免本地变量承接组件触发 static-components 规则。
  const icon = createElement(toolIcon(msg.tool), { className: 'h-3.5 w-3.5' });
  // 卡片存在性判定与 ExpandedBody 的分派保持一致（行能否展开取决于有无任何展开体）。
  const status = stateStatus(state);
  const title = chip.title;
  const statusIcon = state === 'running' ? <LoaderCircle className={css.runningIcon} aria-hidden /> : state === 'error' ? <CircleX className={css.errorIcon} aria-hidden /> : state === 'stopped' ? <StateDot state="warning" /> : <Check className={css.okIcon} aria-hidden />;
  return (
    <div className={css.root} data-tool={msg.tool} data-state={state} role="listitem">
      {status !== null && <span className={css.visuallyHidden}>{status}</span>}
      <button type="button" className={css.chip} data-selected={selected || undefined} onClick={onToggle} aria-expanded={selected} aria-pressed={selected} title={title}>
        <span className={css.index}>{index ?? 1}</span>
        <span className={css.icon}>{icon}</span>
        <span className={css.title}>{title}</span>
        {duration !== null && <span className={css.summarySuffix}>{duration}</span>}
        <span className={css.status}>{statusIcon}</span>
      </button>
    </div>
  );
}

import { ChevronDown } from 'lucide-react';
import { AnimatePresence, motion, useReducedMotion } from 'motion/react';
import { useId, useState, type ReactNode } from 'react';

/** 折叠触发阈值：落定正文超过该长度才折叠（流式与短回答一律完整渲染）。 */
export const LONG_ANSWER_THRESHOLD = 1_600;
/** 标题截断前提：首个 h2/h3 必须出现在该偏移之后，保证预览有体量。 */
const HEADING_MIN_OFFSET = 300;
/** 无合适标题时，段落边界截断点上限。 */
const PARAGRAPH_MAX_CUT = 800;

/** ATX 二级/三级标题行首（`\n## ` / `\n### `，不吞四级及更深）。 */
const HEADING_BOUNDARY = /\n#{2,3}(?:[ \t]|\r?\n)/;
/** 围栏代码块的开启/闭合行（行首至多 3 个空格 + 至少 3 个反引号）。 */
const FENCE_LINE = /^[ \t]{0,3}`{3,}/gm;

export interface LongAnswerSplit {
  /** 折叠态展示的正文前缀（已去掉尾部空白）。 */
  preview: string;
  truncated: boolean;
}

/**
 * 长回答截断点计算（纯函数）。返回 null 表示不折叠（短于阈值，或找不到
 * 合法截断点）。截断规则：
 * 1. 优先在第一个二级/三级标题处截断，前提是它出现在 300 字符之后；
 * 2. 否则在不超过 800 字符的最后一个段落边界（`\n\n`）处截断；
 * 3. 截断点绝不落在围栏代码块内——截取段内 ``` 围栏未闭合时回退到该围栏之前。
 */
export function splitLongAnswer(text: string): LongAnswerSplit | null {
  if (text.length <= LONG_ANSWER_THRESHOLD) return null;

  const heading = HEADING_BOUNDARY.exec(text);
  if (heading && heading.index >= HEADING_MIN_OFFSET) {
    const cut = fenceSafeCut(text, heading.index);
    if (cut > 0) return { preview: text.slice(0, cut).trimEnd(), truncated: true };
  }

  const boundary = lastParagraphBoundary(text);
  if (boundary !== null) {
    const cut = fenceSafeCut(text, boundary);
    if (cut > 0) return { preview: text.slice(0, cut).trimEnd(), truncated: true };
  }

  return null;
}

function lastParagraphBoundary(text: string): number | null {
  let found: number | null = null;
  for (const match of text.matchAll(/\n\n+/g)) {
    if (match.index === undefined || match.index > PARAGRAPH_MAX_CUT) break;
    found = match.index;
  }
  return found;
}

/** 截取段内围栏未闭合（奇数条围栏线）时，回退到最后一条围栏开启行之前。 */
function fenceSafeCut(text: string, cut: number): number {
  let fenceCount = 0;
  let lastFenceStart = -1;
  for (const match of text.slice(0, cut).matchAll(FENCE_LINE)) {
    fenceCount += 1;
    lastFenceStart = match.index ?? -1;
  }
  return fenceCount % 2 === 1 ? lastFenceStart : cut;
}

/**
 * 长回答渐进披露：落定长文默认收起为预览 + 「展开全文」；展开/收起是组件
 * 本地 state。流式态与不满足折叠条件时原样透传 renderBody（无额外包裹，
 * 不影响 `.chat-prose` 的直接子元素节奏）。
 */
export function LongAnswerFold({
  text,
  streaming = false,
  renderBody,
}: {
  text: string;
  streaming?: boolean;
  /** 渲染可见正文（预览或全文），markdown 渲染权留给调用方。 */
  renderBody: (visibleText: string) => ReactNode;
}) {
  const [expanded, setExpanded] = useState(false);
  const reduceMotion = useReducedMotion();
  const bodyId = useId();
  // 流式永不折叠；落定后经 splitLongAnswer 判定。streaming→settled 由外层
  // key 重挂载，展开态自然复位为默认收起。
  const split = streaming ? null : splitLongAnswer(text);
  if (!split) return <>{renderBody(text)}</>;

  const collapsed = !expanded;
  const visibleText = collapsed ? split.preview : text;
  const hiddenCount = text.length - split.preview.length;

  return (
    <>
      {/* 折叠/展开切换预览↔全文两个 body；mode="wait" 避免两份长文同时
          占位撑爆布局。height auto 模式对齐 reasoning-activity-row。 */}
      <AnimatePresence initial={false} mode="wait">
        <motion.div
          key={collapsed ? 'preview' : 'full'}
          id={collapsed ? undefined : bodyId}
          className="chat-prose relative"
          initial={reduceMotion ? false : { opacity: 0, height: 0 }}
          animate={{ opacity: 1, height: 'auto' }}
          exit={reduceMotion ? { opacity: 0 } : { opacity: 0, height: 0 }}
          transition={reduceMotion ? { duration: 0 } : { duration: 0.26, ease: [0.16, 1, 0.3, 1] }}
        >
          {renderBody(visibleText)}
          {collapsed && <span className="chat-long-answer-fade" aria-hidden />}
        </motion.div>
      </AnimatePresence>
      <button
        type="button"
        className="chat-long-answer-toggle"
        onClick={() => setExpanded((value) => !value)}
        aria-expanded={expanded}
        // 折叠态全文不在 DOM 里，不输出悬空的 aria-controls（对齐思考面板）。
        aria-controls={collapsed ? undefined : bodyId}
      >
        {collapsed ? `展开全文（约 ${hiddenCount} 字）` : '收起'}
        <ChevronDown className="chat-long-answer-chevron h-3 w-3 shrink-0 motion-reduce:transition-none" aria-hidden />
      </button>
    </>
  );
}

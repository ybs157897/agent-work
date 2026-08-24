import { useEffect, useMemo, useState } from 'react';
import { cx } from './cx';
import { headTailCap } from './head-tail-cap';
import { useCopyFeedback } from './use-copy-feedback';
import css from './ReadBlock.module.css';

/** 折叠阈值：超过该行数收头尾（与其他块的默认高度帽一致）。 */
export const DEFAULT_READ_MAX_LINES = 16;

/** Read 卡的一行：文件行号 + 该行文本。 */
export interface ReadBlockLine {
  /** 文件内 1 起的行号（窗口读取越过 offset 时保留文件自身编号）。 */
  number: number;
  /** 行文本，无尾随换行。 */
  text: string;
}

export interface ReadBlockProps {
  /** 横幅标签（文件路径）；缺省不画。 */
  label?: string | undefined;
  /** 读取窗口的行，按文件顺序，各带文件行号。 */
  lines: readonly ReadBlockLine[];
  /** 文件总行数；> lines.length 时横幅显示「显示 N / M 行」（无窗口信息时缺省）。 */
  totalLines?: number | undefined;
  /** highlight.js 语言 id；未知/缺省 = 纯等宽文本。 */
  lang?: string | undefined;
  /** 折叠阈值（内容行数），默认 16。 */
  maxLines?: number | undefined;
  /** 追加到根元素的额外 class。 */
  className?: string | undefined;
}

/** 高亮结果：连同其原文一起保存；原文不等于当前展示文本时回落纯文本（异步竞态守卫）。 */
interface Highlighted {
  raw: string;
  html: string;
}

/**
 * 用 highlight.js 整段高亮（跨行语法上下文一次成型）。
 * 公共语言包懒加载；import 失败或声明语言未注册返回 null，纯文本是既定降级。
 */
async function highlightRaw(raw: string, lang: string): Promise<string | null> {
  try {
    const { default: hljs } = await import('highlight.js/lib/common');
    if (!hljs.getLanguage(lang)) return null;
    const { value } = hljs.highlight(raw, { language: lang, ignoreIllegals: true });
    return value;
  } catch {
    return null;
  }
}

/**
 * Read 工具结果卡：横幅（路径 + 语言 + 窗口行数注记 + 复制）盖在带行号、可选语法高亮的源码上。
 * 高亮走「整段一次 hljs.highlight → 单个 pre 注入」；行号是独立 grid 列，与内容列同行高对齐
 * （内容 white-space: pre 不换行、随 body 横向滚动，行号不会错位）。
 */
export function ReadBlock({
  label,
  lines,
  totalLines,
  lang,
  maxLines = DEFAULT_READ_MAX_LINES,
  className,
}: ReadBlockProps) {
  const [expanded, setExpanded] = useState(false);
  const { hidden, capped, headLines, tailLines } = headTailCap(lines.length, maxLines, expanded);

  // 折叠时只展示头 + 尾；头尾之间的空行是展开按钮行的占位（行高一致，故行号列仍逐行对齐）。
  // 空行直接写进送入高亮的原文，让 hljs 自己产出含空行的完整 HTML，避免事后在跨行 span 上动刀。
  const head = useMemo(() => (capped ? lines.slice(0, headLines) : lines), [lines, capped, headLines]);
  const tail = useMemo(
    () => (capped ? lines.slice(lines.length - tailLines) : []),
    [lines, capped, tailLines],
  );
  const shownRaw = useMemo(() => {
    const headText = head.map((line) => line.text).join('\n');
    if (!capped) return headText;
    return `${headText}\n\n${tail.map((line) => line.text).join('\n')}`;
  }, [head, tail, capped]);

  // 高亮异步一次完成（Read 卡非流式，无需 code-block 的防抖）；结果带原文做竞态守卫。
  const [highlighted, setHighlighted] = useState<Highlighted | null>(null);
  useEffect(() => {
    let cancelled = false;
    if (lang === undefined || lang === '' || shownRaw === '') {
      setHighlighted(null);
      return;
    }
    void highlightRaw(shownRaw, lang).then((html) => {
      if (!cancelled) setHighlighted(html === null ? null : { raw: shownRaw, html });
    });
    return () => {
      cancelled = true;
    };
  }, [shownRaw, lang]);
  const html = highlighted?.raw === shownRaw ? highlighted.html : null;

  // 复制写完整窗口原文（含折叠时隐藏的行），而非当前展示切片或渲染树。
  const raw = useMemo(() => lines.map((line) => line.text).join('\n'), [lines]);
  const { copied, onCopy } = useCopyFeedback(raw);

  // 窗口语义：给了总行数且返回行更少时，明示文件不止于此。
  const windowed = totalLines !== undefined && lines.length < totalLines;

  // grid 行号：行高钉死 22px，pre 跨全部内容行；折叠时尾段行号越过按钮占位行（headLines + 1）落位，
  // 展开后按钮退到全部行之下（收起入口常驻，对齐 DSH 行为）。
  const rowCount = shownRaw === '' ? 0 : shownRaw.split('\n').length;
  const overCap = hidden > 0; // 超阈值：两种状态下都要画展开/收起按钮
  const gridRows = rowCount + (overCap && !capped ? 1 : 0);
  return (
    <div className={cx(css.block, className)} data-read="">
      <div className={css.banner}>
        <div className={css.label}>{label ?? ''}</div>
        <div className={css.action}>
          <span className={css.lang}>{lang ?? ''}</span>
          {windowed && (
            <span className={css.count}>{`显示 ${lines.length} / ${totalLines} 行`}</span>
          )}
          {/* 空文件（lines: []）不画复制，避免把剪贴板清成空串 */}
          {lines.length > 0 && (
            <button type="button" className={css.copyButton} onClick={onCopy}>
              {copied ? '复制成功' : '复制'}
            </button>
          )}
        </div>
      </div>
      <div className={css.body}>
        {rowCount > 0 && (
          <div className={css.grid} style={{ gridTemplateRows: `repeat(${gridRows}, 22px)` }}>
            {head.map((line, index) => (
              <span key={line.number} className={css.gutter} style={{ gridRow: index + 1 }} aria-hidden>
                {line.number}
              </span>
            ))}
            {overCap && (
              <button
                type="button"
                className={css.expand}
                style={{ gridRow: capped ? headLines + 1 : rowCount + 1 }}
                aria-expanded={expanded}
                aria-label={expanded ? '收起内容' : `展开其余 ${hidden} 行`}
                onClick={() => setExpanded((value) => !value)}
              >
                {expanded ? '收起' : `… 其余 ${hidden} 行`}
              </button>
            )}
            {capped &&
              tail.map((line, index) => (
                <span
                  key={line.number}
                  className={css.gutter}
                  style={{ gridRow: headLines + 2 + index }}
                  aria-hidden
                >
                  {line.number}
                </span>
              ))}
            {html === null ? (
              <pre className={cx(css.content, 'hljs')} style={{ gridRow: `1 / span ${rowCount}` }}>
                {shownRaw}
              </pre>
            ) : (
              <pre
                className={cx(css.content, 'hljs')}
                style={{ gridRow: `1 / span ${rowCount}` }}
                dangerouslySetInnerHTML={{ __html: html }}
              />
            )}
          </div>
        )}
      </div>
    </div>
  );
}

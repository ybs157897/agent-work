import { useCallback, useState, type ReactNode } from 'react';
import { cx } from './cx';
import { headTailCap } from './head-tail-cap';
import { useCopyFeedback } from './use-copy-feedback';
import css from './SearchBlock.module.css';

/** 折叠阈值：结果行数超过该值收头尾（与其他块的默认高度帽一致）。 */
export const DEFAULT_SEARCH_MAX_LINES = 16;

/** 一个文件组内的一条命中行：文件内 1 起行号 + 行文本。 */
export interface SearchBlockLineMatch {
  /** 命中行在文件内的 1 起行号。 */
  lineNumber: number;
  /** 命中行文本，按工具输出的原样。 */
  line: string;
}

/** 一个文件的分组命中，按首见文件顺序。 */
export interface SearchFileGroup {
  /** 命中所属文件（展示用路径）。 */
  path: string;
  /** 该文件的命中行，按输出顺序。 */
  matches: SearchBlockLineMatch[];
}

/** 两种形态共有的字段（调用方负责定位；本组件只负责绘制）。 */
interface SearchBlockCommon {
  /** 工具是否截断了内联结果：形态里只带保留结果，不带全部。 */
  truncated: boolean;
  /** 截断前的可信结果总数；协议未提供时保持 undefined，绝不拿保留数冒充总数。 */
  total?: number | undefined;
  /** 折叠阈值（结果行数），默认 16。 */
  maxLines?: number | undefined;
  /** 追加到根元素的额外 class。 */
  className?: string | undefined;
}

/** grep 分组命中形态的 props。 */
export interface SearchMatchesBlockProps extends SearchBlockCommon {
  kind: 'matches';
  /** 按文件分组的命中行，首见文件顺序。 */
  files: SearchFileGroup[];
}

/** glob 路径列表形态的 props。 */
export interface SearchPathsBlockProps extends SearchBlockCommon {
  kind: 'paths';
  /** 发现的路径，按工具输出顺序（截断时为保留页）。 */
  paths: string[];
}

/** SearchBlock props：一张卡、两种按 kind 判别的形态。 */
export type SearchBlockProps = SearchMatchesBlockProps | SearchPathsBlockProps;

/** 扁平化后的渲染行。matches 卡每组一个 file 头行、组展开时每条命中一个 match 行；paths 卡每路径一个 path 行。折叠阈值统一按行计——文件头与命中行/路径行等价各占一行。 */
type SearchRow =
  | { type: 'file'; path: string; count: number; index: number; collapsed: boolean }
  | { type: 'match'; lineNumber: number; line: string; key: string; fileIndex: number }
  | { type: 'path'; path: string };

/** 复制控件写的纯文本形态：与折叠状态、组折叠无关的完整结构化结果，让剪贴板带走结果本身而非卡片恰好展示的部分。 */
function copyText(props: SearchBlockProps): string {
  if (props.kind === 'paths') return props.paths.join('\n');
  return props.files
    .map((file) => [file.path, ...file.matches.map((m) => `${m.lineNumber}: ${m.line}`)].join('\n'))
    .join('\n\n');
}

/** 卡内保留的结果数：matches 形态为全部文件的命中行数，paths 形态为路径数。截断时横幅拿它与 total 对比。 */
function shownCount(props: SearchBlockProps): number {
  return props.kind === 'paths'
    ? props.paths.length
    : props.files.reduce((sum, file) => sum + file.matches.length, 0);
}

/** 横幅摘要：只有可信 total 才显示「显示 X / 共 N」；未知总数明确写「已截断」。 */
function summaryText(
  props: SearchBlockProps,
  shown: number,
  truncated: boolean,
  total: number | undefined,
): string {
  const count = truncated
    ? total !== undefined && total > shown
      ? `显示 ${shown} / 共 ${total}`
      : `已截断 · 显示 ${shown}`
    : `${shown}`;
  return props.kind === 'paths'
    ? `${count} 个路径`
    : `${count} 处匹配 · ${props.files.length} 个文件`;
}

/** 把卡片形态扁平化为渲染行，折叠的文件组丢掉其命中行。 */
function toRows(props: SearchBlockProps, collapsed: ReadonlySet<number>): SearchRow[] {
  if (props.kind === 'paths') return props.paths.map((path): SearchRow => ({ type: 'path', path }));
  const rows: SearchRow[] = [];
  props.files.forEach((file, index) => {
    const isCollapsed = collapsed.has(index);
    rows.push({ type: 'file', path: file.path, count: file.matches.length, index, collapsed: isCollapsed });
    if (isCollapsed) return;
    for (const match of file.matches) {
      rows.push({
        type: 'match',
        lineNumber: match.lineNumber,
        line: match.line,
        key: `${index}:${match.lineNumber}`,
        fileIndex: index,
      });
    }
  });
  return rows;
}

/** 扁平化渲染行的稳定 React key：各类型带类型前缀或组下标，互不冲突。 */
function rowKey(row: SearchRow): string {
  switch (row.type) {
    case 'match':
      return `match:${row.key}`;
    case 'file':
      return `file:${row.index}`;
    case 'path':
      return `path:${row.path}`;
  }
}

/** 搜索（内容 grep / 路径 glob）完成结果卡：分组命中或平铺路径，头尾折叠收长结果。 */
export function SearchBlock(props: SearchBlockProps) {
  const { truncated, total, maxLines = DEFAULT_SEARCH_MAX_LINES, className } = props;
  const [expanded, setExpanded] = useState(false);
  const [collapsed, setCollapsed] = useState<ReadonlySet<number>>(() => new Set());

  // props 每次渲染都是新对象，memoize 不会命中；扁平化本身廉价，按折叠集内联重算即可。
  const rows = toRows(props, collapsed);
  const shown = shownCount(props);
  const empty = rows.length === 0;
  const { copied, onCopy } = useCopyFeedback(copyText(props));

  const onToggle = useCallback(() => {
    setExpanded((value) => !value);
  }, []);

  const toggleFile = useCallback((index: number) => {
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(index)) next.delete(index);
      else next.add(index);
      return next;
    });
  }, []);

  const { hidden, capped, headLines, tailLines } = headTailCap(rows.length, maxLines, expanded);
  const head = capped ? rows.slice(0, headLines) : rows;
  const naturalTail = capped ? rows.slice(rows.length - tailLines) : [];
  // 尾段切片若起始于某文件的命中中间，该文件自己的组头在切口之上未被展示，尾段行就没了归属。
  // 在尾段顶部补回所属组头——除非头段已带同一组头（单一大文件场景），否则会重复。
  const tailLead = naturalTail[0];
  const tailHeader =
    tailLead?.type === 'match' &&
    !head.some((row) => row.type === 'file' && row.index === tailLead.fileIndex)
      ? rows.find(
          (row): row is Extract<SearchRow, { type: 'file' }> =>
            row.type === 'file' && row.index === tailLead.fileIndex,
        )
      : undefined;
  // 补回的组头自身也占一行。若额外放着会把卡片撑到 maxLines + 1 行、hidden 多算一行，
  // 因此它消耗一个尾段名额：顶掉尾段第一行（它的组头正是为此补的那行的所属行）。
  // 可见行数守住 maxLines、hidden 保持精确；被顶掉的命中行归入隐藏中段。
  const tail = tailHeader === undefined ? naturalTail : naturalTail.slice(1);

  const renderRow = (row: SearchRow): ReactNode => {
    if (row.type === 'path') return <div className={css.line}>{row.path}</div>;
    if (row.type === 'match') {
      return (
        <div className={css.line}>
          <span className={css.lineNumber}>{row.lineNumber}: </span>
          {row.line}
        </div>
      );
    }
    return (
      <button
        type="button"
        className={css.fileHeader}
        aria-expanded={!row.collapsed}
        onClick={() => {
          toggleFile(row.index);
        }}
      >
        <span className={css.filePath}>{row.path}</span>
        <span className={css.fileCount}>{row.count}</span>
      </button>
    );
  };

  return (
    <div className={cx(css.block, className)} data-search={props.kind}>
      <div className={css.header}>
        <span className={css.summary}>{summaryText(props, shown, truncated, total)}</span>
        {!empty && (
          <button type="button" className={css.copyButton} onClick={onCopy}>
            {copied ? '复制成功' : '复制'}
          </button>
        )}
      </div>
      {empty ? (
        <div className={css.empty}>无结果</div>
      ) : (
        <div className={css.body}>
          {head.map((row) => (
            <div key={rowKey(row)}>{renderRow(row)}</div>
          ))}
          {hidden > 0 && (
            <button
              type="button"
              className={css.expand}
              aria-expanded={expanded}
              aria-label={expanded ? '收起结果' : `展开其余 ${hidden} 行结果`}
              onClick={onToggle}
            >
              {expanded ? '收起' : `… 其余 ${hidden} 行`}
            </button>
          )}
          {tailHeader !== undefined && (
            <div key={`tailHeader:${rowKey(tailHeader)}`}>{renderRow(tailHeader)}</div>
          )}
          {tail.map((row) => (
            <div key={rowKey(row)}>{renderRow(row)}</div>
          ))}
        </div>
      )}
    </div>
  );
}

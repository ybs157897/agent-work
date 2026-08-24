import { Fragment, useEffect, useMemo, useRef, useState } from 'react';
import { Check, ChevronDown, ChevronRight, Copy, FileDiff } from 'lucide-react';

/**
 * Unified diff 卡（对齐 codex-desktop 渲染对比 Q6 的 Web 子集）：
 * 文件头（路径 + +N/−M 徽章）+ 行底色（增删用 status tokens）+ unified⇄split 切换；
 * >25 文件或 >2000 增删行默认折叠只显摘要。
 * DSH DiffBlock 能力吸收：卡头复制按钮（复制 unified 纯文本形态）、unified 展开态头尾
 * 行帽（>16 行折叠中段，跨文件按展平行数计）、底部 `└ +N −M · N 个文件` 汇总页脚。
 * split 双栏窄视口自动回落 unified；不做行内评论 / merge conflict 采纳（桌面高级特性）。
 */

/** 折叠阈值：文件数 ≤25 且增删行 ≤2000 时默认展开（对齐 codex editor-diff 常量）。 */
export const DIFF_COLLAPSE_FILES = 25;
export const DIFF_COLLAPSE_LINES = 2000;

/** 头尾帽上限（DSH DiffBlock 默认 16）：unified 展开态展平行数超过它才折叠中段。 */
export const DIFF_BODY_MAX_LINES = 16;

/** 复制成功反馈窗口：窗口内 Check 图标 + 「已复制」aria，超时回落 Copy。 */
const COPY_FEEDBACK_MS = 1000;

export interface DiffLine {
  kind: 'context' | 'add' | 'del' | 'meta';
  text: string;
}

export interface DiffFile {
  path: string;
  additions: number;
  deletions: number;
  lines: DiffLine[];
}

/** 过滤 ANSI 转义序列与其余控制字符（保留 \n 与 \t）；\r\n 与裸 \r 归一到 \n 语义。 */
export function stripControlChars(text: string): string {
  return text
    .replace(/\x1b\[[0-9;?]*[ -/]*[@-~]/g, '')
    .replace(/[\x00-\x08\x0b-\x1f\x7f]/g, '');
}

/** 检测 unified diff 特征：同时出现 `--- ` 行、`+++ ` 行与 `@@ ... @@` hunk 头。 */
export function looksLikeUnifiedDiff(text: string): boolean {
  let hasOld = false;
  let hasNew = false;
  let hasHunk = false;
  for (const line of text.split('\n')) {
    if (line.startsWith('--- ')) hasOld = true;
    else if (line.startsWith('+++ ')) hasNew = true;
    else if (/^@@.*@@/.test(line)) hasHunk = true;
    if (hasOld && hasNew && hasHunk) return true;
  }
  return hasOld && hasNew && hasHunk;
}

/** `+++ b/path.go` / `--- a/path.go` → path.go；剥 a//b/ 前缀与行尾时间戳；/dev/null 原样保留。 */
function cleanPath(header: string): string {
  const p = header.split('\t')[0].trim();
  if (p === '/dev/null') return p;
  return p.replace(/^[ab]\//, '');
}

/** git 多文件头（diff --git / index / mode / rename / binary）：文件边界，不归入任何文件的行集。 */
const GIT_FILE_HEADER = /^(diff --git |index |old mode |new mode |new file mode|deleted file mode|similarity index|dissimilarity index|rename (from|to) |copy (from|to) |Binary files|GIT binary patch)/;

/**
 * 解析 unified diff 文本为按文件分组的行集。宽容解析：无法识别的行归 meta。
 * 文件路径优先取 `+++` 侧；新增文件（--- /dev/null）回落 `+++`，删除文件（+++ /dev/null）取 `---` 侧。
 */
export function parseUnifiedDiff(text: string): DiffFile[] {
  const files: DiffFile[] = [];
  let current: DiffFile | null = null;
  for (const raw of text.split('\n')) {
    const line = raw.replace(/\s+$/, '');
    if (GIT_FILE_HEADER.test(line)) {
      current = null; // 进入下一文件的前导头：与上一文件断开归属
      continue;
    }
    if (line.startsWith('--- ')) {
      current = { path: cleanPath(line.slice(4)), additions: 0, deletions: 0, lines: [] };
      files.push(current);
      continue;
    }
    if (line.startsWith('+++ ')) {
      const p = cleanPath(line.slice(4));
      if (current && current.path === '/dev/null' && p !== '/dev/null') current.path = p;
      continue;
    }
    if (line.startsWith('@@')) {
      current?.lines.push({ kind: 'meta', text: line });
      continue;
    }
    if (line.startsWith('+')) {
      if (!current) continue;
      current.additions += 1;
      current.lines.push({ kind: 'add', text: line.slice(1) });
      continue;
    }
    if (line.startsWith('-')) {
      if (!current) continue;
      current.deletions += 1;
      current.lines.push({ kind: 'del', text: line.slice(1) });
      continue;
    }
    if (line.startsWith(' ')) {
      current?.lines.push({ kind: 'context', text: line.slice(1) });
      continue;
    }
    // `\ No newline at end of file`、index 行等杂项：有归属文件才保留展示。
    current?.lines.push({ kind: 'meta', text: line });
  }
  return files.filter((f) => f.path !== '');
}

export function diffTotalChanges(files: DiffFile[]): { additions: number; deletions: number } {
  return files.reduce(
    (acc, f) => ({ additions: acc.additions + f.additions, deletions: acc.deletions + f.deletions }),
    { additions: 0, deletions: 0 },
  );
}

/** 超阈值（>25 文件或 >2000 增删行）默认折叠。 */
export function shouldCollapseBySize(files: DiffFile[]): boolean {
  const { additions, deletions } = diffTotalChanges(files);
  return files.length > DIFF_COLLAPSE_FILES || additions + deletions > DIFF_COLLAPSE_LINES;
}

/**
 * 复制到剪贴板的纯文本形态：每文件先路径行，再按行加前缀（add `+ `、del `- `、context
 * 两个空格对齐 +/- 前缀宽），meta 行原样；文件间不额外空行。split 视图下复制的也是
 * unified 形态——复制物与当前视图解耦。
 */
export function diffCopyText(files: DiffFile[]): string {
  const rows: string[] = [];
  for (const f of files) {
    rows.push(f.path);
    for (const line of f.lines) {
      if (line.kind === 'add') rows.push(`+ ${line.text}`);
      else if (line.kind === 'del') rows.push(`- ${line.text}`);
      else if (line.kind === 'context') rows.push(`  ${line.text}`);
      else rows.push(line.text);
    }
  }
  return rows.join('\n');
}

export type DiffView = 'unified' | 'split';

/** 窄视口下 split 不可用：一律回落 unified，不渲染双栏。 */
export function effectiveView(view: DiffView, narrow: boolean): DiffView {
  return narrow ? 'unified' : view;
}

export type SplitRow =
  | { kind: 'meta'; text: string }
  | { kind: 'pair'; left: DiffLine | null; right: DiffLine | null };

/**
 * 单文件 lines → split 双栏行集：hunk 内连续的 -/+ 变更块按出现顺序逐位配对
 * （`-a -b / +x` → (a,x) + (b,∅)），多出侧单列展示；上下文行双侧同文。
 * 任何 meta 行（@@ 头与 `\ No newline` 标记）都是配对边界：配对绝不跨 hunk/标记。
 */
export function toSplitHunks(file: DiffFile): SplitRow[] {
  const rows: SplitRow[] = [];
  let dels: DiffLine[] = [];
  let adds: DiffLine[] = [];
  const flush = () => {
    for (let i = 0; i < Math.max(dels.length, adds.length); i++) {
      rows.push({ kind: 'pair', left: dels[i] ?? null, right: adds[i] ?? null });
    }
    dels = [];
    adds = [];
  };
  for (const line of file.lines) {
    if (line.kind === 'add') adds.push(line);
    else if (line.kind === 'del') dels.push(line);
    else {
      flush();
      if (line.kind === 'meta') rows.push({ kind: 'meta', text: line.text });
      else rows.push({ kind: 'pair', left: line, right: line });
    }
  }
  flush();
  return rows;
}

/** unified 展平渲染行：文件头行与 diff 行各占一行；头尾帽按这个一维序列切（跨文件计）。 */
export type FlatRow =
  | { kind: 'file-head'; file: DiffFile }
  | { kind: 'line'; file: DiffFile; line: DiffLine };

/** 按渲染顺序把所有文件的行展平成一维序列（文件头行先于该文件的 diff 行）。 */
export function flattenUnifiedRows(files: DiffFile[]): FlatRow[] {
  const rows: FlatRow[] = [];
  for (const file of files) {
    rows.push({ kind: 'file-head', file });
    for (const line of file.lines) rows.push({ kind: 'line', file, line });
  }
  return rows;
}

export interface UnifiedCap {
  hidden: number;
  capped: boolean;
  head: number;
  tail: number;
}

/**
 * 头尾帽切片参数：超帽且未展开时 capped，渲染头 ceil(16/2)=8 行 + 尾 8 行，hidden 为中段
 * 藏掉的行数；未超帽或已展开时整段直出（head=total、tail=0），hidden 仍非零——「收起」
 * 按钮靠它保留。
 */
export function capUnifiedRows(total: number, expanded: boolean): UnifiedCap {
  const hidden = Math.max(0, total - DIFF_BODY_MAX_LINES);
  const capped = hidden > 0 && !expanded;
  const head = Math.ceil(DIFF_BODY_MAX_LINES / 2);
  return { hidden, capped, head: capped ? head : total, tail: capped ? DIFF_BODY_MAX_LINES - head : 0 };
}

/** 头尾帽作用后的可见行两段（折叠按钮渲染在两段之间）；未戴帽时 tail 段为空。 */
export function visibleUnifiedRows(
  files: DiffFile[],
  expanded: boolean,
): { cap: UnifiedCap; headRows: FlatRow[]; tailRows: FlatRow[] } {
  const rows = flattenUnifiedRows(files);
  const cap = capUnifiedRows(rows.length, expanded);
  return {
    cap,
    headRows: rows.slice(0, cap.head),
    tailRows: cap.tail > 0 ? rows.slice(rows.length - cap.tail) : [],
  };
}

/**
 * 视口 <640px 判定（matchMedia，Tailwind sm 断点之下）。只看视口宽、不响应父容器
 * 收缩——diff 卡所在聊天栏随视口伸缩，容器级监听（ResizeObserver）复杂度不值当。
 */
function useNarrowViewport(): boolean {
  const [narrow, setNarrow] = useState(false);
  useEffect(() => {
    if (typeof window === 'undefined' || !window.matchMedia) return;
    const mq = window.matchMedia('(max-width: 639px)');
    const update = () => setNarrow(mq.matches);
    update();
    mq.addEventListener('change', update);
    return () => mq.removeEventListener('change', update);
  }, []);
  return narrow;
}

function lineClass(kind: DiffLine['kind']): string {
  if (kind === 'add') return 'chat-diff-line chat-diff-add';
  if (kind === 'del') return 'chat-diff-line chat-diff-del';
  if (kind === 'meta') return 'chat-diff-line chat-diff-meta';
  return 'chat-diff-line';
}

function linePrefix(kind: DiffLine['kind']): string {
  if (kind === 'add') return '+';
  if (kind === 'del') return '−';
  return kind === 'meta' ? '' : ' ';
}

/** split 单元格：空侧渲染无底色占位，保持与另一列同行高对齐。 */
function SplitCell({ line, right }: { line: DiffLine | null; right?: boolean }) {
  const kind: DiffLine['kind'] = line?.kind ?? 'context';
  return (
    <div className={right ? `${lineClass(kind)} chat-diff-right` : lineClass(kind)}>
      <span className="chat-diff-prefix">{linePrefix(kind)}</span>
      <span className="chat-diff-text">{line?.text}</span>
    </div>
  );
}

/** 文件头行：路径 + 该文件 +N/−M 徽章（unified 展平与 split 嵌套两条渲染路径共用）。 */
function FileHead({ file }: { file: DiffFile }) {
  return (
    <div className="chat-diff-file-head">
      <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-text-primary" title={file.path}>
        {file.path}
      </span>
      <span className="flex shrink-0 items-center gap-1 tabular-nums">
        <span className="text-[11px] text-status-success">+{file.additions}</span>
        <span className="text-[11px] text-status-error">−{file.deletions}</span>
      </span>
    </div>
  );
}

/**
 * 展平行的按文件聚组渲染。切片把某文件全部行藏进中段时该组整组不产出（文件头随之
 * 不渲染）；tail 起点落在文件中间时不补文件头——diff 行自带 +/− 前缀语义，裸行可读
 * （与 DSH SearchBlock 的补头策略不同，此处有意接受）。
 */
function UnifiedGroups({ rows }: { rows: FlatRow[] }) {
  const groups: { file: DiffFile; head: boolean; lines: DiffLine[] }[] = [];
  for (const row of rows) {
    const last = groups[groups.length - 1];
    if (row.kind === 'file-head') groups.push({ file: row.file, head: true, lines: [] });
    else if (last?.file === row.file) last.lines.push(row.line);
    else groups.push({ file: row.file, head: false, lines: [row.line] });
  }
  return (
    <>
      {groups.map((g, i) => (
        <div key={i} className="chat-diff-file">
          {g.head && <FileHead file={g.file} />}
          <div className="chat-diff-body">
            {g.lines.map((l, j) => (
              <div key={j} className={lineClass(l.kind)}>
                <span className="chat-diff-prefix">{linePrefix(l.kind)}</span>
                <span className="chat-diff-text">{l.text}</span>
              </div>
            ))}
          </div>
        </div>
      ))}
    </>
  );
}

/**
 * 工具输出里的 unified diff 渲染卡。text 应为原始输出（内部做控制字符过滤）；
 * 解析不出任何文件时返回 null（调用方回落等宽 pre）。
 * 视图模式与头尾帽展开态均为组件本地状态：不入 URL / store，随卡实例存续。
 */
export function DiffCard({ text }: { text: string }) {
  const files = useMemo(() => parseUnifiedDiff(stripControlChars(text)), [text]);
  const [open, setOpen] = useState(() => (files.length ? !shouldCollapseBySize(files) : false));
  const [view, setView] = useState<DiffView>('unified');
  const [expanded, setExpanded] = useState(false);
  const [copied, setCopied] = useState(false);
  const copyResetTimer = useRef<number | undefined>(undefined);
  const narrow = useNarrowViewport();
  const shown = effectiveView(view, narrow);
  const splitRows = useMemo(
    () => (shown === 'split' ? files.map((f) => toSplitHunks(f)) : null),
    [files, shown],
  );
  // 头尾帽只作用于 unified：split 的配对行数与双栏网格没有自然的「一行」切法，不戴帽（复杂度取舍）。
  const unified = useMemo(() => visibleUnifiedRows(files, expanded), [files, expanded]);

  useEffect(() => () => window.clearTimeout(copyResetTimer.current), []);

  /** clipboard 不可用或写入被拒时静默：不置 copied（复制失败即无反馈，不做 execCommand 兜底）。 */
  async function onCopy() {
    if (copied || !navigator.clipboard?.writeText) return;
    try {
      await navigator.clipboard.writeText(diffCopyText(files));
    } catch {
      return;
    }
    setCopied(true);
    window.clearTimeout(copyResetTimer.current);
    copyResetTimer.current = window.setTimeout(() => setCopied(false), COPY_FEEDBACK_MS);
  }

  if (!files.length) return null;
  const { additions, deletions } = diffTotalChanges(files);
  const { cap, headRows, tailRows } = unified;
  return (
    <div className="chat-diff">
      <div className="chat-diff-head">
        <button className="chat-diff-summary" onClick={() => setOpen((v) => !v)} aria-expanded={open}>
          {open ? <ChevronDown className="h-3.5 w-3.5 shrink-0" /> : <ChevronRight className="h-3.5 w-3.5 shrink-0" />}
          <FileDiff className="h-3.5 w-3.5 shrink-0 text-text-tertiary" />
          <span className="font-medium">{files.length} 个文件变更</span>
          <span className="ml-auto flex shrink-0 items-center gap-1.5 tabular-nums">
            <span className="rounded bg-status-success/15 px-1 text-[11px] text-status-success">+{additions}</span>
            <span className="rounded bg-status-error/15 px-1 text-[11px] text-status-error">−{deletions}</span>
          </span>
        </button>
        {/* 复制按钮须在 summary toggle 之外（button 套 button 是非法 HTML），故紧随其后落在徽章组右侧。 */}
        <button
          type="button"
          aria-label={copied ? '已复制' : '复制 diff'}
          title={copied ? '已复制' : '复制 diff'}
          onClick={() => void onCopy()}
          className="ml-0.5 mr-1 shrink-0 self-center rounded p-1 text-text-tertiary transition-colors hover:bg-black/[0.06] hover:text-text-primary"
        >
          {copied ? <Check className="h-3.5 w-3.5 text-status-success" /> : <Copy className="h-3.5 w-3.5" />}
        </button>
        {open && !narrow && (
          <div className="chat-diff-toolbar" role="group" aria-label="Diff 视图">
            <button
              type="button"
              className="chat-diff-toggle"
              aria-pressed={shown === 'unified'}
              onClick={() => setView('unified')}
            >
              Unified
            </button>
            <button
              type="button"
              className="chat-diff-toggle"
              aria-pressed={shown === 'split'}
              onClick={() => setView('split')}
            >
              Split
            </button>
          </div>
        )}
      </div>
      {open && (
        <>
          <div className="chat-diff-files">
            {splitRows ? (
              files.map((f, fi) => (
                <div key={`${f.path}-${f.additions}-${f.deletions}`} className="chat-diff-file">
                  <FileHead file={f} />
                  <div className="chat-diff-body">
                    <div className="chat-diff-split">
                      {splitRows[fi].map((row, i) =>
                        row.kind === 'meta' ? (
                          <div key={i} className="chat-diff-line chat-diff-meta chat-diff-span">
                            <span className="chat-diff-prefix" />
                            <span className="chat-diff-text">{row.text}</span>
                          </div>
                        ) : (
                          <Fragment key={i}>
                            <SplitCell line={row.left} />
                            <SplitCell line={row.right} right />
                          </Fragment>
                        ),
                      )}
                    </div>
                  </div>
                </div>
              ))
            ) : (
              <>
                <UnifiedGroups rows={headRows} />
                {cap.hidden > 0 && (
                  <button
                    type="button"
                    className="w-full px-2.5 py-1 text-left font-mono text-[11px] text-text-tertiary transition-colors hover:bg-surface-sunken hover:text-text-secondary"
                    aria-expanded={expanded}
                    aria-label={expanded ? '收起差异' : `展开其余 ${cap.hidden} 行差异`}
                    onClick={() => setExpanded((v) => !v)}
                  >
                    {expanded ? '收起' : `… 其余 ${cap.hidden} 行`}
                  </button>
                )}
                <UnifiedGroups rows={tailRows} />
              </>
            )}
          </div>
          {/* 页脚汇总与卡头徽章信息重复是有意的（DSH 语义：页脚是 diff 的一部分）。 */}
          <div className="px-2.5 py-1 font-mono text-[11px] tabular-nums text-text-tertiary">
            └ +{additions} −{deletions} · {files.length} 个文件
          </div>
        </>
      )}
    </div>
  );
}

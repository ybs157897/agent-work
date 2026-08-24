import { useMemo, useState } from 'react';
import { ChevronDown, ChevronRight, FileDiff } from 'lucide-react';

/**
 * Unified diff 卡（对齐 codex-desktop 渲染对比 Q6 的 Web 子集）：
 * 文件头（路径 + +N/−M 徽章）+ 行底色（增删用 status tokens）；
 * >25 文件或 >2000 增删行默认折叠只显摘要。
 * 不做 split 模式 / 行内评论 / merge conflict 采纳（文档列为桌面高级特性）。
 */

/** 折叠阈值：文件数 ≤25 且增删行 ≤2000 时默认展开（对齐 codex editor-diff 常量）。 */
export const DIFF_COLLAPSE_FILES = 25;
export const DIFF_COLLAPSE_LINES = 2000;

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

/**
 * 工具输出里的 unified diff 渲染卡。text 应为原始输出（内部做控制字符过滤）；
 * 解析不出任何文件时返回 null（调用方回落等宽 pre）。
 */
export function DiffCard({ text }: { text: string }) {
  const files = useMemo(() => parseUnifiedDiff(stripControlChars(text)), [text]);
  const [open, setOpen] = useState(() => (files.length ? !shouldCollapseBySize(files) : false));
  if (!files.length) return null;
  const { additions, deletions } = diffTotalChanges(files);
  return (
    <div className="chat-diff">
      <button className="chat-diff-summary" onClick={() => setOpen((v) => !v)} aria-expanded={open}>
        {open ? <ChevronDown className="h-3.5 w-3.5 shrink-0" /> : <ChevronRight className="h-3.5 w-3.5 shrink-0" />}
        <FileDiff className="h-3.5 w-3.5 shrink-0 text-text-tertiary" />
        <span className="font-medium">{files.length} 个文件变更</span>
        <span className="ml-auto flex shrink-0 items-center gap-1.5 tabular-nums">
          <span className="rounded bg-status-success/15 px-1 text-[11px] text-status-success">+{additions}</span>
          <span className="rounded bg-status-error/15 px-1 text-[11px] text-status-error">−{deletions}</span>
        </span>
      </button>
      {open && (
        <div className="chat-diff-files">
          {files.map((f) => (
            <div key={`${f.path}-${f.additions}-${f.deletions}`} className="chat-diff-file">
              <div className="chat-diff-file-head">
                <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-text-primary" title={f.path}>
                  {f.path}
                </span>
                <span className="flex shrink-0 items-center gap-1 tabular-nums">
                  <span className="text-[11px] text-status-success">+{f.additions}</span>
                  <span className="text-[11px] text-status-error">−{f.deletions}</span>
                </span>
              </div>
              <div className="chat-diff-body">
                {f.lines.map((l, i) => (
                  <div key={i} className={lineClass(l.kind)}>
                    <span className="chat-diff-prefix">{linePrefix(l.kind)}</span>
                    <span className="chat-diff-text">{l.text}</span>
                  </div>
                ))}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

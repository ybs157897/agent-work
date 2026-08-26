import { Check, Copy } from 'lucide-react';
import { motion, useReducedMotion } from 'motion/react';
import { useEffect, useRef, useState, type ReactNode } from 'react';
import { writeClipboard } from './blocks/clipboard';

/** 复制成功后 Check 反馈保持的时长（对齐 use-copy-feedback 的 1s）。 */
const COPY_FEEDBACK_MS = 1000;

/** 行单元格数组 → TSV 文本：`\t` 拼列、`\n` 拼行；空单元格保留占位（连续分隔符）。 */
export function tableToTsv(rows: string[][]): string {
  return rows.map((cells) => cells.join('\t')).join('\n');
}

/**
 * markdown 表格的容器替身（对齐 Codex TableContainer 思路）：`div.chat-table-wrap`
 * 横向滚动 + 悬停浮现复制按钮，复制内容为表格 DOM 抽出的 TSV。
 * children 是 react-markdown 渲出的 thead/tbody。
 */
export function TableCard({ children }: { children?: ReactNode }) {
  const tableRef = useRef<HTMLTableElement>(null);
  const [copied, setCopied] = useState(false);
  const reduceMotion = useReducedMotion();
  const copyResetTimer = useRef<number | undefined>(undefined);

  useEffect(() => () => window.clearTimeout(copyResetTimer.current), []);

  async function onCopy() {
    const table = tableRef.current;
    if (!table) return;
    if (!(await writeClipboard(tableToTsv(readTsvRows(table))))) return;
    setCopied(true);
    window.clearTimeout(copyResetTimer.current);
    copyResetTimer.current = window.setTimeout(() => setCopied(false), COPY_FEEDBACK_MS);
  }

  return (
    <motion.div
      className="chat-table-wrap group"
      initial={reduceMotion ? false : { opacity: 0, y: 5 }}
      animate={{ opacity: 1, y: 0 }}
      transition={reduceMotion ? { duration: 0 } : { duration: 0.28, ease: [0.16, 1, 0.3, 1] }}
    >
      <motion.button
        type="button"
        aria-label="复制表格"
        title={copied ? '已复制' : '复制表格'}
        onClick={() => void onCopy()}
        className="absolute right-0 top-0 z-10 inline-flex h-7 w-7 items-center justify-center rounded-button bg-surface-raised/85 text-text-tertiary opacity-0 shadow-card transition-opacity hover:bg-surface-sunken hover:text-text-primary group-hover:opacity-100 group-focus-within:opacity-100"
        whileHover={reduceMotion ? undefined : { scale: 1.06, y: -1 }}
        whileTap={reduceMotion ? undefined : { scale: 0.95 }}
      >
        {copied ? <Check className="h-3.5 w-3.5 text-status-success" /> : <Copy className="h-3.5 w-3.5" />}
      </motion.button>
      <table ref={tableRef}>{children}</table>
    </motion.div>
  );
}

/** 表格 DOM → 行×单元格文本；单元格内换行/制表压成空格，保证 TSV 行列结构不被内容破坏。 */
function readTsvRows(table: HTMLTableElement): string[][] {
  return Array.from(table.querySelectorAll('tr'), (tr) =>
    Array.from(tr.querySelectorAll<HTMLElement>('th, td'), (cell) =>
      cell.innerText.replace(/[\t\n\r]+/g, ' ').trim(),
    ),
  );
}

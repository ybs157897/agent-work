import { Check, Copy, GitBranch } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { formatTime } from '../../utils/format';
import { writeClipboard } from './blocks/clipboard';

const COPY_FEEDBACK_MS = 1_000;

/**
 * 消息级悬停操作行（复制 + 时钟）：默认 opacity-0，由父级容器（group 类）hover 显现。
 * side='left' 时钟在动作前（assistant），side='right' 在动作后（user 气泡）。
 */
export function MessageActions({
  text,
  at,
  side,
  className,
  onFork,
}: {
  /** 复制到剪贴板的原文。 */
  text: string;
  /** 消息时间戳；undefined 不显时钟。 */
  at?: string;
  side: 'left' | 'right';
  className?: string;
  /** 分叉对话入口（可选）：从本条消息截取上下文创建分叉会话。 */
  onFork?: () => void;
}) {
  const [copied, setCopied] = useState(false);
  const resetTimer = useRef<number | undefined>(undefined);

  useEffect(() => () => window.clearTimeout(resetTimer.current), []);

  async function onCopy() {
    if (copied) return;
    if (!(await writeClipboard(text))) return;
    setCopied(true);
    window.clearTimeout(resetTimer.current);
    resetTimer.current = window.setTimeout(() => setCopied(false), COPY_FEEDBACK_MS);
  }

  const clock =
    at === undefined ? null : (
      <span className="text-caption tabular-nums text-text-tertiary">{formatTime(at)}</span>
    );

  return (
    <div
      className={`flex items-center justify-end gap-1 opacity-0 transition-opacity group-hover:opacity-100 ${
        className ?? ''
      }`}
    >
      {side === 'left' ? clock : null}
      <button
        type="button"
        aria-label={copied ? '已复制' : '复制'}
        title={copied ? '已复制' : '复制'}
        onClick={() => void onCopy()}
        className="rounded p-0.5 text-text-tertiary transition-colors hover:text-text-primary"
      >
        {copied ? (
          <Check className="h-3.5 w-3.5 text-status-success" />
        ) : (
          <Copy className="h-3.5 w-3.5" />
        )}
      </button>
      {onFork ? (
        <button
          type="button"
          aria-label="分叉对话"
          title="分叉对话"
          onClick={onFork}
          className="rounded p-0.5 text-text-tertiary transition-colors hover:text-text-primary"
        >
          <GitBranch className="h-3.5 w-3.5" />
        </button>
      ) : null}
      {side === 'right' ? clock : null}
    </div>
  );
}

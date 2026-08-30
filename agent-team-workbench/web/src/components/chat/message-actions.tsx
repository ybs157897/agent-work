import { Check, Copy, GitBranch } from 'lucide-react';
import { useEffect, useRef, useState, type ReactNode } from 'react';
import { writeClipboard } from './blocks/clipboard';

const COPY_FEEDBACK_MS = 1_000;

/**
 * 消息级操作行：细指针默认弱显，父级 hover/focus 时完整显现。
 */
export function MessageActions({
  text,
  className,
  onFork,
  children,
}: {
  /** 复制到剪贴板的原文。 */
  text: string;
  className?: string;
  /** 分叉对话入口（可选）：从本条消息截取上下文创建分叉会话。 */
  onFork?: () => void;
  /** 消息域专属动作（可选），排在通用动作之前（如用户消息的「钉为决策」）。 */
  children?: ReactNode;
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

  return (
    <div
      className={`chat-msg-actions flex items-center justify-end gap-1 ${
        className ?? ''
      }`}
    >
      {children}
      <button
        type="button"
        aria-label={copied ? '已复制' : '复制'}
        title={copied ? '已复制' : '复制'}
        onClick={() => void onCopy()}
        className="inline-flex h-7 w-7 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-surface-sunken hover:text-text-primary"
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
          className="inline-flex h-7 w-7 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-surface-sunken hover:text-text-primary"
        >
          <GitBranch className="h-3.5 w-3.5" />
        </button>
      ) : null}
    </div>
  );
}

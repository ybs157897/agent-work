import { useCallback, useState } from 'react';
import { writeClipboard } from './clipboard';

/** 写入成功后 copied 保持为 true 的时长（ms）。 */
const COPIED_FEEDBACK_MS = 1000;

/** 复制反馈 hook 的返回：瞬时标志与复制处理器。 */
export interface CopyFeedback {
  /** 写入成功后为 true，持续 1000ms；调用方据此渲染「已复制」类提示。 */
  copied: boolean;
  /** 触发复制；copied 仍为 true 期间不重复写，写入被拒绝时保持静默。 */
  onCopy: () => void;
}

/**
 * 复制 text 到剪贴板并附带一秒成功反馈：写入被宿主拒绝时不翻转标志，
 * 控件永远不会宣称一次并未发生的复制。
 */
export function useCopyFeedback(text: string): CopyFeedback {
  const [copied, setCopied] = useState(false);
  const onCopy = useCallback(() => {
    if (copied) return;
    void writeClipboard(text).then((ok) => {
      if (!ok) return;
      setCopied(true);
      window.setTimeout(() => {
        setCopied(false);
      }, COPIED_FEEDBACK_MS);
    });
  }, [copied, text]);
  return { copied, onCopy };
}

/** Codex thinking-placeholder：正文/计划之后、turn 进行中的 shimmer 等待行。 */
export function ThinkingPlaceholder() {
  return (
    <div className="chat-thinking-placeholder flex items-center gap-2 py-2 text-caption text-text-tertiary" aria-label="Thinking">
      <span className="chat-thinking-sweep relative flex h-3.5 w-3.5 shrink-0 items-center justify-center overflow-hidden rounded-full">
        <span className="chat-thinking-sweep-inner" aria-hidden />
      </span>
      <span className="font-normal text-text-secondary">Thinking</span>
      <span className="chat-thinking-shimmer min-w-0 flex-1 truncate">正在生成回答…</span>
    </div>
  );
}

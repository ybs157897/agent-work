import { ChevronDown, ChevronRight } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';

/** Codex 风格「思考过程」灰底可滚动面板（每条 thinking 独立实例，折叠互不影响）。 */
export function ReasoningProcessPanel({
  panelKey,
  text,
  streaming = false,
  defaultExpanded = true,
}: {
  /** 稳定 id（msg.key）；用于 React key 隔离各段折叠态 */
  panelKey: string;
  text: string;
  streaming?: boolean;
  /** 落定后默认是否展开 */
  defaultExpanded?: boolean;
}) {
  const [expanded, setExpanded] = useState(defaultExpanded);
  const bodyRef = useRef<HTMLDivElement>(null);
  const open = expanded || streaming;
  const showBody = open && Boolean(text);

  useEffect(() => {
    setExpanded(defaultExpanded);
  }, [panelKey, defaultExpanded]);

  useEffect(() => {
    if (!streaming || !showBody) return;
    const el = bodyRef.current;
    if (!el) return;
    const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 48;
    if (!nearBottom) return;
    const id = requestAnimationFrame(() => {
      el.scrollTop = el.scrollHeight;
    });
    return () => cancelAnimationFrame(id);
  }, [text, streaming, showBody]);

  if (!text && !streaming) return null;

  const subtitle = streaming ? '思考中…' : '持续了几秒';

  return (
    <div className="chat-reasoning-panel" data-streaming={streaming ? 'true' : undefined} data-panel-key={panelKey}>
      <button
        type="button"
        className="chat-reasoning-panel-head"
        onClick={() => {
          if (streaming) return;
          setExpanded((v) => !v);
        }}
        aria-expanded={open}
        aria-controls={`reasoning-body-${panelKey}`}
      >
        {open ? (
          <ChevronDown className="h-3.5 w-3.5 shrink-0 text-text-tertiary" />
        ) : (
          <ChevronRight className="h-3.5 w-3.5 shrink-0 text-text-tertiary" />
        )}
        <span className="font-medium text-text-secondary">思考过程</span>
        <span className="text-text-tertiary">{subtitle}</span>
        {!streaming && (
          <ChevronDown
            className={`ml-auto h-3.5 w-3.5 shrink-0 text-text-tertiary transition-transform ${open ? 'rotate-180' : ''}`}
            aria-hidden
          />
        )}
      </button>
      <div
        id={`reasoning-body-${panelKey}`}
        ref={bodyRef}
        className="chat-reasoning-panel-body"
        hidden={!showBody}
        aria-hidden={!showBody}
      >
        {streaming && (
          <span className="chat-reasoning-sweep pointer-events-none absolute inset-0 rounded-md" aria-hidden />
        )}
        <div className="relative whitespace-pre-wrap break-words text-caption leading-6 text-text-secondary">
          {text}
        </div>
      </div>
    </div>
  );
}

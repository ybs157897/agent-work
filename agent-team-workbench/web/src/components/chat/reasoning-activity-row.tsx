import { ChevronDown } from 'lucide-react';
import { AnimatePresence, motion, useReducedMotion } from 'motion/react';
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
  const reduceMotion = useReducedMotion();
  const bodyRef = useRef<HTMLDivElement>(null);
  // 流式默认展开，但任何时候都允许手动折叠——长思考期间面板是用户
  // 唯一可控的扫描密度阀门（对齐 Ant Design X ThoughtChain 的头行折叠交互）。
  const open = expanded;
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

  return (
    <div className="chat-reasoning-panel" data-streaming={streaming ? 'true' : undefined} data-panel-key={panelKey}>
      <button
        type="button"
        className="chat-reasoning-panel-head"
        onClick={() => setExpanded((v) => !v)}
        aria-expanded={open}
        aria-controls={showBody ? `reasoning-body-${panelKey}` : undefined}
      >
        <span className="font-medium text-text-secondary">思考过程</span>
        {streaming && <span className="text-text-tertiary">思考中…</span>}
        <ChevronDown
          className={`ml-auto h-3.5 w-3.5 shrink-0 text-text-tertiary transition-transform ${open ? 'rotate-180' : ''}`}
          aria-hidden
        />
      </button>
      <AnimatePresence initial={false} mode="sync">
        {showBody && (
          <motion.div
            id={`reasoning-body-${panelKey}`}
            ref={bodyRef}
            className="chat-reasoning-panel-body"
            initial={reduceMotion ? false : { opacity: 0, height: 0, y: -4 }}
            animate={{ opacity: 1, height: 'auto', y: 0 }}
            exit={reduceMotion ? { opacity: 0 } : { opacity: 0, height: 0, y: -4 }}
            transition={reduceMotion ? { duration: 0 } : { duration: 0.26, ease: [0.16, 1, 0.3, 1] }}
            aria-hidden={false}
          >
            {streaming && (
              <span className="chat-reasoning-sweep pointer-events-none absolute rounded-md" aria-hidden />
            )}
            <div className="relative whitespace-pre-wrap break-words text-caption leading-6 text-text-secondary">
              {text}
            </div>
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  );
}

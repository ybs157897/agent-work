import { Brain, ChevronRight } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';

function firstLine(text: string): string {
  const newline = text.indexOf('\n');
  return newline === -1 ? text : text.slice(0, newline);
}

function latestLine(text: string): string {
  const visible = text.trimEnd();
  const newline = visible.lastIndexOf('\n');
  return newline === -1 ? visible : visible.slice(newline + 1);
}

/** 思考过程折叠区（对齐 DSH ReasoningRow / agent-chat .ac-reasoning）。 */
export function ReasoningDisclosure({
  text,
  streaming = false,
}: {
  text: string;
  streaming?: boolean;
}) {
  const [expanded, setExpanded] = useState(false);
  const summaryRef = useRef<HTMLSpanElement>(null);
  const preview = streaming ? latestLine(text) : firstLine(text);

  useEffect(() => {
    const el = summaryRef.current;
    if (!el) return;
    el.scrollLeft = streaming ? el.scrollWidth - el.clientWidth : 0;
  }, [streaming, preview]);

  if (!text && !streaming) return null;

  return (
    <div
      className="chat-reasoning mb-4"
      data-state={streaming ? 'running' : 'ok'}
    >
      <button
        type="button"
        className="chat-reasoning-toggle relative flex w-full cursor-pointer items-center gap-2 overflow-hidden border-0 bg-transparent p-0 text-left text-caption text-text-tertiary"
        aria-expanded={expanded}
        onClick={() => setExpanded((v) => !v)}
      >
        {streaming && <span className="chat-reasoning-sweep" aria-hidden />}
        <Brain className="h-3.5 w-3.5 shrink-0 opacity-70" />
        <span className="font-normal text-text-secondary">Think</span>
        <span className="h-0.5 w-0.5 shrink-0 rounded-full bg-text-tertiary" aria-hidden />
        <span
          ref={summaryRef}
          className={`min-w-0 flex-1 overflow-hidden text-text-tertiary ${streaming ? 'whitespace-nowrap' : 'truncate'}`}
        >
          {text ? preview || '正在生成推理…' : '正在思考…'}
        </span>
        {streaming && <span className="shrink-0 text-text-tertiary">生成中</span>}
        <ChevronRight
          className={`h-3.5 w-3.5 shrink-0 opacity-60 transition-transform ${expanded ? 'rotate-90' : ''}`}
        />
      </button>
      {expanded && text ? (
        <div className="chat-reasoning-body max-h-[50vh] overflow-y-auto whitespace-pre-wrap break-words pl-[22px] pt-1 text-caption leading-6 text-text-tertiary">
          {text}
        </div>
      ) : expanded && streaming ? (
        <p className="pt-1 pl-[22px] text-caption text-text-tertiary">OpenRouter 模型可能需 20–40 秒</p>
      ) : null}
    </div>
  );
}

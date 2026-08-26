import { useEffect, useId, useState } from "react";

// 正文恒在 tx 暗色作用域内（见 notes tx-transcript-standalone-skin）：dark 主题
// 避免亮色图表在墨底上成为白岛；最终适配若回水墨基线，此处同步改回 "default"。
const MERMAID_THEME = "dark";
const RENDER_DEBOUNCE_MS = 300;
const svgCache = new Map<string, string>();

export function hashCode(value: string): string {
  let hash = 0;
  for (let index = 0; index < value.length; index += 1) {
    hash = ((hash << 5) - hash + value.charCodeAt(index)) | 0;
  }
  return `mmd${Math.abs(hash).toString(36)}`;
}

export function mermaidCacheKey(source: string, theme = MERMAID_THEME): string {
  return `${source}\0${theme}`;
}

/** Lazy Mermaid keeps normal chat messages free of the diagram bundle and DOM work. */
export function MermaidDiagram({ source }: { source: string }) {
  const [svg, setSvg] = useState<string | null>(null);
  const [error, setError] = useState(false);
  const uniqueId = useId().replace(/:/g, "_");

  useEffect(() => {
    let cancelled = false;
    const key = mermaidCacheKey(source);
    setSvg(svgCache.get(key) ?? null);
    setError(false);
    const timer = window.setTimeout(() => {
      void (async () => {
        try {
          const [{ default: mermaid }, { default: DOMPurify }] =
            await Promise.all([import("mermaid"), import("dompurify")]);
          const cached = svgCache.get(key);
          if (cached) {
            if (!cancelled) setSvg(cached);
            return;
          }
          mermaid.initialize({
            startOnLoad: false,
            securityLevel: "strict",
            theme: MERMAID_THEME,
          });
          const rendered = await mermaid.render(
            `${uniqueId}-${hashCode(source)}`,
            source,
          );
          const safeSvg = DOMPurify.sanitize(rendered.svg, {
            USE_PROFILES: { svg: true, svgFilters: true },
          });
          svgCache.set(key, safeSvg);
          if (!cancelled) setSvg(safeSvg);
        } catch {
          if (!cancelled) setError(true);
        }
      })();
    }, RENDER_DEBOUNCE_MS);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [source, uniqueId]);

  if (error) {
    return (
      <div className="chat-mermaid-error">
        <span>图表渲染失败，显示 Mermaid 源码</span>
        <pre className="chat-mermaid-fallback">{source}</pre>
      </div>
    );
  }
  if (!svg) {
    return (
      <div
        className="chat-mermaid-loading"
        role="status"
        aria-label="正在渲染图表"
      >
        正在渲染图表…
      </div>
    );
  }
  return (
    <div className="chat-mermaid" dangerouslySetInnerHTML={{ __html: svg }} />
  );
}

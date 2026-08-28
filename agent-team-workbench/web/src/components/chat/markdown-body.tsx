import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import remarkMath from "remark-math";
import rehypeKatex from "rehype-katex";
import { CodeBlock, languageFromClassName, parseCodeFenceMeta } from "./code-block";
import { MarkdownErrorBoundary } from "./error-boundary";
import { MermaidDiagram } from "./mermaid-diagram";
import { TableCard } from "./table-card";
import { isSafeContentUrl } from "../../utils/content-blocks";
import { LanguageGuiFence } from "./content-blocks/languagegui-fence";
import { splitStreamingMarkdownBlocks, type StreamingMarkdownBlocks } from "../../utils/streaming-markdown";
import { isOutputTraceEnabled, outputTraceHash, traceOutputDeduped } from "../../utils/output-trace";

const STREAMING_PARSE_INTERVAL_MS = 100;
const useCommitEffect = typeof window === "undefined" ? useEffect : useLayoutEffect;

export function useThrottledMarkdown(text: string, streaming: boolean): string {
  const [value, setValue] = useState(text);
  const latest = useRef(text);
  const lastFlush = useRef(0);
  const timer = useRef<number | null>(null);
  useEffect(() => {
    latest.current = text;
    if (!streaming) {
      if (timer.current !== null) window.clearTimeout(timer.current);
      timer.current = null;
      setValue(text);
      return;
    }
    const elapsed = Date.now() - lastFlush.current;
    const flush = () => {
      lastFlush.current = Date.now();
      timer.current = null;
      setValue(latest.current);
    };
    if (elapsed >= STREAMING_PARSE_INTERVAL_MS) flush();
    else if (timer.current === null)
      timer.current = window.setTimeout(
        flush,
        STREAMING_PARSE_INTERVAL_MS - elapsed,
      );
  }, [text, streaming]);
  useEffect(
    () => () => {
      if (timer.current !== null) window.clearTimeout(timer.current);
    },
    [],
  );
  return streaming ? value : text;
}

export function stripThinkTags(text: string): string {
  return /<think>/i.test(text)
    ? text.replace(/<think>[\s\S]*?<\/think>/gi, "").replace(/^\n+/, "")
    : text;
}

export const CALLOUT_TITLES: Record<string, string> = {
  note: "Note",
  info: "Info",
  tip: "Tip",
  success: "Success",
  warning: "Warning",
  caution: "Caution",
  danger: "Danger",
  important: "Important",
};
export function normalizeCalloutType(raw: string): string {
  const type = raw.toLowerCase();
  return type in CALLOUT_TITLES ? type : "note";
}
export function isSafeMarkdownImageSource(source: string | undefined): boolean {
  const value = source?.trim() ?? "";
  if (!value || /^(?:(?:javascript|vbscript|file):|data:(?!image\/))/i.test(value))
    return false;
  return true;
}
function classNames(value: unknown): string[] {
  if (Array.isArray(value))
    return value.filter((item): item is string => typeof item === "string");
  return typeof value === "string" ? value.split(/\s+/).filter(Boolean) : [];
}
export type MdNode = {
  type: string;
  value?: string;
  lang?: string | null;
  meta?: string | null;
  children?: MdNode[];
  data?: { hName?: string; hProperties?: Record<string, unknown> };
};

/** Carries mdast code-fence meta through remark-rehype to the pre renderer. */
export function remarkFenceMeta() {
  return (tree: MdNode) => {
    const visit = (node: MdNode) => {
      if (node.type === 'code' && node.meta) {
        node.data ??= {};
        node.data.hProperties ??= {};
        node.data.hProperties['data-fence-meta'] = node.meta;
      }
      node.children?.forEach(visit);
    };
    visit(tree);
  };
}
function calloutNode(type: string, body: MdNode[]): MdNode {
  return {
    type: "blockquote",
    data: {
      hName: "div",
      hProperties: { className: ["chat-callout", `chat-callout-${type}`] },
    },
    children: [
      {
        type: "paragraph",
        data: {
          hName: "div",
          hProperties: { className: ["chat-callout-title"] },
        },
        children: [{ type: "text", value: CALLOUT_TITLES[type] }],
      },
      ...body,
    ],
  };
}

export function remarkCallouts() {
  return (tree: MdNode) => {
    if (!tree.children) return;
    const output: MdNode[] = [];
    for (let i = 0; i < tree.children.length; i += 1) {
      const node = tree.children[i]!;
      const first =
        node.type === "blockquote"
          ? node.children?.[0]?.children?.[0]
          : node.children?.[0];
      const firstValue = first?.type === "text" ? (first.value ?? "") : "";
      if (
        node.type === "blockquote" &&
        node.children?.[0]?.type === "paragraph"
      ) {
        const match = firstValue.match(/^\[!(\w+)\][ \t]*(?:\r?\n)?/);
        if (match) {
          first!.value = firstValue.slice(match[0].length);
          if (!first!.value) node.children[0]!.children?.shift();
          if (!node.children[0]!.children?.length) node.children.shift();
          output.push(
            calloutNode(normalizeCalloutType(match[1]!), node.children),
          );
          continue;
        }
      }
      if (node.type === "paragraph" && firstValue) {
        const open = firstValue.match(/^:::(\w+)[ \t]*(?:\r?\n)?/);
        if (open) {
          const type = normalizeCalloutType(open[1]!);
          const last = node.children?.[node.children.length - 1];
          if (
            last?.type === "text" &&
            /\r?\n?:::[ \t]*$/.test(last.value ?? "")
          ) {
            first!.value = firstValue.slice(open[0].length);
            last.value = (last.value ?? "").replace(/\r?\n?:::[ \t]*$/, "");
            output.push(calloutNode(type, [node]));
            continue;
          }
          const close = tree.children.findIndex(
            (candidate, index) =>
              index > i &&
              candidate.type === "paragraph" &&
              candidate.children?.length === 1 &&
              candidate.children[0]?.type === "text" &&
              candidate.children[0].value?.trim() === ":::",
          );
          if (close !== -1) {
            first!.value = firstValue.slice(open[0].length);
            const body: MdNode[] = [];
            const children = node.children ?? [];
            if (children.some((child) => child.value)) body.push(node);
            body.push(...tree.children.slice(i + 1, close));
            output.push(calloutNode(type, body));
            i = close;
            continue;
          }
        }
      }
      output.push(node);
    }
    tree.children = output;
  };
}

function extractCode(
  nodes?: Array<{ value?: string; children?: unknown[] }>,
): string {
  return (
    nodes
      ?.map(
        (node) =>
          node.value ??
          extractCode(
            node.children as
              | Array<{ value?: string; children?: unknown[] }>
              | undefined,
          ),
      )
      .join("") ?? ""
  );
}

export function MarkdownBody({
  text,
  streaming = false,
  runId,
  messageId,
}: {
  text: string;
  streaming?: boolean;
  runId?: string;
  messageId?: string;
}) {
  const parsedText = useThrottledMarkdown(stripThinkTags(text), streaming);
  const streamSlice = useMemo<StreamingMarkdownBlocks>(
    () => streaming
      ? splitStreamingMarkdownBlocks(parsedText)
      : { completedBlocks: [], currentBlock: parsedText, unsafePending: "" },
    [parsedText, streaming],
  );
  const visibleText = streamSlice.completedBlocks.join("") + streamSlice.currentBlock;
  const pendingText = streamSlice.unsafePending;
  const remarkPlugins = useMemo(
    () => [remarkGfm, remarkMath, remarkCallouts, remarkFenceMeta],
    [],
  );
  const rehypePlugins = useMemo(() => [rehypeKatex], []);
  const lastCommitTrace = useRef("");
  useCommitEffect(() => {
    if (!isOutputTraceEnabled()) return;
    const signature = `markdown.committed:${runId ?? ""}:${messageId ?? ""}:${streaming ? "streaming" : "final"}:${outputTraceHash(visibleText)}:${pendingText.length}`;
    if (lastCommitTrace.current === signature) return;
    lastCommitTrace.current = signature;
    traceOutputDeduped(signature, {
      stage: "markdown.committed",
      mode: streaming ? "streaming" : "final",
      source: "react-commit",
      text: visibleText,
      runId,
      messageId,
      projection: { pendingChars: pendingText.length },
      metadata: {
        sourceChars: parsedText.length,
        pendingKind: streamSlice.pendingKind ?? "none",
      },
    });
  }, [messageId, parsedText.length, pendingText.length, runId, streamSlice.pendingKind, streaming, visibleText]);
  const components = useMemo(
    () =>
      ({
        a: ({ href, children }: { href?: string; children?: ReactNode }) =>
          isSafeContentUrl(href) ? (
            <a href={href} target="_blank" rel="noreferrer noopener">
              {children}
            </a>
          ) : <span>{children}</span>,
        code: ({
          className,
          children,
        }: {
          className?: string;
          children?: ReactNode;
        }) => <code className={className}>{children}</code>,
        pre: ({
          children,
          node,
          ...rest
        }: {
          children?: ReactNode;
          node?: {
            children?: Array<{
              tagName?: string;
              properties?: Record<string, unknown>;
              children?: Array<{ value?: string }>;
            }>;
            data?: { meta?: string | null };
          };
          [key: string]: unknown;
        }) => {
          const code = node?.children?.[0];
          if (code?.tagName === "code") {
            const classes = classNames(code.properties?.className);
            const language = languageFromClassName(classes.join(" "));
            const meta = parseCodeFenceMeta(typeof code?.properties?.['data-fence-meta'] === 'string' ? code.properties['data-fence-meta'] : undefined);
            if (language === "languagegui" || language === "lgui") {
              const fallback = <CodeBlock>{children}</CodeBlock>;
              return (
                <LanguageGuiFence
                  source={extractCode(code.children).trim()}
                  fallback={fallback}
                  trace={{ runId, messageId, mode: streaming ? "streaming" : "final" }}
                />
              );
            }
            if (language === "mermaid")
              return (
                <MermaidDiagram source={extractCode(code.children).trim()} />
              );
            return <CodeBlock filename={meta.filename} title={meta.title} highlightedLines={meta.highlightedLines}>{children}</CodeBlock>;
          }
          return <pre {...rest}>{children}</pre>;
        },
        img: ({ src, alt }: { src?: string; alt?: string }) => {
          const value = src?.trim() ?? "";
          if (!isSafeMarkdownImageSource(value)) {
            return (
              <span className="chat-markdown-image-fallback">
                图片不可用{alt?.trim() ? `：${alt.trim()}` : ""}
              </span>
            );
          }
          return <img src={value} alt={alt ?? ""} loading="lazy" />;
        },
        table: ({ children }: { children?: ReactNode }) => (
          <TableCard>{children}</TableCard>
        ),
        input: ({
          type,
          checked,
          node: _node,
          ...props
        }: {
          type?: string;
          checked?: boolean;
          node?: unknown;
          [key: string]: unknown;
        }) => {
          void _node;
          return <input {...props} type={type} checked={checked} readOnly />;
        },
      }) as unknown as Components,
    [messageId, runId, streaming],
  );
  const markdownBlocks = streaming
    ? [...streamSlice.completedBlocks, streamSlice.currentBlock].filter((block) => block.length > 0)
    : [visibleText];
  if (markdownBlocks.length === 0) return null;
  return (
    <>
      {markdownBlocks.map((block, index) => (
        <MarkdownErrorBoundary
          key={streaming ? `markdown-block-${index === markdownBlocks.length - 1 ? "current" : index}` : "markdown-final"}
          resetKey={block}
          fallback={<pre className="whitespace-pre-wrap break-words">{block}</pre>}
        >
          <ReactMarkdown
            remarkPlugins={remarkPlugins}
            rehypePlugins={rehypePlugins}
            components={components}
          >
            {block}
          </ReactMarkdown>
        </MarkdownErrorBoundary>
      ))}
    </>
  );
}

import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { nodeText, CodeBlock } from './code-block';
import { MarkdownErrorBoundary } from './error-boundary';
import { TableCard } from './table-card';
import { InkReveal } from '../ink/ink-reveal';
import { TextGenerateEffect } from '../aceternity/text-generate-effect';

/** 汉字脚本检测：命中中日韩使用的汉字；谚文/假名不属于 Script=Han，不在此规则内。 */
const HAN_SCRIPT = /\p{Script=Han}/u;

/** 段落是否含汉字：命中时 p 打 data-markdown-han-text，配合 index.css 的 CJK 连续段距规则。 */
export function hasHanText(text: string): boolean {
  return HAN_SCRIPT.test(text);
}

/** Streaming markdown is intentionally static; settled output gets one-shot semantic reveals. */
export function shouldAnimateMarkdown(streaming: boolean): boolean {
  return !streaming;
}

/** Agent 正文 Markdown：逐标签格局对齐 Codex 桌面端（样式见 index.css 的 .chat-markdown 区）。 */
export function MarkdownBody({ text, streaming = false }: { text: string; streaming?: boolean }) {
  if (!text.trim()) return null;
  const reveal = shouldAnimateMarkdown(streaming);
  return (
    <MarkdownErrorBoundary resetKey={text} fallback={<PlainTextFallback text={text} />}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          a: ({ href, children }) => (
            <a href={href} target="_blank" rel="noreferrer noopener" className="text-brand-primary hover:underline">
              {children}
            </a>
          ),
          p: ({ children }) => (
            <InkReveal as="p" enabled={reveal} delay={0.03} data-markdown-han-text={hasHanText(nodeText(children)) ? '' : undefined}>{children}</InkReveal>
          ),
          h1: ({ children }) => <MarkdownHeading level="h1" children={children} streaming={streaming} />,
          h2: ({ children }) => <MarkdownHeading level="h2" children={children} streaming={streaming} />,
          h3: ({ children }) => <MarkdownHeading level="h3" children={children} streaming={streaming} />,
          h4: ({ children }) => <MarkdownHeading level="h4" children={children} streaming={streaming} />,
          h5: ({ children }) => <MarkdownHeading level="h5" children={children} streaming={streaming} />,
          h6: ({ children }) => <MarkdownHeading level="h6" children={children} streaming={streaming} />,
          ul: ({ children }) => <InkReveal as="ul" enabled={reveal} delay={0.05}>{children}</InkReveal>,
          ol: ({ children }) => <InkReveal as="ol" enabled={reveal} delay={0.05}>{children}</InkReveal>,
          li: ({ children }) => <InkReveal as="li" enabled={reveal} delay={0.07}>{children}</InkReveal>,
          blockquote: ({ children }) => <InkReveal as="blockquote" enabled={reveal} delay={0.08}>{children}</InkReveal>,
          // 行内/块级 code 的外观分别由 index.css（.chat-markdown :not(pre) > code）与 CodeBlock 接管；
          // 只解构 className/children——react-markdown 以 passNode:true 注入的 node prop 不能泄到 DOM
          code: ({ className, children }) => (
            <code className={className}>{children}</code>
          ),
          table: ({ children }) => <InkReveal enabled={reveal} delay={0.1} className="my-3"><TableCard>{children}</TableCard></InkReveal>,
          pre: ({ children }) => <InkReveal enabled={reveal} delay={0.1} className="my-3"><CodeBlock>{children}</CodeBlock></InkReveal>,
        }}
      >
        {text}
      </ReactMarkdown>
    </MarkdownErrorBoundary>
  );
}

function MarkdownHeading({ level, children, streaming }: { level: 'h1' | 'h2' | 'h3' | 'h4' | 'h5' | 'h6'; children: React.ReactNode; streaming: boolean }) {
  const plain = typeof children === 'string';
  if (plain && !streaming) {
    return <TextGenerateEffect as={level} words={children} filter duration={0.46} />;
  }
  return <InkReveal as={level} enabled={!streaming} delay={0.02}>{children}</InkReveal>;
}

/** 渲染崩溃兜底：保底可读的纯文本。 */
function PlainTextFallback({ text }: { text: string }) {
  return (
    <div>
      <div className="text-caption text-text-tertiary">Markdown 渲染失败，已按纯文本展示</div>
      <pre className="mt-1 whitespace-pre-wrap break-words font-mono text-[13px] leading-relaxed text-text-secondary">
        {text}
      </pre>
    </div>
  );
}

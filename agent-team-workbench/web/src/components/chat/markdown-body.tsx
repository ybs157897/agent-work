import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { nodeText, CodeBlock } from './code-block';
import { MarkdownErrorBoundary } from './error-boundary';
import { TableCard } from './table-card';

/** 汉字脚本检测：命中中日韩使用的汉字；谚文/假名不属于 Script=Han，不在此规则内。 */
const HAN_SCRIPT = /\p{Script=Han}/u;

/** 段落是否含汉字：命中时 p 打 data-markdown-han-text，配合 index.css 的 CJK 连续段距规则。 */
export function hasHanText(text: string): boolean {
  return HAN_SCRIPT.test(text);
}

/** Agent 正文 Markdown：逐标签格局对齐 Codex 桌面端（样式见 index.css 的 .chat-markdown 区）。 */
export function MarkdownBody({ text }: { text: string }) {
  if (!text.trim()) return null;
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
            <p data-markdown-han-text={hasHanText(nodeText(children)) ? '' : undefined}>{children}</p>
          ),
          // 行内/块级 code 的外观分别由 index.css（.chat-markdown :not(pre) > code）与 CodeBlock 接管；
          // 只解构 className/children——react-markdown 以 passNode:true 注入的 node prop 不能泄到 DOM
          code: ({ className, children }) => (
            <code className={className}>{children}</code>
          ),
          table: ({ children }) => <TableCard>{children}</TableCard>,
          pre: ({ children }) => <CodeBlock>{children}</CodeBlock>,
        }}
      >
        {text}
      </ReactMarkdown>
    </MarkdownErrorBoundary>
  );
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

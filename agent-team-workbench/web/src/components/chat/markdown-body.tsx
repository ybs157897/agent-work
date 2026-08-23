import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

/** Agent 正文 Markdown（对齐 deepseek-harness agent-chat 的 .ac-markdown）。 */
export function MarkdownBody({ text }: { text: string }) {
  if (!text.trim()) return null;
  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm]}
      components={{
        a: ({ href, children }) => (
          <a href={href} target="_blank" rel="noreferrer noopener" className="text-brand-primary hover:underline">
            {children}
          </a>
        ),
        code: ({ className, children, ...props }) => {
          const inline = !className;
          if (inline) {
            return (
              <code className="rounded px-1 py-0.5 bg-black/[0.06] text-[0.92em]" {...props}>
                {children}
              </code>
            );
          }
          return (
            <code className={className} {...props}>
              {children}
            </code>
          );
        },
        pre: ({ children }) => (
          <pre className="my-2 overflow-x-auto rounded-md bg-surface-sunken px-3 py-2 text-[13px] leading-relaxed">
            {children}
          </pre>
        ),
      }}
    >
      {text}
    </ReactMarkdown>
  );
}

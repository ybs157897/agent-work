import { Check, Copy, Download } from 'lucide-react';
import { Children, isValidElement, useEffect, useRef, useState, type ReactElement, type ReactNode } from 'react';
import { cn } from '@/lib/utils';

const HIGHLIGHT_DEBOUNCE_MS = 120;
const COPY_FEEDBACK_MS = 2000;
type Highlighted = { code: string; html: string; language: string };

export function languageFromClassName(className?: string | null): string | null {
  if (!className) return null;
  const match = /(?:^|\s)language-([\w#+.-]+)/.exec(className);
  return match ? match[1].toLowerCase() : null;
}

export function nodeText(node: ReactNode): string {
  if (node == null || typeof node === 'boolean') return '';
  if (typeof node === 'string' || typeof node === 'number') return String(node);
  if (Array.isArray(node)) return node.map(nodeText).join('');
  if (isValidElement(node)) return nodeText((node.props as { children?: ReactNode }).children);
  return '';
}

export async function copyText(text: string): Promise<boolean> {
  const clipboard = typeof navigator !== 'undefined' ? navigator.clipboard : undefined;
  if (clipboard?.writeText) {
    try {
      await clipboard.writeText(text);
      return true;
    } catch {
      // 权限拒绝等失败，继续尝试旧版 DOM API。
    }
  }
  return legacyCopy(text);
}

function legacyCopy(text: string): boolean {
  if (typeof document === 'undefined') return false;
  const textarea = document.createElement('textarea');
  textarea.value = text;
  textarea.style.position = 'fixed';
  textarea.style.opacity = '0';
  document.body.appendChild(textarea);
  textarea.select();
  let ok = false;
  try {
    ok = document.execCommand('copy');
  } catch {
    // execCommand 被策略禁用时保持静默降级。
  }
  textarea.remove();
  return ok;
}

const LANGUAGE_EXTENSIONS: Record<string, string> = {
  bash: 'sh', c: 'c', 'c++': 'cpp', 'c#': 'cs', css: 'css', go: 'go', html: 'html', java: 'java',
  javascript: 'js', js: 'js', json: 'json', jsx: 'jsx', markdown: 'md', md: 'md', python: 'py', py: 'py',
  rust: 'rs', rs: 'rs', shell: 'sh', sql: 'sql', ts: 'ts', typescript: 'ts', tsx: 'tsx', yaml: 'yml', yml: 'yml',
};

export function codeFilename(language: string | null): string {
  const normalized = language?.toLowerCase() ?? '';
  const extension = LANGUAGE_EXTENSIONS[normalized] ?? (normalized.replace(/[^a-z0-9]+/g, '') || 'txt');
  return `code.${extension}`;
}

export function downloadCode(code: string, language: string | null): void {
  const blob = new Blob([code], { type: 'text/plain' });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = codeFilename(language);
  anchor.click();
  URL.revokeObjectURL(url);
}

async function highlightCode(code: string, declared: string | null): Promise<Highlighted | null> {
  if (!code) return null;
  try {
    const { default: hljs } = await import('highlight.js/lib/common');
    if (declared) {
      if (!hljs.getLanguage(declared)) return null;
      const { value } = hljs.highlight(code, { language: declared, ignoreIllegals: true });
      return { code, html: value, language: declared };
    }
    const auto = hljs.highlightAuto(code);
    return auto.relevance > 0 ? { code, html: auto.value, language: auto.language ?? '' } : null;
  } catch {
    return null;
  }
}

interface CodeBlockProps {
  children?: ReactNode;
  className?: string;
  streaming?: boolean;
}

export function CodeBlock({ children, className, streaming = false }: CodeBlockProps) {
  const codeElement = Children.toArray(children).find(isValidElement) as
    | ReactElement<{ className?: string; children?: ReactNode }>
    | undefined;
  const codeClassName = codeElement?.props.className;
  const declared = languageFromClassName(codeClassName);
  const code = nodeText(codeElement?.props.children);
  const [highlighted, setHighlighted] = useState<Highlighted | null>(null);
  const [copied, setCopied] = useState(false);
  const copyResetTimer = useRef<number | undefined>(undefined);

  useEffect(() => {
    if (streaming) {
      setHighlighted(null);
      return;
    }
    let cancelled = false;
    const timer = window.setTimeout(() => {
      void highlightCode(code, declared).then((result) => {
        if (!cancelled) setHighlighted(result);
      });
    }, HIGHLIGHT_DEBOUNCE_MS);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [code, declared, streaming]);

  useEffect(() => () => window.clearTimeout(copyResetTimer.current), []);

  async function onCopy() {
    if (!(await copyText(code))) return;
    setCopied(true);
    window.clearTimeout(copyResetTimer.current);
    copyResetTimer.current = window.setTimeout(() => setCopied(false), COPY_FEEDBACK_MS);
  }

  const current = !streaming && highlighted?.code === code ? highlighted : null;
  const label = declared ?? current?.language ?? 'text';
  return (
    <div className={cn('chat-code-panel my-3', className)}>
      <div className="chat-code-panel__toolbar" aria-hidden={false}>
        <span className="chat-code-panel__lang">{label}</span>
        <div className="chat-code-panel__actions">
          <button type="button" onClick={() => downloadCode(code, declared)} className="chat-code-panel__action" aria-label="下载代码" title="下载代码"><Download className="size-3.5" /></button>
          <button type="button" onClick={() => void onCopy()} className="chat-code-panel__action" aria-label="复制代码" title={copied ? '已复制' : '复制代码'}>
            {copied ? <Check className="size-3.5 text-status-success" /> : <Copy className="size-3.5" />}
          </button>
        </div>
      </div>
      <pre className="chat-code-panel__pre">
        {current ? <code className={`hljs ${codeClassName ?? ''}`.trim()} dangerouslySetInnerHTML={{ __html: current.html }} /> : <code className={codeClassName}>{code}</code>}
      </pre>
    </div>
  );
}

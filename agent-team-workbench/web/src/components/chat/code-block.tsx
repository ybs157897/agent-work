import { Check, ChevronDown, Copy, FileDown, Upload } from 'lucide-react';
import { Children, isValidElement, useEffect, useRef, useState, type ReactElement, type ReactNode } from 'react';
import { cn } from '@/lib/utils';

const HIGHLIGHT_DEBOUNCE_MS = 120;
const COPY_FEEDBACK_MS = 2000;
type Highlighted = { code: string; html: string; language: string };

export interface CodeFenceMeta {
  filename?: string;
  title?: string;
  highlightedLines: number[];
}

const SAFE_FILENAME = /^[\p{L}\p{N}][\p{L}\p{N}._ ()@+-]*$/u;
const MAX_HIGHLIGHTED_LINES = 500;

/** Parses optional markdown fence metadata without making it part of the code body. */
export function parseCodeFenceMeta(meta?: string | null): CodeFenceMeta {
  const result: CodeFenceMeta = { highlightedLines: [] };
  if (!meta?.trim()) return result;
  const ranges = new Set<number>();
  const tokens = meta.match(/(?:[^\s"']|"[^"]*"|'[^']*')+/g) ?? [];
  for (const token of tokens) {
    const equals = token.indexOf('=');
    if (equals > 0) {
      const key = token.slice(0, equals).toLowerCase();
      const value = token.slice(equals + 1).replace(/^["']|["']$/g, '').trim();
      if (key === 'filename' && value) result.filename = safeCodeDisplayName(value);
      if (key === 'title' && value) result.title = safeCodeDisplayName(value);
      if ((key === 'highlight' || key === 'highlights') && value) addLineRanges(value, ranges);
      continue;
    }
    const range = token.match(/^\{([^}]*)\}$/)?.[1];
    if (range) addLineRanges(range, ranges);
  }
  result.highlightedLines = [...ranges].sort((a, b) => a - b);
  return result;
}

function addLineRanges(raw: string, output: Set<number>): void {
  for (const part of raw.split(',')) {
    const match = part.trim().match(/^(\d+)(?:-(\d+))?$/);
    if (!match) continue;
    const start = Number(match[1]);
    const end = Math.max(start, Number(match[2] ?? start));
    if (start < 1) continue;
    for (let line = start; line <= Math.min(end, start + MAX_HIGHLIGHTED_LINES); line += 1) {
      if (output.size >= MAX_HIGHLIGHTED_LINES) return;
      output.add(line);
    }
  }
}

export function safeCodeDisplayName(value: string): string | undefined {
  const name = value.replace(/[\u0000-\u001f\u007f]/g, '').trim().slice(0, 120);
  return name || undefined;
}

export function safeCodeFilename(value: string): string | undefined {
  const basename = value.replace(/[\\/]+$/g, '').split(/[\\/]/).pop() ?? '';
  const name = basename.replace(/[\u0000-\u001f\u007f]/g, '').trim().slice(0, 120);
  if (!name || name === '.' || name === '..' || !/[\p{L}\p{N}]/u.test(name) || !SAFE_FILENAME.test(name)) return undefined;
  return name;
}

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
  const extension = LANGUAGE_EXTENSIONS[normalized] ?? (normalized.replace(/[^a-z0-9]+/g, '').slice(0, 24) || 'txt');
  return `code.${extension}`;
}

export function downloadCode(code: string, language: string | null, filename?: string): void {
  const blob = new Blob([code], { type: 'text/plain;charset=utf-8' });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = safeCodeFilename(filename ?? '') ?? codeFilename(language);
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
  filename?: string;
  title?: string;
  highlightedLines?: readonly number[];
}

export function CodeBlock({ children, className, streaming = false, filename, title, highlightedLines = [] }: CodeBlockProps) {
  const codeElement = Children.toArray(children).find(isValidElement) as
    | ReactElement<{ className?: string; children?: ReactNode }>
    | undefined;
  const codeClassName = codeElement?.props.className;
  const declared = languageFromClassName(codeClassName);
  const code = nodeText(codeElement?.props.children);
  const [highlighted, setHighlighted] = useState<Highlighted | null>(null);
  const [copied, setCopied] = useState(false);
  const [exportOpen, setExportOpen] = useState(false);
  const exportRef = useRef<HTMLDivElement>(null);
  const exportButtonRef = useRef<HTMLButtonElement>(null);
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

  useEffect(() => {
    if (!exportOpen) return;
    const onPointerDown = (event: PointerEvent) => {
      if (exportRef.current && !exportRef.current.contains(event.target as Node)) {
        setExportOpen(false);
      }
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        setExportOpen(false);
        exportButtonRef.current?.focus();
      }
    };
    document.addEventListener('pointerdown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('pointerdown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [exportOpen]);

  async function onCopy() {
    if (!(await copyText(code))) return;
    setCopied(true);
    window.clearTimeout(copyResetTimer.current);
    copyResetTimer.current = window.setTimeout(() => setCopied(false), COPY_FEEDBACK_MS);
  }

  async function onCopyMarkdown() {
    const fence = declared ?? 'text';
    const meta = [filename ? `filename=${filename}` : title ? `title="${title}"` : '', highlightedLines.length ? `{${highlightedLines.join(',')}}` : ''].filter(Boolean).join(' ');
    const markdown = `\`\`\`${fence}${meta ? ` ${meta}` : ''}\n${code.replace(/\n$/, '')}\n\`\`\``;
    if (!(await copyText(markdown))) return;
    setCopied(true);
    setExportOpen(false);
    window.clearTimeout(copyResetTimer.current);
    copyResetTimer.current = window.setTimeout(() => setCopied(false), COPY_FEEDBACK_MS);
  }

  const current = !streaming && highlighted?.code === code ? highlighted : null;
  const label = declared ?? current?.language ?? 'text';
  const lineHtml = current ? splitHighlightedHtml(current.html) : undefined;
  const visualCode = code.endsWith('\n') ? code.slice(0, -1) : code;
  const lineText = visualCode.split('\n');
  const selectedLines = highlightedLines.filter((line) => line >= 1 && line <= lineText.length);
  const displayName = title ?? filename;
  return (
    <div className={cn('chat-code-panel my-3', className)}>
      <div className="chat-code-panel__toolbar" aria-hidden={false}>
        <span className="flex min-w-0 items-center gap-2">
          {displayName && <span className="chat-code-panel__filename" title={displayName}>{displayName}</span>}
          <span className="chat-code-panel__lang shrink-0">{label}</span>
        </span>
        <div className="chat-code-panel__actions">
          <button type="button" onClick={() => void onCopy()} className="inline-flex items-center gap-1 rounded-button px-2 py-1 text-caption text-text-secondary hover:bg-surface-sunken hover:text-text-primary" aria-label="复制代码" title={copied ? '已复制' : '复制代码'}>
            {copied ? <Check className="size-3.5 text-status-success" /> : <Copy className="size-3.5" />}复制代码
          </button>
          <div className="relative" ref={exportRef}>
            <button ref={exportButtonRef} type="button" onClick={() => setExportOpen((open) => !open)} className="chat-code-panel__export" aria-label="导出代码" title="导出代码" aria-expanded={exportOpen} aria-haspopup="menu"><Upload className="size-3.5" aria-hidden />导出<ChevronDown className="size-3" aria-hidden /></button>
            {exportOpen && (
              <div role="menu" className="absolute right-0 top-full z-20 mt-1 min-w-32 rounded-button border border-border-subtle bg-surface-raised p-1 shadow-card">
                <button role="menuitem" type="button" className="flex w-full items-center gap-2 rounded-button px-2 py-1.5 text-caption text-text-secondary hover:bg-surface-sunken" onClick={() => { downloadCode(code, declared, filename); setExportOpen(false); exportButtonRef.current?.focus(); }}><FileDown className="size-3.5" />下载文件</button>
                <button role="menuitem" type="button" className="flex w-full items-center gap-2 rounded-button px-2 py-1.5 text-caption text-text-secondary hover:bg-surface-sunken" onClick={() => void onCopyMarkdown()}><Copy className="size-3.5" />复制 Markdown</button>
              </div>
            )}
          </div>
        </div>
      </div>
      <pre className="chat-code-panel__pre">
        {lineHtml ? (
          <code className={`hljs ${codeClassName ?? ''}`.trim()}>
            {lineHtml.slice(0, lineText.length).map((html, index) => <span key={index} className={selectedLines.includes(index + 1) ? 'block bg-brand-primary/10' : 'block'}><span className="mr-4 inline-block w-8 select-none text-right text-text-tertiary" aria-hidden>{index + 1}</span><span dangerouslySetInnerHTML={{ __html: html || ' ' }} /></span>)}
          </code>
        ) : (
          <code className={codeClassName}>{lineText.map((line, index) => <span key={index} className={selectedLines.includes(index + 1) ? 'block bg-brand-primary/10' : 'block'}><span className="mr-4 inline-block w-8 select-none text-right text-text-tertiary" aria-hidden>{index + 1}</span>{line || ' '}</span>)}</code>
        )}
      </pre>
    </div>
  );
}

/** Splits highlight.js HTML by lines while reopening tokens across newline boundaries. */
export function splitHighlightedHtml(html: string): string[] {
  const lines: string[] = [];
  let line = '';
  const openTags: string[] = [];
  const tagPattern = /<!--[\s\S]*?-->|<\/?[a-zA-Z][^>]*>/g;
  let cursor = 0;
  const closeOpen = () => openTags.slice().reverse().map((tag) => `</${tag.match(/^<([\w-]+)/)?.[1] ?? 'span'}>`).join('');
  const reopen = () => openTags.join('');
  for (const match of html.matchAll(tagPattern)) {
    const before = html.slice(cursor, match.index);
    const parts = before.split('\n');
    line += parts.shift() ?? '';
    for (const part of parts) { lines.push(line + closeOpen()); line = reopen() + part; }
    const tag = match[0];
    line += tag;
    const closing = tag.match(/^<\/([\w-]+)/);
    const opening = tag.match(/^<([\w-]+)/);
    if (closing) { const index = openTags.map((value) => value.match(/^<([\w-]+)/)?.[1]).lastIndexOf(closing[1]!); if (index >= 0) openTags.splice(index, 1); }
    else if (opening && !tag.endsWith('/>') && !tag.startsWith('<!--')) openTags.push(tag);
    cursor = (match.index ?? 0) + tag.length;
  }
  const tail = html.slice(cursor).split('\n');
  line += tail.shift() ?? '';
  for (const part of tail) { lines.push(line + closeOpen()); line = reopen() + part; }
  lines.push(line + closeOpen());
  return lines;
}

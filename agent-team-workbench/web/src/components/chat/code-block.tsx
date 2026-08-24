import { Check, Copy, WrapText } from 'lucide-react';
import { Children, isValidElement, useEffect, useRef, useState, type ReactElement, type ReactNode } from 'react';

/** 流式期间 markdown 整树重渲染，防抖窗口内不重复触发高亮计算。 */
const HIGHLIGHT_DEBOUNCE_MS = 120;
const COPY_FEEDBACK_MS = 1500;

type Highlighted = { code: string; html: string; language: string };

/** 从 code 元素 className 提取 markdown fence 语言标记（language-xxx），无标记返回 null。 */
export function languageFromClassName(className?: string | null): string | null {
  if (!className) return null;
  const match = /(?:^|\s)language-([\w#+.-]+)/.exec(className);
  return match ? match[1].toLowerCase() : null;
}

/** 递归提取 React 子树的纯文本；react-markdown 的 code 内容通常是单字符串，此处兜嵌套/数组形态。 */
export function nodeText(node: ReactNode): string {
  if (node == null || typeof node === 'boolean') return '';
  if (typeof node === 'string' || typeof node === 'number') return String(node);
  if (Array.isArray(node)) return node.map(nodeText).join('');
  if (isValidElement(node)) return nodeText((node.props as { children?: ReactNode }).children);
  return '';
}

/** 复制到剪贴板：优先 navigator.clipboard，失败或不可用时降级 execCommand；全不可用返回 false（调用方保持静默）。 */
export async function copyText(text: string): Promise<boolean> {
  const clipboard = typeof navigator !== 'undefined' ? navigator.clipboard : undefined;
  if (clipboard?.writeText) {
    try {
      await clipboard.writeText(text);
      return true;
    } catch {
      // 权限拒绝等失败 → 落到 execCommand 兜底
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
    // execCommand 抛错（被策略禁用）→ 纯文本降级路径的一部分
  }
  textarea.remove();
  return ok;
}

/**
 * 高亮并返回可安全注入的 HTML；纯文本降级（懒加载失败 / 声明语言未注册 / 自动检测无置信度）返回 null。
 * 只对 highlight.js 产出的 HTML 使用 dangerouslySetInnerHTML——其转义了全部原文，注入面收敛于 token span。
 */
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
    if (auto.relevance <= 0) return null;
    // relevance > 0 时必有检测语言；类型上兜空串（渲染为无标签）
    return { code, html: auto.value, language: auto.language ?? '' };
  } catch {
    // 动态 import 失败（弱网/加载中断）→ 纯文本是既定降级，不向上抛
    return null;
  }
}

/**
 * markdown pre 的替身：顶部栏（语言标签 + 换行开关 + 复制）包裹代码体。
 * children 是 react-markdown 渲出的 code 元素，语言与原文均从其 props 提取，渲染由本组件接管。
 */
export function CodeBlock({ children }: { children?: ReactNode }) {
  const codeElement = Children.toArray(children).find(isValidElement) as
    | ReactElement<{ className?: string; children?: ReactNode }>
    | undefined;
  const codeClassName = codeElement?.props.className;
  const declared = languageFromClassName(codeClassName);
  const code = nodeText(codeElement?.props.children);

  const [highlighted, setHighlighted] = useState<Highlighted | null>(null);
  const [copied, setCopied] = useState(false);
  const [wrap, setWrap] = useState(false);
  const copyResetTimer = useRef<number | undefined>(undefined);

  useEffect(() => {
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
  }, [code, declared]);

  useEffect(() => () => window.clearTimeout(copyResetTimer.current), []);

  async function onCopy() {
    if (!(await copyText(code))) return;
    setCopied(true);
    window.clearTimeout(copyResetTimer.current);
    copyResetTimer.current = window.setTimeout(() => setCopied(false), COPY_FEEDBACK_MS);
  }

  // 流式中 code 持续变化，仅当高亮结果对应当前原文时才使用，否则渲染纯文本
  const current = highlighted?.code === code ? highlighted : null;

  return (
    <div className="code-block my-2 overflow-hidden rounded-md bg-surface-sunken">
      <div className="flex items-center justify-between gap-2 px-3 pt-1.5">
        <span className="min-w-0 truncate font-mono text-[11px] uppercase tracking-wider text-text-tertiary">
          {declared ?? current?.language ?? ''}
        </span>
        <div className="flex shrink-0 items-center gap-0.5">
          <button
            type="button"
            aria-pressed={wrap}
            title={wrap ? '取消自动换行' : '自动换行'}
            onClick={() => setWrap((v) => !v)}
            className={`rounded p-1 transition-colors hover:bg-black/[0.06] hover:text-text-primary ${
              wrap ? 'text-brand-primary' : 'text-text-tertiary'
            }`}
          >
            <WrapText className="h-3.5 w-3.5" />
          </button>
          <button
            type="button"
            aria-label="复制代码"
            title={copied ? '已复制' : '复制'}
            onClick={() => void onCopy()}
            className="rounded p-1 text-text-tertiary transition-colors hover:bg-black/[0.06] hover:text-text-primary"
          >
            {copied ? <Check className="h-3.5 w-3.5 text-status-success" /> : <Copy className="h-3.5 w-3.5" />}
          </button>
        </div>
      </div>
      <pre
        className={`px-3 pb-2.5 pt-1.5 text-[13px] leading-relaxed ${
          wrap ? 'whitespace-pre-wrap break-words' : 'overflow-x-auto'
        }`}
      >
        {current ? (
          <code
            className={`hljs ${codeClassName ?? ''}`.trim()}
            dangerouslySetInnerHTML={{ __html: current.html }}
          />
        ) : (
          <code className={codeClassName}>{code}</code>
        )}
      </pre>
    </div>
  );
}

import { Check, Copy, Download, FileText, Maximize2 } from 'lucide-react';
import { useEffect, useId, useRef, useState } from 'react';
import { documentDownloadName } from '../../utils/delivered-document';
import { Drawer } from '../drawer';
import { writeClipboard } from './blocks/clipboard';
import { MarkdownBody } from './markdown-body';

const COPY_FEEDBACK_MS = 2_000;

/**
 * 交付文档 `.md` 下载：正文是消息原文切片，不做清洗；文件名由标题派生。
 * 与 CodeBlock 的下载同形（Blob + 临时 a），node 环境没有 document 时静默跳过。
 */
export function downloadDocumentMarkdown(markdown: string, title: string): void {
  if (typeof document === 'undefined') return;
  const blob = new Blob([markdown], { type: 'text/markdown;charset=utf-8' });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = documentDownloadName(title);
  anchor.click();
  URL.revokeObjectURL(url);
}

/**
 * 正文携带的完整交付文档的独立阅读面：面板默认全文可读，动作行提供复制
 * 原文 Markdown、下载 `.md` 与在 Drawer 里单独阅读。正文渲染一律复用
 * `MarkdownBody`（与聊天同一条管线），本组件不引入第二渲染器。
 */
export function DeliveredDocumentPanel({
  title,
  markdown,
  streaming = false,
  showCaret = true,
  runId,
  messageId,
}: {
  title: string;
  markdown: string;
  streaming?: boolean;
  showCaret?: boolean;
  runId?: string;
  messageId?: string;
}) {
  const headingId = useId();
  const [copied, setCopied] = useState(false);
  const [reading, setReading] = useState(false);
  const resetTimer = useRef<number | undefined>(undefined);

  useEffect(() => () => window.clearTimeout(resetTimer.current), []);

  async function onCopy() {
    if (copied) return;
    if (!(await writeClipboard(markdown))) return;
    setCopied(true);
    window.clearTimeout(resetTimer.current);
    resetTimer.current = window.setTimeout(() => setCopied(false), COPY_FEEDBACK_MS);
  }

  return (
    <>
      <section className="chat-document" data-delivered-document aria-labelledby={headingId}>
        <header className="chat-document-head">
          <span className="chat-document-icon" aria-hidden><FileText className="h-4 w-4" /></span>
          <div className="min-w-0 flex-1">
            <h3 id={headingId} className="chat-document-title" title={title}>{title}</h3>
            <p className="chat-document-meta">完整文档正文，与消息原文一致</p>
          </div>
          <div className="chat-document-actions">
            <button
              type="button"
              className="chat-document-action"
              onClick={() => void onCopy()}
              aria-label={copied ? '已复制文档 Markdown' : '复制文档 Markdown'}
              title={copied ? '已复制' : '复制原文 Markdown'}
            >
              {copied ? <Check className="h-3.5 w-3.5" aria-hidden /> : <Copy className="h-3.5 w-3.5" aria-hidden />}
              {copied ? '已复制' : '复制 Markdown'}
            </button>
            <button
              type="button"
              className="chat-document-action"
              onClick={() => downloadDocumentMarkdown(markdown, title)}
              aria-label="下载文档 .md"
              title="下载 .md"
            >
              <Download className="h-3.5 w-3.5" aria-hidden />
              下载 .md
            </button>
            <button
              type="button"
              className="chat-document-action"
              onClick={() => setReading(true)}
              aria-label="单独阅读文档"
              title="单独阅读"
              aria-haspopup="dialog"
            >
              <Maximize2 className="h-3.5 w-3.5" aria-hidden />
              单独阅读
            </button>
          </div>
        </header>
        <div className="chat-document-body">
          <div className="chat-prose chat-document-prose">
            <MarkdownBody text={markdown} streaming={streaming} runId={runId} messageId={messageId} />
            {showCaret && streaming && <span className="chat-stream-caret" aria-hidden />}
          </div>
        </div>
      </section>
      <Drawer
        open={reading}
        onClose={() => setReading(false)}
        title={title}
        ariaLabel={`文档阅读：${title}`}
        width={860}
      >
        <div className="chat-document-reader">
          <div className="chat-prose chat-document-reader-prose">
            <MarkdownBody text={markdown} streaming={false} runId={runId} messageId={messageId} />
          </div>
        </div>
      </Drawer>
    </>
  );
}

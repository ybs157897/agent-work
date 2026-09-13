import { useEffect, useLayoutEffect, useMemo, useRef } from 'react';
import { MarkdownBody } from './markdown-body';
import { DeliveredDocumentPanel } from './delivered-document-panel';
import { projectDeliveredDocument } from '../../utils/delivered-document';
import { normalizeBareLanguageGuiDocuments } from '../../utils/bare-languagegui';
import type { ContentBlockDocument } from '../../utils/content-blocks';
import {
  countLanguageGuiFences,
  embedCanonicalLanguageGuiFence,
  stripLanguageGuiFences,
} from '../../utils/content-blocks';
import { ContentBlockList } from './content-blocks/content-block-renderer';
import {
  isOutputTraceEnabled,
  outputTraceHash,
  stableOutputTraceJson,
  traceOutputDeduped,
} from '../../utils/output-trace';

const useCommitEffect = typeof window === 'undefined' ? useEffect : useLayoutEffect;

/**
 * Agent 输出的纯正文投影。
 *
 * 该组件只负责 Markdown、LanguageGUI fence 与 canonical content blocks；
 * Chat 的消息操作、Task 的 Agent/run 元信息均由各自外层负责，避免两个
 * 记录域为了复用正文而互相依赖 store。
 */
export function AgentOutput({
  text = '',
  streaming = false,
  contentBlocks,
  runId,
  messageId,
  showCaret = true,
}: {
  text?: string;
  streaming?: boolean;
  contentBlocks?: ContentBlockDocument;
  runId?: string;
  messageId?: string;
  showCaret?: boolean;
}) {
  const normalizedText = useMemo(() => normalizeBareLanguageGuiDocuments(text, streaming), [text, streaming]);
  const languageGuiFenceCount = contentBlocks ? countLanguageGuiFences(normalizedText) : 0;
  const embeddedText = contentBlocks && languageGuiFenceCount === 1
    ? embedCanonicalLanguageGuiFence(normalizedText, contentBlocks)
    : null;
  const displayText = embeddedText
    ?? (contentBlocks && languageGuiFenceCount === 0 ? stripLanguageGuiFences(normalizedText) : normalizedText);
  const standaloneContentBlocks = contentBlocks && languageGuiFenceCount === 0
    ? contentBlocks
    : undefined;
  // 交付文档投影只切分 `displayText` 原文：前置解释留在原位，文档整体进独立面板。
  // 识别是对整段文本单调的（文档只增长、前言一旦定界不再变化），因此流式期间
  // 不会来回切换；未识别时完全维持原来的整篇 Markdown 直出。
  const deliveredDocument = useMemo(() => projectDeliveredDocument(displayText), [displayText]);
  const lastCommitTrace = useRef('');

  useCommitEffect(() => {
    if (!isOutputTraceEnabled()) return;
    const blockJson = contentBlocks ? stableOutputTraceJson(contentBlocks) : '';
    const signature = `assistant.committed:${runId ?? ''}:${messageId ?? ''}:${streaming ? 'streaming' : 'final'}:${outputTraceHash(displayText)}:${outputTraceHash(blockJson)}`;
    if (lastCommitTrace.current === signature) return;
    lastCommitTrace.current = signature;
    traceOutputDeduped(signature, {
      stage: 'assistant.committed',
      mode: streaming ? 'streaming' : 'final',
      source: 'react-commit',
      text: displayText,
      runId,
      messageId,
      projection: contentBlocks
        ? {
            contentBlocks: contentBlocks.blocks.length,
            blockTypes: contentBlocks.blocks.map((block) => block.type),
            hash: outputTraceHash(blockJson),
          }
        : { contentBlocks: 0 },
    });
  }, [contentBlocks, displayText, messageId, runId, streaming]);

  if (!text && !streaming && !contentBlocks) return null;

  return (
    <>
      {displayText ? (
        deliveredDocument ? (
          <>
            {deliveredDocument.introMarkdown ? (
              <div className="chat-prose">
                <MarkdownBody
                  text={deliveredDocument.introMarkdown}
                  streaming={streaming}
                  runId={runId}
                  messageId={messageId}
                />
              </div>
            ) : null}
            <DeliveredDocumentPanel
              title={deliveredDocument.title}
              markdown={deliveredDocument.documentMarkdown}
              streaming={streaming}
              showCaret={showCaret}
              runId={runId}
              messageId={messageId}
            />
          </>
        ) : (
          <div className="chat-prose">
            <MarkdownBody
              text={displayText}
              streaming={streaming}
              runId={runId}
              messageId={messageId}
            />
            {showCaret && streaming && <span className="chat-stream-caret" aria-hidden />}
          </div>
        )
      ) : null}
      {standaloneContentBlocks && (
        <ContentBlockList
          document={standaloneContentBlocks}
          trace={{ runId, messageId, mode: streaming ? 'streaming' : 'final' }}
        />
      )}
    </>
  );
}

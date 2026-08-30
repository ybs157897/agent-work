import { useEffect, useLayoutEffect, useRef } from 'react';
import { MarkdownBody } from './markdown-body';
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
  const languageGuiFenceCount = contentBlocks ? countLanguageGuiFences(text) : 0;
  const embeddedText = contentBlocks && languageGuiFenceCount === 1
    ? embedCanonicalLanguageGuiFence(text, contentBlocks)
    : null;
  const displayText = embeddedText
    ?? (contentBlocks && languageGuiFenceCount === 0 ? stripLanguageGuiFences(text) : text);
  const standaloneContentBlocks = contentBlocks && languageGuiFenceCount === 0
    ? contentBlocks
    : undefined;
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
        <div className="chat-prose">
          <MarkdownBody
            text={displayText}
            streaming={streaming}
            runId={runId}
            messageId={messageId}
          />
          {showCaret && streaming && <span className="chat-stream-caret" aria-hidden />}
        </div>
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

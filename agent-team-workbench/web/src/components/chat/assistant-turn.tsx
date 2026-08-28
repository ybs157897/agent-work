import { useEffect, useLayoutEffect, useRef } from 'react';
import { MarkdownBody } from './markdown-body';
import { LongAnswerFold } from './long-answer-fold';
import { MessageActions } from './message-actions';
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

/** Agent 一轮回复：正文与消息操作（reasoning 已迁入活动组）。 */
export function AssistantTurn({
  text = '',
  streaming = false,
  forkKey,
  onFork,
  agentName,
  contentBlocks,
  runId,
  messageId,
}: {
  text?: string;
  streaming?: boolean;
  forkKey?: string;
  onFork?: (key: string) => void;
  agentName?: string;
  contentBlocks?: ContentBlockDocument;
  runId?: string;
  messageId?: string;
}) {
  const name = agentName ?? 'Agent';
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
    <article className="chat-assistant-turn group" aria-label={`${name} 的消息`}>
      <div className="w-full min-w-0">
        {displayText ? (
          <div>
            <div className="chat-prose">
              <LongAnswerFold
                text={displayText}
                streaming={streaming}
                disabled={Boolean(contentBlocks)}
                renderBody={(bodyText) => (
                  <>
                    <MarkdownBody
                      text={bodyText}
                      streaming={streaming}
                      runId={runId}
                      messageId={messageId}
                    />
                    {streaming && <span className="chat-stream-caret" aria-hidden />}
                  </>
                )}
              />
            </div>
          </div>
        ) : null}
        {standaloneContentBlocks && (
          <ContentBlockList
            document={standaloneContentBlocks}
            trace={{ runId, messageId, mode: streaming ? 'streaming' : 'final' }}
          />
        )}
        {!streaming && text ? (
          <MessageActions
            text={text}
            className="mt-2"
            onFork={forkKey && onFork ? () => onFork(forkKey) : undefined}
          />
        ) : null}
      </div>
    </article>
  );
}

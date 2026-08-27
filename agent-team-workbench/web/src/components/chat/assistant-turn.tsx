import { MarkdownBody } from './markdown-body';
import { MessageActions } from './message-actions';
import type { ContentBlockDocument } from '../../utils/content-blocks';
import { stripLanguageGuiFences } from '../../utils/content-blocks';
import { ContentBlockList } from './content-blocks/content-block-renderer';

/** Agent 一轮回复：正文与消息操作（reasoning 已迁入活动组）。 */
export function AssistantTurn({
  text = '',
  streaming = false,
  forkKey,
  onFork,
  agentName,
  contentBlocks,
}: {
  text?: string;
  streaming?: boolean;
  forkKey?: string;
  onFork?: (key: string) => void;
  agentName?: string;
  contentBlocks?: ContentBlockDocument;
}) {
  if (!text && !streaming && !contentBlocks) return null;

  const name = agentName ?? 'Agent';
  const displayText = contentBlocks ? stripLanguageGuiFences(text) : text;
  return (
    <article className="chat-assistant-turn group" aria-label={`${name} 的消息`}>
      <div className="w-full min-w-0">
        {displayText ? (
          <div key={streaming ? 'streaming' : 'settled'} className={streaming ? 'chat-streaming' : undefined}>
            <div className="chat-prose">
              <MarkdownBody text={displayText} streaming={streaming} />
              {streaming && <span className="chat-stream-caret" aria-hidden />}
            </div>
          </div>
        ) : null}
        {contentBlocks && <ContentBlockList document={contentBlocks} />}
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

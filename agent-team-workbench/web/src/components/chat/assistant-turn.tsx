import { AgentOutput } from './agent-output';
import { MessageActions } from './message-actions';
import type { ContentBlockDocument } from '../../utils/content-blocks';

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
  if (!text && !streaming && !contentBlocks) return null;

  return (
    <article className="chat-assistant-turn group" aria-label={`${name} 的消息`}>
      <div className="w-full min-w-0">
        <AgentOutput
          text={text}
          streaming={streaming}
          contentBlocks={contentBlocks}
          runId={runId}
          messageId={messageId}
        />
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

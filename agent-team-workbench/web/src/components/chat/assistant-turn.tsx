import { MarkdownBody } from './markdown-body';
import { MessageActions } from './message-actions';
import { formatTime } from '../../utils/format';

/** Agent 一轮回复：角色标签、正文与消息操作（reasoning 已迁入活动组）。 */
export function AssistantTurn({
  text = '',
  at,
  streaming = false,
  forkKey,
  onFork,
  agentName,
}: {
  text?: string;
  at?: string;
  streaming?: boolean;
  forkKey?: string;
  onFork?: (key: string) => void;
  agentName?: string;
}) {
  if (!text && !streaming) return null;

  const name = agentName ?? 'Agent';
  return (
    <article className="group" aria-label={`${name} 的消息`}>
      <div className="w-full min-w-0">
        <div className="chat-role-label">
          <span className="chat-role-dot" aria-hidden />
          <span>{name}</span>
          {at && <span className="chat-msg-time">{formatTime(at)}</span>}
        </div>
        {text ? (
          <div key={streaming ? 'streaming' : 'settled'} className={streaming ? 'chat-streaming' : undefined}>
            <div className="chat-prose">
              <MarkdownBody text={text} streaming={streaming} />
              {streaming && <span className="chat-stream-caret" aria-hidden />}
            </div>
          </div>
        ) : null}
        {at && !streaming && text ? (
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

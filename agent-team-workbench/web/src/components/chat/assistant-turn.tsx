import { MarkdownBody } from './markdown-body';
import { MessageActions } from './message-actions';

/** Agent 一轮回复：正文 Markdown（reasoning 已迁入活动组）。 */
export function AssistantTurn({
  text = '',
  at,
  streaming = false,
  forkKey,
  onFork,
}: {
  text?: string;
  at?: string;
  streaming?: boolean;
  forkKey?: string;
  onFork?: (key: string) => void;
}) {
  if (!text && !streaming) return null;

  return (
    <div className="group flex justify-start py-1">
      <div className="w-full min-w-0">
        {text ? (
          <div className={streaming ? 'chat-streaming' : undefined}>
            <div className="chat-markdown">
              <MarkdownBody text={text} />
            </div>
          </div>
        ) : null}
        {at && !streaming && text ? (
          <MessageActions
            text={text}
            at={at}
            side="left"
            className="mt-2"
            onFork={forkKey && onFork ? () => onFork(forkKey) : undefined}
          />
        ) : null}
      </div>
    </div>
  );
}

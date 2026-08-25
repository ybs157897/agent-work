import { MarkdownBody } from './markdown-body';
import { MessageActions } from './message-actions';
import { Avatar } from '../avatar';
import { formatTime } from '../../utils/format';

/** Agent 一轮回复：回合头（头像+名字+时间）+ 正文 Markdown（reasoning 已迁入活动组）。 */
export function AssistantTurn({
  text = '',
  at,
  streaming = false,
  forkKey,
  onFork,
  agentName,
  agentAvatar,
}: {
  text?: string;
  at?: string;
  streaming?: boolean;
  forkKey?: string;
  onFork?: (key: string) => void;
  agentName?: string;
  agentAvatar?: string;
}) {
  if (!text && !streaming) return null;

  const name = agentName ?? 'Agent';
  return (
    <div className="group flex justify-start py-1">
      <div className="w-full min-w-0">
        <div className="mb-1 flex items-center gap-1.5">
          <Avatar name={name} url={agentAvatar} size={20} />
          <span className="text-caption font-semibold text-text-secondary">{name}</span>
          {at && <span className="text-[11px] tabular-nums text-text-tertiary">{formatTime(at)}</span>}
        </div>
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
            side="left"
            className="mt-2"
            onFork={forkKey && onFork ? () => onFork(forkKey) : undefined}
          />
        ) : null}
      </div>
    </div>
  );
}

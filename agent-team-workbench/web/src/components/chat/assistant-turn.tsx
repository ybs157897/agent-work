import { MarkdownBody } from './markdown-body';
import { ReasoningDisclosure } from './reasoning-disclosure';
import { formatTime } from '../../utils/format';

/** Agent 一轮回复：思考折叠 + 正文 Markdown（对齐 agent-chat 透明气泡）。 */
export function AssistantTurn({
  reasoning = '',
  text = '',
  at,
  streaming = false,
  reasoningOnly = false,
}: {
  reasoning?: string;
  text?: string;
  at?: string;
  streaming?: boolean;
  reasoningOnly?: boolean;
}) {
  const showReasoning = Boolean(reasoning);
  const showTyping = streaming && !text && !reasoning;

  return (
    <div
      className="flex justify-start py-1"
      aria-live={streaming ? 'polite' : undefined}
      aria-busy={streaming || undefined}
      aria-atomic={streaming ? false : undefined}
    >
      <div className="w-full min-w-0">
        {showReasoning && <ReasoningDisclosure text={reasoning} streaming={streaming && !text} />}
        {showTyping && (
          <div className="chat-typing flex items-center gap-1 py-2" role="status" aria-label="正在思考">
            <span /><span /><span />
          </div>
        )}
        {(text || (reasoningOnly && !text)) && (
          <div className={streaming && text ? 'chat-streaming' : undefined}>
            {text ? (
              <div className="chat-markdown">
                <MarkdownBody text={text} />
              </div>
            ) : (
              <p className="text-caption text-text-tertiary">本轮没有可见回答</p>
            )}
          </div>
        )}
        {at && !streaming && (
          <time className="mt-2 block text-right text-[11px] tabular-nums text-text-tertiary">
            {formatTime(at)}
          </time>
        )}
      </div>
    </div>
  );
}

import { MarkdownBody } from './markdown-body';
import { MessageActions } from './message-actions';
import { ReasoningDisclosure } from './reasoning-disclosure';

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
    <div className="group flex justify-start py-1">
      <div className="w-full min-w-0">
        {showReasoning && <ReasoningDisclosure text={reasoning} streaming={streaming && !text} />}
        {showTyping && (
          <div className="chat-typing flex items-center gap-1 py-2" aria-label="正在思考">
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
        {/* 悬停操作行：streaming 不渲染；reasoningOnly 且无正文时无可复制内容，也不渲染。 */}
        {at && !streaming && text ? <MessageActions text={text} at={at} side="left" className="mt-2" /> : null}
      </div>
    </div>
  );
}

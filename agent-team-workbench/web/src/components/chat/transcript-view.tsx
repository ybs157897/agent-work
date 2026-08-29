import { AssistantTurn } from './assistant-turn';
import { MessageActions } from './message-actions';
import { PinDecisionAction } from './pin-decision-action';
import { TurnDiffCard } from './turn-diff-card';
import { OutputLoadingIndicator, WorkActivityTimeline } from './work-activity-timeline';
import { useChatStore } from '../../stores/chat.store';
import type { ChatMessage } from '../../stores/chat.store';
import {
  presentedTranscriptSegmentKey,
  type PresentedTranscriptSegment,
} from '../../utils/work-activity-timeline';

export function TranscriptView({
  segments,
  onFork,
  agent,
}: {
  segments: PresentedTranscriptSegment[];
  onFork?: (key: string) => void;
  /** 当前会话归属的 Agent（助手回合头展示身份）。 */
  agent?: { name: string; avatar?: string };
}) {
  return (
    <>
      {segments.map((seg) => (
        <TranscriptSegmentView
          key={presentedTranscriptSegmentKey(seg)}
          seg={seg}
          onFork={onFork}
          agent={agent}
        />
      ))}
    </>
  );
}

function TranscriptSegmentView({
  seg,
  onFork,
  agent,
}: {
  seg: PresentedTranscriptSegment;
  onFork?: (key: string) => void;
  agent?: { name: string; avatar?: string };
}) {
  switch (seg.kind) {
    case 'user':
      return <UserBubble msg={seg.msg} />;
    case 'assistant':
      return (
        <AssistantTurn
          text={seg.msg.text}
          streaming={seg.streaming}
          forkKey={seg.msg.key}
          onFork={onFork}
          agentName={agent?.name}
          contentBlocks={seg.msg.contentBlocks}
          runId={seg.msg.runId}
          messageId={seg.renderKey ?? seg.msg.key}
        />
      );
    case 'work-timeline':
      return <WorkActivityTimeline segment={seg} />;
    case 'thinking-placeholder':
      return <OutputLoadingIndicator />;
    case 'turn-diff':
      return <TurnDiffCard text={seg.text} />;
  }
}

function UserBubble({ msg }: { msg: ChatMessage }) {
  // 钉为决策锚到当前会话的任务台账；分叉/切换会话时 conversationId 随之切换。
  const conversationId = useChatStore((s) => s.conversationId);
  return (
    <article className="chat-user-turn group" aria-label="你的消息">
      <div className="chat-user-card whitespace-pre-wrap break-words">
        {msg.text}
      </div>
      <MessageActions text={msg.text} className="mt-1">
        <PinDecisionAction
          quote={msg.text}
          workItemId={conversationId ?? undefined}
          sourceRunId={msg.runId}
          idempotencyKey={`decision:${conversationId ?? ''}:${msg.key}`}
        />
      </MessageActions>
    </article>
  );
}

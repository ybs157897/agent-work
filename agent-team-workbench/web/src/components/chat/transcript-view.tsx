import { AssistantTurn } from './assistant-turn';
import { MessageActions } from './message-actions';
import { TurnDiffCard } from './turn-diff-card';
import { OutputLoadingIndicator, WorkActivityTimeline } from './work-activity-timeline';
import type { ChatMessage } from '../../stores/chat.store';
import type { SwarmMemberProjection } from '../../stores/chat.store';
import {
  presentedTranscriptSegmentKey,
  type PresentedTranscriptSegment,
} from '../../utils/work-activity-timeline';

export function AgentTranscriptReader({
  segments,
  onFork,
  agent,
  onSelectSwarmMember,
  selectedSwarmMemberKey,
}: {
  segments: PresentedTranscriptSegment[];
  onFork?: (key: string) => void;
  /** 当前会话归属的 Agent（助手回合头展示身份）。 */
  agent?: { name: string; avatar?: string };
  onSelectSwarmMember?: (runId: string, swarmId: string, member: SwarmMemberProjection) => void;
  selectedSwarmMemberKey?: string;
}) {
  return (
    <>
      {segments.map((seg) => (
        <TranscriptSegmentView
          key={presentedTranscriptSegmentKey(seg)}
          seg={seg}
          onFork={onFork}
          agent={agent}
          onSelectSwarmMember={onSelectSwarmMember}
          selectedSwarmMemberKey={selectedSwarmMemberKey}
        />
      ))}
    </>
  );
}

function TranscriptSegmentView({
  seg,
  onFork,
  agent,
  onSelectSwarmMember,
  selectedSwarmMemberKey,
}: {
  seg: PresentedTranscriptSegment;
  onFork?: (key: string) => void;
  agent?: { name: string; avatar?: string };
  onSelectSwarmMember?: (runId: string, swarmId: string, member: SwarmMemberProjection) => void;
  selectedSwarmMemberKey?: string;
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
      return <WorkActivityTimeline segment={seg} onSelectSwarmMember={onSelectSwarmMember} selectedSwarmMemberKey={selectedSwarmMemberKey} />;
    case 'thinking-placeholder':
      return <OutputLoadingIndicator />;
    case 'turn-diff':
      return <TurnDiffCard text={seg.text} />;
  }
}

function UserBubble({ msg }: { msg: ChatMessage }) {
  return (
    <article className="chat-user-turn group" aria-label="你的消息">
      <div className="chat-user-card whitespace-pre-wrap break-words">
        {msg.text}
      </div>
      <MessageActions text={msg.text} className="mt-1" />
    </article>
  );
}

import { AssistantTurn } from './assistant-turn';
import { MessageActions } from './message-actions';
import { ReasoningProcessPanel } from './reasoning-activity-row';
import { ThinkingPlaceholder } from './thinking-placeholder';
import { TurnDiffCard } from './turn-diff-card';
import { ActivityGroup } from './tool-card';
import type { ChatMessage } from '../../stores/chat.store';
import { ApprovalCard } from './approval-card';
import type { TranscriptSegment } from '../../utils/chronological-transcript';
import { transcriptSegmentKey } from '../../utils/approval-transcript';
import { formatTime } from '../../utils/format';
import type { RefObject } from 'react';

export function TranscriptView({
  segments,
  stoppedRuns,
  onFork,
  agent,
  scrollContainerRef,
}: {
  segments: TranscriptSegment[];
  stoppedRuns?: ReadonlySet<string>;
  onFork?: (key: string) => void;
  /** 当前会话归属的 Agent（助手回合头展示身份）。 */
  agent?: { name: string; avatar?: string };
  /** 聊天拥有独立滚动视口，供 Aceternity TracingBeam 正确计算进度。 */
  scrollContainerRef?: RefObject<HTMLDivElement | null>;
}) {
  return (
    <>
      {segments.map((seg) => (
        <TranscriptSegmentView
          key={transcriptSegmentKey(seg)}
          seg={seg}
          stoppedRuns={stoppedRuns}
          onFork={onFork}
          agent={agent}
          scrollContainerRef={scrollContainerRef}
        />
      ))}
    </>
  );
}

function TranscriptSegmentView({
  seg,
  stoppedRuns,
  onFork,
  agent,
  scrollContainerRef,
}: {
  seg: TranscriptSegment;
  stoppedRuns?: ReadonlySet<string>;
  onFork?: (key: string) => void;
  agent?: { name: string; avatar?: string };
  scrollContainerRef?: RefObject<HTMLDivElement | null>;
}) {
  switch (seg.kind) {
    case 'user':
      return <UserBubble msg={seg.msg} />;
    case 'assistant':
      return (
        <AssistantTurn
          text={seg.msg.text}
          at={seg.msg.at}
          streaming={seg.streaming}
          forkKey={seg.msg.key}
          onFork={onFork}
          agentName={agent?.name}
          agentAvatar={agent?.avatar}
          scrollContainerRef={scrollContainerRef}
        />
      );
    case 'thinking':
      return (
        <ReasoningProcessPanel
          key={seg.msg.key}
          panelKey={seg.msg.key}
          text={seg.msg.text}
          streaming={seg.streaming}
          defaultExpanded
        />
      );
    case 'meta':
      return <MetaLine msg={seg.msg} />;
    case 'activity':
      return (
        <ActivityGroup
          items={seg.items}
          stoppedRuns={stoppedRuns}
          defaultCollapsed={stoppedRuns?.has(seg.runId) ?? false}
        />
      );
    case 'thinking-placeholder':
      return <ThinkingPlaceholder />;
    case 'turn-diff':
      return <TurnDiffCard text={seg.text} />;
    case 'approval':
      return <ApprovalCard approval={seg.approval} />;
  }
}

function UserBubble({ msg }: { msg: ChatMessage }) {
  return (
    <div className="group flex justify-end py-1">
      <div className="w-max max-w-full min-w-0 rounded-3xl border border-brand-primary/20 bg-brand-primary/[0.07] px-3 py-1.5 text-base leading-6 text-text-primary whitespace-pre-wrap break-words">
        {msg.text}
        <MessageActions text={msg.text} at={msg.at} side="right" className="mt-1" />
      </div>
    </div>
  );
}

function MetaLine({ msg }: { msg: ChatMessage }) {
  return (
    <div className="py-0.5">
      <div className={`text-center text-caption ${msg.kind === 'error' ? 'text-status-error' : 'text-text-tertiary'}`}>
        {msg.kind === 'error' ? '✕ ' : ''}{msg.text}
        <span className="ml-1 tabular-nums">{formatTime(msg.at)}</span>
      </div>
      {msg.detail && (
        <pre className="mx-auto mt-1 max-h-48 w-full overflow-y-auto whitespace-pre-wrap break-words rounded-md bg-surface-base px-3 py-2 text-left font-mono text-[11px] leading-4 text-text-secondary">
          {msg.detail}
        </pre>
      )}
    </div>
  );
}

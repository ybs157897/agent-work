import { AssistantTurn } from './assistant-turn';
import { MessageActions } from './message-actions';
import { ThinkingPlaceholder } from './thinking-placeholder';
import { TurnDiffCard } from './turn-diff-card';
import { ActivityGroup } from './tool-card';
import type { TranscriptTurn } from '../../utils/transcript-layout';
import { formatTime } from '../../utils/format';
import type { ChatMessage } from '../../stores/chat.store';

export function TurnBlock({
  turn,
  stoppedRuns,
  onFork,
}: {
  turn: TranscriptTurn;
  stoppedRuns?: ReadonlySet<string>;
  onFork?: (key: string) => void;
}) {
  const hasPreActivity = turn.preActivity.length > 0 || turn.reasoning !== undefined;
  const hasPostActivity = turn.postActivity.length > 0;
  const suppressToolDiff = Boolean(turn.turnDiff);

  return (
    <div className="chat-turn flex flex-col gap-3" data-run-id={turn.runId}>
      {turn.user && <UserBubble msg={turn.user} />}

      {hasPreActivity && (
        <ActivityGroup
          items={turn.preActivity}
          reasoning={turn.reasoning}
          assistantAt={turn.assistant?.at}
          stoppedRuns={stoppedRuns}
          defaultCollapsed={stoppedRuns?.has(turn.runId) ?? false}
          suppressDiff={suppressToolDiff}
        />
      )}

      {turn.assistant && (
        <AssistantTurn
          text={turn.assistant.text}
          at={turn.assistant.at}
          streaming={turn.assistant.streaming}
          forkKey={turn.assistant.forkKey}
          onFork={onFork}
        />
      )}

      {hasPostActivity && (
        <ActivityGroup
          items={turn.postActivity}
          stoppedRuns={stoppedRuns}
          defaultCollapsed={stoppedRuns?.has(turn.runId) ?? false}
          suppressDiff={suppressToolDiff}
        />
      )}

      {turn.showThinkingPlaceholder && <ThinkingPlaceholder />}

      {turn.turnDiff && <TurnDiffCard text={turn.turnDiff} />}

      {turn.meta.map((m) => (
        <MetaLine key={m.key} msg={m} />
      ))}
    </div>
  );
}

function UserBubble({ msg }: { msg: ChatMessage }) {
  return (
    <div className="group flex justify-end py-1">
      <div className="w-max max-w-full min-w-0 rounded-3xl border border-border-subtle/80 bg-surface-raised/70 px-3 py-1.5 text-base leading-6 text-text-primary backdrop-blur-sm whitespace-pre-wrap break-words">
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

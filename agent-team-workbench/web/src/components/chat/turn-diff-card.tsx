import { DiffCard } from './diff-card';

/** 回合级 unified diff 汇总（Codex turn-diff 槽位）。 */
export function TurnDiffCard({ text }: { text: string }) {
  if (!text.trim()) return null;
  return (
    <div className="chat-turn-diff py-1">
      <DiffCard text={text} />
    </div>
  );
}

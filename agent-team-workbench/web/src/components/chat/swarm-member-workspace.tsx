import { X } from 'lucide-react';
import type { SwarmMemberProjection } from '../../stores/chat.store';
import { AgentTranscriptReader } from './transcript-view';
import type { PresentedTranscriptSegment } from '../../utils/work-activity-timeline';

export interface SwarmMemberSelection {
  runId: string;
  swarmId: string;
  memberId: string;
}

export function isSameSwarmMemberSelection(left: SwarmMemberSelection | null, right: SwarmMemberSelection): boolean {
  return left?.runId === right.runId && left.swarmId === right.swarmId && left.memberId === right.memberId;
}
export function swarmAgentLabel(name: string): string {
  const raw = name.trim().toLowerCase();
  if (raw === 'explore') return '探索 Agent';
  if (raw === 'coder') return '编码 Agent';
  return name || '未知 Agent';
}

export function buildMemberTranscript(runId: string, member: SwarmMemberProjection, segments?: PresentedTranscriptSegment[]): PresentedTranscriptSegment[] {
  const taskSegment: PresentedTranscriptSegment = {
    kind: 'user',
    msg: {
      key: `${runId}:subagent-task:${member.id}`,
      runId,
      kind: 'user',
      text: member.description,
      at: member.updatedAt,
    },
  };
  if (segments?.length) return [taskSegment, ...segments];

  const fallback = member.summary || member.error || member.reason
    || (member.status === 'running'
      ? 'Agent 正在输出，结果将在此处实时出现。'
      : member.status === 'waiting'
        ? '正在等待前置结果。'
        : member.status === 'failed'
          ? '该 Agent 未返回结果。'
          : '暂无输出。');
  return [
    taskSegment,
    {
      kind: 'assistant',
      msg: {
        key: `${runId}:subagent-fallback:${member.id}`,
        runId,
        kind: 'assistant',
        text: fallback,
        at: member.updatedAt,
      },
    },
  ];
}

export function SwarmMemberWorkspace({ runId, member, segments, onClose }: { runId: string; member: SwarmMemberProjection; segments?: PresentedTranscriptSegment[]; onClose: () => void }) {
  const agentLabel = swarmAgentLabel(member.name);
  const runtimeLabel = member.runtime === 'codex' ? 'Codex 子 Agent' : 'Kimi AgentSwarm';
  const transcript = buildMemberTranscript(runId, member, segments);
  return (
    <aside className="flex min-h-0 w-[min(28rem,42vw)] shrink-0 flex-col border-l border-border-strong bg-surface-warm" aria-label={member.runtime === 'codex' ? 'Codex 子 Agent 详情' : `第 ${member.index} 项详情`}>
      <div className="flex h-12 shrink-0 items-center justify-between border-b border-border-subtle px-4">
        <div className="min-w-0"><p className="text-caption text-text-tertiary">{runtimeLabel}{member.runtime === 'codex' ? '' : ` · 第 ${member.index} 项`}</p><h2 className="truncate font-display text-body-lg text-text-primary">{agentLabel}</h2></div>
        <button type="button" aria-label="关闭子 Agent 详情" title="关闭" onClick={onClose} className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-surface-sunken hover:text-text-primary focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand-primary"><X className="h-4 w-4" aria-hidden="true" /></button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-4">
        <div className="space-y-3">
          <AgentTranscriptReader segments={transcript} agent={{ name: agentLabel }} />
        </div>
      </div>
    </aside>
  );
}

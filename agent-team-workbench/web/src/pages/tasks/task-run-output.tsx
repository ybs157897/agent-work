import { useEffect, useMemo } from 'react';
import type { DispatchRun, RunStatus } from '../../api/types';
import { AgentOutput } from '../../components/chat/agent-output';
import { MessageActions } from '../../components/chat/message-actions';
import { Button } from '../../components/ui';
import { useRunsStore, type TimelineEntry } from '../../stores/runs.store';
import { parseContentBlockDocument, type ContentBlockDocument } from '../../utils/content-blocks';

export interface TaskRunIOProjection {
  input: string;
  output: string;
  contentBlocks?: ContentBlockDocument;
}

/**
 * Task 详情只投影一次执行的输入与最终结果。
 *
 * `message.completed` 在同一 run 中可能包含阶段性说明；只有 run 成功落终态后，
 * 最后一条 assistant completed 才能作为最终输出，避免把执行过程误标成结果。
 */
export function projectTaskRunIO(
  timeline: readonly TimelineEntry[],
  status: RunStatus,
): TaskRunIOProjection {
  const created = timeline.find((entry) => entry.type === 'run.created');
  const input = typeof created?.data?.instruction === 'string' ? created.data.instruction.trim() : '';

  if (status !== 'succeeded') return { input, output: '' };

  let final: TimelineEntry | undefined;
  for (const entry of timeline) {
    if (entry.type !== 'message.completed') continue;
    // 同一 Run 可包含子 Agent 事件；Task 行代表主执行 Agent，因此只允许
    // main（以及旧事件缺省的 agent_id）成为最终输出。
    if (entry.agent_id && entry.agent_id !== 'main') continue;
    const role = entry.role ?? (typeof entry.data?.role === 'string' ? entry.data.role : undefined);
    const itemType = typeof entry.data?.item_type === 'string' ? entry.data.item_type : undefined;
    if ((role && role !== 'assistant') || itemType === 'plan') continue;
    final = entry;
  }

  const output = typeof final?.text === 'string'
    ? final.text.trim()
    : typeof final?.data?.text === 'string'
      ? final.data.text.trim()
      : '';
  const contentBlocks = parseContentBlockDocument(final?.data?.content_blocks) ?? undefined;
  return { input, output, ...(contentBlocks ? { contentBlocks } : {}) };
}

export function TaskRunOutput({ run, agentName }: { run: DispatchRun; agentName: string }) {
  const snapshot = useRunsStore((state) => state.runs[run.id]);
  const timeline = useRunsStore((state) => state.timelines[run.id]);
  const historyError = useRunsStore((state) => state.historyErrors[run.id]);
  const historyLoading = useRunsStore((state) => state.historyLoading[run.id]);
  const loadHistory = useRunsStore((state) => state.loadHistory);
  const watchRun = useRunsStore((state) => state.watchRun);
  const unwatchRun = useRunsStore((state) => state.unwatchRun);

  useEffect(() => {
    watchRun(run.id);
    return () => unwatchRun(run.id);
  }, [run.id, unwatchRun, watchRun]);

  const status = snapshot?.status ?? run.status;
  const projection = useMemo(
    () => projectTaskRunIO(timeline ?? [], status),
    [status, timeline],
  );
  const input = projection.input || run.summary?.trim() || '';
  const outputPlaceholder = finalOutputPlaceholder(status);

  return (
    <div className="plane-task-agent-io" data-task-run-output={run.id}>
      <section className="plane-task-io-block" aria-label={`${agentName} 的输入`}>
        <h4 className="mb-tight text-caption font-semibold text-text-tertiary">输入</h4>
        {timeline === undefined && !historyError ? (
          <p className="text-body text-text-tertiary">输入加载中…</p>
        ) : input ? (
          <p className="whitespace-pre-wrap break-words text-body leading-relaxed text-text-primary">{input}</p>
        ) : (
          <p className="text-body text-text-tertiary">{historyError ? '输入暂时无法读取' : '无可展示输入'}</p>
        )}
      </section>

      <section className="plane-task-io-block" aria-label={`${agentName} 的最终输出`}>
        <h4 className="mb-tight text-caption font-semibold text-text-tertiary">最终输出</h4>
        {historyError && (
          <div className="mb-snug flex flex-wrap items-center justify-between gap-tight rounded-control border border-status-error/30 bg-status-error/5 p-snug" role="alert">
            <p className="text-body text-status-error">{historyError}</p>
            <Button size="sm" type="button" disabled={historyLoading} onClick={() => void loadHistory(run.id)} aria-label={`重试加载 ${agentName} 的输入与输出`}>
              重试加载
            </Button>
          </div>
        )}
        {timeline === undefined && !historyError ? (
          <p className="text-body text-text-tertiary">结果加载中…</p>
        ) : projection.output || projection.contentBlocks ? (
          <>
            <AgentOutput
              text={projection.output}
              contentBlocks={projection.contentBlocks}
              runId={run.id}
              messageId={`${run.id}-task-final`}
              showCaret={false}
            />
            {projection.output && <MessageActions text={projection.output} className="mt-tight" />}
          </>
        ) : !historyError ? (
          <p className="text-body text-text-tertiary">{outputPlaceholder}</p>
        ) : null}
      </section>
    </div>
  );
}

function finalOutputPlaceholder(status: RunStatus): string {
  if (status === 'failed' || status === 'lost') return '本次执行失败，未生成最终结果';
  if (status === 'cancelled' || status === 'interrupted') return '本次执行已结束，未生成最终结果';
  if (status === 'succeeded') return '本次执行未返回可展示的最终结果';
  return 'Agent 完成执行后在这里显示最终结果';
}

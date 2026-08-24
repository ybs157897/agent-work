import type { ChatMessage } from '../stores/chat.store';
import { ACTIVE } from '../stores/chat.store';
import { aggregateTurnDiff } from './turn-diff';

export interface AssistantSlot {
  text: string;
  at?: string;
  forkKey?: string;
  streaming?: boolean;
}

export interface ReasoningSlot {
  key: string;
  text: string;
  streaming?: boolean;
  /** thinking 落定时间（message.completed），用于 worked-for 估算。 */
  at?: string;
}

/** 单 run 的 Codex 槽位编排（§0.3）。 */
export interface TranscriptTurn {
  runId: string;
  user?: ChatMessage;
  preActivity: ChatMessage[];
  reasoning?: ReasoningSlot;
  assistant?: AssistantSlot;
  postActivity: ChatMessage[];
  plan?: ChatMessage;
  turnDiff?: string;
  meta: ChatMessage[];
  showThinkingPlaceholder: boolean;
}

export interface LayoutTranscriptOptions {
  /** runId → status */
  runStatuses?: Readonly<Record<string, string>>;
  liveRunId?: string;
  liveStream?: { reasoning: string; answerDraft: string };
  awaitingReply?: boolean;
  hasPendingApproval?: boolean;
}

function isActivityMessage(m: ChatMessage): boolean {
  return m.toolStatus !== undefined;
}

function isMetaMessage(m: ChatMessage): boolean {
  return (m.kind === 'system' || m.kind === 'error') && m.toolStatus === undefined;
}

function toolIsPostAssistant(m: ChatMessage, assistantAt: string | undefined): boolean {
  if (!assistantAt) return false;
  const t = m.startedAt ?? m.at;
  return t > assistantAt;
}

function partitionActivity(
  tools: ChatMessage[],
  assistantAt: string | undefined,
): { pre: ChatMessage[]; post: ChatMessage[] } {
  if (!assistantAt) return { pre: tools, post: [] };
  const pre: ChatMessage[] = [];
  const post: ChatMessage[] = [];
  for (const m of tools) {
    if (toolIsPostAssistant(m, assistantAt)) post.push(m);
    else pre.push(m);
  }
  return { pre, post };
}

function hasRunningTools(items: readonly ChatMessage[]): boolean {
  return items.some((m) => m.toolStatus === 'running');
}

function buildTurnFromMessages(runId: string, msgs: ChatMessage[], opts: LayoutTranscriptOptions): TranscriptTurn {
  let user: ChatMessage | undefined;
  let plan: ChatMessage | undefined;
  let assistantMsg: ChatMessage | undefined;
  let thinking: ChatMessage | undefined;
  const activity: ChatMessage[] = [];
  const meta: ChatMessage[] = [];

  for (const m of msgs) {
    switch (m.kind) {
      case 'user':
        user = m;
        break;
      case 'plan':
        plan = m;
        break;
      case 'assistant':
        assistantMsg = m;
        break;
      case 'thinking':
        thinking = m;
        break;
      default:
        if (isActivityMessage(m)) activity.push(m);
        else if (isMetaMessage(m)) meta.push(m);
        break;
    }
  }

  const isLive = opts.liveRunId === runId && (opts.awaitingReply ?? false);
  const liveStream = isLive ? opts.liveStream : undefined;

  let reasoning: ReasoningSlot | undefined;
  if (thinking?.text) {
    reasoning = { key: thinking.key, text: thinking.text, streaming: false, at: thinking.at };
  }
  if (liveStream?.reasoning) {
    reasoning = {
      key: `${runId}-live-reasoning`,
      text: liveStream.reasoning,
      streaming: true,
    };
  }

  let assistant: AssistantSlot | undefined;
  if (assistantMsg?.text && assistantMsg.itemType !== 'plan') {
    assistant = {
      text: assistantMsg.text,
      at: assistantMsg.at,
      forkKey: assistantMsg.key,
      streaming: false,
    };
  } else if (liveStream?.answerDraft) {
    assistant = {
      text: liveStream.answerDraft,
      streaming: true,
    };
  }

  const assistantAt = assistantMsg?.at;
  const { pre, post } = partitionActivity(activity, assistantAt);

  const allActivity = [...pre, ...post];
  const turnDiff = aggregateTurnDiff(allActivity) ?? undefined;

  const status = opts.runStatuses?.[runId];
  const runActive = status !== undefined && ACTIVE.has(status);
  const hasAssistantText = Boolean(assistant?.text?.trim());
  const running = hasRunningTools(allActivity);
  const showThinkingPlaceholder = Boolean(
    isLive &&
    runActive &&
    !hasAssistantText &&
    !running &&
    !opts.hasPendingApproval,
  );

  return {
    runId,
    user,
    preActivity: pre,
    reasoning,
    assistant,
    postActivity: post,
    plan,
    turnDiff,
    meta,
    showThinkingPlaceholder,
  };
}

/**
 * 将 buildMessages 产物按 run 分组并重排为 Codex turn 槽位。
 * 多 run 按 messages 中首次出现的 runId 顺序输出。
 */
export function layoutTranscript(messages: ChatMessage[], opts: LayoutTranscriptOptions = {}): TranscriptTurn[] {
  const runOrder: string[] = [];
  const byRun = new Map<string, ChatMessage[]>();

  for (const m of messages) {
    if (!byRun.has(m.runId)) {
      runOrder.push(m.runId);
      byRun.set(m.runId, []);
    }
    byRun.get(m.runId)!.push(m);
  }

  // 流式 run 可能尚无 messages 条目
  if (opts.liveRunId && opts.awaitingReply && !byRun.has(opts.liveRunId)) {
    runOrder.push(opts.liveRunId);
    byRun.set(opts.liveRunId, []);
  }

  return runOrder.map((runId) => buildTurnFromMessages(runId, byRun.get(runId) ?? [], opts));
}

/** reasoning 折叠预览：流式取最后一行，落定取首行。 */
export function reasoningPreview(text: string, streaming: boolean): string {
  const visible = text.trimEnd();
  if (!visible) return '';
  if (streaming) {
    const nl = visible.lastIndexOf('\n');
    return nl === -1 ? visible : visible.slice(nl + 1);
  }
  const nl = visible.indexOf('\n');
  return nl === -1 ? visible : visible.slice(0, nl);
}

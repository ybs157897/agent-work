import { create } from 'zustand';
import { ApiError } from '../api/client';
import {
  analyzeTaskIntake,
  type TaskIntakeDraft,
  type TaskIntakeMessage,
  type TaskIntakeQuestion,
} from '../api/task-intake';
import { createWorkItem, type CreateWorkItemInput } from '../api/endpoints';
import type { Priority, WorkItem } from '../api/types';
import { captureScope, isCurrent, registerWorkspaceScopedReset, type Scope } from './scope';

const STORAGE_VERSION = 1;
export const TASK_INTAKE_STORAGE_PREFIX = 'task-intake:v1:';
export const TASK_INTAKE_MAX_MESSAGES = 40;
export const TASK_INTAKE_MAX_MESSAGE_RUNES = 8_000;
export const TASK_INTAKE_MAX_TRANSCRIPT_RUNES = 40_000;

export interface TaskIntakeMessageRecord extends TaskIntakeMessage {
  id: string;
}

export interface TaskIntakeQuestionAnswer {
  selected: string[];
  supplement: string;
  submitted: boolean;
  /** submitted = true 代表显式提交答案；continued 代表通过自由对话跳过本题。 */
  lockedReason?: 'submitted' | 'continued';
}

export type TaskIntakeQuestionAnswers = Record<string, TaskIntakeQuestionAnswer>;

export interface TaskIntakeAnalyzeOptions {
  /** 提交问题时保留底部尚未发送的自由输入。 */
  preserveComposer?: boolean;
}

interface PersistedTaskIntakeState {
  version: typeof STORAGE_VERSION;
  messages: TaskIntakeMessageRecord[];
  questionAnswers: TaskIntakeQuestionAnswers;
  draft: TaskIntakeDraft | null;
  /** 仅供后续分析理解，不代表当前可发布草案。 */
  analysisContextDraft: TaskIntakeDraft | null;
  composerText: string;
  modelRef: string;
  priority: Priority;
  dueDate: string;
  clientKey: string | null;
  clientKeyPayload: string | null;
  pendingAnalysis: PendingAnalysis | null;
  pendingPublication: PendingPublication | null;
  publishedReceipt: TaskIntakePublicationReceipt | null;
}

export type TaskIntakeErrorKind = 'analyze' | 'publish';

/**
 * 服务端可能已经接受了发布请求但响应在网络中丢失。此快照冻结原始
 * payload 与 key，关闭/刷新后仍只能重试同一发布，避免误建第二个任务。
 */
export interface PendingPublication {
  workspaceId: string;
  input: CreateWorkItemInput;
  clientKey: string;
  fingerprint: string;
}

export interface TaskIntakePublicationReceipt {
  workspaceId: string;
  workItemId: string;
  title: string;
  publishedAt: string;
}

/** 刷新或关闭时仍可恢复的最后一次分析请求；不含运行时 Scope。 */
export interface PendingAnalysis {
  workspaceId: string;
  messages: TaskIntakeMessageRecord[];
  draft?: TaskIntakeDraft;
  modelRef?: string;
}

export interface TaskIntakeStore {
  /** 当前内存投影所属 Workspace；切换期间为 null，避免串读。 */
  workspaceId: string | null;
  messages: TaskIntakeMessageRecord[];
  questionAnswers: TaskIntakeQuestionAnswers;
  draft: TaskIntakeDraft | null;
  /** 最新需求上下文；draft=null 时仍可保留，绝不直接用于发布。 */
  analysisContextDraft: TaskIntakeDraft | null;
  composerText: string;
  /** 空串表示使用服务端配置的任务默认模型。 */
  modelRef: string;
  priority: Priority;
  dueDate: string;
  /** 发布失败重试时保留；payload 变化后会换新 key。 */
  clientKey: string | null;
  clientKeyPayload: string | null;
  pendingAnalysis: PendingAnalysis | null;
  pendingPublication: PendingPublication | null;
  publishedReceipt: TaskIntakePublicationReceipt | null;
  sending: boolean;
  publishing: boolean;
  error: string | null;
  errorKind: TaskIntakeErrorKind | null;
  canRetry: boolean;
  hydrated: boolean;

  hydrate: (workspaceId: string) => void;
  reset: () => void;
  clear: () => void;
  analyze: (workspaceId: string, content: string, options?: TaskIntakeAnalyzeOptions) => Promise<void>;
  retryAnalyze: () => Promise<void>;
  submitQuestionAnswers: (workspaceId: string, messageId: string) => Promise<void>;
  setQuestionSelection: (messageId: string, questionId: string, selected: string[]) => void;
  setQuestionSupplement: (messageId: string, questionId: string, supplement: string) => void;
  invalidateDraft: () => void;
  setDraft: (draft: TaskIntakeDraft | null) => void;
  setDraftField: (field: keyof TaskIntakeDraft, value: string | string[]) => void;
  setPriority: (priority: Priority) => void;
  setDueDate: (dueDate: string) => void;
  setComposerText: (text: string) => void;
  setModelRef: (modelRef: string) => void;
  publish: (workspaceId: string) => Promise<WorkItem | undefined>;
}

const DEFAULT_PRIORITY: Priority = 'medium';

function emptyState(workspaceId: string | null = null): Pick<
  TaskIntakeStore,
  | 'workspaceId'
  | 'messages'
  | 'questionAnswers'
  | 'draft'
  | 'analysisContextDraft'
  | 'composerText'
  | 'modelRef'
  | 'priority'
  | 'dueDate'
  | 'clientKey'
  | 'clientKeyPayload'
  | 'pendingAnalysis'
  | 'pendingPublication'
  | 'publishedReceipt'
  | 'sending'
  | 'publishing'
  | 'error'
  | 'errorKind'
  | 'canRetry'
  | 'hydrated'
> {
  return {
    workspaceId,
    messages: [],
    questionAnswers: {},
    draft: null,
    analysisContextDraft: null,
    composerText: '',
    modelRef: '',
    priority: DEFAULT_PRIORITY,
    dueDate: '',
    clientKey: null,
    clientKeyPayload: null,
    pendingAnalysis: null,
    pendingPublication: null,
    publishedReceipt: null,
    sending: false,
    publishing: false,
    error: null,
    errorKind: null,
    canRetry: false,
    hydrated: false,
  };
}

export function taskIntakeStorageKey(workspaceId: string): string {
  return `${TASK_INTAKE_STORAGE_PREFIX}${encodeURIComponent(workspaceId)}`;
}

function getStorage(): Storage | null {
  if (typeof window === 'undefined') return null;
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null;
}

function isPriority(value: unknown): value is Priority {
  return value === 'low' || value === 'medium' || value === 'high' || value === 'urgent';
}

function normalizeDraft(value: unknown): TaskIntakeDraft | null {
  if (value === null || value === undefined) return null;
  if (!isRecord(value)) return null;
  if (typeof value.title !== 'string' || typeof value.description !== 'string') return null;
  if (!Array.isArray(value.acceptance_criteria)) return null;
  const acceptanceCriteria = value.acceptance_criteria.filter((item): item is string => typeof item === 'string');
  if (acceptanceCriteria.length !== value.acceptance_criteria.length) return null;
  return {
    title: value.title,
    description: value.description,
    acceptance_criteria: acceptanceCriteria,
  };
}

function normalizeQuestions(value: unknown): TaskIntakeQuestion[] | null {
  if (!Array.isArray(value) || value.length > 3) return null;
  const seen = new Set<string>();
  const questions: TaskIntakeQuestion[] = [];
  for (const item of value) {
    if (!isRecord(item) || typeof item.id !== 'string' || typeof item.title !== 'string' || typeof item.multiple !== 'boolean' || !Array.isArray(item.options)) return null;
    const id = item.id.trim();
    const title = item.title.trim();
    if (!id || !title || seen.has(id) || item.options.length < 2 || item.options.length > 5) return null;
    const options = item.options.map((option) => typeof option === 'string' ? option.trim() : '');
    if (options.some((option) => !option) || new Set(options).size !== options.length) return null;
    seen.add(id);
    questions.push({ id, title, options, multiple: item.multiple });
  }
  return questions;
}

function normalizeQuestionAnswers(value: unknown): TaskIntakeQuestionAnswers | null {
  if (value === undefined) return {};
  if (!isRecord(value)) return null;
  const answers: TaskIntakeQuestionAnswers = {};
  for (const [key, item] of Object.entries(value)) {
    if (!isRecord(item) || !Array.isArray(item.selected) || item.selected.some((option) => typeof option !== 'string') || typeof item.supplement !== 'string' || typeof item.submitted !== 'boolean') return null;
    if (item.lockedReason !== undefined && item.lockedReason !== 'submitted' && item.lockedReason !== 'continued') return null;
    answers[key] = {
      selected: [...item.selected],
      supplement: item.supplement,
      submitted: item.submitted,
      ...(item.lockedReason ? { lockedReason: item.lockedReason } : {}),
    };
  }
  return answers;
}

function normalizeMessage(value: unknown): TaskIntakeMessageRecord | null {
  if (!isRecord(value) || typeof value.id !== 'string' || typeof value.content !== 'string') return null;
  if (value.role !== 'user' && value.role !== 'assistant') return null;
  const questions = value.questions === undefined ? undefined : normalizeQuestions(value.questions);
  if (value.questions !== undefined && !questions) return null;
  return { id: value.id, role: value.role, content: value.content, ...(questions ? { questions } : {}) };
}

function normalizeCreateWorkItemInput(value: unknown): CreateWorkItemInput | null {
  if (!isRecord(value) || typeof value.title !== 'string' || value.record_kind !== 'task') return null;
  if (value.description !== undefined && typeof value.description !== 'string') return null;
  if (value.status !== undefined && value.status !== 'todo') return null;
  if (value.priority !== undefined && !isPriority(value.priority)) return null;
  if (value.due_date !== undefined && value.due_date !== null && typeof value.due_date !== 'string') return null;
  if (!Array.isArray(value.acceptance_criteria) || value.acceptance_criteria.some((item) => typeof item !== 'string')) return null;
  if (typeof value.client_key !== 'string' || !value.client_key) return null;
  return {
    title: value.title,
    record_kind: 'task',
    status: 'todo',
    ...(typeof value.description === 'string' ? { description: value.description } : {}),
    ...(isPriority(value.priority) ? { priority: value.priority } : {}),
    ...(value.due_date === null || typeof value.due_date === 'string' ? { due_date: value.due_date } : {}),
    acceptance_criteria: [...value.acceptance_criteria],
    client_key: value.client_key,
  };
}

function normalizePendingPublication(value: unknown): PendingPublication | null {
  if (!isRecord(value) || typeof value.workspaceId !== 'string' || typeof value.clientKey !== 'string' || typeof value.fingerprint !== 'string') return null;
  const input = normalizeCreateWorkItemInput(value.input);
  if (!input || input.client_key !== value.clientKey) return null;
  return { workspaceId: value.workspaceId, input, clientKey: value.clientKey, fingerprint: value.fingerprint };
}

function normalizePublishedReceipt(value: unknown): TaskIntakePublicationReceipt | null {
  if (!isRecord(value)) return null;
  if (typeof value.workspaceId !== 'string' || typeof value.workItemId !== 'string' || typeof value.title !== 'string' || typeof value.publishedAt !== 'string') return null;
  if (!value.workspaceId || !value.workItemId || !value.title || !value.publishedAt) return null;
  return {
    workspaceId: value.workspaceId,
    workItemId: value.workItemId,
    title: value.title,
    publishedAt: value.publishedAt,
  };
}

function normalizePendingAnalysis(value: unknown): PendingAnalysis | null {
  if (!isRecord(value) || typeof value.workspaceId !== 'string' || !Array.isArray(value.messages)) return null;
  const messages = value.messages.map(normalizeMessage).filter((item): item is TaskIntakeMessageRecord => item !== null);
  if (messages.length !== value.messages.length) return null;
  const draft = normalizeDraft(value.draft);
  if (value.draft !== undefined && value.draft !== null && !draft) return null;
  const modelRef = typeof value.modelRef === 'string' && value.modelRef.trim() ? value.modelRef : undefined;
  return { workspaceId: value.workspaceId, messages, ...(draft ? { draft } : {}), ...(modelRef ? { modelRef } : {}) };
}

function readPersisted(workspaceId: string): Partial<PersistedTaskIntakeState> {
  const storage = getStorage();
  if (!storage) return {};
  try {
    const raw = storage.getItem(taskIntakeStorageKey(workspaceId));
    if (!raw) return {};
    const parsed: unknown = JSON.parse(raw);
    if (!isRecord(parsed) || parsed.version !== STORAGE_VERSION) return {};
    const messages = Array.isArray(parsed.messages)
      ? parsed.messages.map(normalizeMessage).filter((item): item is TaskIntakeMessageRecord => item !== null)
      : [];
    const questionAnswers = normalizeQuestionAnswers(parsed.questionAnswers);
    if (!questionAnswers) return {};
    const draft = normalizeDraft(parsed.draft);
    return {
      version: STORAGE_VERSION,
      messages,
      questionAnswers,
      draft,
      analysisContextDraft: normalizeDraft(parsed.analysisContextDraft),
      composerText: typeof parsed.composerText === 'string' ? parsed.composerText : '',
      modelRef: typeof parsed.modelRef === 'string' ? parsed.modelRef : '',
      priority: isPriority(parsed.priority) ? parsed.priority : DEFAULT_PRIORITY,
      dueDate: typeof parsed.dueDate === 'string' ? parsed.dueDate : '',
      clientKey: typeof parsed.clientKey === 'string' ? parsed.clientKey : null,
      clientKeyPayload: typeof parsed.clientKeyPayload === 'string' ? parsed.clientKeyPayload : null,
      pendingAnalysis: normalizePendingAnalysis(parsed.pendingAnalysis),
      pendingPublication: normalizePendingPublication(parsed.pendingPublication),
      publishedReceipt: normalizePublishedReceipt(parsed.publishedReceipt),
    };
  } catch {
    // 失效或被禁用的 Storage 不应阻断本次对话。
    return {};
  }
}

function persistState(state: Pick<
  TaskIntakeStore,
  | 'workspaceId'
  | 'messages'
  | 'questionAnswers'
  | 'draft'
  | 'analysisContextDraft'
  | 'composerText'
  | 'modelRef'
  | 'priority'
  | 'dueDate'
  | 'clientKey'
  | 'clientKeyPayload'
  | 'pendingAnalysis'
  | 'pendingPublication'
  | 'publishedReceipt'
>): void {
  if (!state.workspaceId) return;
  const storage = getStorage();
  if (!storage) return;
  const payload: PersistedTaskIntakeState = {
    version: STORAGE_VERSION,
    messages: state.messages,
    questionAnswers: state.questionAnswers,
    draft: state.draft,
    analysisContextDraft: state.analysisContextDraft,
    composerText: state.composerText,
    modelRef: state.modelRef,
    priority: state.priority,
    dueDate: state.dueDate,
    clientKey: state.clientKey,
    clientKeyPayload: state.clientKeyPayload,
    pendingAnalysis: state.pendingAnalysis,
    pendingPublication: state.pendingPublication,
    publishedReceipt: state.publishedReceipt,
  };
  try {
    storage.setItem(taskIntakeStorageKey(state.workspaceId), JSON.stringify(payload));
  } catch {
    // Storage 是增强能力；内存态仍然有效。
  }
}

function removePersisted(workspaceId: string | null): void {
  if (!workspaceId) return;
  const storage = getStorage();
  if (!storage) return;
  try {
    storage.removeItem(taskIntakeStorageKey(workspaceId));
  } catch {
    // Storage 不可写时保留内存清理结果。
  }
}

function cloneDraft(draft: TaskIntakeDraft | null | undefined): TaskIntakeDraft | undefined {
  if (!draft) return undefined;
  return {
    title: draft.title,
    description: draft.description,
    acceptance_criteria: [...draft.acceptance_criteria],
  };
}

function cloneQuestions(questions: readonly TaskIntakeQuestion[] | undefined): TaskIntakeQuestion[] | undefined {
  if (!questions) return undefined;
  return questions.map((question) => ({ ...question, options: [...question.options] }));
}

export function taskIntakeQuestionAnswerKey(messageId: string, questionId: string): string {
  return `${messageId}:${questionId}`;
}

/** 将 assistant 的结构化题目展开为后续分析可读的明文上下文。 */
export function formatTaskIntakeAssistantContext(
  message: Pick<TaskIntakeMessageRecord, 'role' | 'content' | 'questions'>,
): string {
  if (message.role !== 'assistant' || !message.questions?.length) return message.content;
  const questions = message.questions.map((question, index) =>
    `${index + 1}. ${question.title}\n选项：${question.options.join('、')}\n回答方式：${question.multiple ? '可多选' : '单选'}`,
  );
  return `${message.content}\n\n[需求澄清问题及选项]\n${questions.join('\n')}`;
}

/** 将一组题目的回答发送为一条普通 user 消息，不触发任何发布动作。 */
export function formatTaskIntakeQuestionAnswers(
  messageId: string,
  message: Pick<TaskIntakeMessageRecord, 'questions'>,
  answers: TaskIntakeQuestionAnswers,
): string {
  const lines = ['回答需求澄清问题：'];
  for (const [index, question] of (message.questions ?? []).entries()) {
    const answer = answers[taskIntakeQuestionAnswerKey(messageId, question.id)]
      ?? { selected: [], supplement: '', submitted: false };
    lines.push(`${index + 1}. ${question.title}`);
    if (answer.selected.length > 0) lines.push(`选择：${answer.selected.join('、')}`);
    if (answer.supplement.trim()) lines.push(`补充：${answer.supplement.trim()}`);
  }
  return lines.join('\n');
}

function markQuestionAnswersContinued(
  messages: readonly TaskIntakeMessageRecord[],
  answers: TaskIntakeQuestionAnswers,
): TaskIntakeQuestionAnswers {
  const next = Object.fromEntries(Object.entries(answers).map(([key, answer]) => [key, {
    selected: [...answer.selected],
    supplement: answer.supplement,
    submitted: answer.submitted,
    ...(answer.submitted ? { lockedReason: 'submitted' as const } : { lockedReason: 'continued' as const }),
  }])) as TaskIntakeQuestionAnswers;
  for (const message of messages) {
    if (message.role !== 'assistant' || !message.questions?.length) continue;
    for (const question of message.questions) {
      const key = taskIntakeQuestionAnswerKey(message.id, question.id);
      const answer = next[key];
      if (answer?.submitted || answer?.lockedReason === 'submitted') continue;
      next[key] = {
        selected: [...(answer?.selected ?? [])],
        supplement: answer?.supplement ?? '',
        submitted: false,
        lockedReason: 'continued',
      };
    }
  }
  return next;
}

function latestAnswerableQuestionMessage(
  messages: readonly TaskIntakeMessageRecord[],
  answers: TaskIntakeQuestionAnswers,
): TaskIntakeMessageRecord | undefined {
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    const message = messages[index];
    if (message.role !== 'assistant' || !message.questions?.length) continue;
    if (message.questions.some((question) => {
      const answer = answers[taskIntakeQuestionAnswerKey(message.id, question.id)];
      return !answer?.submitted && !answer?.lockedReason;
    })) return message;
  }
  return undefined;
}

function markQuestionAnswerSubmitted(
  answers: TaskIntakeQuestionAnswers,
  messageId: string,
  questions: readonly TaskIntakeQuestion[],
): TaskIntakeQuestionAnswers {
  const next = { ...answers };
  for (const question of questions) {
    const key = taskIntakeQuestionAnswerKey(messageId, question.id);
    const answer = next[key] ?? { selected: [], supplement: '', submitted: false };
    next[key] = {
      selected: [...answer.selected],
      supplement: answer.supplement,
      submitted: true,
      lockedReason: 'submitted',
    };
  }
  return next;
}

function cloneCreateWorkItemInput(input: CreateWorkItemInput): CreateWorkItemInput {
  return {
    ...input,
    ...(input.acceptance_criteria ? { acceptance_criteria: [...input.acceptance_criteria] } : {}),
  };
}

function isDefinitelyNotCreated(error: unknown): boolean {
  // 4xx validation/auth responses are definitive. Timeouts, 429/5xx and
  // idempotency conflicts can follow a server-side commit, so retain the key.
  return error instanceof ApiError
    && error.status >= 400
    && error.status < 500
    && error.status !== 408
    && error.status !== 409;
}

/** 分析响应边界校验：畸形响应可见地失败，绝不把未知内容当成可发布草案。 */
export function parseTaskIntakeResponse(value: unknown): {
  reply: string;
  draft?: TaskIntakeDraft;
  questions?: TaskIntakeQuestion[];
} {
  if (!isRecord(value) || typeof value.reply !== 'string') {
    throw new Error('分析响应格式无效，缺少 reply');
  }
  const draft = value.draft === undefined || value.draft === null ? undefined : normalizeDraft(value.draft);
  if (value.draft !== undefined && value.draft !== null && !draft) throw new Error('分析响应格式无效，draft 字段不完整');
  const questions = value.questions === undefined ? undefined : normalizeQuestions(value.questions);
  if (value.questions !== undefined && !questions) throw new Error('分析响应格式无效，questions 字段不完整');
  return {
    reply: value.reply,
    ...(draft ? { draft } : {}),
    ...(questions ? { questions } : {}),
  };
}

export function canPublishTaskIntakeDraft(draft: TaskIntakeDraft | null): boolean {
  return Boolean(draft?.title.trim() && draft.acceptance_criteria.some((item) => item.trim()));
}

export function taskIntakePayloadFingerprint(
  draft: TaskIntakeDraft,
  priority: Priority,
  dueDate: string,
): string {
  return JSON.stringify({
    title: draft.title.trim(),
    description: draft.description.trim(),
    acceptance_criteria: draft.acceptance_criteria.map((item) => item.trim()).filter(Boolean),
    priority,
    due_date: dueDate || null,
  });
}

function newClientKey(workspaceId: string): string {
  const uuid = typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
    ? crypto.randomUUID()
    : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
  return `task-intake:${workspaceId}:${uuid}`;
}

function analysisErrorMessage(error: unknown): string {
  if (error instanceof ApiError) return error.message;
  if (error instanceof Error && error.message) return error.message;
  return '分析失败，请重试';
}

function runeLength(value: string): number {
  return Array.from(value).length;
}

/** 与后端 taskintake.ValidateInput 对齐的发送前边界检查。 */
export function validateTaskIntakeMessage(
  content: string,
  messages: readonly Pick<TaskIntakeMessageRecord, 'content'>[],
): string | null {
  if (messages.length + 1 > TASK_INTAKE_MAX_MESSAGES) {
    return `澄清对话最多保留 ${TASK_INTAKE_MAX_MESSAGES} 条消息，请重新开始后继续。`;
  }
  const normalized = content.trim();
  const length = runeLength(normalized);
  if (length === 0) return '请输入要分析的任务描述。';
  if (length > TASK_INTAKE_MAX_MESSAGE_RUNES) {
    return `单条消息不能超过 ${TASK_INTAKE_MAX_MESSAGE_RUNES} 个字符。`;
  }
  const total = messages.reduce((sum, message) => sum + runeLength(message.content.trim()), 0) + length;
  if (total > TASK_INTAKE_MAX_TRANSCRIPT_RUNES) {
    return `澄清对话累计不能超过 ${TASK_INTAKE_MAX_TRANSCRIPT_RUNES} 个字符，请重新开始后继续。`;
  }
  return null;
}

interface FailedAnalysisRequest {
  workspaceId: string;
  scope: Scope;
  messages: TaskIntakeMessageRecord[];
  draft?: TaskIntakeDraft;
  modelRef?: string;
}

export const useTaskIntakeStore = create<TaskIntakeStore>()((set, get) => {
  let analysisSequence = 0;
  let failedAnalysis: FailedAnalysisRequest | null = null;

  const persistCurrent = () => persistState(get());

  const runAnalysis = async (request: FailedAnalysisRequest): Promise<void> => {
    const sequence = ++analysisSequence;
    set({ sending: true, error: null, errorKind: null, canRetry: false });
    try {
      const input = {
        messages: request.messages.map((message) => ({
          role: message.role,
          content: formatTaskIntakeAssistantContext(message),
        })),
        ...(request.draft ? { draft: cloneDraft(request.draft) } : {}),
        ...(request.modelRef ? { model_ref: request.modelRef } : {}),
      };
      const response = parseTaskIntakeResponse(await analyzeTaskIntake(request.workspaceId, input));
      if (sequence !== analysisSequence || !isCurrent(request.scope)) return;

      const assistant = response.reply.trim() || response.questions?.length
        ? [{
            id: `assistant:${Date.now().toString(36)}:${sequence}`,
            role: 'assistant' as const,
            content: response.reply,
            ...(response.questions ? { questions: cloneQuestions(response.questions) } : {}),
          }]
        : [];
      set({
        messages: [...request.messages, ...assistant],
        draft: response.draft ?? null,
        analysisContextDraft: cloneDraft(response.draft) ?? cloneDraft(request.draft) ?? null,
        sending: false,
        error: null,
        errorKind: null,
        canRetry: false,
        clientKey: null,
        clientKeyPayload: null,
        pendingAnalysis: null,
      });
      failedAnalysis = null;
      persistCurrent();
    } catch (error) {
      if (sequence !== analysisSequence || !isCurrent(request.scope)) return;
      failedAnalysis = request;
      set({
        sending: false,
        error: analysisErrorMessage(error),
        errorKind: 'analyze',
        canRetry: true,
        pendingAnalysis: {
          workspaceId: request.workspaceId,
          messages: request.messages,
          ...(request.draft ? { draft: cloneDraft(request.draft) } : {}),
          ...(request.modelRef ? { modelRef: request.modelRef } : {}),
        },
      });
      persistCurrent();
    }
  };

  return {
    ...emptyState(),

    hydrate: (workspaceId) => {
      if (!workspaceId) {
        failedAnalysis = null;
        analysisSequence += 1;
        set(emptyState());
        return;
      }
      const current = get();
      if (current.workspaceId === workspaceId && current.hydrated) return;
      failedAnalysis = null;
      analysisSequence += 1;
      const persisted = readPersisted(workspaceId);
      const pendingPublication = persisted.pendingPublication && persisted.pendingPublication.workspaceId === workspaceId
        ? persisted.pendingPublication
        : null;
      const pendingAnalysis = !pendingPublication
        && !persisted.publishedReceipt
        && persisted.pendingAnalysis
        && persisted.pendingAnalysis.workspaceId === workspaceId
        ? persisted.pendingAnalysis
        : null;
      const publishedReceipt = !pendingPublication
        && persisted.publishedReceipt
        && persisted.publishedReceipt.workspaceId === workspaceId
        ? persisted.publishedReceipt
        : null;
      if (pendingAnalysis) {
        const scope = captureScope();
        failedAnalysis = { ...pendingAnalysis, scope };
      }
      set({
        ...emptyState(workspaceId),
        messages: persisted.messages ?? [],
        questionAnswers: persisted.questionAnswers ?? {},
        draft: persisted.draft ?? null,
        analysisContextDraft: persisted.analysisContextDraft ?? persisted.draft ?? null,
        composerText: persisted.composerText ?? '',
        modelRef: persisted.modelRef ?? '',
        priority: persisted.priority ?? DEFAULT_PRIORITY,
        dueDate: persisted.dueDate ?? '',
        clientKey: persisted.clientKey ?? null,
        clientKeyPayload: persisted.clientKeyPayload ?? null,
        pendingAnalysis,
        pendingPublication,
        publishedReceipt,
        error: pendingPublication
          ? '发布结果尚未确认，请重试确认'
          : pendingAnalysis
            ? '上次分析未完成，请重试分析'
            : null,
        errorKind: pendingPublication ? 'publish' : pendingAnalysis ? 'analyze' : null,
        canRetry: Boolean(pendingAnalysis),
        hydrated: true,
      });
    },

    reset: () => {
      failedAnalysis = null;
      analysisSequence += 1;
      set(emptyState());
    },

    clear: () => {
      if (get().pendingPublication) return;
      const workspaceId = get().workspaceId;
      failedAnalysis = null;
      analysisSequence += 1;
      removePersisted(workspaceId);
      set({ ...emptyState(workspaceId), hydrated: Boolean(workspaceId) });
    },

    analyze: async (workspaceId, content, options) => {
      const text = content.trim();
      if (!text) return;
      const scope = captureScope();
      if (scope.workspaceId !== workspaceId || !isCurrent(scope)) return;
      const state = get();
      if (state.workspaceId !== workspaceId || !state.hydrated || state.sending || state.publishing || state.pendingPublication || state.publishedReceipt) return;
      const validationError = validateTaskIntakeMessage(text, state.messages);
      if (validationError) {
        failedAnalysis = null;
        set({ error: validationError, errorKind: 'analyze', canRetry: false, pendingAnalysis: null });
        persistCurrent();
        return;
      }

      // 发送新的澄清问题前，旧草案及其发布身份立即失效；旧草案只作为分析上下文提交。
      const contextDraft = cloneDraft(state.draft) ?? cloneDraft(state.analysisContextDraft);
      const messages: TaskIntakeMessageRecord[] = [
        ...state.messages,
        { id: `user:${Date.now().toString(36)}:${analysisSequence + 1}`, role: 'user', content: text },
      ];
      const request: FailedAnalysisRequest = {
        workspaceId,
        scope,
        messages,
        ...(contextDraft ? { draft: contextDraft } : {}),
        ...(state.modelRef ? { modelRef: state.modelRef } : {}),
      };
      failedAnalysis = null;
      set({
        messages,
        questionAnswers: markQuestionAnswersContinued(state.messages, state.questionAnswers),
        composerText: options?.preserveComposer ? state.composerText : '',
        draft: null,
        analysisContextDraft: contextDraft ?? null,
        clientKey: null,
        clientKeyPayload: null,
        pendingAnalysis: {
          workspaceId,
          messages,
          ...(contextDraft ? { draft: contextDraft } : {}),
          ...(state.modelRef ? { modelRef: state.modelRef } : {}),
        },
        error: null,
        errorKind: null,
        canRetry: false,
      });
      persistCurrent();
      await runAnalysis(request);
    },

    retryAnalyze: async () => {
      const request = failedAnalysis;
      if (!request || !get().canRetry || get().sending || get().publishing || get().pendingPublication || get().publishedReceipt) return;
      if (request.workspaceId !== get().workspaceId || !isCurrent(request.scope)) return;
      await runAnalysis(request);
    },

    submitQuestionAnswers: async (workspaceId, messageId) => {
      const scope = captureScope();
      const state = get();
      if (
        scope.workspaceId !== workspaceId
        || !isCurrent(scope)
        || state.workspaceId !== workspaceId
        || !state.hydrated
        || state.sending
        || state.publishing
        || state.pendingPublication
        || state.publishedReceipt
      ) return;
      if (latestAnswerableQuestionMessage(state.messages, state.questionAnswers)?.id !== messageId) return;
      const message = state.messages.find((item) => item.id === messageId && item.role === 'assistant');
      if (!message?.questions?.length) return;
      const questions = message.questions;
      const answers = state.questionAnswers;
      const missing = questions.filter((question) => {
        const answer = answers[taskIntakeQuestionAnswerKey(messageId, question.id)];
        return !answer || answer.lockedReason || answer.submitted || (!answer.selected.length && !answer.supplement.trim());
      });
      if (missing.length > 0) {
        set({
          error: `请回答：${missing.map((question) => question.title).join('、')}。可选择选项或填写补充说明。`,
          errorKind: 'analyze',
          canRetry: false,
        });
        persistCurrent();
        return;
      }
      const content = formatTaskIntakeQuestionAnswers(messageId, message, answers);
      const validationError = validateTaskIntakeMessage(content, state.messages);
      if (validationError) {
        set({ error: validationError, errorKind: 'analyze', canRetry: false });
        persistCurrent();
        return;
      }
      set({
        questionAnswers: markQuestionAnswerSubmitted(answers, messageId, questions),
        error: null,
        errorKind: null,
        canRetry: false,
      });
      persistCurrent();
      // 提交问题与底部自由 composer 是两个独立入口；问题提交不能顺带
      // 清掉尚未发送的自由补充，且该内容必须在 pendingAnalysis 落盘前保留。
      await get().analyze(workspaceId, content, { preserveComposer: true });
    },

    setQuestionSelection: (messageId, questionId, selected) => {
      const state = get();
      if (state.sending || state.publishing || state.pendingPublication || state.publishedReceipt) return;
      if (latestAnswerableQuestionMessage(state.messages, state.questionAnswers)?.id !== messageId) return;
      const message = state.messages.find((item) => item.id === messageId && item.role === 'assistant');
      const question = message?.questions?.find((item) => item.id === questionId);
      if (!question) return;
      const current = state.questionAnswers[taskIntakeQuestionAnswerKey(messageId, questionId)];
      if (current?.lockedReason || current?.submitted) return;
      const allowed = [...new Set(selected.filter((option) => question.options.includes(option)))];
      const nextSelected = question.multiple ? allowed : allowed.slice(0, 1);
      set({
        questionAnswers: {
          ...state.questionAnswers,
          [taskIntakeQuestionAnswerKey(messageId, questionId)]: {
            selected: nextSelected,
            supplement: current?.supplement ?? '',
            submitted: false,
          },
        },
      });
      persistCurrent();
    },

    setQuestionSupplement: (messageId, questionId, supplement) => {
      const state = get();
      if (state.sending || state.publishing || state.pendingPublication || state.publishedReceipt) return;
      if (latestAnswerableQuestionMessage(state.messages, state.questionAnswers)?.id !== messageId) return;
      const message = state.messages.find((item) => item.id === messageId && item.role === 'assistant');
      const question = message?.questions?.find((item) => item.id === questionId);
      if (!question) return;
      const current = state.questionAnswers[taskIntakeQuestionAnswerKey(messageId, questionId)];
      if (current?.lockedReason || current?.submitted) return;
      set({
        questionAnswers: {
          ...state.questionAnswers,
          [taskIntakeQuestionAnswerKey(messageId, questionId)]: {
            selected: [...(current?.selected ?? [])],
            supplement,
            submitted: false,
          },
        },
      });
      persistCurrent();
    },

    invalidateDraft: () => {
      if (get().pendingPublication || get().publishedReceipt) return;
      set({ draft: null, analysisContextDraft: null, clientKey: null, clientKeyPayload: null, error: null, errorKind: null, canRetry: false });
      persistCurrent();
    },

    setDraft: (draft) => {
      if (get().pendingPublication || get().publishedReceipt) return;
      const nextDraft = cloneDraft(draft) ?? null;
      set({ draft: nextDraft, analysisContextDraft: nextDraft, clientKey: null, clientKeyPayload: null, error: null, errorKind: null, canRetry: false });
      persistCurrent();
    },

    setDraftField: (field, value) => {
      if (get().pendingPublication || get().publishedReceipt) return;
      const draft = get().draft;
      if (!draft) return;
      const next: TaskIntakeDraft = {
        ...draft,
        [field]: Array.isArray(value) ? [...value] : value,
      } as TaskIntakeDraft;
      set({ draft: next, analysisContextDraft: next, error: get().errorKind === 'publish' ? null : get().error, errorKind: get().errorKind === 'publish' ? null : get().errorKind });
      persistCurrent();
    },

    setPriority: (priority) => {
      if (get().pendingPublication || get().publishedReceipt) return;
      set({ priority });
      persistCurrent();
    },

    setDueDate: (dueDate) => {
      if (get().pendingPublication || get().publishedReceipt) return;
      set({ dueDate });
      persistCurrent();
    },

    setComposerText: (text) => {
      if (get().pendingPublication || get().publishedReceipt) return;
      set({ composerText: text });
      persistCurrent();
    },

    setModelRef: (modelRef) => {
      const state = get();
      if (state.sending || state.publishing || state.pendingPublication || state.publishedReceipt) return;
      const nextModelRef = modelRef.trim();
      const pendingAnalysis = state.pendingAnalysis ? { ...state.pendingAnalysis } : null;
      if (pendingAnalysis) {
        if (nextModelRef) pendingAnalysis.modelRef = nextModelRef;
        else delete pendingAnalysis.modelRef;
      }
      if (failedAnalysis && failedAnalysis.workspaceId === state.workspaceId) {
        failedAnalysis = { ...failedAnalysis };
        if (nextModelRef) failedAnalysis.modelRef = nextModelRef;
        else delete failedAnalysis.modelRef;
      }
      set({ modelRef: nextModelRef, pendingAnalysis });
      persistCurrent();
    },

    publish: async (workspaceId) => {
      const scope = captureScope();
      const state = get();
      if (
        scope.workspaceId !== workspaceId
        || !isCurrent(scope)
        || state.workspaceId !== workspaceId
        || !state.hydrated
        || state.sending
        || state.publishing
        || state.publishedReceipt
      ) return undefined;

      // An uncertain response freezes the exact request. Never rebuild it from
      // editable state: doing so could create a second task with a new payload.
      const pending = state.pendingPublication && state.pendingPublication.workspaceId === workspaceId
        ? state.pendingPublication
        : null;
      let input: CreateWorkItemInput;
      let fingerprint: string;
      let clientKey: string;
      if (pending) {
        input = cloneCreateWorkItemInput(pending.input);
        fingerprint = pending.fingerprint;
        clientKey = pending.clientKey;
      } else {
        if (!canPublishTaskIntakeDraft(state.draft) || !state.draft) return undefined;
        const draft = cloneDraft(state.draft) as TaskIntakeDraft;
        fingerprint = taskIntakePayloadFingerprint(draft, state.priority, state.dueDate);
        clientKey = state.clientKey && state.clientKeyPayload === fingerprint
          ? state.clientKey
          : newClientKey(workspaceId);
        input = {
          title: draft.title.trim(),
          record_kind: 'task',
          status: 'todo',
          description: draft.description.trim(),
          priority: state.priority,
          due_date: state.dueDate || null,
          acceptance_criteria: draft.acceptance_criteria.map((item) => item.trim()).filter(Boolean),
          client_key: clientKey,
        };
      }
      const frozenPublication: PendingPublication = pending ?? {
        workspaceId,
        input: cloneCreateWorkItemInput(input),
        clientKey,
        fingerprint,
      };
      set({
        publishing: true,
        error: null,
        errorKind: null,
        clientKey,
        clientKeyPayload: fingerprint,
        pendingPublication: frozenPublication,
        canRetry: false,
      });
      persistCurrent();

      try {
        const workItem = await createWorkItem(workspaceId, input);
        if (workItem.workspace_id && workItem.workspace_id !== workspaceId) {
          if (!isCurrent(scope)) removePersisted(workspaceId);
          throw new Error('任务发布返回了其他 Workspace 的任务');
        }
        const publishedReceipt: TaskIntakePublicationReceipt = {
          workspaceId,
          workItemId: workItem.id,
          title: workItem.title || input.title,
          publishedAt: new Date().toISOString(),
        };
        if (!isCurrent(scope)) {
          // The old workspace may have been reset while the request was in
          // flight. Persist only its receipt; never touch the new workspace
          // projection.
          persistState({
            workspaceId,
            messages: state.messages,
            questionAnswers: state.questionAnswers,
            draft: null,
            analysisContextDraft: null,
            composerText: '',
            modelRef: state.modelRef,
            priority: state.priority,
            dueDate: state.dueDate,
            clientKey: null,
            clientKeyPayload: null,
            pendingAnalysis: null,
            pendingPublication: null,
            publishedReceipt,
          });
          return workItem;
        }
        set({
          draft: null,
          analysisContextDraft: null,
          composerText: '',
          clientKey: null,
          clientKeyPayload: null,
          pendingAnalysis: null,
          pendingPublication: null,
          publishedReceipt,
          publishing: false,
          error: null,
          errorKind: null,
          canRetry: false,
        });
        persistCurrent();
        return workItem;
      } catch (error) {
        if (!isCurrent(scope)) return undefined;
        const uncertain = !isDefinitelyNotCreated(error);
        set({
          publishing: false,
          error: uncertain ? '发布结果尚未确认，请重试确认' : analysisErrorMessage(error),
          errorKind: 'publish',
          canRetry: false,
          pendingPublication: uncertain ? frozenPublication : null,
        });
        persistCurrent();
        throw error;
      }
    },
  };
});

registerWorkspaceScopedReset(() => useTaskIntakeStore.getState().reset());

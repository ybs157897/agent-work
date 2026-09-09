import { apiFetch } from './client';

export type ChatAnalysisStatus = 'idle' | 'analyzing' | 'needs_answer' | 'ready' | 'failed' | 'stale';
export type ChatAnalysisSourceKind = 'attachment' | 'conversation' | 'code' | 'knowledge';
export type ChatAnalysisItemKind = 'requirement' | 'normal' | 'exception' | 'conflict' | 'unknown';
export type ChatAnalysisBasis = 'observed' | 'proposed' | 'unverified';
export type ChatAnalysisReadStatus = 'read' | 'partial' | 'failed';
export type ChatDecisionOutcome = 'confirmed' | 'rejected' | 'needs_clarification';
export type ChatDecisionStatus = 'valid' | 'needs_reconfirmation' | 'stale';

export interface ChatAnalysisSourceDependency {
  kind: string;
  ref: string;
  sha256: string;
  version?: number;
}

export interface ChatAnalysisDecision {
  id: string;
  revision: number;
  item_id: string;
  item_fingerprint: string;
  outcome: ChatDecisionOutcome;
  conclusion: string;
  basis: string;
  product_version: string;
  actor_id?: string;
  created_at: string;
  client_key?: string;
  status?: ChatDecisionStatus;
  review_reason?: string;
  source_dependencies?: ChatAnalysisSourceDependency[];
}

export interface ChatAnalysisSource {
  id: string;
  kind: ChatAnalysisSourceKind;
  ref: string;
  sha256: string;
  version?: number;
  locator?: string;
  coverage?: string;
  limitations?: string;
  quote?: string;
  read_status: ChatAnalysisReadStatus;
}

export interface ChatAnalysisItem {
  id: string;
  kind: ChatAnalysisItemKind;
  title: string;
  detail: string;
  source_ids: string[];
  basis: ChatAnalysisBasis;
  impact?: string;
  recommendation?: string;
}

export interface ChatAnalysisQuestionOption {
  id: string;
  label: string;
}

export interface ChatAnalysisQuestion {
  id: string;
  prompt: string;
  selection: 'single' | 'multiple' | 'text';
  options: ChatAnalysisQuestionOption[];
  item_ids: string[];
}

export interface ChatAnalysisDocument {
  version: 'chat-analysis/v1';
  summary: string;
  sources: ChatAnalysisSource[];
  items: ChatAnalysisItem[];
  questions: ChatAnalysisQuestion[];
}

export interface ChatAnalysisAnswer {
  question_id: string;
  selected_option_ids: string[];
  text: string;
  disposition: 'answered' | 'deferred';
  submitted_at: string;
  client_key?: string;
}

export interface ChatAnalysisProjection {
  workspace_id: string;
  chat_id: string;
  agent_id: string;
  version: number;
  revision: number;
  status: ChatAnalysisStatus;
  run_id?: string;
  error?: string;
  document?: ChatAnalysisDocument;
  current_question?: ChatAnalysisQuestion;
  pending_count: number;
  answered_count: number;
  deferred_count: number;
  answers: ChatAnalysisAnswer[];
  /** Server projection; the selected item is a client concern. */
  decisions: ChatAnalysisDecision[];
}

export interface SubmitChatAnalysisAnswerInput {
  expected_version: number;
  revision: number;
  question_id: string;
  selected_option_ids: string[];
  text: string;
  disposition: 'answered' | 'deferred';
  client_key: string;
}

export type SubmitChatAnalysisAnswerResult = ChatAnalysisProjection | {
  projection: ChatAnalysisProjection;
  answer?: ChatAnalysisAnswer;
};

const analysisRoot = (chatId: string) => `/work-items/${encodeURIComponent(chatId)}/analysis`;

export const getChatAnalysis = (chatId: string) =>
  apiFetch<ChatAnalysisProjection>(analysisRoot(chatId));

export const submitChatAnalysisAnswer = (chatId: string, input: SubmitChatAnalysisAnswerInput) =>
  apiFetch<SubmitChatAnalysisAnswerResult>(`${analysisRoot(chatId)}/answers`, {
    method: 'POST',
    body: input,
    idempotencyKey: input.client_key,
  });

const ANALYSIS_STATUSES = new Set<ChatAnalysisStatus>(['idle', 'analyzing', 'needs_answer', 'ready', 'failed', 'stale']);
const SOURCE_KINDS = new Set<ChatAnalysisSourceKind>(['attachment', 'conversation', 'code', 'knowledge']);
const ITEM_KINDS = new Set<ChatAnalysisItemKind>(['requirement', 'normal', 'exception', 'conflict', 'unknown']);
const BASES = new Set<ChatAnalysisBasis>(['observed', 'proposed', 'unverified']);
const READ_STATUSES = new Set<ChatAnalysisReadStatus>(['read', 'partial', 'failed']);
const DECISION_OUTCOMES = new Set<ChatDecisionOutcome>(['confirmed', 'rejected', 'needs_clarification']);
const DECISION_STATUSES = new Set<ChatDecisionStatus>(['valid', 'needs_reconfirmation', 'stale']);
const ANALYSIS_ID = /^[a-z0-9][a-z0-9_-]{0,63}$/;

function record(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null ? value as Record<string, unknown> : null;
}

function requiredString(value: unknown, field: string, max = 8_000): string {
  if (typeof value !== 'string' || !value.trim() || value.length > max) throw new Error(`需求分析响应格式无效：${field}`);
  return value;
}

function optionalString(value: unknown, field: string, max = 8_000): string | undefined {
  if (value === undefined || value === null) return undefined;
  if (typeof value !== 'string' || value.length > max) throw new Error(`需求分析响应格式无效：${field}`);
  return value;
}

function boundedStringArray(value: unknown, field: string, maxItems: number): string[] {
  if (!Array.isArray(value) || value.length > maxItems || value.some((item) => typeof item !== 'string' || !item.trim() || item.length > 240)) {
    throw new Error(`需求分析响应格式无效：${field}`);
  }
  return value.map((item) => item.trim());
}

function nonNegativeInteger(value: unknown, field: string): number {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) throw new Error(`需求分析响应格式无效：${field}`);
  return value;
}

function normalizeSource(value: unknown): ChatAnalysisSource {
  const source = record(value);
  if (!source || typeof source.id !== 'string' || !ANALYSIS_ID.test(source.id) || typeof source.kind !== 'string' || !SOURCE_KINDS.has(source.kind as ChatAnalysisSourceKind)) throw new Error('需求分析响应格式无效：sources');
  const sha256 = requiredString(source.sha256, 'sources.sha256', 64);
  if (!/^[0-9a-f]{64}$/.test(sha256)) throw new Error('需求分析响应格式无效：sources.sha256');
  if (typeof source.read_status !== 'string' || !READ_STATUSES.has(source.read_status as ChatAnalysisReadStatus)) throw new Error('需求分析响应格式无效：sources.read_status');
  const version = typeof source.version === 'number' ? nonNegativeInteger(source.version, 'sources.version') : 0;
  if ((source.kind === 'knowledge' && version < 1) || (source.kind !== 'knowledge' && version !== 0)) throw new Error('需求分析响应格式无效：sources.version');
  return {
    id: source.id,
    kind: source.kind as ChatAnalysisSourceKind,
    ref: requiredString(source.ref, 'sources.ref', 1_024).trim(),
    sha256,
    read_status: source.read_status as ChatAnalysisReadStatus,
    ...(version > 0 ? { version } : {}),
    ...(optionalString(source.locator, 'sources.locator', 1_000) ? { locator: optionalString(source.locator, 'sources.locator', 1_000) } : {}),
    ...(optionalString(source.coverage, 'sources.coverage', 2_000) ? { coverage: optionalString(source.coverage, 'sources.coverage', 2_000) } : {}),
    ...(optionalString(source.limitations, 'sources.limitations', 2_000) ? { limitations: optionalString(source.limitations, 'sources.limitations', 2_000) } : {}),
    ...(optionalString(source.quote, 'sources.quote', 4_000) ? { quote: optionalString(source.quote, 'sources.quote', 4_000) } : {}),
  };
}

function normalizeItem(value: unknown): ChatAnalysisItem {
  const item = record(value);
  if (!item || typeof item.id !== 'string' || !ANALYSIS_ID.test(item.id) || typeof item.kind !== 'string' || !ITEM_KINDS.has(item.kind as ChatAnalysisItemKind)) throw new Error('需求分析响应格式无效：items');
  if (typeof item.basis !== 'string' || !BASES.has(item.basis as ChatAnalysisBasis)) throw new Error('需求分析响应格式无效：items.basis');
  const impact = item.kind === 'conflict' ? requiredString(item.impact, 'items.impact', 2_000) : optionalString(item.impact, 'items.impact', 2_000);
  const recommendation = item.kind === 'conflict' ? requiredString(item.recommendation, 'items.recommendation', 2_000) : optionalString(item.recommendation, 'items.recommendation', 2_000);
  return {
    id: item.id,
    kind: item.kind as ChatAnalysisItemKind,
    title: requiredString(item.title, 'items.title', 1_000),
    detail: requiredString(item.detail, 'items.detail'),
    source_ids: boundedStringArray(item.source_ids, 'items.source_ids', 12),
    basis: item.basis as ChatAnalysisBasis,
    ...(impact ? { impact } : {}),
    ...(recommendation ? { recommendation } : {}),
  };
}

function normalizeQuestion(value: unknown): ChatAnalysisQuestion {
  const question = record(value);
  if (!question || typeof question.id !== 'string' || !ANALYSIS_ID.test(question.id) || typeof question.selection !== 'string' || !['single', 'multiple', 'text'].includes(question.selection)) throw new Error('需求分析响应格式无效：questions');
  const options = Array.isArray(question.options) ? question.options.map((option) => {
    const item = record(option);
    if (!item || typeof item.id !== 'string' || !ANALYSIS_ID.test(item.id) || typeof item.label !== 'string' || !item.label.trim() || item.label.length > 500) throw new Error('需求分析响应格式无效：questions.options');
    return { id: item.id, label: item.label };
  }) : [];
  if (question.selection === 'text' ? options.length !== 0 : options.length < 2 || options.length > 8) throw new Error('需求分析响应格式无效：questions.options');
  if (new Set(options.map((option) => option.id)).size !== options.length) throw new Error('需求分析响应格式无效：questions.options');
  return {
    id: question.id,
    prompt: requiredString(question.prompt, 'questions.prompt', 2_000),
    selection: question.selection as ChatAnalysisQuestion['selection'],
    options,
    item_ids: boundedStringArray(question.item_ids, 'questions.item_ids', 12),
  };
}

function normalizeDocument(value: unknown): ChatAnalysisDocument {
  const document = record(value);
  if (!document || document.version !== 'chat-analysis/v1' || !Array.isArray(document.sources) || document.sources.length < 1 || document.sources.length > 40 || !Array.isArray(document.items) || document.items.length < 1 || document.items.length > 100 || !Array.isArray(document.questions) || document.questions.length > 30) throw new Error('需求分析响应格式无效：document');
  const sources = document.sources.map(normalizeSource);
  const items = document.items.map(normalizeItem);
  const questions = document.questions.map(normalizeQuestion);
  if (new Set(sources.map((source) => source.id)).size !== sources.length || new Set(items.map((item) => item.id)).size !== items.length || new Set(questions.map((question) => question.id)).size !== questions.length) throw new Error('需求分析响应格式无效：重复标识');
  const sourceIds = new Set(sources.map((source) => source.id));
  const itemIds = new Set(items.map((item) => item.id));
  if (items.some((item) => item.source_ids.some((id) => !sourceIds.has(id))) || questions.some((question) => question.item_ids.some((id) => !itemIds.has(id)))) throw new Error('需求分析响应格式无效：来源引用不存在');
  return { version: 'chat-analysis/v1', summary: requiredString(document.summary, 'document.summary', 4_000), sources, items, questions };
}

function normalizeAnswer(value: unknown): ChatAnalysisAnswer {
  const answer = record(value);
  const submittedAt = typeof answer?.submitted_at === 'string' ? answer.submitted_at : answer?.created_at;
  if (!answer || typeof answer.question_id !== 'string' || !answer.question_id.trim() || (answer.text !== undefined && typeof answer.text !== 'string') || typeof submittedAt !== 'string' || !['answered', 'deferred'].includes(String(answer.disposition))) throw new Error('需求分析响应格式无效：answers');
  const selected = answer.selected_option_ids === undefined ? [] : boundedStringArray(answer.selected_option_ids, 'answers.selected_option_ids', 8);
  return {
    question_id: answer.question_id.trim(),
    selected_option_ids: selected,
    text: typeof answer.text === 'string' ? answer.text : '',
    disposition: answer.disposition as ChatAnalysisAnswer['disposition'],
    submitted_at: submittedAt,
    ...(typeof answer.client_key === 'string' && answer.client_key.trim() ? { client_key: answer.client_key } : {}),
  };
}

function normalizeDecision(value: unknown): ChatAnalysisDecision {
  const decision = record(value);
  const id = typeof decision?.id === 'string' ? decision.id : decision?.decision_id;
  const createdAt = typeof decision?.created_at === 'string' ? decision.created_at : decision?.updated_at;
  if (!decision || typeof id !== 'string' || !id.trim() || typeof decision.item_id !== 'string' || !decision.item_id.trim()
    || typeof decision.revision !== 'number' || !Number.isSafeInteger(decision.revision) || decision.revision < 1
    || typeof decision.item_fingerprint !== 'string' || !decision.item_fingerprint.trim()
    || typeof decision.outcome !== 'string' || !DECISION_OUTCOMES.has(decision.outcome as ChatDecisionOutcome)
    || typeof decision.conclusion !== 'string' || !decision.conclusion.trim()
    || typeof decision.basis !== 'string' || !decision.basis.trim()
    || typeof decision.product_version !== 'string' || !decision.product_version.trim()
    || typeof createdAt !== 'string' || !createdAt.trim()) throw new Error('需求分析响应格式无效：decision');
  const status = decision.status === undefined || decision.status === null ? undefined : decision.status;
  if (status !== undefined && (typeof status !== 'string' || !DECISION_STATUSES.has(status as ChatDecisionStatus))) throw new Error('需求分析响应格式无效：decision.status');
  const sourceDependencies = decision.source_dependencies === undefined || decision.source_dependencies === null ? undefined : decision.source_dependencies;
  if (sourceDependencies !== undefined && (!Array.isArray(sourceDependencies) || sourceDependencies.length > 40 || sourceDependencies.some((dependency) => {
    const item = record(dependency);
    return !item || typeof item.kind !== 'string' || !item.kind.trim() || typeof item.ref !== 'string' || !item.ref.trim()
      || typeof item.sha256 !== 'string' || !/^[0-9a-f]{64}$/.test(item.sha256)
      || (item.version !== undefined && (!Number.isSafeInteger(item.version) || Number(item.version) < 0));
  }))) throw new Error('需求分析响应格式无效：decision.source_dependencies');
  return {
    id,
    revision: decision.revision,
    item_id: decision.item_id,
    item_fingerprint: decision.item_fingerprint,
    outcome: decision.outcome as ChatDecisionOutcome,
    conclusion: decision.conclusion,
    basis: decision.basis,
    product_version: decision.product_version,
    created_at: createdAt,
    ...(typeof decision.actor_id === 'string' && decision.actor_id.trim() ? { actor_id: decision.actor_id } : {}),
    ...(typeof decision.client_key === 'string' && decision.client_key.trim() ? { client_key: decision.client_key } : {}),
    ...(status ? { status: status as ChatDecisionStatus } : {}),
    ...(typeof decision.review_reason === 'string' && decision.review_reason.trim() ? { review_reason: decision.review_reason } : {}),
    ...(sourceDependencies ? {
      source_dependencies: sourceDependencies.map((dependency) => {
        const item = dependency as Record<string, unknown>;
        return {
          kind: item.kind as string,
          ref: item.ref as string,
          sha256: item.sha256 as string,
          ...(typeof item.version === 'number' ? { version: item.version } : {}),
        };
      }),
    } : {}),
  };
}

export function parseChatAnalysisProjection(value: unknown): ChatAnalysisProjection {
  const projection = record(value);
  if (!projection || typeof projection.workspace_id !== 'string' || typeof projection.chat_id !== 'string' || typeof projection.agent_id !== 'string' || typeof projection.status !== 'string' || !ANALYSIS_STATUSES.has(projection.status as ChatAnalysisStatus)) throw new Error('需求分析响应格式无效：projection');
  const answers = projection.answers === undefined ? [] : projection.answers;
  if (!Array.isArray(answers) || answers.length > 30) throw new Error('需求分析响应格式无效：answers');
  if (!Array.isArray(projection.decisions) || projection.decisions.length > 100) throw new Error('需求分析响应格式无效：decisions');
  const document = projection.document !== undefined && projection.document !== null ? normalizeDocument(projection.document) : undefined;
  const currentQuestion = projection.current_question !== undefined && projection.current_question !== null ? normalizeQuestion(projection.current_question) : undefined;
  const rawDecisions = projection.decisions;
  const decisions = rawDecisions.map(normalizeDecision);
  if (document && currentQuestion) {
    const declared = document.questions.find((question) => question.id === currentQuestion.id);
    if (!declared || JSON.stringify(declared) !== JSON.stringify(currentQuestion)) throw new Error('需求分析响应格式无效：current_question');
    const itemIds = new Set(document.items.map((item) => item.id));
    if (currentQuestion.item_ids.some((id) => !itemIds.has(id))) throw new Error('需求分析响应格式无效：current_question.item_ids');
  }
  return {
    workspace_id: projection.workspace_id,
    chat_id: projection.chat_id,
    agent_id: projection.agent_id,
    version: nonNegativeInteger(projection.version, 'version'),
    revision: nonNegativeInteger(projection.revision, 'revision'),
    status: projection.status as ChatAnalysisStatus,
    pending_count: nonNegativeInteger(projection.pending_count, 'pending_count'),
    answered_count: nonNegativeInteger(projection.answered_count, 'answered_count'),
    deferred_count: nonNegativeInteger(projection.deferred_count, 'deferred_count'),
    answers: answers.map(normalizeAnswer),
    decisions,
    ...(typeof projection.run_id === 'string' && projection.run_id.trim() ? { run_id: projection.run_id } : {}),
    ...(typeof projection.error === 'string' && projection.error ? { error: projection.error } : {}),
    ...(document ? { document } : {}),
    ...(currentQuestion ? { current_question: currentQuestion } : {}),
  };
}

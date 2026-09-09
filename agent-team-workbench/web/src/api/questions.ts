import { apiFetch } from './client';

export type NativeQuestionStatus = 'pending' | 'answered' | 'dismissed' | 'expired';
export type NativeQuestionAnswer =
  | { kind: 'single'; option_id: string }
  | { kind: 'multi'; option_ids: string[] }
  | { kind: 'other'; text: string }
  | { kind: 'multi_with_other'; option_ids: string[]; other_text: string }
  | { kind: 'skipped' };

export interface NativeQuestionOption {
  id: string;
  label: string;
  description?: string;
}

export interface NativeQuestionItem {
  id: string;
  question: string;
  header?: string;
  body?: string;
  options: NativeQuestionOption[];
  multi_select?: boolean;
  allow_other?: boolean;
  other_label?: string;
  other_description?: string;
}

export interface NativeQuestion {
  id: string;
  run_id: string;
  work_item_id: string;
  session_ref: string;
  provider_id: string;
  agent_id?: string;
  turn_id?: number;
  tool_call_id?: string;
  questions: NativeQuestionItem[];
  status: NativeQuestionStatus;
  response?: { answers: Record<string, NativeQuestionAnswer>; method?: string; note?: string };
  created_at: string;
  resolved_at?: string;
}

export interface NativeQuestionResponse {
  answers: Record<string, NativeQuestionAnswer>;
  method?: 'enter' | 'space' | 'number_key' | 'click';
  note?: string;
}

export const listNativeQuestions = (runId: string) =>
  apiFetch<{ items: NativeQuestion[] }>(`/runs/${encodeURIComponent(runId)}/questions`);

export const resolveNativeQuestion = (runId: string, questionId: string, response: NativeQuestionResponse) =>
  apiFetch<NativeQuestion>(`/runs/${encodeURIComponent(runId)}/questions/${encodeURIComponent(questionId)}/commands/resolve`, {
    method: 'POST',
    body: response,
    idempotencyKey: `question:${questionId}`,
  });

export const dismissNativeQuestion = (runId: string, questionId: string) =>
  apiFetch<NativeQuestion>(`/runs/${encodeURIComponent(runId)}/questions/${encodeURIComponent(questionId)}/commands/dismiss`, {
    method: 'POST',
    body: {},
    idempotencyKey: `question:${questionId}:dismiss`,
  });

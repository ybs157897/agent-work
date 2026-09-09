import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import type { NativeQuestion } from '../../api/questions';
import { buildNativeQuestionResponse, NativeQuestionCard } from './native-question-card';

const question: NativeQuestion = {
  id: 'question_1', run_id: 'run_1', work_item_id: 'wi_1', session_ref: 'session_1', provider_id: 'provider_q_1',
  questions: [{ id: 'q_0', question: '请选择颜色', options: [{ id: 'opt_0_0', label: '蓝色' }, { id: 'opt_0_1', label: '绿色' }], allow_other: true }],
  status: 'pending', created_at: '2026-09-09T12:00:00Z',
};

describe('NativeQuestionCard', () => {
  it('renders accessible typed controls and skip action without approval wording', () => {
    const html = renderToStaticMarkup(<NativeQuestionCard question={question} submitting={false} onResolve={vi.fn(async () => undefined)} />);
    expect(html).toContain('需要你的回答');
    expect(html).toContain('请选择颜色');
    expect(html).toContain('type="radio"');
    expect(html).toContain('type="button"');
    expect(html).toContain('跳过');
    expect(html).toContain('回答后，智能体会继续处理');
  });

  it('does not render a terminal question again', () => {
    const html = renderToStaticMarkup(<NativeQuestionCard question={{ ...question, status: 'answered' }} submitting={false} onResolve={vi.fn(async () => undefined)} />);
    expect(html).toBe('');
  });

  it('uses single-select Other text instead of silently keeping the old option', () => {
    expect(buildNativeQuestionResponse(question, { q_0: ['opt_0_0'] }, { q_0: '紫色' })).toEqual({ answers: { q_0: { kind: 'other', text: '紫色' } }, method: 'click' });
    expect(buildNativeQuestionResponse({ ...question, questions: [{ ...question.questions[0], multi_select: true }] }, { q_0: ['opt_0_0'] }, { q_0: '紫色' })).toEqual({ answers: { q_0: { kind: 'multi_with_other', option_ids: ['opt_0_0'], other_text: '紫色' } }, method: 'click' });
  });
});

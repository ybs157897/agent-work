import { afterEach, describe, expect, it, vi } from 'vitest';
import { analyzeTaskIntake, type AnalyzeTaskIntakeResponse } from './task-intake';

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

describe('task intake endpoint', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('把对话消息与可选草案发送到独立分析路由', async () => {
    const response: AnalyzeTaskIntakeResponse = {
      reply: '还需要补充失败时的处理方式。',
      questions: [{ id: 'failure-scope', title: '覆盖哪些端？', options: ['Web', '移动端'], multiple: true }],
      draft: {
        title: '完善登录流程',
        description: '让登录失败能够被定位和恢复。',
        acceptance_criteria: ['失败时展示原因', '补充自动化测试'],
      },
    };
    const fetchMock = vi.fn().mockResolvedValue(json(response));
    vi.stubGlobal('fetch', fetchMock);

    const result = await analyzeTaskIntake('ws_1', {
      messages: [
        { role: 'user', content: '我想把登录流程做得更可靠。' },
        { role: 'assistant', content: '请补充验收标准。' },
        { role: 'user', content: '失败时要能看懂原因。' },
      ],
      draft: {
        title: '登录流程',
        description: '',
        acceptance_criteria: ['登录可用'],
      },
      model_ref: 'openai/gpt-5.6',
    });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(result.questions).toEqual([{ id: 'failure-scope', title: '覆盖哪些端？', options: ['Web', '移动端'], multiple: true }]);
    expect(url).toBe('/api/v1/workspaces/ws_1/task-intake/analyze');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body as string)).toEqual({
      messages: [
        { role: 'user', content: '我想把登录流程做得更可靠。' },
        { role: 'assistant', content: '请补充验收标准。' },
        { role: 'user', content: '失败时要能看懂原因。' },
      ],
      draft: {
        title: '登录流程',
        description: '',
        acceptance_criteria: ['登录可用'],
      },
      model_ref: 'openai/gpt-5.6',
    });
    expect((init.headers as Record<string, string>)['Idempotency-Key']).toBeTruthy();
  });
});

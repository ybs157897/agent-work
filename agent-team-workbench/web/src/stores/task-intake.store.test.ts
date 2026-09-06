import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { WorkItem } from '../api/types';
import { analyzeTaskIntake } from '../api/task-intake';
import { createWorkItem } from '../api/endpoints';
import { useWorkspaceStore } from './workspace.store';
import {
  TASK_INTAKE_STORAGE_PREFIX,
  canPublishTaskIntakeDraft,
  formatTaskIntakeAssistantContext,
  formatTaskIntakeQuestionAnswers,
  taskIntakeStorageKey,
  taskIntakeQuestionAnswerKey,
  parseTaskIntakeResponse,
  validateTaskIntakeMessage,
  useTaskIntakeStore,
} from './task-intake.store';

vi.mock('../api/task-intake', () => ({ analyzeTaskIntake: vi.fn() }));
vi.mock('../api/endpoints', () => ({ createWorkItem: vi.fn() }));

const analyzeMock = vi.mocked(analyzeTaskIntake);
const createWorkItemMock = vi.mocked(createWorkItem);

function createStorage() {
  const values = new Map<string, string>();
  return {
    values,
    storage: {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => values.set(key, value),
      removeItem: (key: string) => values.delete(key),
    },
  };
}

const draft = {
  title: '整理登录流程',
  description: '让失败原因可定位。',
  acceptance_criteria: ['失败时展示原因', '补充自动化测试'],
};

const workItem = {
  id: 'wi_1',
  workspace_id: 'ws_1',
  record_kind: 'task',
  title: draft.title,
  description: draft.description,
  status: 'todo',
  priority: 'medium',
  due_date: null,
  acceptance_criteria: draft.acceptance_criteria,
  runs_count: 0,
  version: 1,
  created_at: '2026-09-05T00:00:00Z',
  updated_at: '2026-09-05T00:00:00Z',
} as WorkItem;

describe('task intake store', () => {
  beforeEach(() => {
    const { storage } = createStorage();
    vi.stubGlobal('window', { localStorage: storage });
    useWorkspaceStore.setState({
      workspace: { id: 'ws_1', name: 'workspace', timezone: 'UTC', version: 1 },
      selectedWorkspaceId: 'ws_1',
      generation: 1,
    });
    useTaskIntakeStore.getState().reset();
    useTaskIntakeStore.getState().hydrate('ws_1');
    analyzeMock.mockReset();
    createWorkItemMock.mockReset();
  });

  afterEach(() => vi.unstubAllGlobals());

  it('按 workspace 隔离并持久化未发送的 composer 文本与草案', () => {
    useTaskIntakeStore.getState().setComposerText('先记住这段描述');
    useTaskIntakeStore.getState().setDraft(draft);
    useTaskIntakeStore.getState().setPriority('high');

    useTaskIntakeStore.getState().reset();
    useTaskIntakeStore.getState().hydrate('ws_2');
    expect(useTaskIntakeStore.getState().composerText).toBe('');
    expect(useTaskIntakeStore.getState().draft).toBeNull();

    useTaskIntakeStore.getState().reset();
    useTaskIntakeStore.getState().hydrate('ws_1');
    expect(useTaskIntakeStore.getState().composerText).toBe('先记住这段描述');
    expect(useTaskIntakeStore.getState().draft).toEqual(draft);
    expect(useTaskIntakeStore.getState().priority).toBe('high');
  });

  it('发送时立刻使旧草案失效，返回回复与新草案后才重新显示', async () => {
    useTaskIntakeStore.getState().setDraft(draft);
    analyzeMock.mockResolvedValue({ reply: '还需要确认上线范围。', draft: { ...draft, title: '完善登录流程' } });

    const pending = useTaskIntakeStore.getState().analyze('ws_1', '还要覆盖移动端');
    expect(useTaskIntakeStore.getState().draft).toBeNull();
    await pending;

    expect(analyzeMock).toHaveBeenCalledWith('ws_1', {
      messages: [{ role: 'user', content: '还要覆盖移动端' }],
      draft,
    });
    expect(useTaskIntakeStore.getState().messages.map((message) => message.content)).toEqual([
      '还要覆盖移动端',
      '还需要确认上线范围。',
    ]);
    expect(useTaskIntakeStore.getState().draft?.title).toBe('完善登录流程');
  });

  it('分析失败后持久化可重试请求，刷新恢复时不重复追加用户消息', async () => {
    useTaskIntakeStore.getState().setDraft(draft);
    useTaskIntakeStore.getState().setModelRef('registry/model-a');
    analyzeMock.mockRejectedValueOnce(new Error('网络不可用'));
    await useTaskIntakeStore.getState().analyze('ws_1', '描述一个可恢复的登录流程');
    expect(analyzeMock.mock.calls[0][1].model_ref).toBe('registry/model-a');
    expect(useTaskIntakeStore.getState().error).toBe('网络不可用');
    expect(useTaskIntakeStore.getState().canRetry).toBe(true);

    useTaskIntakeStore.getState().reset();
    useTaskIntakeStore.getState().hydrate('ws_1');
    expect(useTaskIntakeStore.getState().canRetry).toBe(true);
    expect(useTaskIntakeStore.getState().messages).toHaveLength(1);

    analyzeMock.mockResolvedValueOnce({ reply: '已整理。', draft });
    await useTaskIntakeStore.getState().retryAnalyze();
    expect(analyzeMock).toHaveBeenCalledTimes(2);
    expect(analyzeMock.mock.calls[1][1].messages).toEqual([
      { role: 'user', content: '描述一个可恢复的登录流程' },
    ]);
    expect(analyzeMock.mock.calls[1][1].draft).toEqual(draft);
    expect(analyzeMock.mock.calls[1][1].model_ref).toBe('registry/model-a');
    expect(useTaskIntakeStore.getState().messages).toHaveLength(2);
    expect(useTaskIntakeStore.getState().pendingAnalysis).toBeNull();
  });

  it('发布网络结果不确定时冻结原始 payload，刷新后用同一个 key 重试', async () => {
    useTaskIntakeStore.setState({ messages: [{ id: 'user-1', role: 'user', content: '原始需求' }] });
    useTaskIntakeStore.getState().setDraft(draft);
    createWorkItemMock.mockRejectedValueOnce(new TypeError('Failed to fetch'));
    await expect(useTaskIntakeStore.getState().publish('ws_1')).rejects.toThrow('Failed to fetch');

    const firstInput = createWorkItemMock.mock.calls[0][1];
    expect(useTaskIntakeStore.getState().pendingPublication?.input).toEqual(firstInput);
    expect(useTaskIntakeStore.getState().error).toBe('发布结果尚未确认，请重试确认');

    useTaskIntakeStore.getState().reset();
    useTaskIntakeStore.getState().hydrate('ws_1');
    expect(useTaskIntakeStore.getState().pendingPublication?.input).toEqual(firstInput);
    expect(useTaskIntakeStore.getState().draft).toEqual(draft);

    createWorkItemMock.mockResolvedValueOnce(workItem);
    await useTaskIntakeStore.getState().publish('ws_1');
    const secondInput = createWorkItemMock.mock.calls[1][1];
    expect(secondInput).toEqual(firstInput);
    expect(secondInput.client_key).toBe(firstInput.client_key);
    expect(useTaskIntakeStore.getState().pendingPublication).toBeNull();
    expect(useTaskIntakeStore.getState().draft).toBeNull();
    expect(useTaskIntakeStore.getState().messages).toEqual([{ id: 'user-1', role: 'user', content: '原始需求' }]);
    expect(useTaskIntakeStore.getState().publishedReceipt).toMatchObject({ workItemId: 'wi_1', title: draft.title, workspaceId: 'ws_1' });

    useTaskIntakeStore.getState().reset();
    useTaskIntakeStore.getState().hydrate('ws_1');
    expect(useTaskIntakeStore.getState().publishedReceipt).toMatchObject({ workItemId: 'wi_1' });
    expect(useTaskIntakeStore.getState().messages).toHaveLength(1);
    useTaskIntakeStore.getState().clear();
    expect(useTaskIntakeStore.getState().publishedReceipt).toBeNull();
    expect(useTaskIntakeStore.getState().messages).toHaveLength(0);
  });

  it('发布响应跨 workspace 返回时只持久化原 workspace receipt，不覆盖新 workspace', async () => {
    useTaskIntakeStore.getState().setDraft(draft);
    let resolveCreate!: (item: WorkItem) => void;
    const deferred = new Promise<WorkItem>((resolve) => { resolveCreate = resolve; });
    createWorkItemMock.mockReturnValueOnce(deferred);

    const publishing = useTaskIntakeStore.getState().publish('ws_1');
    useWorkspaceStore.setState({
      workspace: { id: 'ws_2', name: 'other', timezone: 'UTC', version: 1 },
      selectedWorkspaceId: 'ws_2',
      generation: 2,
    });
    useTaskIntakeStore.getState().reset();
    useTaskIntakeStore.getState().hydrate('ws_2');
    resolveCreate(workItem);
    await publishing;

    expect(useTaskIntakeStore.getState().publishedReceipt).toBeNull();
    useWorkspaceStore.setState({
      workspace: { id: 'ws_1', name: 'workspace', timezone: 'UTC', version: 1 },
      selectedWorkspaceId: 'ws_1',
      generation: 3,
    });
    useTaskIntakeStore.getState().reset();
    useTaskIntakeStore.getState().hydrate('ws_1');
    expect(useTaskIntakeStore.getState().publishedReceipt).toMatchObject({ workItemId: 'wi_1', workspaceId: 'ws_1' });
  });

  it('分析缺凭据后切换模型再重试，沿用原消息且只发送一次用户内容', async () => {
    useTaskIntakeStore.getState().setModelRef('registry/missing-credentials');
    analyzeMock.mockRejectedValueOnce(new Error('当前模型凭据未配置'));
    await useTaskIntakeStore.getState().analyze('ws_1', '让登录失败可恢复');

    useTaskIntakeStore.getState().setModelRef('registry/working-model');
    expect(useTaskIntakeStore.getState().pendingAnalysis?.modelRef).toBe('registry/working-model');
    analyzeMock.mockResolvedValueOnce({ reply: '已按新模型重新分析。', draft });
    await useTaskIntakeStore.getState().retryAnalyze();

    expect(analyzeMock).toHaveBeenCalledTimes(2);
    expect(analyzeMock.mock.calls[1][1]).toMatchObject({
      model_ref: 'registry/working-model',
      messages: [{ role: 'user', content: '让登录失败可恢复' }],
    });
    expect(analyzeMock.mock.calls[1][1].messages).toHaveLength(1);
  });

  it('草案至少有标题和一条非空验收标准才允许发布', () => {
    expect(canPublishTaskIntakeDraft(null)).toBe(false);
    expect(canPublishTaskIntakeDraft({ title: ' ', description: '', acceptance_criteria: ['可完成'] })).toBe(false);
    expect(canPublishTaskIntakeDraft({ title: '任务', description: '', acceptance_criteria: [' ', ''] })).toBe(false);
    expect(canPublishTaskIntakeDraft(draft)).toBe(true);
    expect(taskIntakeStorageKey('ws/1')).toBe(`${TASK_INTAKE_STORAGE_PREFIX}ws%2F1`);
  });

  it('严格接收真实 questions，并将历史题目与选项明文带回后续分析上下文', () => {
    const parsed = parseTaskIntakeResponse({
      reply: '请确认范围。',
      questions: [{ id: 'scope', title: '覆盖哪些端？', options: ['Web', '移动端'], multiple: true }],
    });
    expect(parsed.questions).toEqual([{ id: 'scope', title: '覆盖哪些端？', options: ['Web', '移动端'], multiple: true }]);
    expect(() => parseTaskIntakeResponse({ reply: '坏题', questions: [{ id: 'scope', title: '缺选项', options: ['只有一个'], multiple: false }] })).toThrow('questions');
    expect(formatTaskIntakeAssistantContext({ role: 'assistant', content: '请确认范围。', questions: parsed.questions })).toContain('Web、移动端');
    expect(formatTaskIntakeQuestionAnswers('assistant-1', { questions: parsed.questions }, {
      [taskIntakeQuestionAnswerKey('assistant-1', 'scope')]: { selected: ['Web'], supplement: '优先桌面端', submitted: false },
    })).toContain('选择：Web');
  });

  it('提交问题回答会发一条普通 user 消息，底部未发送 composer 保持独立', async () => {
    analyzeMock.mockResolvedValueOnce({
      reply: '请补充上线范围。',
      questions: [{ id: 'scope', title: '覆盖哪些端？', options: ['Web', '移动端'], multiple: true }],
    });
    await useTaskIntakeStore.getState().analyze('ws_1', '我想让登录更可靠');
    const assistant = useTaskIntakeStore.getState().messages.find((message) => message.role === 'assistant');
    if (!assistant) throw new Error('expected assistant question');
    useTaskIntakeStore.getState().setQuestionSelection(assistant.id, 'scope', ['Web']);
    useTaskIntakeStore.getState().setQuestionSupplement(assistant.id, 'scope', '优先桌面端');
    useTaskIntakeStore.getState().setComposerText('还有一个独立补充');
    analyzeMock.mockResolvedValueOnce({ reply: '已收到。' });

    await useTaskIntakeStore.getState().submitQuestionAnswers('ws_1', assistant.id);
    const request = analyzeMock.mock.calls[1][1];
    expect(request.messages.at(-1)?.content).toContain('选择：Web');
    expect(request.messages.at(-1)?.content).toContain('补充：优先桌面端');
    expect(request.messages.at(-1)?.content).not.toContain('独立补充');
    expect(useTaskIntakeStore.getState().composerText).toBe('还有一个独立补充');
    const answer = useTaskIntakeStore.getState().questionAnswers[taskIntakeQuestionAnswerKey(assistant.id, 'scope')];
    expect(answer).toMatchObject({ submitted: true, lockedReason: 'submitted' });
  });

  it('提交回答发起 deferred 分析时立即保留 composer，刷新 hydrate 仍可恢复且请求不携带它', async () => {
    const assistant = {
      id: 'assistant-deferred',
      role: 'assistant' as const,
      content: '请确认范围。',
      questions: [{ id: 'scope', title: '覆盖哪些端？', options: ['Web', '移动端'], multiple: false }],
    };
    useTaskIntakeStore.setState({
      messages: [assistant],
      questionAnswers: {
        [taskIntakeQuestionAnswerKey(assistant.id, 'scope')]: { selected: ['Web'], supplement: '', submitted: false },
      },
      composerText: '尚未发送的自由补充',
    });
    let resolveAnalysis!: (value: { reply: string }) => void;
    const deferred = new Promise<{ reply: string }>((resolve) => { resolveAnalysis = resolve; });
    analyzeMock.mockReturnValueOnce(deferred);

    const pendingSubmit = useTaskIntakeStore.getState().submitQuestionAnswers('ws_1', assistant.id);
    expect(useTaskIntakeStore.getState().composerText).toBe('尚未发送的自由补充');
    expect(useTaskIntakeStore.getState().pendingAnalysis).not.toBeNull();
    expect(JSON.stringify(analyzeMock.mock.calls[0][1])).not.toContain('尚未发送的自由补充');
    const stored = JSON.parse(window.localStorage.getItem(taskIntakeStorageKey('ws_1')) ?? '{}') as { composerText?: string };
    expect(stored.composerText).toBe('尚未发送的自由补充');

    useTaskIntakeStore.getState().reset();
    useTaskIntakeStore.getState().hydrate('ws_1');
    expect(useTaskIntakeStore.getState().composerText).toBe('尚未发送的自由补充');
    expect(useTaskIntakeStore.getState().pendingAnalysis).not.toBeNull();

    resolveAnalysis({ reply: '已收到回答。' });
    await pendingSubmit;
  });

  it('编辑草案后进入 questions 阶段仍保留独立分析上下文，刷新提交答案继续携带它', async () => {
    analyzeMock.mockResolvedValueOnce({ reply: '已整理初稿。', draft });
    await useTaskIntakeStore.getState().analyze('ws_1', '先整理登录任务');
    useTaskIntakeStore.getState().setDraftField('description', '用户编辑后的完整范围与限制');
    const editedDraft = { ...draft, description: '用户编辑后的完整范围与限制' };

    analyzeMock.mockResolvedValueOnce({
      reply: '请确认覆盖端。',
      questions: [{ id: 'scope', title: '覆盖哪些端？', options: ['Web', '移动端'], multiple: false }],
    });
    await useTaskIntakeStore.getState().analyze('ws_1', '再确认一下上线范围');
    expect(analyzeMock.mock.calls[1][1].draft).toEqual(editedDraft);
    expect(useTaskIntakeStore.getState().draft).toBeNull();
    expect(useTaskIntakeStore.getState().analysisContextDraft).toEqual(editedDraft);

    useTaskIntakeStore.getState().reset();
    useTaskIntakeStore.getState().hydrate('ws_1');
    const restoredQuestion = useTaskIntakeStore.getState().messages.find((message) => message.questions?.length);
    if (!restoredQuestion) throw new Error('expected restored question');
    expect(useTaskIntakeStore.getState().draft).toBeNull();
    expect(useTaskIntakeStore.getState().analysisContextDraft).toEqual(editedDraft);

    useTaskIntakeStore.getState().setQuestionSelection(restoredQuestion.id, 'scope', ['Web']);
    const finalDraft = { ...editedDraft, title: '最终登录任务' };
    analyzeMock.mockResolvedValueOnce({ reply: '已收到范围。', draft: finalDraft });
    await useTaskIntakeStore.getState().submitQuestionAnswers('ws_1', restoredQuestion.id);

    expect(analyzeMock.mock.calls[2][1].draft).toEqual(editedDraft);
    expect(useTaskIntakeStore.getState().draft).toEqual(finalDraft);
    expect(useTaskIntakeStore.getState().analysisContextDraft).toEqual(finalDraft);
  });

  it('自由 composer 继续时锁定未提交选择为已继续对话，不把选择偷偷带入消息', async () => {
    analyzeMock.mockResolvedValueOnce({
      reply: '请确认范围。',
      questions: [{ id: 'scope', title: '覆盖哪些端？', options: ['Web', '移动端'], multiple: false }],
    });
    await useTaskIntakeStore.getState().analyze('ws_1', '先从登录开始');
    const assistant = useTaskIntakeStore.getState().messages.find((message) => message.role === 'assistant');
    if (!assistant) throw new Error('expected assistant question');
    useTaskIntakeStore.getState().setQuestionSelection(assistant.id, 'scope', ['Web']);
    analyzeMock.mockResolvedValueOnce({ reply: '好的，我继续了解。' });
    await useTaskIntakeStore.getState().analyze('ws_1', '我还想补充失败提示');

    expect(analyzeMock.mock.calls[1][1].messages.at(-1)?.content).toBe('我还想补充失败提示');
    expect(useTaskIntakeStore.getState().questionAnswers[taskIntakeQuestionAnswerKey(assistant.id, 'scope')]).toMatchObject({
      selected: ['Web'], submitted: false, lockedReason: 'continued',
    });
  });

  it('未完成问题时指出具体待答题目且不发送分析请求', async () => {
    analyzeMock.mockResolvedValueOnce({
      reply: '请确认范围。',
      questions: [{ id: 'scope', title: '覆盖哪些端？', options: ['Web', '移动端'], multiple: false }],
    });
    await useTaskIntakeStore.getState().analyze('ws_1', '请帮我完善登录');
    const assistant = useTaskIntakeStore.getState().messages.find((message) => message.role === 'assistant');
    if (!assistant) throw new Error('expected assistant question');
    await useTaskIntakeStore.getState().submitQuestionAnswers('ws_1', assistant.id);

    expect(analyzeMock).toHaveBeenCalledTimes(1);
    expect(useTaskIntakeStore.getState().error).toContain('覆盖哪些端？');
    expect(useTaskIntakeStore.getState().messages).toHaveLength(2);
  });

  it('零条目旧题在自由发送得到新草案后锁为已跳过，旧题选择和提交均 no-op', async () => {
    analyzeMock.mockResolvedValueOnce({
      reply: '请确认上线范围。',
      questions: [{ id: 'scope', title: '覆盖哪些端？', options: ['Web', '移动端'], multiple: false }],
    });
    await useTaskIntakeStore.getState().analyze('ws_1', '我想完善登录');
    const oldAssistant = useTaskIntakeStore.getState().messages.find((message) => message.role === 'assistant');
    if (!oldAssistant) throw new Error('expected assistant question');

    analyzeMock.mockResolvedValueOnce({ reply: '已整理任务范围。', draft });
    await useTaskIntakeStore.getState().analyze('ws_1', '我再补充失败提示');
    const before = useTaskIntakeStore.getState();
    expect(before.draft).toEqual(draft);
    expect(before.questionAnswers[taskIntakeQuestionAnswerKey(oldAssistant.id, 'scope')]).toMatchObject({
      selected: [], supplement: '', submitted: false, lockedReason: 'continued',
    });

    useTaskIntakeStore.getState().setQuestionSelection(oldAssistant.id, 'scope', ['Web']);
    await useTaskIntakeStore.getState().submitQuestionAnswers('ws_1', oldAssistant.id);
    const after = useTaskIntakeStore.getState();
    expect(analyzeMock).toHaveBeenCalledTimes(2);
    expect(after.draft).toEqual(draft);
    expect(after.questionAnswers[taskIntakeQuestionAnswerKey(oldAssistant.id, 'scope')]).toMatchObject({
      selected: [], submitted: false, lockedReason: 'continued',
    });
  });

  it('发送前按 Unicode code point 校验消息边界，拒绝长消息后保留原 composer 和草案', async () => {
    const emojiMessage = '🙂'.repeat(8000);
    expect(validateTaskIntakeMessage(emojiMessage, [])).toBeNull();
    expect(validateTaskIntakeMessage(`${emojiMessage}x`, [])).toContain('8000');
    expect(validateTaskIntakeMessage('新消息', Array.from({ length: 40 }, (_, index) => ({ content: String(index) })))).toContain('40');
    expect(validateTaskIntakeMessage('x'.repeat(2), [{ content: 'x'.repeat(40_000) }])).toContain('40000');

    useTaskIntakeStore.getState().setComposerText(`${emojiMessage}x`);
    useTaskIntakeStore.getState().setDraft(draft);
    await useTaskIntakeStore.getState().analyze('ws_1', `${emojiMessage}x`);
    expect(useTaskIntakeStore.getState().messages).toHaveLength(0);
    expect(useTaskIntakeStore.getState().composerText).toBe(`${emojiMessage}x`);
    expect(useTaskIntakeStore.getState().draft).toEqual(draft);

    analyzeMock.mockResolvedValueOnce({ reply: '短消息可以继续分析。' });
    useTaskIntakeStore.getState().setComposerText('短消息');
    await useTaskIntakeStore.getState().analyze('ws_1', '短消息');
    expect(useTaskIntakeStore.getState().messages.map((message) => message.content)).toEqual(['短消息', '短消息可以继续分析。']);
  });

  it('重新开始会清除未发布状态，但 pendingPublication 期间拒绝清除', () => {
    useTaskIntakeStore.getState().setDraft(draft);
    useTaskIntakeStore.getState().setComposerText('未发布描述');
    useTaskIntakeStore.getState().clear();
    expect(useTaskIntakeStore.getState().draft).toBeNull();
    expect(useTaskIntakeStore.getState().composerText).toBe('');

    useTaskIntakeStore.setState({
      draft,
      pendingPublication: {
        workspaceId: 'ws_1',
        clientKey: 'task-intake:ws_1:key',
        fingerprint: 'fingerprint',
        input: {
          title: draft.title, record_kind: 'task', status: 'todo', description: draft.description,
          priority: 'medium', due_date: null, acceptance_criteria: draft.acceptance_criteria,
          client_key: 'task-intake:ws_1:key',
        },
      },
    });
    useTaskIntakeStore.getState().clear();
    expect(useTaskIntakeStore.getState().draft).toEqual(draft);
    expect(useTaskIntakeStore.getState().pendingPublication?.clientKey).toBe('task-intake:ws_1:key');
  });
});

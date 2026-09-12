import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { NativeQuestion } from '../api/questions';
import type { ExecutionRun } from '../api/types';
import { useAgentsStore } from '../stores/agents.store';
import { useChatStore } from '../stores/chat.store';
import { useNativeQuestionsStore } from '../stores/questions.store';
import { useWorkspaceStore } from '../stores/workspace.store';
import { useWorkbenchThemeStore } from '../stores/workbench-theme.store';
import ChatPage from './chat.page';

vi.mock('../stores/agents.store', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../stores/agents.store')>();
  return { ...actual, useAgentsStore: Object.assign((selector: (state: ReturnType<typeof actual.useAgentsStore.getState>) => unknown) => selector(actual.useAgentsStore.getState()), actual.useAgentsStore) };
});
vi.mock('../stores/questions.store', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../stores/questions.store')>();
  return { ...actual, useNativeQuestionsStore: Object.assign((selector: (state: ReturnType<typeof actual.useNativeQuestionsStore.getState>) => unknown) => selector(actual.useNativeQuestionsStore.getState()), actual.useNativeQuestionsStore) };
});
vi.mock('../stores/chat.store', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../stores/chat.store')>();
  return { ...actual, useChatStore: Object.assign((selector: (state: ReturnType<typeof actual.useChatStore.getState>) => unknown) => selector(actual.useChatStore.getState()), actual.useChatStore) };
});
vi.mock('../stores/workspace.store', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../stores/workspace.store')>();
  return { ...actual, useWorkspaceStore: Object.assign((selector: (state: ReturnType<typeof actual.useWorkspaceStore.getState>) => unknown) => selector(actual.useWorkspaceStore.getState()), actual.useWorkspaceStore) };
});
vi.mock('../stores/workbench-theme.store', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../stores/workbench-theme.store')>();
  return { ...actual, useWorkbenchThemeStore: Object.assign((selector: (state: ReturnType<typeof actual.useWorkbenchThemeStore.getState>) => unknown) => selector(actual.useWorkbenchThemeStore.getState()), actual.useWorkbenchThemeStore) };
});

const run = (id: string, status?: string): ExecutionRun => ({ id, work_item_id: 'wi_1', status } as ExecutionRun);

const pendingQuestion: NativeQuestion = {
  id: 'question_1',
  run_id: 'run_q',
  work_item_id: 'wi_1',
  session_ref: 'session_1',
  provider_id: 'provider_q_1',
  questions: [{
    id: 'q_0',
    question: '「模糊匹配」按什么规则命中？',
    header: '匹配规则',
    options: [
      { id: 'opt_0_0', label: '子串包含（推荐）' },
      { id: 'opt_0_1', label: '多词任意命中' },
    ],
  }],
  status: 'pending',
  created_at: '2026-09-09T15:15:03Z',
};

describe('native question card visibility', () => {
  const originals = { agents: useAgentsStore.getState(), chat: useChatStore.getState(), questions: useNativeQuestionsStore.getState(), workspace: useWorkspaceStore.getState(), theme: useWorkbenchThemeStore.getState() };

  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ items: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } })));
    useWorkspaceStore.setState({ workspace: { id: 'ws_test', name: '测试工作区', timezone: 'Asia/Shanghai', version: 1 }, me: { user_id: 'u_test', name: 'Owner', role: 'owner', feature_flags: {} }, phase: 'ready' });
    useAgentsStore.setState({ agents: [{ id: 'agent_product', name: 'Nova', role: 'pm', skills: [], availability: 'enabled', presence: 'idle', version: 1 }] });
    useChatStore.setState({ agentId: 'agent_product', conversationId: 'wi_1', conversations: [], runs: [], queue: [], pendingUsers: {}, runAlerts: {}, sending: false, sendError: null });
    useNativeQuestionsStore.setState({ itemsByRun: {}, loadingByRun: {}, errorByRun: {}, submittingByQuestion: {} });
  });

  afterEach(() => {
    useAgentsStore.setState(originals.agents, true);
    useChatStore.setState(originals.chat, true);
    useNativeQuestionsStore.setState(originals.questions, true);
    useWorkspaceStore.setState(originals.workspace, true);
    useWorkbenchThemeStore.setState(originals.theme, true);
    vi.unstubAllGlobals();
  });

  const render = () => renderToStaticMarkup(<MemoryRouter initialEntries={['/chat?agent=agent_product&c=wi_1']}><ChatPage /></MemoryRouter>);

  it('run 状态快照缺失时，待答问题仍渲染可交互卡片（防：卡被藏起来 → 用户答不了 → idle 看门狗判死）', () => {
    useChatStore.setState({ runs: [run('run_q')] });
    useNativeQuestionsStore.setState({ itemsByRun: { run_q: [pendingQuestion] } });
    const html = render();
    expect(html).toContain('data-testid="native-question-question_1"');
    expect(html).toContain('需要你的回答');
    expect(html).toContain('子串包含（推荐）');
  });

  it('run 快照落在非活跃状态时，待答问题同样渲染卡片（store 是权威，不看快照）', () => {
    useChatStore.setState({ runs: [run('run_q', 'failed')] });
    useNativeQuestionsStore.setState({ itemsByRun: { run_q: [pendingQuestion] } });
    expect(render()).toContain('data-testid="native-question-question_1"');
  });

  it('没有待答问题时不渲染提问卡', () => {
    useChatStore.setState({ runs: [run('run_q', 'running')] });
    expect(render()).not.toContain('data-testid="native-question-');
  });
});

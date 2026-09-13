import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { AgentProfile } from '../api/types';
import { useAgentsStore } from '../stores/agents.store';
import { useChatStore } from '../stores/chat.store';
import { useNativeQuestionsStore } from '../stores/questions.store';
import { useWorkspaceStore } from '../stores/workspace.store';
import { buildCanvasMessage } from '../utils/agent-knowledge-canvas';
import { AgentTranscriptReader } from '../components/chat/transcript-view';
import ChatPage, { KnowledgeReferenceChip } from './chat.page';

// 静态渲染取不到 zustand 的 SSR 快照，按仓内既有做法让 hook 直接读当前 state。
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

const agent = (patch: Partial<AgentProfile>): AgentProfile => ({
  id: 'a_pm', name: '产品智能体', role: 'pm', skills: [], availability: 'enabled', presence: 'idle', version: 1, ...patch,
});

const reference = {
  workspaceId: 'ws_test',
  agentId: 'a_pm',
  documentId: 'doc_1',
  version: 2,
  releaseId: 'rel_7',
  title: '退款规则',
  quote: '退款期限为 14 天。',
  heading: '退款窗口',
};

describe('产品知识画布的入口与引用渲染', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ items: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } })));
    useWorkspaceStore.setState({ workspace: { id: 'ws_test', name: '测试工作区', timezone: 'Asia/Shanghai', version: 1 }, me: { user_id: 'u_test', name: 'Owner', role: 'owner', feature_flags: {} }, phase: 'ready' });
    useNativeQuestionsStore.setState({ itemsByRun: {}, loadingByRun: {}, errorByRun: {}, submittingByQuestion: {} });
  });

  afterEach(() => {
    useAgentsStore.getState().reset();
    useChatStore.setState({ agentId: null, conversationId: null });
    vi.unstubAllGlobals();
  });

  const renderChat = (agentProfile: AgentProfile) => {
    useAgentsStore.setState({ agents: [agentProfile] });
    useChatStore.setState({ agentId: agentProfile.id, conversationId: null, conversations: [], runs: [], queue: [], pendingUsers: {}, runAlerts: {}, sending: false, sendError: null });
    return renderToStaticMarkup(<MemoryRouter initialEntries={[`/chat?agent=${agentProfile.id}`]}><ChatPage /></MemoryRouter>);
  };

  it('pm 普通成员默认进入知识画布工作区', () => {
    const html = renderChat(agent({}));
    expect(html).toContain('chat-knowledge-page');
    expect(html).toContain('aria-label="产品知识画布"');
    expect(html).toContain('aria-label="知识画布视图"');
    expect(html).toContain('只读画布');
  });

  it('非 pm 的普通成员没有任何画布入口', () => {
    const html = renderChat(agent({ id: 'a_dev', name: '开发智能体', role: 'developer' }));
    expect(html).not.toContain('知识画布');
    expect(html).not.toContain('chat-knowledge-page');
  });

  it('系统成员即使角色是 pm 也不进画布', () => {
    const html = renderChat(agent({ id: 'a_lib', role: 'pm', is_system: true, kind: 'knowledge_librarian' }));
    expect(html).not.toContain('知识画布');
  });

  it('停用的 pm 成员不进画布', () => {
    const html = renderChat(agent({ availability: 'disabled' }));
    expect(html).not.toContain('知识画布');
    expect(html).not.toContain('chat-knowledge-page');
  });

  it('左栏只剩对话列表：没有 chips、对话资源 tabs、Library 与 Apps 占位面板', () => {
    const html = renderChat(agent({}));
    expect(html).not.toContain('选择要咨询的智能体');
    expect(html).not.toContain('对话资源');
    expect(html).not.toContain('Prompt Library');
    expect(html).not.toContain('>Library<');
    expect(html).not.toContain('>Apps<');
    expect(html).not.toContain('外部 Apps');
    expect(html).not.toContain('在 Agent 配置中管理工具权限');
    expect(html).not.toContain('chat-agent-chip');
    expect(html).not.toContain('chat-sidebar-nav');
    // 保留项：搜索框、新对话按钮、底部「查看团队知识」
    expect(html).toContain('chat-conversation-search');
    expect(html).toContain('新对话');
    expect(html).toContain('查看团队知识');
    expect(html).toContain('href="/library"');
  });

  it('?agent=<id> 深链照常选中该成员并显示在对话头', () => {
    const html = renderChat(agent({ id: 'a_pm', name: '产品智能体' }));
    expect(html).toContain('产品智能体');
    expect(html).toContain('aria-label="产品知识画布"');
  });
});

describe('画布引用在对话两端的渲染', () => {
  it('待发送的引用 chip 展示文档标题、版本与可展开摘录', () => {
    const html = renderToStaticMarkup(<KnowledgeReferenceChip reference={reference} onRemove={() => undefined} />);
    expect(html).toContain('退款规则 · 第 2 版');
    expect(html).toContain('退款期限为 14 天。');
    expect(html).toContain('退款窗口');
    expect(html).toContain('aria-label="移除知识引用"');
    expect(html).toContain('<details');
  });

  it('已发送的消息只展示问题本身，引用折成可展开卡片而不是原始标记', () => {
    const message = buildCanvasMessage('请解释这个规则。', reference);
    const html = renderToStaticMarkup(
      <AgentTranscriptReader segments={[{ kind: 'user', msg: { key: 'm1', runId: 'r1', kind: 'user', text: message, at: '2026-09-09T00:00:00Z' } }]} />,
    );
    expect(html).toContain('请解释这个规则。');
    expect(html).not.toContain('atw-knowledge-reference-v1');
    expect(html).toContain('已发送的知识引用');
    expect(html).toContain('退款规则 · 第 2 版');
    expect(html).toContain('退款期限为 14 天。');
  });

  it('普通消息不会被当成引用卡片', () => {
    const html = renderToStaticMarkup(
      <AgentTranscriptReader segments={[{ kind: 'user', msg: { key: 'm2', runId: 'r1', kind: 'user', text: '看看《退款规则》', at: '2026-09-09T00:00:00Z' } }]} />,
    );
    expect(html).toContain('看看《退款规则》');
    expect(html).not.toContain('已发送的知识引用');
  });
});

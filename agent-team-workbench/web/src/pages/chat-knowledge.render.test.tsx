import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useAgentsStore } from '../stores/agents.store';
import { useChatStore } from '../stores/chat.store';
import { useWorkspaceStore } from '../stores/workspace.store';
import { useWorkbenchThemeStore } from '../stores/workbench-theme.store';
import ChatPage from './chat.page';

vi.mock('../components/knowledge-canvas/knowledge-canvas', () => ({
  KnowledgeCanvas: ({ agentId, requesterAgentId }: { agentId: string; requesterAgentId?: string }) => <div data-testid="knowledge-canvas" data-owner={agentId} data-reader={requesterAgentId ?? 'shared'} />,
}));

vi.mock('../stores/agents.store', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../stores/agents.store')>();
  return { ...actual, useAgentsStore: Object.assign((selector: (state: ReturnType<typeof actual.useAgentsStore.getState>) => unknown) => selector(actual.useAgentsStore.getState()), actual.useAgentsStore) };
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

describe('Product Agent canvas layout and visibility boundary', () => {
  const originals = { agents: useAgentsStore.getState(), chat: useChatStore.getState(), workspace: useWorkspaceStore.getState(), theme: useWorkbenchThemeStore.getState() };

  beforeEach(() => {
    useWorkspaceStore.setState({ workspace: { id: 'ws_test', name: '测试工作区', timezone: 'Asia/Shanghai', version: 1 }, me: { user_id: 'u_test', name: 'Owner', role: 'owner', feature_flags: {} }, phase: 'ready' });
    useAgentsStore.setState({ agents: [{ id: 'agent_product', name: 'Nova', role: 'pm', skills: [], availability: 'enabled', presence: 'idle', version: 1 }] });
    useChatStore.setState({ agentId: 'agent_product', conversationId: null, conversations: [], runs: [], queue: [], pendingUsers: {}, runAlerts: {}, sending: false, sendError: null });
  });

  afterEach(() => {
    useAgentsStore.setState(originals.agents, true);
    useChatStore.setState(originals.chat, true);
    useWorkspaceStore.setState(originals.workspace, true);
    useWorkbenchThemeStore.setState(originals.theme, true);
    vi.unstubAllGlobals();
  });

  const render = (entry = '/chat?agent=agent_product') => renderToStaticMarkup(<MemoryRouter initialEntries={[entry]}><ChatPage /></MemoryRouter>);

  it('opens the declared product role with the real conversation and scoped knowledge side by side', () => {
    const html = render();
    expect(html).toContain('knowledge-chat-layout-active');
    expect(html).toContain('data-owner="agent_product" data-reader="agent_product"');
    expect(html).toContain('data-chat-scroll="transcript"');
    expect(html).toContain('aria-label="发送消息"');
    expect(html).toContain('aria-label="切换成员与会话列表"');
  });

  it('restricts a viewer to the shared read perspective without changing ownership', () => {
    useWorkspaceStore.setState({ me: { user_id: 'u_viewer', name: 'Viewer', role: 'viewer', feature_flags: {} } });
    const html = render();
    expect(html).toContain('data-owner="agent_product" data-reader="shared"');
  });

  it('inherits global dark appearance and keeps non-product Chat in its existing layout', () => {
    useWorkbenchThemeStore.setState({ theme: 'dark' });
    expect(render()).toContain('data-theme="dark"');
    useAgentsStore.setState({ agents: [{ ...useAgentsStore.getState().agents[0], role: 'developer' }] });
    const html = render();
    expect(html).not.toContain('data-testid="knowledge-canvas"');
    expect(html).not.toContain('knowledge-chat-layout-active');
    expect(html).toContain('aria-label="打开知识画布"');
  });

  it('exposes the code workspace only for an enabled user-managed developer Agent', () => {
    useAgentsStore.setState({ agents: [{ ...useAgentsStore.getState().agents[0], role: 'developer', availability: 'enabled' }] });
    const html = render();
    expect(html).toContain('aria-label="打开代码工作台"');

    const codeHtml = render('/chat?agent=agent_product&canvas=code');
    expect(codeHtml).toContain('Java 代码工作台');
    expect(codeHtml).toContain('knowledge-chat-layout-active');
    expect(codeHtml).toContain('aria-label="切换成员与会话列表"');

    useAgentsStore.setState({ agents: [{ ...useAgentsStore.getState().agents[0], availability: 'disabled' }] });
    expect(render()).not.toContain('aria-label="打开代码工作台"');
  });
});

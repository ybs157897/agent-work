import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ExecutionRun, WorkItem } from '../api/types';
import { useChatStore } from './chat.store';
import { readChatSelection, readChatWorkspaceState, writeChatWorkspaceState } from './chat-workspace-state';
import { useRunsStore } from './runs.store';
import { useWorkspaceStore } from './workspace.store';

const oldChat = {
  id: 'wi_old', workspace_id: 'ws_fresh', agent_profile_id: 'agent_librarian', record_kind: 'chat',
  title: '旧对话', description: '', created_at: '', updated_at: '',
} as WorkItem;
const oldRun = { id: 'run_old', work_item_id: oldChat.id, status: 'running', created_at: '' } as ExecutionRun;
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });

describe('explicit fresh conversation entry', () => {
  beforeEach(() => {
    const data = new Map<string, string>();
    vi.stubGlobal('window', { localStorage: {
      getItem: (key: string) => data.get(key) ?? null,
      setItem: (key: string, value: string) => { data.set(key, value); },
      removeItem: (key: string) => { data.delete(key); },
      key: (index: number) => [...data.keys()][index] ?? null,
      get length() { return data.size; },
    } });
    useWorkspaceStore.setState({ workspace: { id: 'ws_fresh', name: 'Fresh', timezone: 'UTC', version: 1 } });
    useChatStore.setState({
      workspaceId: 'ws_fresh', agentId: oldChat.agent_profile_id, conversationId: oldChat.id,
      conversations: [oldChat], runs: [oldRun], runsLoadedConversationId: oldChat.id,
      queue: [{ text: '旧会话排队消息', clientKey: 'q:old' }], sending: false, sendError: '旧发送错误',
      runAlerts: { run_old: { code: 'reply_timeout', message: '旧等待错误', at: '' } },
    });
    useRunsStore.setState({ runs: { run_old: oldRun }, timelines: {}, watchRun: () => {} });
    vi.stubGlobal('fetch', vi.fn(async () => json({ items: [oldChat], next_cursor: null })));
  });
  afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });

  it.each(['running', 'failed'] as const)('leaves the old %s Run intact and opens a clean independent draft', async (status) => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date('2026-09-09T12:00:00Z'));
    const run = { ...oldRun, status };
    useChatStore.setState({ runs: [run] });
    useRunsStore.setState({ runs: { run_old: run } });
    const savedOld = { composer: { draft: '旧会话未发送内容', reference: null }, queue: [{ text: '旧排队', clientKey: 'q:old' }] };
    writeChatWorkspaceState('ws_fresh', 'agent_librarian', oldChat.id, savedOld);
    writeChatWorkspaceState('ws_fresh', 'agent_librarian', null, {
      composer: { draft: '上一次未发送的新会话草稿', reference: null }, queue: [{ text: '旧新建队列', clientKey: 'q:draft' }],
    });

    vi.setSystemTime(new Date('2026-09-09T12:00:01Z'));
    useChatStore.getState().startConversation('agent_librarian');
    await vi.waitFor(() => expect(useChatStore.getState().conversations).toEqual([oldChat]));

    expect(useChatStore.getState()).toMatchObject({
      agentId: 'agent_librarian', conversationId: null, runs: [], queue: [],
      runsLoadedConversationId: null, sendError: null, runAlerts: {}, sending: false,
    });
    expect(useRunsStore.getState().runs.run_old).toEqual(run);
    expect(readChatWorkspaceState('ws_fresh', 'agent_librarian', oldChat.id)).toMatchObject(savedOld);
    expect(readChatWorkspaceState('ws_fresh', 'agent_librarian', null)).toMatchObject({ composer: { draft: '', reference: null }, queue: [] });
    expect(readChatSelection('ws_fresh')).toEqual({ agentId: 'agent_librarian', conversationId: null });
    expect(vi.mocked(fetch).mock.calls.every(([, init]) => !init?.method || init.method === 'GET')).toBe(true);
  });

  it('discards old history hydration that completes after the fresh entry', async () => {
    let finish!: (value: Response) => void;
    const pending = new Promise<Response>((resolve) => { finish = resolve; });
    vi.stubGlobal('fetch', vi.fn(async (url: RequestInfo | URL) => String(url).endsWith('/wi_old/runs')
      ? pending : json({ items: [oldChat], next_cursor: null })));
    const opening = useChatStore.getState().openConversation(oldChat.id);
    useChatStore.getState().startConversation('agent_librarian');
    finish(json({ items: [oldRun] }));
    expect(await opening).toBe(false);
    expect(useChatStore.getState()).toMatchObject({ conversationId: null, runs: [], sendError: null, runAlerts: {} });
  });

  it('first send creates a new chat and Run instead of queuing or steering the old active conversation', async () => {
    const freshChat = { ...oldChat, id: 'wi_new', title: '新的问题' };
    let created = false;
    const requests = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === 'POST' && url.endsWith('/work-items')) { created = true; return json(freshChat); }
      if (init?.method === 'POST' && url.endsWith('/wi_new/runs')) return json({ run_id: 'run_new', work_item_id: 'wi_new', status: 'queued', version: 1 });
      if (url.endsWith('/wi_new/runs')) return json({ items: [{ ...oldRun, id: 'run_new', work_item_id: 'wi_new', status: 'queued' }] });
      return json({ items: created ? [freshChat, oldChat] : [oldChat], next_cursor: null });
    });
    vi.stubGlobal('fetch', requests);
    useChatStore.getState().startConversation('agent_librarian');
    await useChatStore.getState().refreshConversations();
    expect(await useChatStore.getState().send('新的问题')).toBe(true);
    const writes = requests.mock.calls.filter(([, init]) => init?.method === 'POST');
    expect(writes.map(([url]) => String(url))).toEqual([
      '/api/v1/workspaces/ws_fresh/work-items', '/api/v1/work-items/wi_new/runs',
    ]);
    expect(JSON.parse(String(writes[0][1]?.body))).toMatchObject({ record_kind: 'chat', agent_profile_id: 'agent_librarian' });
    expect(JSON.parse(String(writes[1][1]?.body)).input.instruction).toBe('新的问题');
    expect(useChatStore.getState().conversationId).toBe('wi_new');
    expect(useChatStore.getState().queue).toEqual([]);
    expect(useRunsStore.getState().runs.run_old.status).toBe('running');
  });
});

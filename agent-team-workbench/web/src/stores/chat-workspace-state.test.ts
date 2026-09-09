import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  readChatSelection,
  readChatWorkspaceState,
  readLegacyTaskIntakeRecovery,
  writeChatSelection,
  writeChatWorkspaceState,
} from './chat-workspace-state';

function memoryStorage() {
  const values = new Map<string, string>();
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value),
    removeItem: (key: string) => values.delete(key),
    key: (index: number) => [...values.keys()][index] ?? null,
    get length() { return values.size; },
  };
}

describe('chat workspace local recovery', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('隔离 A/B 的 Agent、会话和未发送内容，且保留完整文本', () => {
    const storage = memoryStorage();
    vi.stubGlobal('window', { localStorage: storage });
    const longDraft = '未发送内容'.repeat(12_000);
    writeChatWorkspaceState('ws_a', 'agent_1', 'wi_a', { composer: { draft: longDraft, reference: null }, queue: [{ text: '排队消息', clientKey: 'q1' }] });
    writeChatSelection('ws_a', 'agent_1', 'wi_a');
    writeChatWorkspaceState('ws_b', 'agent_2', null, { composer: { draft: 'B 的草稿', reference: null }, queue: [] });
    writeChatSelection('ws_b', 'agent_2', null);
    expect(readChatSelection('ws_a')).toEqual({ agentId: 'agent_1', conversationId: 'wi_a' });
    expect(readChatWorkspaceState('ws_a', 'agent_1', 'wi_a')?.composer.draft).toBe(longDraft);
    expect(readChatWorkspaceState('ws_b', 'agent_2', null)?.composer.draft).toBe('B 的草稿');
    expect(readChatWorkspaceState('ws_b', 'agent_1', 'wi_a')).toBeNull();
  });

  it('切换不同 Chat 后返回原会话仍恢复该会话草稿，不被目标会话清空覆盖', () => {
    const storage = memoryStorage();
    vi.stubGlobal('window', { localStorage: storage });
    writeChatWorkspaceState('ws_a', 'agent_1', 'wi_first', { composer: { draft: 'A 会话草稿', reference: null }, queue: [] });
    writeChatWorkspaceState('ws_a', 'agent_1', 'wi_second', { composer: { draft: 'B 会话草稿', reference: null }, queue: [] });
    expect(readChatWorkspaceState('ws_a', 'agent_1', 'wi_second')?.composer.draft).toBe('B 会话草稿');
    expect(readChatWorkspaceState('ws_a', 'agent_1', 'wi_first')?.composer.draft).toBe('A 会话草稿');
  });

  it('按 Chat/revision/question 保存未提交的分析回答草稿，不混入普通 composer', () => {
    const storage = memoryStorage();
    vi.stubGlobal('window', { localStorage: storage });
    writeChatWorkspaceState('ws_a', 'agent_1', 'wi_first', {
      composer: { draft: '普通消息', reference: null },
      queue: [],
      analysisDraft: { version: 4, revision: 2, questionId: 'q1', selectedOptionIds: ['read'], text: '补充' },
    });
    const restored = readChatWorkspaceState('ws_a', 'agent_1', 'wi_first');
    expect(restored?.composer.draft).toBe('普通消息');
    expect(restored?.analysisDraft).toMatchObject({ version: 4, revision: 2, questionId: 'q1', selectedOptionIds: ['read'], text: '补充' });
    expect(readChatWorkspaceState('ws_a', 'agent_1', 'wi_second')).toBeNull();
  });

  it('保留独立 publicationDraft，不覆盖 U04/U05 草稿和队列', () => {
    const storage = memoryStorage();
    vi.stubGlobal('window', { localStorage: storage });
    writeChatWorkspaceState('ws_a', 'agent_1', 'wi_first', {
      composer: { draft: '普通消息', reference: null },
      queue: [{ text: '排队', clientKey: 'q1' }],
      analysisDraft: { version: 4, revision: 2, questionId: 'q1', selectedOptionIds: ['read'], text: '补充' },
      decisionDraft: { version: 4, revision: 2, itemId: 'item_1', outcome: 'confirmed', conclusion: '确认', basis: '依据', productVersion: 'v1', clientKey: 'decision:key' },
      publicationDraft: {
        expectedVersion: 5,
        revision: 2,
        itemIds: ['item_1'],
        title: 'Java 能力',
        description: '服务端描述',
        acceptanceCriteria: ['可验收'],
        clientKey: 'draft:key',
        status: 'ready',
        frozen: {
          draft_id: 'pubdraft_1', workspace_id: 'ws_a', chat_id: 'wi_first', agent_id: 'agent_1', analysis_revision: 2,
          item_ids: ['item_1'], confirmation_ids: ['cad_1'], title: 'Java 能力', description: '服务端描述', acceptance_criteria: ['可验收'],
          source_dependencies: [], project_baseline: { repository_identity: 'repo', ref_kind: 'root', branch_name: 'main', checkout_ref: 'root', head: 'h', staged_digest: 's', unstaged_digest: 'u', untracked_digest: 'n', content_digest: 'c' },
          status: 'ready', context_snapshot_id: 'ctx_1', fingerprint: 'fp_1', client_key: 'draft:key', version: 1,
        },
      },
    });
    const restored = readChatWorkspaceState('ws_a', 'agent_1', 'wi_first');
    expect(restored?.queue).toEqual([{ text: '排队', clientKey: 'q1' }]);
    expect(restored?.analysisDraft?.questionId).toBe('q1');
    expect(restored?.decisionDraft?.clientKey).toBe('decision:key');
    expect(restored?.publicationDraft).toMatchObject({ itemIds: ['item_1'], title: 'Java 能力', clientKey: 'draft:key', frozen: { draft_id: 'pubdraft_1' } });
  });

  it('只读恢复旧 task-intake 记录，不删除也不自动换绑', () => {
    const storage = memoryStorage();
    storage.setItem('task-intake:v1:ws_a', JSON.stringify({ version: 1, composerText: '旧输入', messages: [{ role: 'user', content: '旧需求' }] }));
    vi.stubGlobal('window', { localStorage: storage });
    expect(readLegacyTaskIntakeRecovery('ws_a')).toMatchObject({ composerText: '旧输入', messages: [{ content: '旧需求' }] });
    expect(storage.getItem('task-intake:v1:ws_a')).toBeTruthy();
    expect(readLegacyTaskIntakeRecovery('ws_b')).toBeNull();
  });

  it('发现 project-specific v2 分桶并逐桶恢复，保留旧发布状态提示', () => {
    const storage = memoryStorage();
    storage.setItem('task-intake:v2:ws_a:project-a', JSON.stringify({
      version: 2,
      messages: [{ id: 'm1', role: 'user', content: '项目 A 的旧需求' }],
      questionAnswers: {},
      draft: { title: 'A 草案', description: 'A 描述', acceptance_criteria: ['A 验收'] },
      composerText: '',
      pendingPublication: { input: { title: 'A 草案', description: 'A 描述', acceptance_criteria: ['A 验收'] } },
    }));
    storage.setItem('task-intake:v2:ws_a:project-b', JSON.stringify({
      version: 2,
      messages: [{ id: 'm2', role: 'user', content: '项目 B 的旧需求' }],
      questionAnswers: {},
      draft: null,
      composerText: 'B 未发送',
    }));
    storage.setItem('task-intake:project-binding:v1:ws_a', JSON.stringify({ version: 1, selection: { workspace_location_id: 'loc_a', ref_kind: 'root' }, snapshot: null, includeUncommitted: false }));
    vi.stubGlobal('window', { localStorage: storage });
    const recovery = readLegacyTaskIntakeRecovery('ws_a');
    expect(recovery?.entries).toHaveLength(2);
    expect(recovery?.entries.map((entry) => entry.projectKey)).toEqual(['project-a', 'project-b']);
    expect(recovery?.entries[0].hasTerminalState).toBe(true);
    expect(recovery?.bindingPresent).toBe(true);
    expect(recovery?.entries[1].composerText).toBe('B 未发送');
  });
});

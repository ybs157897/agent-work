import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { ChatAnalysisDecision, ChatAnalysisItem } from '../../api/chat-analysis';
import { TaskDraftPreview } from './task-draft-preview';
import type { PersistedPublicationDraft } from '../../stores/chat-workspace-state';

const items: ChatAnalysisItem[] = [{
  id: 'item_1', kind: 'requirement', title: 'Java 能力范围', detail: '已确认范围', source_ids: ['s1'], basis: 'observed',
}];

const decisions: ChatAnalysisDecision[] = [{
  id: 'cad_1', revision: 2, item_id: 'item_1', item_fingerprint: 'a', outcome: 'confirmed', conclusion: '支持代码阅读', basis: '产品记录', product_version: 'v1', created_at: '2026-09-09T00:00:00Z', status: 'valid',
}];

const draft: PersistedPublicationDraft = {
  expectedVersion: 5, revision: 2, itemIds: ['item_1'], title: 'Java 能力', description: '服务端草案描述', acceptanceCriteria: ['可验收'], clientKey: 'draft:key', draftId: 'pubdraft_1', taskId: 'wi_task_1', status: 'ready',
  frozen: {
    draft_id: 'pubdraft_1', workspace_id: 'ws_1', chat_id: 'wi_1', agent_id: 'agent_1', analysis_revision: 2, item_ids: ['item_1'], confirmation_ids: ['cad_1'], title: 'Java 能力', description: '服务端草案描述', acceptance_criteria: ['可验收'], source_dependencies: [], project_baseline: { repository_identity: 'repo', ref_kind: 'root', branch_name: 'main', checkout_ref: 'root', head: 'head', staged_digest: 's', unstaged_digest: 'u', untracked_digest: 'n', content_digest: 'c' }, status: 'ready', context_snapshot_id: 'ctx_1', fingerprint: 'fp', client_key: 'draft:key', version: 1,
  },
};

const handlers = {
  onOpen: () => undefined,
  onToggleItem: () => undefined,
  onTitle: () => undefined,
  onNewDraft: () => undefined,
  onSave: () => undefined,
  onPublish: () => undefined,
  onRetry: () => undefined,
};

describe('TaskDraftPreview', () => {
  it('shows explicit item selection, server-derived preview and separate publish action', () => {
    const html = renderToStaticMarkup(<TaskDraftPreview items={items} decisions={decisions} draft={{ ...draft, status: 'published', taskId: 'wi_task_1' }} analysisBlocked={false} loading={false} saving={false} publishing={false} error={null} {...handlers} />);
    expect(html).toContain('任务草案预览');
    expect(html).toContain('支持代码阅读');
    expect(html).toContain('服务端草案描述');
    expect(html).toContain('保存任务草案');
    expect(html).toContain('发布任务');
    expect(html).toContain('查看任务');
    expect(html).toContain('新建任务草案');
    expect(html).toContain('/tasks/wi_task_1');
  });

  it('does not offer a publishable empty state when no valid confirmed item exists', () => {
    const html = renderToStaticMarkup(<TaskDraftPreview items={[]} decisions={[]} draft={null} analysisBlocked={true} loading={false} saving={false} publishing={false} error={null} {...handlers} />);
    expect(html).toContain('当前没有可用于形成任务的有效产品确认');
    expect(html).not.toContain('发布任务');
  });

  it('disables editing and saving after publication', () => {
    const html = renderToStaticMarkup(<TaskDraftPreview items={items} decisions={decisions} draft={{ ...draft, status: 'published', taskId: 'wi_task_1' }} analysisBlocked={false} loading={false} saving={false} publishing={false} error={null} {...handlers} />);
    expect(html).toContain('value="Java 能力"');
    expect(html.match(/保存任务草案/g)?.length).toBe(1);
    expect(html).toMatch(/保存任务草案[^<]*<\/button>|<button[^>]*disabled=""[^>]*>保存任务草案/);
    expect(html).toContain('查看任务');
  });

  it('locks the frozen payload and exposes only same-key publish retry after an uncertain failure', () => {
    const html = renderToStaticMarkup(<TaskDraftPreview items={items} decisions={decisions} draft={{ ...draft, status: 'failed', lastOperation: 'publish', publishClientKey: 'publish:key' }} analysisBlocked={false} loading={false} saving={false} publishing={false} error="发布结果暂未确认" {...handlers} />);
    expect(html).toContain('重试发布');
    expect(html).toContain('发布结果暂未确认');
    expect(html).toMatch(/<fieldset disabled=""[^>]*aria-label="选择任务事项"/);
    expect(html).not.toContain('新建任务草案');
  });
});

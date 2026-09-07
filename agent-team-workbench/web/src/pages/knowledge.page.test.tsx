import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { useWorkspaceStore } from '../stores/workspace.store';
import type { KnowledgeSubmission } from '../api/knowledge';
import KnowledgePage, {
  buildKnowledgeCandidateChange,
  canPublishKnowledgeSubmission,
  formatKnowledgeScope,
  knowledgeKindLabel,
  isKnowledgeScopeFilter,
  isKnowledgeJobTerminal,
  knowledgeJobNeedsSubmissionRefresh,
  jobPollNeedsSubmissionRefresh,
  knowledgeStatusClass,
  relationLabel,
  sourceKindLabel,
  sourceVerificationLabel,
} from './knowledge.page';

describe('KnowledgePage presentation contract', () => {
  beforeEach(() => {
    useWorkspaceStore.setState({
      phase: 'ready',
      error: null,
      notice: null,
      me: null,
      workspace: null,
      workspaces: [],
      selectedWorkspaceId: null,
      generation: 0,
      switching: false,
      health: null,
      eventCursor: 0,
      sseStatus: 'online',
    });
  });

  afterEach(() => {
    useWorkspaceStore.setState({ workspace: null, selectedWorkspaceId: null });
  });

  it('没有工作区时给出等待态，不把空数据伪装成知识结果', () => {
    const html = renderToStaticMarkup(
      <MemoryRouter>
        <KnowledgePage />
      </MemoryRouter>,
    );
    expect(html).toContain('知识管理员');
    expect(html).toContain('等待工作区');
    expect(html).not.toContain('暂无已发布知识');
  });

  it('状态、关系和 scope 展示使用稳定的语义映射', () => {
    expect(relationLabel('depends_on')).toBe('依赖');
    expect(knowledgeKindLabel('fact')).toBe('事实');
    expect(sourceKindLabel('artifact')).toBe('产物');
    expect(sourceVerificationLabel({
      id: 'kbs_1', workspace_id: 'ws_1', submitted_by_agent_id: 'agent_1', kind: 'run', ref: 'run_1', created_at: '',
      metadata: { verification: 'run_output_verified', digest_verified: true },
    })).toBe('已核验运行记录');
    expect(sourceVerificationLabel({
      id: 'kbs_2', workspace_id: 'ws_1', submitted_by_agent_id: 'agent_1', kind: 'artifact', ref: 'artifact_1', created_at: '',
      metadata: { verification: 'artifact_manifest_verified', digest_verified: true, content_read: false },
    })).toBe('已核验元信息');
    expect(sourceVerificationLabel({
      id: 'kbs_3', workspace_id: 'ws_1', submitted_by_agent_id: 'agent_1', kind: 'document', ref: 'doc.md', created_at: '',
      metadata: { verification: 'submitted_excerpt' },
    })).toBe('已登记摘录');
    expect(sourceVerificationLabel({
      id: 'kbs_4', workspace_id: 'ws_1', submitted_by_agent_id: 'agent_1', kind: 'document', ref: 'doc.md', created_at: '',
      metadata: { verified: true },
    })).toBe('待核验');
    expect(sourceVerificationLabel({
      id: '', workspace_id: 'ws_1', submitted_by_agent_id: 'agent_1', kind: 'run', ref: 'run_1', created_at: '',
      metadata: { verification: 'run_output_verified', digest_verified: true },
    })).toBe('待核验');
    expect(formatKnowledgeScope({ project: 'atlas', branch: 'main' })).toBe('project=atlas · branch=main');
    expect(formatKnowledgeScope(undefined)).toBe('未限定适用范围');
    expect(knowledgeStatusClass('effective')).toContain('text-status-success');
    expect(isKnowledgeJobTerminal('completed')).toBe(true);
    expect(isKnowledgeJobTerminal('running')).toBe(false);
    expect(knowledgeJobNeedsSubmissionRefresh({ mode: 'curation', status: 'completed' })).toBe(true);
    expect(knowledgeJobNeedsSubmissionRefresh({ mode: 'curation', status: 'incomplete' })).toBe(true);
    expect(knowledgeJobNeedsSubmissionRefresh({ mode: 'inquiry', status: 'completed' })).toBe(false);
    expect(knowledgeJobNeedsSubmissionRefresh({ mode: 'inquiry', status: 'running' })).toBe(false);
    expect(jobPollNeedsSubmissionRefresh('running', 'incomplete')).toBe(true);
    expect(jobPollNeedsSubmissionRefresh('running', 'running')).toBe(false);
    expect(jobPollNeedsSubmissionRefresh('completed', 'completed')).toBe(false);
  });

  it('scope 筛选只接受 key=value 或 JSON 对象', () => {
    expect(isKnowledgeScopeFilter('project=atlas')).toBe(true);
    expect(isKnowledgeScopeFilter('{"project":"atlas","branch":"main"}')).toBe(true);
    expect(isKnowledgeScopeFilter('')).toBe(true);
    expect(isKnowledgeScopeFilter('project')).toBe(false);
    expect(isKnowledgeScopeFilter('{"project":')).toBe(false);
    expect(isKnowledgeScopeFilter('["atlas"]')).toBe(false);
  });

  it('只有整理完成且结果条目与版本数量一致的待复核提交可发布', () => {
    const submission = (overrides: Partial<KnowledgeSubmission>): KnowledgeSubmission => ({
      id: 'kss_1',
      workspace_id: 'ws_1',
      agent_id: 'agent_1',
      client_key: 'submission_1',
      status: 'received',
      version: 1,
      created_at: '',
      updated_at: '',
      ...overrides,
    });

    expect(canPublishKnowledgeSubmission(submission({
      status: 'needs_review',
      result_item_ids: ['kb_1'],
      result_version_ids: ['kbv_1'],
    }))).toBe(true);
    expect(canPublishKnowledgeSubmission(submission({ status: 'received' }))).toBe(false);
    expect(canPublishKnowledgeSubmission(submission({
      status: 'needs_review',
      result_item_ids: ['kb_1'],
      result_version_ids: [],
    }))).toBe(false);
    expect(canPublishKnowledgeSubmission(submission({
      status: 'accepted',
      result_item_ids: ['kb_1'],
      result_version_ids: ['kbv_1'],
    }))).toBe(false);
    expect(canPublishKnowledgeSubmission(submission({
      status: 'merged',
      result_item_ids: ['kb_1'],
      result_version_ids: ['kbv_1'],
    }))).toBe(false);
  });

  it('构造候选时分别保留引用、证据摘录和候选正文，并按引用前缀选择来源类型', () => {
    const runChange = buildKnowledgeCandidateChange({
      title: '运行事实',
      body: '候选正文：运行在重试后完成。',
      kind: 'fact',
      visibility: 'workspace',
      evidenceRef: 'run_123',
      evidenceExcerpt: '原始资料：第二次调用返回成功。',
    });
    expect(runChange.body).toBe('候选正文：运行在重试后完成。');
    expect(runChange.sources).toEqual([{
      kind: 'run',
      ref: 'run_123',
      excerpt: '原始资料：第二次调用返回成功。',
    }]);

    expect(buildKnowledgeCandidateChange({
      title: '产物事实', body: '正文', kind: 'fact', visibility: 'workspace',
      evidenceRef: 'artifact_456', evidenceExcerpt: '产物中的原始段落',
    }).sources?.[0]).toMatchObject({ kind: 'artifact', ref: 'artifact_456', excerpt: '产物中的原始段落' });
    expect(buildKnowledgeCandidateChange({
      title: '文档事实', body: '正文', kind: 'fact', visibility: 'workspace',
      evidenceRef: 'acceptance-specification@v1', evidenceExcerpt: '文档中的原始段落',
    }).sources?.[0]).toMatchObject({ kind: 'document', ref: 'acceptance-specification@v1', excerpt: '文档中的原始段落' });
  });
});

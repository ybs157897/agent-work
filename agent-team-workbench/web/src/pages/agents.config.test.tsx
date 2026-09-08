import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AgentProfile } from '../api/types';
import { useAgentsStore } from '../stores/agents.store';
import AgentsPage, { editableAgentPatch } from './agents.page';

// Closed portal surfaces are outside this configuration-render regression.
vi.mock('../components/drawer', () => ({ Drawer: () => null }));
vi.mock('../components/modal', () => ({ Modal: () => null }));
vi.mock('../stores/agents.store', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../stores/agents.store')>();
  return {
    useAgentsStore: Object.assign(
      (selector: (state: ReturnType<typeof actual.useAgentsStore.getState>) => unknown) => selector(actual.useAgentsStore.getState()),
      actual.useAgentsStore,
    ),
  };
});

const agent = (id: string, role: string, skills: AgentProfile['skills']): AgentProfile => ({
  id, name: id === 'librarian' ? '资料管理员' : '团队成员', role, skills,
  availability: 'enabled', presence: 'idle', version: 1,
});

afterEach(() => {
  useAgentsStore.getState().reset();
});

describe('管理员配置入口', () => {
  it('知识库深链直接渲染空技能的管理员，不崩溃或误选第一个成员', () => {
    useAgentsStore.setState({ agents: [agent('worker', 'developer', ['开发']), { ...agent('librarian', 'knowledge_librarian', null), kind: 'knowledge_librarian', is_system: true, instructions: '内置职责', instructions_editable: false }] });
    const html = renderToStaticMarkup(<MemoryRouter initialEntries={['/agents?agent=librarian']}><AgentsPage /></MemoryRouter>);
    expect(html).toContain('value="资料管理员"');
    expect(html).toContain('<option value="knowledge_librarian" selected="">知识库管理员</option>');
    expect(html).toContain('与知识库管理员对话');
    expect(html).toContain('href="/chat?agent=librarian"');
    expect(html).toContain('内置职责（由系统维护）');
    expect(html).not.toContain('>唤醒<');
    expect(html).not.toContain('value="团队成员"');
  });

  it('内置管理员保存只发送模型与runtime，不发送固定身份/提示词/权限', () => {
    const builtin = { ...agent('librarian', 'knowledge_librarian', null), kind: 'knowledge_librarian', is_system: true };
    expect(editableAgentPatch(builtin, {
      name: '不要修改', role: 'developer', skills: ['不要修改'], instructions: '不要覆盖',
      policy: { sandbox: 'danger-full-access' },
      runtime_preference: { preferred: 'codex_local' }, model_override: { model: 'configured-model' }, expected_version: 2,
    })).toEqual({ runtime_preference: { preferred: 'codex_local' }, model_override: { model: 'configured-model' }, expected_version: 2 });
  });

  it('未知自定义角色在配置表单中保留，避免保存时被改成其他角色', () => {
    useAgentsStore.setState({ agents: [agent('custom', 'researcher', [])] });
    const html = renderToStaticMarkup(<MemoryRouter><AgentsPage /></MemoryRouter>);
    expect(html).toContain('<option value="researcher" selected="">researcher</option>');
  });

  it('系统 Coordinator 深链仍不能进入普通成员配置', () => {
    useAgentsStore.setState({ agents: [{ ...agent('system', 'task_coordinator', null), is_system: true }] });
    const html = renderToStaticMarkup(<MemoryRouter initialEntries={['/agents?agent=system']}><AgentsPage /></MemoryRouter>);
    expect(html).toContain('选择左侧智能体进行配置');
    expect(html).not.toContain('系统提示词');
  });
});

import { describe, expect, it } from 'vitest';
import type { AgentProfile, RuntimeBinding } from '../api/types';
import { agentRuntimeView, resolveAgentRuntimeBinding } from './agent-runtime-status';

const agent = {
  id: 'agent_1',
  name: 'Nova',
  role: '协调者',
  skills: [],
  availability: 'enabled',
  presence: 'idle',
  runtime_preference: { preferred: 'codex_local', fallbacks: ['dsh_local'] },
  version: 1,
} satisfies AgentProfile;

const binding = (runtime_label: string, status: string) => ({
  id: `binding_${runtime_label}`,
  runtime_label,
  adapter_id: 'test',
  adapter_version: '1',
  provider: 'test',
  model: 'test',
  capabilities: {},
  status,
  version: 1,
}) satisfies RuntimeBinding;

describe('agentRuntimeView', () => {
  it('按 preferred、fallback、mock 的顺序解析运行绑定', () => {
    const bindings = [binding('mock', 'ready'), binding('dsh_local', 'ready')];
    expect(resolveAgentRuntimeBinding(agent, bindings)?.runtime_label).toBe('dsh_local');
  });

  it('preferred 不可用时跳过它并使用 ready fallback', () => {
    const bindings = [
      binding('codex_local', 'unavailable'),
      binding('dsh_local', 'ready'),
      binding('mock', 'ready'),
    ];
    expect(resolveAgentRuntimeBinding(agent, bindings)?.runtime_label).toBe('dsh_local');
    expect(agentRuntimeView(agent, bindings)).toMatchObject({ canSend: true, label: '待命' });
  });

  it('真实 Runtime 均不可用时回退到 ready mock', () => {
    const bindings = [
      binding('codex_local', 'unavailable'),
      binding('dsh_local', 'unavailable'),
      binding('mock', 'ready'),
    ];
    expect(resolveAgentRuntimeBinding(agent, bindings)?.runtime_label).toBe('mock');
    expect(agentRuntimeView(agent, bindings)).toMatchObject({ canSend: true });
  });

  it('Runtime unavailable 时不再显示在线，并阻止发送', () => {
    expect(agentRuntimeView(agent, [binding('codex_local', 'unavailable')])).toEqual({
      presence: 'offline',
      label: '运行环境不可用',
      canSend: false,
      blockedReason: '运行环境不可用',
    });
  });

  it('Runtime ready 时使用真实 Agent presence 文案', () => {
    expect(agentRuntimeView(agent, [binding('codex_local', 'ready')])).toMatchObject({
      presence: 'idle',
      label: '待命',
      canSend: true,
    });
  });

  it('Runtime 列表加载失败时显示未知但不武断阻断发送', () => {
    expect(agentRuntimeView(agent, null)).toMatchObject({
      label: '运行状态未知',
      canSend: true,
    });
  });
});

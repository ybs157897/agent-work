import type { AgentPresence, AgentProfile, RuntimeBinding } from '../api/types';
import { presenceText } from '../components/status';

export type RuntimeBindingsSnapshot = RuntimeBinding[] | null | undefined;

export interface AgentRuntimeView {
  presence: AgentPresence;
  label: string;
  canSend: boolean;
  blockedReason?: string;
}

function runtimeCandidateLabels(agent: AgentProfile): string[] {
  return [
    agent.runtime_preference?.preferred,
    ...(agent.runtime_preference?.fallbacks ?? []),
    'mock',
  ].filter((label, index, items): label is string => Boolean(label) && items.indexOf(label) === index);
}

/** 与控制平面调度顺序一致：Agent preferred → fallbacks → mock，取第一个 ready。 */
export function resolveAgentRuntimeBinding(
  agent: AgentProfile,
  bindings: RuntimeBinding[],
): RuntimeBinding | undefined {
  return runtimeCandidateLabels(agent)
    .map((label) => bindings.find((binding) => binding.runtime_label === label))
    .find((binding): binding is RuntimeBinding => binding?.status === 'ready');
}

/** Agent presence 只描述调度投影；发送能力还必须结合 RuntimeBinding 状态。 */
export function agentRuntimeView(
  agent: AgentProfile,
  bindings: RuntimeBindingsSnapshot,
): AgentRuntimeView {
  if (agent.availability === 'disabled') {
    return {
      presence: 'offline',
      label: '已停用',
      canSend: false,
      blockedReason: 'Agent 已停用',
    };
  }

  if (bindings === undefined) {
    return {
      presence: agent.presence,
      label: '检测运行环境…',
      canSend: false,
      blockedReason: '正在检查运行环境',
    };
  }

  // Runtime 列表加载失败时不武断阻断发送，但明确告知状态未知。
  if (bindings === null) {
    return {
      presence: agent.presence,
      label: '运行状态未知',
      canSend: true,
    };
  }

  const binding = resolveAgentRuntimeBinding(agent, bindings);
  if (binding) {
    return {
      presence: agent.presence,
      label: presenceText(agent.presence),
      canSend: true,
    };
  }

  const configuredBinding = runtimeCandidateLabels(agent)
    .map((label) => bindings.find((candidate) => candidate.runtime_label === label))
    .find((candidate): candidate is RuntimeBinding => Boolean(candidate));
  if (!configuredBinding) {
    return {
      presence: 'offline',
      label: '未配置运行环境',
      canSend: false,
      blockedReason: '未配置可用的运行环境',
    };
  }

  switch (configuredBinding.status) {
    case 'probing':
      return {
        presence: 'degraded',
        label: '环境检测中',
        canSend: false,
        blockedReason: '运行环境正在检测',
      };
    case 'degraded':
      return {
        presence: 'degraded',
        label: '运行环境异常',
        canSend: false,
        blockedReason: '运行环境异常',
      };
    case 'unavailable':
      return {
        presence: 'offline',
        label: '运行环境不可用',
        canSend: false,
        blockedReason: '运行环境不可用',
      };
    default:
      return {
        presence: 'degraded',
        label: '运行状态未知',
        canSend: false,
        blockedReason: '运行环境状态未知',
      };
  }
}

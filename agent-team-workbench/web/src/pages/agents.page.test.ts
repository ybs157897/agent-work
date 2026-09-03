import { describe, expect, it } from 'vitest';
import type { RuntimeBinding } from '../api/types';
import { isTaskCoordinatorAgent } from '../utils/agent-scope';
import { buildWakePayload, createAgentNotice, formatTokenCount, isCodexRuntime, normalizeReasoningEffort, runtimeDisplayLabel, sessionDisplayName } from './agents.page';

describe('Agent runtime options', () => {
  it('hides the protected system Coordinator from ordinary Agent configuration', () => {
    expect(isTaskCoordinatorAgent({ is_system: true, kind: 'task_coordinator' })).toBe(true);
    expect(isTaskCoordinatorAgent({ is_system: false, kind: 'user' })).toBe(false);
    expect(isTaskCoordinatorAgent({})).toBe(false);
  });

  it('renders the built-in Codex runtime with a product label', () => {
    expect(runtimeDisplayLabel('codex_local')).toBe('Codex app-server');
    expect(runtimeDisplayLabel('dsh_local')).toBe('deepseek harness');
  });

  it('detects Codex by canonical label or adapter id', () => {
    expect(isCodexRuntime('codex_local', null)).toBe(true);
    expect(isCodexRuntime('custom', { adapter_id: 'codex-appserver' } as RuntimeBinding)).toBe(true);
    expect(isCodexRuntime('dsh_local', { adapter_id: 'dsh' } as RuntimeBinding)).toBe(false);
  });

  it('defaults missing Codex effort to Ultra while preserving explicit values', () => {
    expect(normalizeReasoningEffort()).toBe('ultra');
    expect(normalizeReasoningEffort('HIGH')).toBe('high');
    expect(normalizeReasoningEffort('ultra')).toBe('ultra');
    expect(normalizeReasoningEffort('unknown')).toBe('ultra');
  });
});

describe('buildWakePayload', () => {
  it('trims instruction and keeps task_key', () => {
    expect(buildWakePayload('wi_1', '  看下这个任务  ')).toEqual({ task_key: 'wi_1', instruction: '看下这个任务' });
  });

  it('omits blank instruction so the server falls back to the heartbeat template', () => {
    expect(buildWakePayload('wi_1', '')).toEqual({ task_key: 'wi_1' });
    expect(buildWakePayload('wi_1', '   ')).toEqual({ task_key: 'wi_1' });
  });
});

describe('sessionDisplayName', () => {
  it('prefers display_id over session_ref', () => {
    expect(sessionDisplayName({ display_id: 'sess-42', session_ref: 'dsh://x' })).toBe('sess-42');
    expect(sessionDisplayName({ session_ref: 'dsh://x' })).toBe('dsh://x');
    expect(sessionDisplayName({})).toBe('（无 ref）');
  });
});

describe('formatTokenCount', () => {
  it('formats compact units', () => {
    expect(formatTokenCount(999)).toBe('999');
    expect(formatTokenCount(1200)).toBe('1.2k');
    expect(formatTokenCount(2_500_000)).toBe('2.5M');
  });
});

describe('createAgentNotice', () => {
  it('surfaces pending external sync instead of claiming full success', () => {
    expect(createAgentNotice({ config_sync_pending: true }, 'Forge')).toEqual({
      kind: 'warning',
      message: '已创建 Agent Forge，外部配置同步待完成；修复条件后请重载配置',
    });
  });

  it('keeps the normal success notice for an applied bundle', () => {
    expect(createAgentNotice({}, 'Forge')).toEqual({ kind: 'success', message: '已添加 Agent Forge' });
  });
});

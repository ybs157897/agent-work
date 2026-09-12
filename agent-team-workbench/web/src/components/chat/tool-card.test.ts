import { describe, expect, it } from 'vitest';
import { FilePen, FileText, Plug, Search, Terminal, Wrench } from 'lucide-react';
import type { ChatMessage } from '../../stores/chat.store';
import { activityGroupModel, groupActivity, humanizeToolName, shouldAutoCollapseActivity, toolChipModel, toolIcon } from './tool-card';

const msg = (over: Partial<ChatMessage> & Pick<ChatMessage, 'key' | 'runId' | 'kind' | 'text' | 'at'>): ChatMessage => over;

describe('toolIcon', () => {
  it('按类别映射：shell→Terminal、read→FileText、write/edit→FilePen、search→Search、mcp→Plug', () => {
    expect(toolIcon('shell')).toBe(Terminal);
    expect(toolIcon('Bash')).toBe(Terminal);
    expect(toolIcon('Read')).toBe(FileText);
    expect(toolIcon('view_file')).toBe(FileText);
    expect(toolIcon('Write')).toBe(FilePen);
    expect(toolIcon('MultiEdit')).toBe(FilePen);
    expect(toolIcon('apply_patch')).toBe(FilePen);
    expect(toolIcon('Grep')).toBe(Search);
    expect(toolIcon('WebSearch')).toBe(Search);
    expect(toolIcon('mcp__github__create_issue')).toBe(Plug);
  });

  it('未知与缺失落 Wrench；词边界避免误匹配（spread 不算 read）', () => {
    expect(toolIcon('Task')).toBe(Wrench);
    expect(toolIcon('')).toBe(Wrench);
    expect(toolIcon(undefined)).toBe(Wrench);
    expect(toolIcon('spread_output')).toBe(Wrench);
  });
});

describe('humanizeToolName', () => {
  it('把工具 snake_case 转成标题，缺失名称回退工具族', () => {
    expect(humanizeToolName('web_search', 'search')).toBe('Web Search');
    expect(humanizeToolName('', 'bash')).toBe('Bash');
  });
});

describe('toolChipModel', () => {
  it('keeps the human title and terminal state stable for a chip', () => {
    const message = msg({ key: 'chip', runId: 'r1', kind: 'tool', at: '', tool: 'bash', toolStatus: 'failed', text: '调用工具 bash：pnpm test' });
    expect(toolChipModel(message)).toMatchObject({ title: 'Bash', summary: 'Command failed', state: 'error' });
    expect(toolChipModel({ ...message, toolStatus: 'running' }, true).state).toBe('stopped');
  });
});

describe('activityGroupModel', () => {
  it('汇总真实工具状态并让运行中优先成为组状态', () => {
    const items = [
      msg({ key: 'ok', runId: 'r1', kind: 'tool', text: 'done', at: '', toolStatus: 'success' }),
      msg({ key: 'run', runId: 'r1', kind: 'tool', text: 'running', at: '', toolStatus: 'running' }),
      msg({ key: 'fail', runId: 'r1', kind: 'tool', text: 'failed', at: '', toolStatus: 'failed' }),
    ];
    expect(activityGroupModel(items)).toEqual({
      total: 3,
      completed: 1,
      running: 1,
      failed: 1,
      stopped: 0,
      state: 'running',
      summary: '1 完成 · 1 进行中 · 1 失败',
      status: '正在执行',
      toolSummary: 'Tool call × 3',
    });
  });

  it('终态 run 的遗留 running 工具只做中断视觉投影，空组有明确空态', () => {
    const item = msg({ key: 'run', runId: 'r1', kind: 'tool', text: 'running', at: '', toolStatus: 'running' });
    expect(activityGroupModel([item], new Set(['r1']))).toMatchObject({
      stopped: 1,
      running: 0,
      state: 'stopped',
      status: '执行中断',
    });
    expect(activityGroupModel([])).toMatchObject({
      total: 0,
      state: 'empty',
      summary: '暂无工具调用',
      status: '等待调用',
      toolSummary: '',
    });
  });
});

describe('shouldAutoCollapseActivity', () => {
  it('只在 running 进入任一终态时自动收起', () => {
    expect(shouldAutoCollapseActivity('running', 'ok')).toBe(true);
    expect(shouldAutoCollapseActivity('running', 'error')).toBe(true);
    expect(shouldAutoCollapseActivity('running', 'stopped')).toBe(true);
    expect(shouldAutoCollapseActivity('ok', 'ok')).toBe(false);
    expect(shouldAutoCollapseActivity(null, 'running')).toBe(false);
  });
});

describe('groupActivity', () => {
  it('连续同 run 的工具行合成一个活动组；非工具消息切段', () => {
    const messages = [
      msg({ key: 'u1', runId: 'r1', kind: 'user', text: 'hi', at: '' }),
      msg({ key: 't1', runId: 'r1', kind: 'tool', text: 'a', at: '', toolStatus: 'success' }),
      msg({ key: 't2', runId: 'r1', kind: 'error', text: 'b', at: '', toolStatus: 'failed' }),
      msg({ key: 'a1', runId: 'r1', kind: 'assistant', text: 'done', at: '' }),
      msg({ key: 't3', runId: 'r1', kind: 'tool', text: 'c', at: '', toolStatus: 'running' }),
    ];
    const segs = groupActivity(messages);
    expect(segs.map((s) => s.kind)).toEqual(['single', 'activity', 'single', 'activity']);
    expect(segs[1].kind === 'activity' && segs[1].items.map((m) => m.key)).toEqual(['t1', 't2']);
    expect(segs[3].kind === 'activity' && segs[3].items.map((m) => m.key)).toEqual(['t3']);
  });

  it('run 边界切段：不同 run 的工具不合并；无 toolStatus 的 error/system 不进组', () => {
    const messages = [
      msg({ key: 't1', runId: 'r1', kind: 'tool', text: 'a', at: '', toolStatus: 'success' }),
      msg({ key: 't2', runId: 'r2', kind: 'tool', text: 'b', at: '', toolStatus: 'success' }),
      msg({ key: 'e1', runId: 'r2', kind: 'error', text: 'run failed', at: '' }),
    ];
    const segs = groupActivity(messages);
    expect(segs.map((s) => s.kind)).toEqual(['activity', 'activity', 'single']);
  });

  it('按 ZCode 活动语义拆分 Explore/Execute，Write 默认保持独立', () => {
    const messages = [
      msg({ key: 'read', runId: 'r1', kind: 'tool', text: 'read', at: '', tool: 'read', toolStatus: 'success' }),
      msg({ key: 'grep', runId: 'r1', kind: 'tool', text: 'grep', at: '', tool: 'grep', toolStatus: 'success' }),
      msg({ key: 'bash', runId: 'r1', kind: 'tool', text: 'bash', at: '', tool: 'bash', toolStatus: 'success' }),
      msg({ key: 'code', runId: 'r1', kind: 'tool', text: 'code', at: '', tool: 'run_code', toolStatus: 'success' }),
      msg({ key: 'write-1', runId: 'r1', kind: 'tool', text: 'write', at: '', tool: 'write', toolStatus: 'success' }),
      msg({ key: 'write-2', runId: 'r1', kind: 'tool', text: 'write', at: '', tool: 'write', toolStatus: 'success' }),
    ];
    expect(groupActivity(messages).map((segment) => segment.kind === 'activity' && segment.items.map((item) => item.key))).toEqual([
      ['read', 'grep'], ['bash', 'code'], ['write-1'], ['write-2'],
    ]);
  });

  it('分组开关是纯参数：关闭 Explore/Execute 后每次工具独立', () => {
    const messages = [
      msg({ key: 'read-1', runId: 'r1', kind: 'tool', text: 'read', at: '', tool: 'read', toolStatus: 'success' }),
      msg({ key: 'read-2', runId: 'r1', kind: 'tool', text: 'read', at: '', tool: 'read', toolStatus: 'success' }),
      msg({ key: 'bash-1', runId: 'r1', kind: 'tool', text: 'bash', at: '', tool: 'bash', toolStatus: 'success' }),
      msg({ key: 'bash-2', runId: 'r1', kind: 'tool', text: 'bash', at: '', tool: 'bash', toolStatus: 'success' }),
    ];
    expect(groupActivity(messages, { groupExplore: false, groupExecute: false }).filter((segment) => segment.kind === 'activity')).toHaveLength(4);
  });
});

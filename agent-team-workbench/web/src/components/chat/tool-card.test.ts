import { describe, expect, it } from 'vitest';
import { FilePen, FileText, Plug, Search, Terminal, Wrench } from 'lucide-react';
import type { ChatMessage } from '../../stores/chat.store';
import { groupActivity, humanizeToolName, toolChipModel, toolIcon } from './tool-card';

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
    expect(toolChipModel(message)).toMatchObject({ title: 'Bash', state: 'error' });
    expect(toolChipModel({ ...message, toolStatus: 'running' }, true).state).toBe('stopped');
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
});

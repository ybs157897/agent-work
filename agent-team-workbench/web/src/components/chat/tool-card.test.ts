import { describe, expect, it } from 'vitest';
import { FilePen, FileText, Plug, Search, Terminal, Wrench } from 'lucide-react';
import type { ChatMessage } from '../../stores/chat.store';
import { groupActivity, groupWorkedFor, toolIcon } from './tool-card';

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

describe('groupWorkedFor', () => {
  it('组内最早 started 到最晚 completed（跨行取并集）', () => {
    const items = [
      msg({ key: 't1', runId: 'r1', kind: 'tool', text: 'a', at: '', startedAt: '2026-08-22T00:00:00Z', completedAt: '2026-08-22T00:00:02Z' }),
      msg({ key: 't2', runId: 'r1', kind: 'tool', text: 'b', at: '', startedAt: '2026-08-22T00:00:01Z', completedAt: '2026-08-22T00:00:05Z' }),
    ];
    expect(groupWorkedFor(items)).toBe('5s');
  });

  it('缺 started 或 completed（纯进行中/孤儿行）返回 null', () => {
    expect(
      groupWorkedFor([msg({ key: 't1', runId: 'r1', kind: 'tool', text: 'a', at: '', startedAt: '2026-08-22T00:00:00Z' })]),
    ).toBeNull();
    expect(groupWorkedFor([msg({ key: 't2', runId: 'r1', kind: 'tool', text: 'b', at: '', completedAt: '2026-08-22T00:00:05Z' })])).toBeNull();
    expect(groupWorkedFor([])).toBeNull();
  });
});

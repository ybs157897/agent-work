import { afterEach, describe, expect, it, vi } from 'vitest';
import { parseAnsiLines, stripAnsi } from './ansi';
import { writeClipboard } from './clipboard';
import { cx } from './cx';
import { headTailCap } from './head-tail-cap';

describe('cx', () => {
  it('过滤 false/null/undefined，其余以空格拼接', () => {
    expect(cx()).toBe('');
    expect(cx(false, null, undefined)).toBe('');
    expect(cx('a', false, null, undefined, 'b')).toBe('a b');
    expect(cx('a', false)).toBe('a');
  });
});

describe('headTailCap', () => {
  it('未超帽：全部可见，无截断', () => {
    expect(headTailCap(5, 10, false)).toEqual({ hidden: -5, capped: false, headLines: 5, tailLines: 5 });
  });

  it('超帽：hidden 为差额，头段取 ceil、尾段取余量', () => {
    expect(headTailCap(25, 10, false)).toEqual({ hidden: 15, capped: true, headLines: 5, tailLines: 5 });
    // 奇数帽子：ceil 给头段（6/5）
    expect(headTailCap(25, 11, false)).toEqual({ hidden: 14, capped: true, headLines: 6, tailLines: 5 });
  });

  it('expanded 解除截断但保持切分数字不变', () => {
    expect(headTailCap(25, 10, true)).toEqual({ hidden: 15, capped: false, headLines: 5, tailLines: 5 });
  });
});

describe('parseAnsiLines', () => {
  it('无转义纯文本不产生 span 包装', () => {
    expect(parseAnsiLines('hello')).toEqual([[{ text: 'hello' }]]);
    expect(parseAnsiLines('hello')[0]?.[0]?.style).toBeUndefined();
  });

  it('31 红前景映射到状态色 token', () => {
    expect(parseAnsiLines('\x1b[31mred\x1b[0m plain')).toEqual([
      [{ text: 'red', style: { color: 'hsl(var(--color-status-error))' } }, { text: ' plain' }],
    ]);
  });

  it('1 映射 bold', () => {
    expect(parseAnsiLines('\x1b[1mbold')).toEqual([[{ text: 'bold', style: { fontWeight: 700 } }]]);
  });

  it('SGR 状态跨行保持，reset 之后不向后泄漏', () => {
    const lines = parseAnsiLines('\x1b[31mred\nplain\x1b[0m\nafter');
    expect(lines[0]).toEqual([{ text: 'red', style: { color: 'hsl(var(--color-status-error))' } }]);
    // 换行不重置：第二行仍带上一行开启的红色
    expect(lines[1]).toEqual([{ text: 'plain', style: { color: 'hsl(var(--color-status-error))' } }]);
    // reset 只影响其后内容
    expect(lines[2]).toEqual([{ text: 'after' }]);
  });

  it('256 色与 truecolor 落字面 rgb', () => {
    // 208 = 立方体 (5,2,0)，levels=[0,95,135,175,215,255] → 255/135/0
    expect(parseAnsiLines('\x1b[38;5;208mx')[0]?.[0]?.style?.color).toBe('rgb(255, 135, 0)');
    expect(parseAnsiLines('\x1b[38;2;12;34;56my')[0]?.[0]?.style?.color).toBe('rgb(12, 34, 56)');
  });

  it('16 色亮黑映射到弱化文字色，背景保持字面 rgb', () => {
    expect(parseAnsiLines('\x1b[90mmuted')[0]?.[0]?.style?.color).toBe('hsl(var(--color-text-tertiary))');
    expect(parseAnsiLines('\x1b[41mX')[0]?.[0]?.style?.backgroundColor).toBe('rgb(187, 0, 0)');
  });

  it('回车重绘按终端列回放：100%\\rOK 显示 OK0%', () => {
    expect(parseAnsiLines('100%\rOK')[0]?.map((span) => span.text).join('')).toBe('OK0%');
  });
});

describe('stripAnsi', () => {
  it('去掉全部转义与控制字符，只保留 \\n 与 \\t', () => {
    expect(stripAnsi('\x1b[31mred\x1b[0m plain\n\ttab\x07bell\x1b]0;title\x07end')).toBe('red plain\n\ttabbellend');
  });
});

describe('writeClipboard', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  /** node 测试环境无 DOM：stub 出降级路径所需的最小 document（含假 textarea）。 */
  function stubClipboardDocument(execCommand: (cmd: string) => boolean): { value: string }[] {
    const created: { value: string }[] = [];
    vi.stubGlobal('document', {
      execCommand,
      createElement: () => {
        const el = {
          value: '',
          style: {} as Record<string, string>,
          setAttribute: () => {},
          select: () => {},
          remove: () => {},
        };
        created.push(el);
        return el;
      },
      body: { appendChild: () => {} },
    });
    return created;
  }

  it('navigator.clipboard 可用时优先走 writeText 且成功', async () => {
    const writeText = vi.fn(async () => undefined);
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    await expect(writeClipboard('hello')).resolves.toBe(true);
    expect(writeText).toHaveBeenCalledWith('hello');
  });

  it('writeText 被拒绝时降级 execCommand 兜底', async () => {
    vi.stubGlobal('navigator', {
      clipboard: {
        writeText: vi.fn(async () => {
          throw new Error('denied');
        }),
      },
    });
    const created = stubClipboardDocument(() => true);
    await expect(writeClipboard('hello')).resolves.toBe(true);
    expect(created[0]?.value).toBe('hello');
  });

  it('无 clipboard API 时走 execCommand 兜底并写入 textarea', async () => {
    vi.stubGlobal('navigator', {});
    const execCommand = vi.fn(() => true);
    const created = stubClipboardDocument(execCommand);
    await expect(writeClipboard('hello')).resolves.toBe(true);
    expect(execCommand).toHaveBeenCalledWith('copy');
    expect(created[0]?.value).toBe('hello');
  });

  it('execCommand 也不可用时返回 false', async () => {
    vi.stubGlobal('navigator', {});
    vi.stubGlobal('document', {});
    await expect(writeClipboard('hello')).resolves.toBe(false);
  });
});

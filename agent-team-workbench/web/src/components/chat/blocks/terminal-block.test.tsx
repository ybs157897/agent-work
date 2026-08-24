// TerminalBlock 的纯函数面钉测（node 环境无 DOM，组件本体不在此测）：
// prompt 行拆分、状态 Pill 优先级、空输出判定。
import { describe, expect, it } from 'vitest';
import type { AnsiLine } from './ansi';
import {
  DEFAULT_TERMINAL_MAX_LINES,
  TerminalBlock,
  commandLines,
  outputIsEmpty,
  terminalStatusText,
} from './TerminalBlock';

describe('commandLines', () => {
  it('单行命令原样一行', () => {
    expect(commandLines('ls -la')).toEqual(['ls -la']);
  });

  it('多行命令逐行拆分；中间空行保留（真正的双换行）', () => {
    expect(commandLines('cd /tmp\nls\necho done')).toEqual(['cd /tmp', 'ls', 'echo done']);
    expect(commandLines('a\n\nb')).toEqual(['a', '', 'b']);
  });

  it('尾换行是终止符，不产生空命令行', () => {
    expect(commandLines('a\nb\n')).toEqual(['a', 'b']);
  });

  it('空命令与纯换行都只得到一个空行', () => {
    expect(commandLines('')).toEqual(['']);
    expect(commandLines('\n')).toEqual(['']);
  });
});

describe('terminalStatusText', () => {
  it('信号优先：有 signal 即显 Pill 且压过退出码', () => {
    expect(terminalStatusText(0, 'SIGTERM')).toBe('信号 SIGTERM');
    expect(terminalStatusText(1, 'SIGKILL')).toBe('信号 SIGKILL');
    expect(terminalStatusText(undefined, 'SIGINT')).toBe('信号 SIGINT');
  });

  it('非零退出码显退出码 Pill', () => {
    expect(terminalStatusText(127, undefined)).toBe('退出码 127');
    expect(terminalStatusText(1, undefined)).toBe('退出码 1');
  });

  it('干净落定（exit 0 / 无信号）与未知退出码均无 Pill', () => {
    expect(terminalStatusText(0, undefined)).toBeUndefined();
    expect(terminalStatusText(undefined, undefined)).toBeUndefined();
  });
});

describe('outputIsEmpty', () => {
  it('全行全 span trim 后皆空 → 空（含纯转义/纯空白输出）', () => {
    expect(outputIsEmpty([])).toBe(true);
    expect(outputIsEmpty([[{ text: '' }]])).toBe(true);
    expect(outputIsEmpty([[{ text: '  ' }], [{ text: ' \t ' }]])).toBe(true);
    expect(outputIsEmpty([[{ text: ' ', style: { color: 'red' } }]])).toBe(true);
  });

  it('任一 span 有可见文本 → 非空', () => {
    const lines: AnsiLine[] = [[{ text: '' }], [{ text: 'x' }]];
    expect(outputIsEmpty(lines)).toBe(false);
    expect(outputIsEmpty([[{ text: 'ok' }]])).toBe(false);
  });
});

describe('TerminalBlock 模块（node 环境无 DOM）', () => {
  it('import 不炸且组件可取用；默认行数上限 16', () => {
    expect(typeof TerminalBlock).toBe('function');
    expect(DEFAULT_TERMINAL_MAX_LINES).toBe(16);
  });
});

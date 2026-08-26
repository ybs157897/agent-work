import { createElement } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { CodeBlock, codeFilename, copyText, downloadCode, languageFromClassName, nodeText } from './code-block';

describe('CodeBlock 模块（node 环境无 DOM）', () => {
  it('import 不炸且组件可取用（highlight.js 懒加载不在 import 期触发）', () => {
    expect(typeof CodeBlock).toBe('function');
  });
});

describe('languageFromClassName', () => {
  it('language-xxx → xxx（统一小写，兼容 c++/c# 形态）', () => {
    expect(languageFromClassName('language-ts')).toBe('ts');
    expect(languageFromClassName('language-Go')).toBe('go');
    expect(languageFromClassName('language-c++')).toBe('c++');
    expect(languageFromClassName('language-objective-c')).toBe('objective-c');
  });

  it('多 class 时从中间取 language- 标记', () => {
    expect(languageFromClassName('hljs language-python extra')).toBe('python');
  });

  it('无标记 → null（无 class / 空串 / 前缀未独立成词）', () => {
    expect(languageFromClassName(undefined)).toBeNull();
    expect(languageFromClassName(null)).toBeNull();
    expect(languageFromClassName('')).toBeNull();
    expect(languageFromClassName('lang-ts')).toBeNull();
    expect(languageFromClassName('mylanguage-ts')).toBeNull();
  });
});

describe('nodeText', () => {
  it('字符串/数字/数组直接拼接，null/布尔丢弃', () => {
    expect(nodeText('a')).toBe('a');
    expect(nodeText(42)).toBe('42');
    expect(nodeText(['a', 1, 'b'])).toBe('a1b');
    expect(nodeText(null)).toBe('');
    expect(nodeText(true)).toBe('');
  });

  it('嵌套 React 元素递归取文本', () => {
    const tree = createElement('code', { className: 'language-ts' }, 'const a = ', createElement('span', null, 1), ';');
    expect(nodeText(tree)).toBe('const a = 1;');
  });
});

describe('copyText', () => {
  afterEach(() => vi.unstubAllGlobals());

  function stubLegacyDocument(execCommandOk: boolean) {
    const textarea = { value: '', style: {} as Record<string, string>, select: vi.fn(), remove: vi.fn() };
    const execCommand = vi.fn(() => execCommandOk);
    vi.stubGlobal('document', {
      createElement: vi.fn(() => textarea),
      body: { appendChild: vi.fn() },
      execCommand,
    });
    return { textarea, execCommand };
  }

  it('navigator.clipboard 可用 → 首选异步剪贴板，不走 execCommand', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    const { execCommand } = stubLegacyDocument(true);
    await expect(copyText('hello')).resolves.toBe(true);
    expect(writeText).toHaveBeenCalledWith('hello');
    expect(execCommand).not.toHaveBeenCalled();
  });

  it('clipboard 写入被拒 → 降级 execCommand 兜底成功', async () => {
    vi.stubGlobal('navigator', {
      clipboard: { writeText: vi.fn().mockRejectedValue(new Error('denied')) },
    });
    const { textarea, execCommand } = stubLegacyDocument(true);
    await expect(copyText('hello')).resolves.toBe(true);
    expect(textarea.value).toBe('hello');
    expect(execCommand).toHaveBeenCalledWith('copy');
  });

  it('clipboard 缺席且无 document（node 原生）→ false，不抛', async () => {
    vi.stubGlobal('navigator', {});
    await expect(copyText('hello')).resolves.toBe(false);
  });
});

describe('code download metadata', () => {
  it('uses a useful extension for known languages and text fallback', () => {
    expect(codeFilename('typescript')).toBe('code.ts');
    expect(codeFilename('C++')).toBe('code.cpp');
    expect(codeFilename(null)).toBe('code.txt');
    expect(codeFilename('unknown language')).toBe('code.unknownlanguage');
  });

  it('sets the language-derived download filename', () => {
    const click = vi.fn();
    const anchor = { href: '', download: '', click };
    const createElement = vi.fn(() => anchor);
    vi.stubGlobal('document', { createElement });
    vi.stubGlobal('URL', { createObjectURL: vi.fn(() => 'blob:test'), revokeObjectURL: vi.fn() });
    downloadCode('const answer = 42;', 'javascript');
    expect(anchor.download).toBe('code.js');
    expect(click).toHaveBeenCalledOnce();
    vi.unstubAllGlobals();
  });
});

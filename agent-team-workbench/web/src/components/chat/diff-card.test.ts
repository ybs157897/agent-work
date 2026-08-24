import { describe, expect, it } from 'vitest';
import {
  DIFF_COLLAPSE_FILES,
  DIFF_COLLAPSE_LINES,
  diffTotalChanges,
  effectiveView,
  looksLikeUnifiedDiff,
  parseUnifiedDiff,
  shouldCollapseBySize,
  stripControlChars,
  toSplitHunks,
} from './diff-card';

const sample = [
  'diff --git a/web/src/a.ts b/web/src/a.ts',
  'index 111..222 100644',
  '--- a/web/src/a.ts',
  '+++ b/web/src/a.ts',
  '@@ -1,3 +1,4 @@',
  ' context',
  '-old line',
  '+new line',
  '+added line',
  '\\ No newline at end of file',
  'diff --git b/new.go b/new.go',
  'new file mode 100644',
  '--- /dev/null',
  '+++ b/new.go',
  '@@ -0,0 +1,1 @@',
  '+created',
  'diff --git a/gone.py b/gone.py',
  'deleted file mode 100644',
  '--- a/gone.py',
  '+++ /dev/null',
  '@@ -1,1 +0,0 @@',
  '-removed',
].join('\n');

describe('stripControlChars', () => {
  it('剥 ANSI 转义与控制字符，保留 \\n 与 \\t', () => {
    expect(stripControlChars('\x1b[31mred\x1b[0m')).toBe('red');
    expect(stripControlChars('a\r\nb')).toBe('a\nb');
    expect(stripControlChars('a\rb')).toBe('ab');
    expect(stripControlChars('bell\x07back\x08space\x7f')).toBe('bellbackspace');
    expect(stripControlChars('keep\ttab\nline')).toBe('keep\ttab\nline');
  });
});

describe('looksLikeUnifiedDiff', () => {
  it('unified diff 三特征齐全才判定', () => {
    expect(looksLikeUnifiedDiff(sample)).toBe(true);
  });

  it('普通文本/代码/只有部分特征的输出不判定', () => {
    expect(looksLikeUnifiedDiff('hello world\nfoo bar')).toBe(false);
    expect(looksLikeUnifiedDiff('+++ only plus\n--- only minus')).toBe(false);
    expect(looksLikeUnifiedDiff('@@ hunk without file headers @@\n+x')).toBe(false);
    expect(looksLikeUnifiedDiff('')).toBe(false);
  });
});

describe('parseUnifiedDiff', () => {
  it('按文件分组：剥 a//b/ 前缀、新增文件取 +++ 侧、删除文件取 --- 侧', () => {
    const files = parseUnifiedDiff(sample);
    expect(files.map((f) => f.path)).toEqual(['web/src/a.ts', 'new.go', 'gone.py']);
  });

  it('增删行计数与行归类（context/add/del/meta，含 no-newline 标记）', () => {
    const [a, newFile, gone] = parseUnifiedDiff(sample);
    expect(a.additions).toBe(2);
    expect(a.deletions).toBe(1);
    expect(a.lines.map((l) => l.kind)).toEqual(['meta', 'context', 'del', 'add', 'add', 'meta']);
    expect(newFile.additions).toBe(1);
    expect(newFile.deletions).toBe(0);
    expect(gone.deletions).toBe(1);
  });

  it('git 头部时间戳剥除；无 ---/+++ 前导杂行不产生文件', () => {
    const files = parseUnifiedDiff('random output\n--- a/x.y\t2026-08-22 10:00:00\n+++ b/x.y\n@@ -1 +1 @@\n-a\n+b');
    expect(files).toHaveLength(1);
    expect(files[0].path).toBe('x.y');
    expect(parseUnifiedDiff('no headers at all')).toHaveLength(0);
  });
});

describe('shouldCollapseBySize', () => {
  const file = (path: string, adds: number, dels: number) => ({
    path,
    additions: adds,
    deletions: dels,
    lines: [],
  });

  it('≤25 文件且增删 ≤2000 不折叠；任一超限折叠', () => {
    const many = Array.from({ length: DIFF_COLLAPSE_FILES }, (_, i) => file(`f${i}`, 1, 0));
    expect(shouldCollapseBySize(many)).toBe(false);
    expect(shouldCollapseBySize([...many, file('over', 1, 0)])).toBe(true);

    const big = [file('huge', 1_000, DIFF_COLLAPSE_LINES - 1_000)];
    expect(shouldCollapseBySize(big)).toBe(false);
    expect(shouldCollapseBySize([file('huge', 1_000, DIFF_COLLAPSE_LINES - 999)])).toBe(true);
  });

  it('diffTotalChanges 跨文件累计', () => {
    expect(diffTotalChanges([file('a', 3, 1), file('b', 2, 4)])).toEqual({ additions: 5, deletions: 5 });
  });
});

describe('toSplitHunks', () => {
  it('上下文行双侧同文；hunk 内 -/+ 依序配对，多出侧单列', () => {
    const [a] = parseUnifiedDiff(sample);
    expect(toSplitHunks(a)).toEqual([
      { kind: 'meta', text: '@@ -1,3 +1,4 @@' },
      { kind: 'pair', left: { kind: 'context', text: 'context' }, right: { kind: 'context', text: 'context' } },
      { kind: 'pair', left: { kind: 'del', text: 'old line' }, right: { kind: 'add', text: 'new line' } },
      { kind: 'pair', left: null, right: { kind: 'add', text: 'added line' } },
      { kind: 'meta', text: '\\ No newline at end of file' },
    ]);
  });

  it('纯新增文件右列单显、纯删除文件左列单显', () => {
    const [, newFile, gone] = parseUnifiedDiff(sample);
    expect(toSplitHunks(newFile)).toEqual([
      { kind: 'meta', text: '@@ -0,0 +1,1 @@' },
      { kind: 'pair', left: null, right: { kind: 'add', text: 'created' } },
    ]);
    expect(toSplitHunks(gone)).toEqual([
      { kind: 'meta', text: '@@ -1,1 +0,0 @@' },
      { kind: 'pair', left: { kind: 'del', text: 'removed' }, right: null },
    ]);
  });

  it('配对不跨 meta 边界：上一 hunk 尾部 - 不与下一 hunk 头部 + 配对', () => {
    const text = [
      '--- a/x.ts',
      '+++ b/x.ts',
      '@@ -1,2 +1,2 @@',
      '-tail del',
      ' shared',
      '@@ -8 +8 @@',
      '+head add',
    ].join('\n');
    expect(toSplitHunks(parseUnifiedDiff(text)[0])).toEqual([
      { kind: 'meta', text: '@@ -1,2 +1,2 @@' },
      { kind: 'pair', left: { kind: 'del', text: 'tail del' }, right: null },
      { kind: 'pair', left: { kind: 'context', text: 'shared' }, right: { kind: 'context', text: 'shared' } },
      { kind: 'meta', text: '@@ -8 +8 @@' },
      { kind: 'pair', left: null, right: { kind: 'add', text: 'head add' } },
    ]);
  });

  it('行数守恒：左列 del 数 = deletions、右列 add 数 = additions', () => {
    for (const f of parseUnifiedDiff(sample)) {
      const pairs = toSplitHunks(f).filter((r): r is Extract<typeof r, { kind: 'pair' }> => r.kind === 'pair');
      expect(pairs.filter((r) => r.left?.kind === 'del')).toHaveLength(f.deletions);
      expect(pairs.filter((r) => r.right?.kind === 'add')).toHaveLength(f.additions);
    }
  });
});

describe('effectiveView', () => {
  it('窄视口强制 unified；宽视口尊重用户选择', () => {
    expect(effectiveView('split', true)).toBe('unified');
    expect(effectiveView('split', false)).toBe('split');
    expect(effectiveView('unified', false)).toBe('unified');
  });
});

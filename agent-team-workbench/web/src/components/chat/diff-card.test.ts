import { describe, expect, it } from 'vitest';
import {
  DIFF_BODY_MAX_LINES,
  DIFF_COLLAPSE_FILES,
  DIFF_COLLAPSE_LINES,
  capUnifiedRows,
  diffCopyText,
  diffTotalChanges,
  effectiveView,
  flattenUnifiedRows,
  looksLikeUnifiedDiff,
  parseUnifiedDiff,
  shouldCollapseBySize,
  stripControlChars,
  toSplitHunks,
  visibleUnifiedRows,
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

describe('diffCopyText', () => {
  it('单文件：路径行 + 前缀行（+/-/两空格 context）+ meta 原样', () => {
    const [a] = parseUnifiedDiff(sample);
    expect(diffCopyText([a])).toBe(
      [
        'web/src/a.ts',
        '@@ -1,3 +1,4 @@',
        '  context',
        '- old line',
        '+ new line',
        '+ added line',
        '\\ No newline at end of file',
      ].join('\n'),
    );
  });

  it('多文件：按序拼接、文件间不额外空行；空文件集为空串', () => {
    const text = diffCopyText(parseUnifiedDiff(sample));
    expect(text.startsWith('web/src/a.ts\n')).toBe(true);
    expect(text).toContain('\\ No newline at end of file\nnew.go');
    expect(text.endsWith('gone.py\n@@ -1,1 +0,0 @@\n- removed')).toBe(true);
    expect(diffCopyText([])).toBe('');
  });
});

describe('capUnifiedRows', () => {
  it('上限常量 = 16（DSH DiffBlock 默认）', () => {
    expect(DIFF_BODY_MAX_LINES).toBe(16);
  });

  it('未超帽：capped=false 整段直出（head=total、tail=0）', () => {
    expect(capUnifiedRows(DIFF_BODY_MAX_LINES, false)).toEqual({ hidden: 0, capped: false, head: 16, tail: 0 });
    expect(capUnifiedRows(3, true)).toEqual({ hidden: 0, capped: false, head: 3, tail: 0 });
  });

  it('超帽未展开：head=8、tail=8、hidden=n-16', () => {
    expect(capUnifiedRows(17, false)).toEqual({ hidden: 1, capped: true, head: 8, tail: 8 });
    expect(capUnifiedRows(25, false)).toEqual({ hidden: 9, capped: true, head: 8, tail: 8 });
  });

  it('超帽已展开：capped=false 整段直出，hidden 保留（「收起」按钮靠它存在）', () => {
    expect(capUnifiedRows(25, true)).toEqual({ hidden: 9, capped: false, head: 25, tail: 0 });
  });
});

describe('flattenUnifiedRows / visibleUnifiedRows', () => {
  // 行数布局：a.ts 头+10 行、b.ts 头+1 行、c.ts 头+10 行 = 24 行展平行；戴帽后中段藏 8 行，
  // b.ts 的头与行全落中段，c.ts 的头与其前 2 行落中段（tail 起点在文件中间）。
  const mkFiles = () => {
    const line = (kind: 'context' | 'add' | 'del' | 'meta', text: string) => ({ kind, text });
    return [
      { path: 'a.ts', additions: 0, deletions: 0, lines: Array.from({ length: 10 }, (_, i) => line('context', `a${i}`)) },
      { path: 'b.ts', additions: 1, deletions: 0, lines: [line('add', 'b0')] },
      { path: 'c.ts', additions: 0, deletions: 10, lines: Array.from({ length: 10 }, (_, i) => line('del', `c${i}`)) },
    ];
  };

  it('展平：文件头行先于该文件 diff 行，行归属正确', () => {
    const files = mkFiles();
    const rows = flattenUnifiedRows(files);
    expect(rows).toHaveLength(24);
    expect(rows[0]).toEqual({ kind: 'file-head', file: files[0] });
    expect(rows[1]).toEqual({ kind: 'line', file: files[0], line: files[0].lines[0] });
    expect(rows[11]).toEqual({ kind: 'file-head', file: files[1] });
    expect(rows.filter((r) => r.kind === 'file-head')).toHaveLength(3);
  });

  it('戴帽切片：头 8 尾 8；全被藏掉的文件（b.ts）整组不产出，文件头也不渲染', () => {
    const files = mkFiles();
    const { cap, headRows, tailRows } = visibleUnifiedRows(files, false);
    expect(cap).toEqual({ hidden: 8, capped: true, head: 8, tail: 8 });
    expect(headRows).toHaveLength(8);
    expect(tailRows).toHaveLength(8);
    expect(headRows[0]).toEqual({ kind: 'file-head', file: files[0] });
    expect(headRows[7]).toEqual({ kind: 'line', file: files[0], line: files[0].lines[6] });
    expect([...headRows, ...tailRows].some((r) => r.file === files[1])).toBe(false);
    // tail 起点落在 c.ts 中间：首行是裸 diff 行，不带文件头
    expect(tailRows[0]).toEqual({ kind: 'line', file: files[2], line: files[2].lines[2] });
    expect(tailRows.some((r) => r.kind === 'file-head')).toBe(false);
  });

  it('未超帽 / 已展开：整段直出、tail 段为空', () => {
    const single = [{ path: 'x.ts', additions: 1, deletions: 0, lines: [{ kind: 'add' as const, text: 'x' }] }];
    expect(visibleUnifiedRows(single, false)).toEqual({
      cap: { hidden: 0, capped: false, head: 2, tail: 0 },
      headRows: flattenUnifiedRows(single),
      tailRows: [],
    });
    const { headRows, tailRows } = visibleUnifiedRows(mkFiles(), true);
    expect(headRows).toHaveLength(24);
    expect(tailRows).toHaveLength(0);
  });
});

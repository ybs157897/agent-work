import { describe, expect, it } from 'vitest';
import { parseGrepOutput, parsePathList } from './search-parse';

describe('parseGrepOutput', () => {
  it('基本解析：path:line:content 逐行命中，连续同 path 归一组', () => {
    const text = 'src/a.go:10:func main() {\nsrc/a.go:12:\tfmt.Println("hi")\nweb/src/b.tsx:3:export function App() {';
    const out = parseGrepOutput(text);
    expect(out).not.toBeNull();
    expect(out?.truncated).toBe(false);
    expect(out?.files).toEqual([
      {
        path: 'src/a.go',
        matches: [
          { lineNumber: 10, line: 'func main() {' },
          { lineNumber: 12, line: '\tfmt.Println("hi")' },
        ],
      },
      { path: 'web/src/b.tsx', matches: [{ lineNumber: 3, line: 'export function App() {' }] },
    ]);
  });

  it('非连续同 path 合并回首见组，组序按首见顺序', () => {
    const out = parseGrepOutput('a.go:1:x\nb.go:2:y\na.go:3:z');
    expect(out?.files).toEqual([
      { path: 'a.go', matches: [{ lineNumber: 1, line: 'x' }, { lineNumber: 3, line: 'z' }] },
      { path: 'b.go', matches: [{ lineNumber: 2, line: 'y' }] },
    ]);
  });

  it('Windows 盘符路径：盘符冒号不误作分隔符；行号后分隔兼容全角冒号', () => {
    const out = parseGrepOutput('C:\\Users\\yin\\main.go:42:func main() {\nC:/dev/util.ts:7：export const x = 1;');
    expect(out?.files[0]).toEqual({
      path: 'C:\\Users\\yin\\main.go',
      matches: [{ lineNumber: 42, line: 'func main() {' }],
    });
    expect(out?.files[1]).toEqual({
      path: 'C:/dev/util.ts',
      matches: [{ lineNumber: 7, line: 'export const x = 1;' }],
    });
  });

  it('普通文本返回 null：无命中、或命中占比不足 60%、或少于 2 行命中', () => {
    // 完全不像 grep
    expect(parseGrepOutput('hello world\nthis is a paragraph\nno grep shape here')).toBeNull();
    // 命中 2 / 非空 5 = 40% < 60%
    expect(parseGrepOutput('a.go:1:x\nplain line\nb.go:2:y\nanother plain\nmore plain')).toBeNull();
    // 仅 1 行命中（100% 但 < 2 行门槛）
    expect(parseGrepOutput('a.go:1:only one hit')).toBeNull();
    // 空文本
    expect(parseGrepOutput('')).toBeNull();
  });

  it('占比门槛为闭区间：命中 3 / 非空 5 = 60% 恰好通过；尾随换行不计入分母', () => {
    const out = parseGrepOutput('a.go:1:x\nb.go:2:y\nc.go:3:z\nplain one\nplain two\n');
    expect(out?.files.map((f) => f.path)).toEqual(['a.go', 'b.go', 'c.go']);
  });

  it('截断标记：文本长度 ≥ 1900 视为被工具截断，末行不完整也照常解析', () => {
    const tail = 'x'.repeat(1400); // 末行被截断拉伸成超长内容，总长落在 1900–2000 区间
    const lines = Array.from({ length: 30 }, (_, i) => `a.go:${i + 1}:content-${i}`);
    lines.push(`a.go:31:${tail}`);
    const text = lines.join('\n');
    expect(text.length).toBeGreaterThanOrEqual(1900);
    const out = parseGrepOutput(text);
    expect(out?.truncated).toBe(true);
    expect(out?.files[0].matches.at(-1)?.line).toBe(tail);
  });
});

describe('parsePathList', () => {
  it('每行含路径分隔符：原样返回行数组；容忍单个尾随换行', () => {
    expect(parsePathList('src/a.go\nweb/src/b.tsx')).toEqual(['src/a.go', 'web/src/b.tsx']);
    expect(parsePathList('src/a.go\nweb/src/b.tsx\n')).toEqual(['src/a.go', 'web/src/b.tsx']);
  });

  it('无分隔符但以 .ext 结尾的行也算路径', () => {
    expect(parsePathList('README.md\nnotes.txt')).toEqual(['README.md', 'notes.txt']);
  });

  it('空文本、含空行、含空白开头行、含不像路径的行均返回 null', () => {
    expect(parsePathList('')).toBeNull();
    expect(parsePathList('\n')).toBeNull();
    // 中部空行：不是纯路径列表
    expect(parsePathList('src/a.go\n\nweb/b.tsx')).toBeNull();
    // 空格开头的行（缩进文本特征）
    expect(parsePathList('src/a.go\n  indented.go')).toBeNull();
    // 既无分隔符也无扩展名结尾（普通词句特征）
    expect(parsePathList('src/a.go\nnot a path line')).toBeNull();
  });
});

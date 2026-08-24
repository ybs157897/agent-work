import { describe, expect, it } from 'vitest';
import { langFromPath, readBlockFromOutput } from './read-model';

describe('langFromPath', () => {
  it('扩展名映射：大小写不敏感，覆盖代表性语言', () => {
    expect(langFromPath('src/main.ts')).toBe('typescript');
    expect(langFromPath('COMPONENT.TSX')).toBe('typescript');
    expect(langFromPath('app.Jsx')).toBe('javascript');
    expect(langFromPath('tool.mjs')).toBe('javascript');
    expect(langFromPath('pkg/cjs/config.cjs')).toBe('javascript');
    expect(langFromPath('main.go')).toBe('go');
    expect(langFromPath('lib.rs')).toBe('rust');
    expect(langFromPath('script.py')).toBe('python');
    expect(langFromPath('style.scss')).toBe('scss');
    expect(langFromPath('view.vue')).toBe('xml');
    expect(langFromPath('config.toml')).toBe('ini');
    expect(langFromPath('deploy.sh')).toBe('bash');
    expect(langFromPath('header.hpp')).toBe('cpp');
    expect(langFromPath('C:\\dev\\win.c')).toBe('c');
  });

  it('无扩展名按 basename 精确匹配特例文件；点前导文件不算有扩展名', () => {
    expect(langFromPath('Dockerfile')).toBe('dockerfile');
    expect(langFromPath('build/dockerfile')).toBe('dockerfile');
    expect(langFromPath('Makefile')).toBe('makefile');
    expect(langFromPath('.gitignore')).toBeUndefined();
  });

  it('未知扩展名 / 空路径返回 undefined', () => {
    expect(langFromPath('data.xyz')).toBeUndefined();
    expect(langFromPath('archive.tar.gz')).toBeUndefined();
    expect(langFromPath('')).toBeUndefined();
    expect(langFromPath('noext')).toBeUndefined();
  });
});

describe('readBlockFromOutput', () => {
  it('空输出（含纯空白）返回 null', () => {
    expect(readBlockFromOutput('')).toBeNull();
    expect(readBlockFromOutput('   \n\t\n  ')).toBeNull();
  });

  it('行号从 1 起；label/lang 取自 filePath；\r\n 与裸 \r 归一', () => {
    const props = readBlockFromOutput('line one\r\nline two\rline three', 'src/a.go');
    expect(props).toEqual({
      label: 'src/a.go',
      lang: 'go',
      lines: [
        { number: 1, text: 'line one' },
        { number: 2, text: 'line two' },
        { number: 3, text: 'line three' },
      ],
    });
    // 不传 totalLines（无窗口信息）
    expect(props?.totalLines).toBeUndefined();
  });

  it('缺省 filePath：label 与 lang 均为 undefined', () => {
    const props = readBlockFromOutput('only line');
    expect(props?.label).toBeUndefined();
    expect(props?.lang).toBeUndefined();
    expect(props?.lines).toEqual([{ number: 1, text: 'only line' }]);
  });
});

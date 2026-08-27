import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { SearchBlock } from './SearchBlock';

describe('SearchBlock summary', () => {
  it('截断但无可信 total 时明确标注已截断，不把保留数冒充总数', () => {
    const html = renderToStaticMarkup(
      <SearchBlock
        kind="matches"
        files={[{
          path: 'src/App.tsx',
          matches: [
            { lineNumber: 3, line: 'const app = true;' },
            { lineNumber: 9, line: 'export default app;' },
          ],
        }]}
        truncated
      />,
    );
    expect(html).toContain('已截断 · 显示 2 处匹配 · 1 个文件');
    expect(html).not.toContain('显示 2 / 共 2');
  });

  it('协议提供可信 total 时保留显示数与总数的对比', () => {
    const html = renderToStaticMarkup(
      <SearchBlock
        kind="paths"
        paths={['src/App.tsx', 'src/main.tsx']}
        truncated
        total={18}
      />,
    );
    expect(html).toContain('显示 2 / 共 18 个路径');
  });
});

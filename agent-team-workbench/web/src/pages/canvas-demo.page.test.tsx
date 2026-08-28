import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import CanvasDemoPage from './canvas-demo.page';

describe('CanvasDemoPage', () => {
  it('renders static research fixtures without backend dependencies', () => {
    const html = renderToStaticMarkup(<CanvasDemoPage />);
    expect(html).toContain('Canvas 调研 Demo');
    expect(html).toContain('data-content-block="canvas"');
    expect(html).toContain('新用户注册 · 用户旅程');
    expect(html).toContain('Agent Workbench · 系统上下文');
    expect(html).toContain('NOW / NEXT / LATER 路线图');
  });
});

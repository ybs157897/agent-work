import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import { LayoutShell } from './layout-shell';

function renderShell(path: string) {
  return renderToStaticMarkup(
    <MemoryRouter initialEntries={[path]}>
      <LayoutShell><section aria-label="页面正文">正文</section></LayoutShell>
    </MemoryRouter>,
  );
}

describe('task conversation navigation', () => {
  it('provides a dedicated sidebar destination and a full-height conversation surface', () => {
    const html = renderShell('/task-chat');
    expect(html).toMatch(/<a(?=[^>]*href="\/task-chat")(?=[^>]*aria-current="page")[^>]*>/);
    expect(html).toContain('任务对话');
    expect(html).toContain('href="/tasks"');
    expect(html).toContain('href="/chat"');
    expect(html).toMatch(/<main(?=[^>]*id="main-content")(?=[^>]*class="[^"]*tx-scope)[^>]*>/);
    expect(html).not.toContain('<header');
    expect(html).not.toContain('role="dialog"');
  });

  it('keeps the board as a separate destination', () => {
    const html = renderShell('/tasks');
    expect(html).toMatch(/<a(?=[^>]*href="\/tasks")(?=[^>]*aria-current="page")[^>]*>/);
    expect(html).not.toMatch(/<a(?=[^>]*href="\/task-chat")(?=[^>]*aria-current="page")[^>]*>/);
    expect(html).toContain('plane-board-shell');
  });
});

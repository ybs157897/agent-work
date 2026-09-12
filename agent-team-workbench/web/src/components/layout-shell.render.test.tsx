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

describe('global chat navigation', () => {
  it('uses the existing Chat destination as the single conversation entry', () => {
    const html = renderShell('/chat');
    expect(html).toMatch(/<a(?=[^>]*href="\/chat")(?=[^>]*aria-current="page")[^>]*>/);
    expect(html).toContain('对话');
    expect(html).not.toContain('任务对话');
    expect(html).toContain('href="/tasks"');
    expect(html).toContain('href="/chat"');
    expect(html).toMatch(/<main(?=[^>]*id="main-content")(?=[^>]*class="[^"]*workbench-chat-surface)[^>]*>/);
    expect(html).not.toContain('<header');
    expect(html).not.toContain('role="dialog"');
    expect(html).not.toContain('打开主导航');
    expect(html).not.toContain('关闭主导航');
    expect(html).toContain('data-theme="light"');
  });

  it('keeps the legacy task-chat URL inside the same full-height shell for redirect recovery', () => {
    const html = renderShell('/task-chat');
    expect(html).not.toMatch(/<a(?=[^>]*href="\/task-chat")(?=[^>]*aria-current="page")[^>]*>/);
    expect(html).toContain('href="/chat"');
    expect(html).toContain('workbench-chat-surface');
  });

  it('keeps the board as a separate destination', () => {
    const html = renderShell('/tasks');
    expect(html).toMatch(/<a(?=[^>]*href="\/tasks")(?=[^>]*aria-current="page")[^>]*>/);
    expect(html).not.toMatch(/<a(?=[^>]*href="\/task-chat")(?=[^>]*aria-current="page")[^>]*>/);
    expect(html).toContain('plane-board-shell');
  });

  it('exposes the knowledge librarian as a first-class workspace destination', () => {
    const html = renderShell('/library');
    expect(html).toMatch(/<a(?=[^>]*href="\/library")(?=[^>]*aria-current="page")[^>]*>/);
    expect(html).toContain('知识库');
    expect(html).toContain('workbench-main-surface');
    expect(html).not.toContain('mesh-bg');
  });
});

import type { ReactNode } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Drawer } from './drawer';
import { Modal } from './modal';

vi.mock('react-dom', async () => {
  const actual = await vi.importActual<typeof import('react-dom')>('react-dom');
  return {
    ...actual,
    createPortal: (children: ReactNode) => children,
  };
});

beforeEach(() => {
  vi.stubGlobal('document', { body: { style: { overflow: '' } } });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('dialog primitives', () => {
  it('Drawer keeps an accessible label, explicit close button type, and viewport-safe width', () => {
    const html = renderToStaticMarkup(
      <Drawer open onClose={() => undefined} ariaLabel="任务详情" width={760}>
        <p>内容</p>
      </Drawer>,
    );

    expect(html).toContain('aria-label="任务详情"');
    expect(html).toContain('type="button"');
    expect(html).toContain('max-width:100vw');
    expect(html).toContain('tabindex="-1"');
    expect(html).toContain('focus-visible:ring-2');
  });

  it('Drawer exposes the task skin on its portal layer', () => {
    const html = renderToStaticMarkup(
      <Drawer open skin="task" onClose={() => undefined} ariaLabel="任务详情" width={760}>
        <p>内容</p>
      </Drawer>,
    );

    expect(html).toContain('data-dialog-layer="drawer"');
    expect(html).toContain('plane-board');
    expect(html).toContain('min-h-0');
  });

  it('Modal keeps a labelled dialog and an explicit close button type', () => {
    const html = renderToStaticMarkup(
      <Modal open onClose={() => undefined} title="确认操作">
        <p>内容</p>
      </Modal>,
    );

    expect(html).toContain('role="dialog"');
    expect(html).toContain('aria-labelledby=');
    expect(html).toContain('type="button"');
    expect(html).toContain('focus-visible:ring-offset-2');
    expect(html).toMatch(/<svg[^>]*aria-hidden="true"/);
  });

  it('Modal raises its layer above Drawer and keeps a reachable footer outside the scroll body', () => {
    const html = renderToStaticMarkup(
      <Modal
        open
        skin="task"
        onClose={() => undefined}
        title="确认操作"
        footer={<button type="button">提交</button>}
      >
        <p>低高度正文</p>
      </Modal>,
    );

    expect(html).toContain('data-dialog-layer="modal"');
    expect(html).toContain('z-[60]');
    expect(html).toContain('z-[70]');
    expect(html).toContain('overflow-y-auto');
    expect(html).toContain('plane-board');
    expect(html).toContain('>提交</button>');
  });
});

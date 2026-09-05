import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { closeDialogOnEscape, registerDialog } from './dialog-interaction';

describe('dialog interaction stack', () => {
  beforeEach(() => {
    vi.stubGlobal('window', {
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('只允许当前最上层 panel 消费 Escape', () => {
    const backgroundPanel = {} as HTMLElement;
    const topPanel = {} as HTMLElement;
    const body = { style: { overflow: 'auto' } };
    vi.stubGlobal('document', {
      body,
      querySelectorAll: () => [backgroundPanel, topPanel],
    });

    const releaseBackground = registerDialog(backgroundPanel);
    const releaseTop = registerDialog(topPanel);
    const backgroundClose = vi.fn();
    const topClose = vi.fn();
    const eventFor = (currentTarget: HTMLElement) => ({
      key: 'Escape',
      currentTarget,
      preventDefault: vi.fn(),
      stopPropagation: vi.fn(),
    }) as never;

    closeDialogOnEscape(eventFor(backgroundPanel), backgroundClose);
    expect(backgroundClose).not.toHaveBeenCalled();

    closeDialogOnEscape(eventFor(topPanel), topClose);
    expect(topClose).toHaveBeenCalledOnce();

    releaseTop();
    releaseBackground();
    expect(body.style.overflow).toBe('auto');
  });

  it('嵌套 dialog 只保留最上层可交互，并在关闭后还原背景状态', () => {
    const root = fakeElement();
    const drawerLayer = fakeElement();
    const modalLayer = fakeElement();
    const drawerPanel = fakeElement(drawerLayer.element);
    const modalPanel = fakeElement(modalLayer.element);
    const body = {
      style: { overflow: 'auto' },
      children: [root.element, drawerLayer.element, modalLayer.element],
    };
    vi.stubGlobal('document', {
      body,
      querySelectorAll: () => [drawerPanel.element, modalPanel.element],
    });

    const releaseDrawer = registerDialog(drawerPanel.element);
    expect(root.element.inert).toBe(true);
    expect(drawerLayer.element.inert).toBe(false);
    expect(modalLayer.element.inert).toBe(true);
    expect(root.attributes.get('aria-hidden')).toBe('true');

    const releaseModal = registerDialog(modalPanel.element);
    expect(root.element.inert).toBe(true);
    expect(drawerLayer.element.inert).toBe(true);
    expect(modalLayer.element.inert).toBe(false);

    body.children = [root.element, drawerLayer.element];
    releaseModal();
    expect(root.element.inert).toBe(true);
    expect(drawerLayer.element.inert).toBe(false);
    expect(modalLayer.element.inert).toBe(false);

    releaseDrawer();
    expect(root.element.inert).toBe(false);
    expect(root.attributes.has('inert')).toBe(false);
    expect(root.attributes.has('aria-hidden')).toBe(false);
    expect(body.style.overflow).toBe('auto');
  });

  it('焦点落到 body 时，window capture 的 Escape 仍只关闭最上层 dialog', () => {
    const listeners = new Map<string, EventListener>();
    const addEventListener = vi.fn((type: string, listener: EventListener) => listeners.set(type, listener));
    vi.stubGlobal('window', {
      addEventListener,
      removeEventListener: vi.fn(),
    });
    const panel = fakeElement().element;
    const close = vi.fn();
    const body = { style: { overflow: 'auto' }, children: [] };
    vi.stubGlobal('document', {
      body,
      querySelectorAll: () => [panel],
    });

    const release = registerDialog(panel, close);
    let defaultPrevented = false;
    const event = {
      key: 'Escape',
      get defaultPrevented() { return defaultPrevented; },
      preventDefault: vi.fn(() => { defaultPrevented = true; }),
      stopPropagation: vi.fn(),
    } as unknown as KeyboardEvent;
    listeners.get('keydown')?.(event);

    expect(close).toHaveBeenCalledOnce();
    expect(event.preventDefault).toHaveBeenCalledOnce();
    expect(event.stopPropagation).toHaveBeenCalledOnce();
    expect(defaultPrevented).toBe(true);

    closeDialogOnEscape({
      key: 'Escape',
      currentTarget: panel,
      defaultPrevented: true,
      preventDefault: vi.fn(),
      stopPropagation: vi.fn(),
    } as never, close);
    expect(close).toHaveBeenCalledOnce();
    release();
  });
});

function fakeElement(layer?: HTMLElement) {
  const attributes = new Map<string, string>();
  const element = {
    inert: false,
    closest: (selector: string) => (selector === '[data-dialog-layer]' ? layer ?? null : null),
    hasAttribute: (name: string) => attributes.has(name),
    setAttribute: (name: string, value: string) => attributes.set(name, value),
    removeAttribute: (name: string) => attributes.delete(name),
    getAttribute: (name: string) => attributes.get(name) ?? null,
  } as unknown as HTMLElement & { inert: boolean };
  return { element, attributes };
}

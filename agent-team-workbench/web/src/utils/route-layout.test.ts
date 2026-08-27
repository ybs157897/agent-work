import { describe, expect, it } from 'vitest';
import { isChatPath, isFullBleedPath, mainContentClassName } from './route-layout';

describe('isFullBleedPath', () => {
  it.each(['/chat', '/models', '/agents'])('keeps %s inside the shared fixed-height shell', (path) => {
    expect(isFullBleedPath(path)).toBe(true);
  });

  it.each(['/', '/tasks', '/logs', '/settings', '/missing'])('lets %s use the main page scroller', (path) => {
    expect(isFullBleedPath(path)).toBe(false);
  });
});

describe('isChatPath', () => {
  it('treats /chat as the dark reading route', () => {
    expect(isChatPath('/chat')).toBe(true);
  });

  it.each(['/', '/models', '/agents', '/chat/extra', '/chats'])('does not treat %s as chat', (path) => {
    expect(isChatPath(path)).toBe(false);
  });
});

describe('mainContentClassName', () => {
  it('mounts tx-scope and drops paper mesh on /chat', () => {
    const cls = mainContentClassName('/chat');
    expect(cls).toContain('tx-scope');
    expect(cls).not.toContain('mesh-bg');
    expect(cls).toContain('overflow-hidden');
    expect(cls).toContain('flex flex-col');
  });

  it('keeps paper mesh on other full-bleed routes', () => {
    expect(mainContentClassName('/models')).toContain('mesh-bg');
    expect(mainContentClassName('/models')).not.toContain('tx-scope');
    expect(mainContentClassName('/agents')).toContain('mesh-bg');
  });

  it('lets page-shell routes scroll the main pane on paper', () => {
    expect(mainContentClassName('/')).toContain('overflow-y-auto');
    expect(mainContentClassName('/')).toContain('mesh-bg');
    expect(mainContentClassName('/')).not.toContain('tx-scope');
  });
});

import { describe, expect, it } from 'vitest';
import { isChatPath, isFullBleedPath, isTasksPath, mainContentClassName } from './route-layout';

describe('isFullBleedPath', () => {
  it.each(['/chat', '/task-chat', '/task-chat/', '/models', '/agents'])('keeps %s inside the shared fixed-height shell', (path) => {
    expect(isFullBleedPath(path)).toBe(true);
  });

  it.each(['/', '/tasks', '/logs', '/settings', '/missing'])('lets %s stay off the shared full-bleed set', (path) => {
    expect(isFullBleedPath(path)).toBe(false);
  });
});

describe('isTasksPath', () => {
  it.each(['/tasks', '/tasks/', '/tasks/wi_1'])('matches Task workspace path %s', (path) => {
    expect(isTasksPath(path)).toBe(true);
  });

  it.each(['/', '/chat', '/tasks-archive'])('does not treat %s as Task workspace', (path) => {
    expect(isTasksPath(path)).toBe(false);
  });
});

describe('isChatPath', () => {
  it.each(['/chat', '/task-chat', '/task-chat/'])('treats %s as a conversation reading route', (path) => {
    expect(isChatPath(path)).toBe(true);
  });

  it.each(['/', '/models', '/agents', '/chat/extra', '/chats', '/task-chat/extra', '/task-chats'])('does not treat %s as chat', (path) => {
    expect(isChatPath(path)).toBe(false);
  });
});

describe('mainContentClassName', () => {
  it.each(['/chat', '/task-chat', '/task-chat/'])('mounts a fixed-height reading surface on %s without decorative skin classes', (path) => {
    const cls = mainContentClassName(path);
    expect(cls).toContain('workbench-chat-surface');
    expect(cls).not.toContain('tx-scope');
    expect(cls).not.toContain('mesh-bg');
    expect(cls).not.toContain('plane-board-shell');
    expect(cls).toContain('overflow-hidden');
    expect(cls).toContain('flex flex-col');
  });

  it('mounts Plane shell on /tasks board and drops paper mesh', () => {
    const cls = mainContentClassName('/tasks');
    expect(cls).toContain('plane-board-shell');
    expect(cls).not.toContain('mesh-bg');
    expect(cls).not.toContain('tx-scope');
    expect(cls).toContain('overflow-hidden');
    expect(cls).toContain('flex flex-col');
  });

  it('uses the shared workbench surface on non-Task full-bleed routes', () => {
    expect(mainContentClassName('/models')).toContain('workbench-main-surface');
    expect(mainContentClassName('/models')).not.toContain('mesh-bg');
    expect(mainContentClassName('/agents')).toContain('workbench-main-surface');
  });

  it('uses the same Plane shell for Task detail', () => {
    expect(mainContentClassName('/tasks/wi_1')).toContain('plane-board-shell');
    expect(mainContentClassName('/tasks/wi_1')).not.toContain('mesh-bg');
  });

  it('lets page-shell routes scroll the main pane on paper', () => {
    expect(mainContentClassName('/')).toContain('overflow-y-auto');
    expect(mainContentClassName('/')).toContain('workbench-main-surface');
    expect(mainContentClassName('/')).not.toContain('mesh-bg');
    expect(mainContentClassName('/')).not.toContain('tx-scope');
  });
});

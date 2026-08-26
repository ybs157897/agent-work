import { describe, expect, it } from 'vitest';
import { isFullBleedPath } from './route-layout';

describe('isFullBleedPath', () => {
  it.each(['/chat', '/models', '/agents'])('keeps %s inside the shared fixed-height shell', (path) => {
    expect(isFullBleedPath(path)).toBe(true);
  });

  it.each(['/', '/tasks', '/logs', '/settings', '/missing'])('lets %s use the main page scroller', (path) => {
    expect(isFullBleedPath(path)).toBe(false);
  });
});

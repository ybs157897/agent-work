import { beforeEach, describe, expect, it } from 'vitest';
import { useToastStore } from './toast.store';

describe('toast.store', () => {
  beforeEach(() => {
    useToastStore.setState({ toasts: [] });
  });

  it('push appends and generates incrementing IDs', () => {
    useToastStore.getState().push('info', 'first');
    useToastStore.getState().push('success', 'second');
    const toasts = useToastStore.getState().toasts;
    expect(toasts).toHaveLength(2);
    expect(toasts[0].message).toBe('first');
    expect(toasts[1].message).toBe('second');
    expect(toasts[1].id).toBe(toasts[0].id + 1);
  });

  it('push keeps only the last 5 when over 5', () => {
    for (let i = 0; i < 6; i++) {
      useToastStore.getState().push('info', `msg${i}`);
    }
    const toasts = useToastStore.getState().toasts;
    expect(toasts).toHaveLength(5);
    expect(toasts[0].message).toBe('msg1');
    expect(toasts[4].message).toBe('msg5');
  });

  it('dismiss removes by id', () => {
    useToastStore.getState().push('info', 'first');
    useToastStore.getState().push('info', 'second');
    const firstId = useToastStore.getState().toasts[0].id;
    useToastStore.getState().dismiss(firstId);
    expect(useToastStore.getState().toasts).toHaveLength(1);
    expect(useToastStore.getState().toasts[0].message).toBe('second');
  });

  it('dismiss on non-existent id is a no-op', () => {
    useToastStore.getState().push('info', 'first');
    useToastStore.getState().dismiss(99999);
    expect(useToastStore.getState().toasts).toHaveLength(1);
  });
});
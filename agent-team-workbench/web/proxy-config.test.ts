import { describe, expect, it } from 'vitest';
import { backendApiProxy } from './vite.config';

describe('Vite backend proxy', () => {
  it('keeps browser Host for HTTP and WebSocket API requests in dev and preview', () => {
    expect(backendApiProxy('http://127.0.0.1:8080')).toEqual({
      target: 'http://127.0.0.1:8080',
      changeOrigin: false,
      ws: true,
    });
  });
});

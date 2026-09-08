import react from '@vitejs/plugin-react';
import { loadEnv } from 'vite';
import { defineConfig } from 'vitest/config';
import { fileURLToPath, URL } from 'node:url';

/** Preserve the browser Host for the backend's same-origin WebSocket check. */
export function backendApiProxy(backend: string) {
  return { target: backend, changeOrigin: false, ws: true } as const;
}

// 开发代理：浏览器 → Vite → Go 控制平面，保持同源语义（协议文档 §5.1）。
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '');
  const backend = env.BACKEND_URL || 'http://localhost:8080';

  return {
    plugins: [react()],
    resolve: {
      alias: {
        '@': fileURLToPath(new URL('./src', import.meta.url)),
      },
    },
    server: {
      port: 5173,
      proxy: {
        '/api': backendApiProxy(backend),
        // LanguageGUI 演示支线：/languagegui-api/* → 本地模型代理（scripts/languagegui-proxy.mjs）。
        '/languagegui-api': {
          target: 'http://127.0.0.1:8790',
          changeOrigin: true,
          rewrite: (p) => p.replace(/^\/languagegui-api/, ''),
        },
      },
    },
    // 演示截图走 preview（无 HMR 长连接，无头浏览器能等到网络空闲）。
    preview: {
      proxy: {
        '/api': backendApiProxy(backend),
        '/languagegui-api': {
          target: 'http://127.0.0.1:8790',
          changeOrigin: true,
          rewrite: (p) => p.replace(/^\/languagegui-api/, ''),
        },
      },
    },
    test: {
      environment: 'node',
      include: ['src/**/*.test.ts', 'src/**/*.test.tsx', '*.test.ts'],
    },
  };
});

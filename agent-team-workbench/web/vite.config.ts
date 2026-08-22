import react from '@vitejs/plugin-react';
import { defineConfig } from 'vitest/config';

// 开发代理：浏览器 → Vite → Go 控制平面，保持同源语义（协议文档 §5.1）。
// 后端默认监听 :8080，可用 BACKEND_URL 覆盖。
const backend = process.env.BACKEND_URL || 'http://localhost:8080';

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': { target: backend, changeOrigin: true },
    },
  },
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts', 'src/**/*.test.tsx'],
  },
});

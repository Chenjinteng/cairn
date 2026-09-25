import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { resolve } from 'node:path';

// 开发态：Vite 提供前端并热更新，/api 反向代理到本地 cairn 服务。
// 生产态：`pnpm build` 产出 web/dist，由 cairn 服务同源托管（或
// cairn 通过 //go:embed 把 dist 直接塞进二进制，参见 server.Build）。
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': resolve(import.meta.dirname, 'src'),
    },
  },
  server: {
    port: 5273,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8787',
        changeOrigin: true,
      },
    },
  },
  build: {
    // 产物直接进 Go embed 目录，`go build -tags webui` 时打进二进制。
    outDir: resolve(import.meta.dirname, '../internal/webui/dist'),
    emptyOutDir: true,
  },
});
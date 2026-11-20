/// <reference types="vitest" />
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  // 界面打包进桌面程序、从本机加载，不需要为网络加载拆分代码块。
  build: { outDir: 'dist', emptyOutDir: true, chunkSizeWarningLimit: 4096 },
  test: { environment: 'jsdom', globals: true, setupFiles: ['src/test/setup.ts'] },
});

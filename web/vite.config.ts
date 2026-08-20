import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

// 产物 embed 进 Go 单二进制：输出到 ../internal/web/dist，相对路径引用
export default defineConfig({
  plugins: [vue()],
  base: './',
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  build: { outDir: '../internal/web/dist', emptyOutDir: true, assetsDir: 'assets', chunkSizeWarningLimit: 700 },
  server: { port: 5173, proxy: { '/api': 'http://127.0.0.1:8080' } }
})

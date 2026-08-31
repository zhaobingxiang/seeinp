import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 构建产物输出到 ../web/build，由后端 go:embed 内嵌
export default defineConfig({
  plugins: [vue()],
  base: './',
  build: {
    outDir: '../web/build',
    emptyOutDir: true
  },
  server: {
    port: 3009,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:65443'
      }
    }
  }
})
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  build: {
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (id.includes('/node_modules/@xterm/')) return 'terminal'
          if (id.includes('/node_modules/element-plus/') || id.includes('/node_modules/@element-plus/')) return 'element-plus'
        },
      },
    },
  },
})

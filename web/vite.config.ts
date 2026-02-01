import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  test: {
    include: ['src/**/*.test.ts'],
  },
  server: {
    port: 3000,
    host: '127.0.0.1',
    proxy: {
      '/api': 'http://localhost:9091',
      '/ws': {
        target: 'ws://localhost:9091',
        ws: true,
      },
    },
  },
})

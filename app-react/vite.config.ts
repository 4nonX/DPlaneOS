import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { resolve } from 'path'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': resolve(__dirname, './src'),
    },
  },
  server: {
    port: 3000,
    proxy: {
      // Proxy /api/* and /ws/* to the Go daemon during development
      '/api': {
        target: 'http://127.0.0.1:9000',
        changeOrigin: true,
      },
      '/ws': {
        target: 'ws://127.0.0.1:9000',
        ws: true,
        changeOrigin: true,
      },
      '/health': {
        target: 'http://127.0.0.1:9000',
        changeOrigin: true,
      },
    },
  },
  build: {
    // Output to app/ directory so the daemon can serve it as static files
    // matching the existing nginx config: root /opt/dplaneos/app
    outDir: '../app-built',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        // Chunk splitting for better caching
        manualChunks: {
          'tanstack-router': ['@tanstack/react-router'],
          'tanstack-query': ['@tanstack/react-query'],
          'zustand': ['zustand'],
        },
      },
    },
  },
})

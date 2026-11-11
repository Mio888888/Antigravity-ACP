import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'node:path'

const gatewayTarget = 'http://127.0.0.1:8080'

// The gateway rejects cross-origin admin writes and WebSocket upgrades; the dev
// server origin differs from the gateway host, so drop Origin on proxied requests.
const stripOrigin = {
  target: gatewayTarget,
  changeOrigin: true,
  configure: (proxy: import('vite').HttpProxy.Server) => {
    proxy.on('proxyReq', (req) => req.removeHeader('origin'))
    proxy.on('proxyReqWs', (req) => req.removeHeader('origin'))
  },
}

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    proxy: {
      '/api': { ...stripOrigin, ws: true },
      '/v1': { ...stripOrigin, ws: true },
      '/healthz': { target: gatewayTarget, changeOrigin: true },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 1000,
    rollupOptions: {
      output: {
        manualChunks: {
          'vendor-react': ['react', 'react-dom'],
          'vendor-icons': ['lucide-react'],
        },
      },
    },
  },
})

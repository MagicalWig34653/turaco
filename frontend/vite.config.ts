import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    // Keep the browser's Host header (changeOrigin: false): the API's
    // same-origin CSRF guard compares Origin with Host, like behind nginx.
    proxy: {
      '/api': { target: 'http://localhost:8080', changeOrigin: false },
      '/health': { target: 'http://localhost:8080', changeOrigin: false },
    },
  },
});

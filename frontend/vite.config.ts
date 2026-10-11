import { configDefaults, defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  // Playwright specs under e2e/ run with `make e2e`, not with vitest.
  test: { exclude: [...configDefaults.exclude, 'e2e/**'] },
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

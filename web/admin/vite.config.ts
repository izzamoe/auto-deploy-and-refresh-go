import { resolve } from 'node:path';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

export default defineConfig(async () => {
  const { default: tailwindcss } = await import('@tailwindcss/vite');

  return {
    plugins: [tailwindcss(), react()],
    resolve: {
      alias: {
        "@": resolve(__dirname, "./src"),
      },
    },
  base: '/admin/',
  root: resolve(__dirname),
  build: {
    // Output straight into the location the Go binary embeds
    // (internal/admin/admin_spa.go: //go:embed all:web/admin/dist). Building
    // into the repo-root web/admin/dist left the embedded copy stale.
    outDir: resolve(__dirname, '../../internal/admin/web/admin/dist'),
    assetsDir: 'assets',
    emptyOutDir: true,
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./setupTests.ts'],
    globals: true,
  },
  };
});

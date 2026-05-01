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
    outDir: 'dist',
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

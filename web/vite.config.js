import { defineConfig } from 'vite';

export default defineConfig({
  root: '.',
  base: '/app/',
  publicDir: 'public',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
});

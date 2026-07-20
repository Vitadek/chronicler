import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import path from 'path';
import {defineConfig} from 'vite';

export default defineConfig(() => {
  return {
    plugins: [react(), tailwindcss()],
    resolve: {
      alias: {
        '@': path.resolve(__dirname, '.'),
      },
    },
    build: {
      // Straight into ../web, which the Go binary embeds (`//go:embed web/*`
      // in embed.go). Building here IS the frontend deploy step — there is no
      // separate copy stage, which is precisely what let the built assets
      // drift from source back when they lived in different repos.
      //
      // emptyOutDir must be explicit because the directory sits outside vite's
      // root; without it vite refuses to clear it and stale content-hashed
      // chunks accumulate in the embedded output.
      outDir: path.resolve(__dirname, '../web'),
      emptyOutDir: true,
    },
    server: {
      // HMR can be disabled with DISABLE_HMR for constrained environments.
      hmr: process.env.DISABLE_HMR !== 'true',
      // Disable file watching when DISABLE_HMR is true to save CPU during agent edits.
      watch: process.env.DISABLE_HMR === 'true' ? null : {},
      // `npm run dev` serves the UI here while the Go binary serves the API.
      // Run `go run .` alongside it and both halves work together with hot
      // reload; without this proxy every API call 404s, because in production
      // Go serves the built assets itself and nothing sits in front of vite.
      proxy: {
        '/api': {target: 'http://localhost:3000', changeOrigin: true},
        '/collab': {target: 'ws://localhost:3000', ws: true},
        '/plugins-raw': {target: 'http://localhost:3000', changeOrigin: true},
      },
    },
  };
});

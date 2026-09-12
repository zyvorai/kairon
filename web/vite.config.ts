import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      // ws: true so `npm run dev` also tunnels the VNC console's
      // WebSocket upgrade (GET /api/v1/machines/.../console); the string
      // shorthand form doesn't enable that.
      '/api': { target: 'http://127.0.0.1:8082', ws: true },
      '/healthz': 'http://127.0.0.1:8082',
    },
  },
});

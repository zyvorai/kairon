// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Dev proxy target: the kairon-ui to talk to (default :18082; override with
// KAIRON_UI_URL=http://host:port npm run dev).
const uiURL = process.env.KAIRON_UI_URL || 'http://127.0.0.1:18082';

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      // ws: true so `npm run dev` also tunnels the VNC console's
      // WebSocket upgrade (GET /api/v1/machines/.../console); the string
      // shorthand form doesn't enable that.
      '/api': { target: uiURL, ws: true },
      '/healthz': uiURL,
    },
  },
});

import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// The console is embedded (internal/dashboard/console.go) and served by the bp
// daemon on the same loopback origin as the API, so assets are referenced with a
// relative base and there is no runtime Node server. `npm run build` emits the
// bundle into dist/, which is committed and go:embed-ed.
//
// During local UI development, `npm run dev` proxies /v1 to a bp daemon running
// `bp serve --api` on 127.0.0.1:8088 so the SPA can talk to a real backend.
export default defineConfig({
  base: "./",
  plugins: [react(), tailwindcss()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      "/v1": {
        target: "http://127.0.0.1:8088",
        changeOrigin: true,
      },
    },
  },
});

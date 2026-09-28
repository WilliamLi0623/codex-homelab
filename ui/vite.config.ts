import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath, URL } from "node:url";

export default defineConfig({
  plugins: [react()],
  server: {
    port: 4173,
    proxy: {
      "/ui/api": "http://127.0.0.1:8081",
      "/api": {
        target: "http://127.0.0.1:8765",
        configure(proxy) {
          proxy.on("proxyReq", (proxyRequest) => {
            proxyRequest.setHeader("Host", "127.0.0.1:8765");
            proxyRequest.setHeader("Origin", "http://127.0.0.1:8765");
          });
        },
      },
    },
  },
  build: {
    outDir: "dist",
    rolldownOptions: {
      input: {
        taskConsole: fileURLToPath(new URL("./index.html", import.meta.url)),
        codexSessions: fileURLToPath(new URL("./codex.html", import.meta.url)),
      },
    },
  },
  test: {
    environment: "node",
  },
});

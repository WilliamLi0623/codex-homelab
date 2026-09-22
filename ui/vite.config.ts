import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  server: {
    port: 4173,
    proxy: {
      "/ui/api": "http://127.0.0.1:8081",
    },
  },
  build: {
    outDir: "dist",
  },
  test: {
    environment: "node",
  },
});

/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The SPA talks to the BFF at /api. In dev, Vite proxies it so the browser
// stays same-origin (the BFF sets an httpOnly cookie that must not be cross-site).
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target: process.env.VITE_BFF_TARGET ?? "http://localhost:8088",
        changeOrigin: true,
      },
    },
  },
  test: {
    // Pure logic tests today (api client, formatters). Switch to "happy-dom"
    // when component tests are added.
    environment: "node",
    globals: true,
    coverage: {
      // lcov feeds SonarCloud (sonar-project.properties); text is for local runs.
      provider: "v8",
      reporter: ["lcov", "text"],
      include: ["src/**"],
    },
  },
});

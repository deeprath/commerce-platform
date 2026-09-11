/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The admin SPA talks to the BFF at /api (same-origin so the httpOnly auth
// cookie rides along). In dev, Vite proxies it to the local BFF.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5174,
    proxy: {
      "/api": {
        target: process.env.VITE_BFF_TARGET ?? "http://localhost:8088",
        changeOrigin: true,
      },
    },
  },
  test: {
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

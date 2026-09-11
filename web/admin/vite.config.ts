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
    // jsdom backs both the pure logic tests and the page/component tests
    // under src/pages. See web/storefront/vite.config.ts's comment: happy-dom
    // doesn't dispatch a form's submit event on a type="submit" button click,
    // which silently no-ops every form-submit test in an app like this one.
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/setupTests.ts"],
    coverage: {
      // lcov feeds SonarCloud (sonar-project.properties); text is for local runs.
      provider: "v8",
      reporter: ["lcov", "text"],
      include: ["src/**"],
    },
  },
});

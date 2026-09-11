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
    // jsdom backs both the pure logic tests and the page/component tests
    // under src/pages and src/components. happy-dom was tried first (it's
    // lighter) but doesn't dispatch a form's submit event when a type="submit"
    // button inside it is clicked, which silently no-ops every form-submit
    // test in this app (SellerProducts, Checkout, etc.) — jsdom does.
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

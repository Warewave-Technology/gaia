/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The build goes into the Go package that embeds it (internal/web).
// In development the API runs on :8080 (gaia server; GAIA_API to change it)
// and Vite proxies to it.
// The config runs in Node; its types are not part of the app's.
const env = (globalThis as { process?: { env: Record<string, string | undefined> } }).process?.env ?? {};

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "../internal/web/dist",
    emptyOutDir: true,
    assetsDir: "assets",
    sourcemap: false,
  },
  server: {
    // The API and the sign-in pages the server renders itself.
    proxy: Object.fromEntries(
      ["/api", "/auth", "/login", "/enroll", "/device", "/cli-login", "/approve"].map((p) => [p, { target: env.GAIA_API ?? "http://127.0.0.1:8080" }]),
    ),
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test-setup.ts"],
  },
});

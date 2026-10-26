import { resolve } from "node:path"
import tailwindcss from "@tailwindcss/vite"
import react from "@vitejs/plugin-react"
import { tanstackRouter } from "@tanstack/router-plugin/vite"
import { defineConfig } from "vitest/config"

// In development the dashboard runs on Vite and the API on Forge. Vite
// proxies the API, so the browser sees one origin, the session cookie stays
// first-party, and Loomplane needs no CORS. In production Forge serves the
// built dashboard itself (extensions/web).
const api = process.env.LOOMPLANE_API_URL ?? "http://localhost:8780"

export default defineConfig({
  plugins: [
    tanstackRouter({
      target: "react",
      autoCodeSplitting: true,
      quoteStyle: "double",
      semicolons: false,
    }),
    react(),
    tailwindcss(),
  ],
  resolve: {
    alias: {
      "@": resolve(import.meta.dirname, "./src"),
    },
  },
  optimizeDeps: {
    // The generated realtime client falls back to require("ws") outside a
    // browser. The browser never reaches it, but the dependency scan still
    // tries to resolve it and gives up on pre-bundling when it can't.
    exclude: ["ws"],
  },
  server: {
    port: 5173,
    proxy: {
      // Each prefix ends in a slash, so a page such as /api-keys stays
      // with the dashboard.
      "/api/": { target: api, ws: true },
      "/authsome/": api,
      "/openapi.json": api,
      "/asyncapi.json": api,
      "/_/": api,
      // A catalog service's admin, through Loomplane's sandboxed proxy.
      "/catalog-admin/": { target: api, ws: true },
    },
  },
  build: {
    sourcemap: true,
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test-setup.ts"],
    css: false,
    // Bound jsdom workers so local Go builds don't starve UI interactions.
    maxWorkers: 4,
  },
})

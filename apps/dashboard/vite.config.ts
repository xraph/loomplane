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
  server: {
    port: 5173,
    proxy: {
      "/api": api,
      "/authsome": api,
      "/openapi.json": api,
      "/_": api,
    },
  },
  build: {
    sourcemap: true,
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test-setup.ts"],
    css: false,
  },
})

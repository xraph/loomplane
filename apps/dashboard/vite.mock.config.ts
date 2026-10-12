// Serves the dashboard mock (mock/index.html, src/mock) on its own: no API
// proxy, no router plugin, and the mock at the root URL instead of /mock/.
//
//   vite --config vite.mock.config.ts    then open http://localhost:4780
import { resolve } from "node:path"
import tailwindcss from "@tailwindcss/vite"
import react from "@vitejs/plugin-react"
import { defineConfig, type Plugin } from "vite"

const port = 4780

// The mock routes on the URL hash, so the only path it needs is "/".
const mockAtRoot: Plugin = {
  name: "loomplane-mock-at-root",
  configureServer(server) {
    server.middlewares.use((req, _res, next) => {
      if (req.url === "/" || req.url === "/index.html") {
        req.url = "/mock/index.html"
      }
      next()
    })
  },
}

export default defineConfig({
  // Its own dependency cache, so running it next to `make dev` doesn't
  // re-optimize the real dashboard's dependencies out from under it.
  cacheDir: "node_modules/.vite-mock",
  plugins: [react(), tailwindcss(), mockAtRoot],
  resolve: {
    alias: {
      "@": resolve(import.meta.dirname, "./src"),
    },
  },
  server: {
    port,
    strictPort: true,
  },
})

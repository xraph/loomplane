import { defineConfig } from "vite";
import rsc from "@vitejs/plugin-rsc";

export default defineConfig({
  plugins: [
    rsc({
      entries: {
        rsc: "./src/rsc.jsx",
        ssr: "./src/ssr.jsx",
        client: "./src/browser.jsx",
      },
    }),
  ],
});

import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite";

// The Go server serves the built assets from RAVN_STATIC_DIR (default
// ./web/dist), so the build must land in ./dist with an absolute base.
export default defineConfig({
  base: "/",
  plugins: [tailwindcss()],
  server: {
    // Dev only: proxy API calls to a locally running ravn-server.
    proxy: {
      "/api": { target: "http://localhost:8080", changeOrigin: true },
    },
  },
  build: {
    outDir: "dist",
  },
});

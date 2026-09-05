import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { "@": path.resolve(import.meta.dirname, "./src") },
  },
  server: {
    port: 5173,
    // In development the API is proxied so the browser still sees one origin,
    // matching what nginx does in the container. Without this the session
    // cookie would be cross-site and the dev experience would differ from
    // production in exactly the way that hides cookie bugs.
    proxy: {
      "/api": {
        target: process.env.VITE_API_TARGET ?? "http://localhost:8080",
        changeOrigin: false,
      },
    },
  },
});

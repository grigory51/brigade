import path from "node:path";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// Сборка фронтенда складывается в web/dist, откуда Makefile копирует её в
// backend/internal/web/dist для встраивания в бинарь через go:embed.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    // Алиас @ → src используется shadcn-компонентами и прикладным кодом.
    alias: {
      "@": path.resolve(__dirname, "src"),
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    // В dev API-запросы и WS проксируются на локальный бэкенд.
    proxy: {
      "/brigade.v1.AuthService": "http://localhost:8080",
      "/brigade.v1.SessionService": "http://localhost:8080",
      "/brigade.v1.AgentService": "http://localhost:8080",
      "/ws": {
        target: "ws://localhost:8080",
        ws: true,
      },
    },
  },
});

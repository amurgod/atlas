import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  server: {
    host: true,
    port: 5173,
    proxy: {
      "/api/user": { target: "http://127.0.0.1:8081", rewrite: (p) => p.replace(/^\/api\/user/, "") },
      "/api/knowledge": { target: "http://127.0.0.1:8082", rewrite: (p) => p.replace(/^\/api\/knowledge/, "") },
      "/api/planner": { target: "http://127.0.0.1:8083", rewrite: (p) => p.replace(/^\/api\/planner/, "") },
      "/api/notify": { target: "http://127.0.0.1:8084", rewrite: (p) => p.replace(/^\/api\/notify/, "") },
    },
  },
});

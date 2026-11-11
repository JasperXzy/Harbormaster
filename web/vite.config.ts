import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";

// 三种模式：
//   development（npm run dev）      —— 模拟数据，不需要后端
//   api（npm run dev:api）          —— 把 /api 代理到本机的 hm-server
//   production（npm run build）     —— 同源 API，产物可独立由静态服务器托管
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, fileURLToPath(new URL(".", import.meta.url)), "");
  const target = env.HM_SERVER_URL || "http://127.0.0.1:8080";
  return {
    plugins: [react()],
    build: { outDir: "dist", emptyOutDir: true, sourcemap: false },
    server: {
      host: "127.0.0.1",
      port: 5173,
      strictPort: true,
      proxy: mode === "api" ? { "/api": { target, changeOrigin: false } } : undefined,
    },
  };
});

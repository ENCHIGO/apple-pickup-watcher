import path from "node:path";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// Tauri 在开发时用固定端口，端口被占用必须直接失败而不是自动换一个 ——
// 换了端口 Rust 那侧配置的 devUrl 就对不上，窗口会一片空白，而且报错信息
// 完全指不到真实原因。
const host = process.env.TAURI_DEV_HOST;

export default defineConfig(async () => ({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { "@": path.resolve(__dirname, "./src") },
  },
  // Tauri 期望一个固定且可预测的开发服务器。
  clearScreen: false,
  server: {
    port: 1420,
    strictPort: true,
    host: host || false,
    hmr: host ? { protocol: "ws", host, port: 1421 } : undefined,
    watch: {
      // Rust 侧的改动由 cargo 自己监听，让 Vite 也去盯只会白白重启。
      ignored: ["**/src-tauri/**", "**/crates/**", "**/target/**"],
    },
  },
}));

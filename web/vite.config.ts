import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// 构建产物写进 internal/server/static,由 Go 程序嵌入并在同一端口提供(决策记录 0010)。
// 文件名固定为 index.html、app.js、style.css:后台只提供这三个路径,
// 而且产物提交进仓库,固定的名字让每次改动的差异只落在这三个文件上。
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "../internal/server/static",
    emptyOutDir: true,
    cssCodeSplit: false,
    modulePreload: false,
    rolldownOptions: {
      output: {
        entryFileNames: "app.js",
        assetFileNames: (info) => ((info.names?.[0] ?? "").endsWith(".css") ? "style.css" : "[name][extname]"),
      },
    },
  },
  // 开发时页面由 Vite 提供,接口转给本机正在运行的后台。
  server: {
    proxy: { "/api": "http://127.0.0.1:7377" },
  },
});

import { defineConfig } from "@playwright/test";

// 浏览器测试:对真实的 bugu 后台(临时数据目录、随机端口)走一遍网页前端的主要流程。
// 页面用的是程序嵌入的那份构建产物,所以先运行 make web 再测;make check 会按顺序做。
export default defineConfig({
  testDir: "e2e",
  testMatch: "*.e2e.ts",
  globalSetup: "./e2e/setup.ts",
  // 所有测试共用一个后台,串行运行,避免任务和运行记录互相干扰。
  workers: 1,
  forbidOnly: !!process.env.CI,
  reporter: process.env.CI ? "list" : "line",
  use: {
    browserName: "chromium",
    // 断言里的文字是中文原文。界面跟随浏览器语言时就是中文;英文另有一条测试。
    locale: "zh-CN",
    trace: "retain-on-failure",
  },
  outputDir: "e2e/.results",
});

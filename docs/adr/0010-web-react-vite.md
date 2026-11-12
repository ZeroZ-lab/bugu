# 0010 网页前端改用 React 和 Vite 构建,产物提交进仓库

日期:2026-11-12

## 背景

M2 的前端是手写的 HTML、CSS 和原生 JS,没有构建步骤。三个页面时够用。接下来要做收件箱、Markdown 渲染、运行器管理和更复杂的任务表单,手写 DOM 的状态同步越来越容易出错,也没有类型检查。

## 决定

1. 前端源码放在 `web/`,用 React、TypeScript 编写,Vite 构建,Vitest 测试。
2. 构建产物写进 `internal/server/static/`,文件名固定为 `index.html`、`app.js`、`style.css`,并**提交进仓库**。后台照旧在编译时嵌入它们、在同一端口提供。
3. `make check` 增加 `web-check`:类型检查、前端测试,并重新构建一遍,确认已提交的产物与源码一致。
4. 前端仍然只通过 JSON 接口工作([ADR 0007](0007-static-pages-without-token.md) 不变)。
5. 第三方前端依赖只放在 `web/package.json`,新增依赖仍按 AGENTS.md 先问人。

## 理由

- React 的生态最大,收件箱、Markdown、表单这类需求都有成熟方案;协作者和 AI agent 对它最熟。
- 提交产物后,只改 Go 代码的人、Linux 服务器上的构建、`go install` 都不需要 Node;程序仍是一个 `go build` 出来的文件。
- 固定文件名让后台的路由不用改,也让每次前端改动在提交里只落在三个产物文件上。
- 产物和源码可能不同步,由 `web-check` 在合并前拦下。

## 否决的方案

- 产物不提交、构建时生成:任何 `go build`、`make check`、服务器构建都要先装 Node。
- Preact 加 htm、不引入构建:省掉 Node,但没有类型检查,复杂表单和收件箱仍要手工管理状态。
- Svelte、Vue:各有优点,但生态和熟悉程度不如 React,这里没有非它们不可的理由。

## 后果

- 改前端和运行 `make check` 需要 Node 和 npm。
- 仓库里多了 `web/package-lock.json` 和压缩后的产物文件;产物在代码审查时不必逐行看。
- 产物约 240KB(压缩传输约 75KB),比手写版大,但只从本机或局域网加载。

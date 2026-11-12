// 布谷的网页前端。只通过后台的 JSON 接口工作,应用窗口和浏览器里是同一份。
// 页面里所有来自后台的内容都用 textContent 写入,不拼接 HTML。
"use strict";

const API = "/api/v1";
const TOKEN_KEY = "bugu.token";
const REFRESH_MS = 5000;
const $ = (id) => document.getElementById(id);

// ---- 令牌 ----
// 桌面应用把令牌放在网址片段里交给页面(决策记录 0007)。读到后立即从地址栏移除,
// 只保存在当前标签页的会话存储里。
function takeTokenFromHash() {
  const params = new URLSearchParams(location.hash.slice(1));
  const token = params.get("token");
  if (!token) return;
  sessionStorage.setItem(TOKEN_KEY, token);
  params.delete("token");
  const rest = params.toString();
  history.replaceState(null, "", location.pathname + (rest ? "#" + rest : ""));
}

class ApiError extends Error {
  constructor(code, message) {
    super(message);
    this.code = code;
  }
}

async function api(method, path, body) {
  const init = { method, headers: { Authorization: "Bearer " + (sessionStorage.getItem(TOKEN_KEY) || "") } };
  if (body !== undefined) {
    init.headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }
  let resp;
  try {
    resp = await fetch(API + path, init);
  } catch (err) {
    throw new ApiError("offline", "连不上后台,它可能已经停止。");
  }
  let data = null;
  try {
    data = await resp.json();
  } catch (err) {
    // 不是 JSON 的响应按状态码处理
  }
  if (!resp.ok) {
    const e = data && data.error;
    throw new ApiError(e ? e.code : "http_" + resp.status, e ? e.message : "后台返回 " + resp.status);
  }
  return data;
}

// ---- 小工具 ----
function h(tag, props, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (k === "class") el.className = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v);
  }
  for (const c of children) el.append(c);
  return el;
}

function show(el, visible) {
  el.hidden = !visible;
}

function setError(el, message) {
  el.textContent = message || "";
  show(el, Boolean(message));
}

const pad = (n) => String(n).padStart(2, "0");

function formatTime(iso) {
  if (!iso) return "-";
  const d = new Date(iso);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// 把 Go 的时长字符串("1h30m0s")换算成分钟,用于表单回填。
function durationToMinutes(s) {
  if (!s) return "";
  let seconds = 0;
  for (const [, n, unit] of s.matchAll(/([0-9.]+)(h|m|s)/g)) {
    seconds += Number(n) * { h: 3600, m: 60, s: 1 }[unit];
  }
  return seconds ? String(Math.round(seconds / 60)) : "";
}

// datetime-local 控件要的是不带时区的本地时间。
function toLocalInput(iso) {
  const d = new Date(iso);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

const STATUS = {
  queued: ["排队中", ""],
  running: ["运行中", "warn"],
  succeeded: ["成功", "ok"],
  failed: ["失败", "bad"],
  timed_out: ["超时", "bad"],
  skipped: ["已跳过", ""],
  missed: ["已错过", ""],
  canceled: ["已取消", ""],
  interrupted: ["中断", "bad"],
};

function badge(text, kind) {
  return h("span", { class: "badge " + (kind || "") }, text);
}

function describeTrigger(tr) {
  return tr.cron ? tr.cron : "一次 " + formatTime(tr.at);
}

// ---- 视图切换 ----
const state = { view: "tasks", tasks: [], editing: null, runId: null, runDone: true, rendered: {} };

// changed 报告某个列表的数据和上次渲染时相比有没有变。没变就不重建表格,
// 这样定期刷新不会打断正在进行的点击和键盘操作。
function changed(key, data) {
  const snapshot = JSON.stringify(data);
  if (state.rendered[key] === snapshot) return false;
  state.rendered[key] = snapshot;
  return true;
}

function showView(view) {
  state.view = view;
  for (const id of ["login", "tasks", "runs"]) show($(id), id === view);
  show($("nav"), view !== "login");
  for (const tab of document.querySelectorAll(".tab")) tab.classList.toggle("active", tab.dataset.view === view);
  setError($("page-error"), "");
  refresh();
}

// refresh 重新读取当前视图的数据。令牌失效时回到连接页。
async function refresh() {
  if (state.view === "login") return;
  try {
    if (state.view === "tasks") await loadTasks();
    else await loadRuns();
    setError($("page-error"), "");
    $("conn").textContent = "已连接 " + location.host;
  } catch (err) {
    if (err.code === "unauthorized") {
      sessionStorage.removeItem(TOKEN_KEY);
      showView("login");
      setError($("login-error"), "令牌无效,请重新填写。");
      return;
    }
    $("conn").textContent = "";
    setError($("page-error"), err.message);
  }
}

// ---- 任务列表 ----
async function loadTasks() {
  state.tasks = await api("GET", "/tasks");
  if (!changed("tasks", state.tasks)) return;
  $("tasks-table").tBodies[0].replaceChildren(...state.tasks.map(taskRow));
  show($("tasks-table"), state.tasks.length > 0);
  show($("tasks-empty"), state.tasks.length === 0);
}

function taskRow(t) {
  const status = t.running ? badge("运行中", "warn") : t.paused ? badge("已暂停", "") : badge("已启用", "ok");
  const act = (label, fn, cls) => h("button", { type: "button", class: cls || "", onclick: () => run(fn) }, label);
  return h("tr", {},
    h("td", {}, t.name),
    h("td", {}, h("code", {}, describeTrigger(t.trigger))),
    h("td", {}, status),
    h("td", {}, formatTime(t.next_run)),
    h("td", { class: "ops" },
      act("立即运行", async () => {
        const r = await api("POST", `/tasks/${encodeURIComponent(t.name)}/run`);
        showView("runs");
        openRun(r.id);
      }),
      act(t.paused ? "恢复" : "暂停", () => api("POST", `/tasks/${encodeURIComponent(t.name)}/${t.paused ? "resume" : "pause"}`)),
      act("编辑", () => openTaskDialog(t)),
      act("删除", async () => {
        if (confirm(`删除任务 ${t.name}?运行记录会保留。`)) await api("DELETE", `/tasks/${encodeURIComponent(t.name)}`);
      }, "danger")));
}

// run 执行一个操作,出错时把后台的说明显示出来,然后刷新列表。
async function run(fn) {
  try {
    await fn();
    await refresh();
  } catch (err) {
    setError($("page-error"), err.message);
  }
}

// ---- 新建和修改任务 ----
const form = $("task-form");

function syncForm() {
  const trigger = form.elements.trigger.value;
  const kind = form.elements.kind.value;
  for (const el of form.querySelectorAll("[data-show]")) {
    show(el, el.dataset.show === trigger || el.dataset.show === kind);
  }
}

function openTaskDialog(t) {
  state.editing = t || null;
  form.reset();
  setError($("task-error"), "");
  $("task-dialog-title").textContent = t ? "修改任务" : "新建任务";
  const f = form.elements;
  f.name.disabled = Boolean(t);
  if (t) {
    f.name.value = t.name;
    f.trigger.value = t.trigger.cron ? "cron" : "at";
    f.cron.value = t.trigger.cron || "";
    f.preset.value = [...f.preset.options].some((o) => o.value === t.trigger.cron) ? t.trigger.cron : "";
    if (t.trigger.at) f.at.value = toLocalInput(t.trigger.at);
    const script = (t.steps[0] && t.steps[0].script) || {};
    f.kind.value = script.path ? "path" : "command";
    f.command.value = script.command || "";
    f.path.value = script.path || "";
    f.cwd.value = t.cwd || "";
    f.timeout.value = durationToMinutes(t.timeout);
    const system = (t.notifications || []).find((n) => n.channel === "system");
    f.notify.value = system ? system.when || "on_content" : "";
    f.env.value = Object.entries(t.env || {}).map(([k, v]) => `${k}=${v}`).join("\n");
  } else {
    f.cron.value = f.preset.value;
  }
  syncForm();
  $("task-dialog").showModal();
}

// readTask 把表单内容整理成任务定义。修改时以原定义为底,表单没有覆盖的字段保持不变。
function readTask() {
  const f = form.elements;
  const t = state.editing ? structuredClone(state.editing) : { name: f.name.value.trim() };
  delete t.next_run;
  delete t.running;

  if (f.trigger.value === "cron") {
    t.trigger = { cron: f.cron.value.trim() };
    if (!t.trigger.cron) throw new Error("请填写 cron 表达式。");
  } else {
    if (!f.at.value) throw new Error("请选择运行时间。");
    t.trigger = { at: new Date(f.at.value).toISOString() };
  }

  const script = f.kind.value === "command" ? { command: f.command.value.trim() } : { path: f.path.value.trim() };
  if (!script.command && !script.path) throw new Error(f.kind.value === "command" ? "请填写要运行的命令。" : "请填写脚本文件的路径。");
  t.steps = [{ type: "script", script }];

  t.cwd = f.cwd.value.trim() || undefined;
  t.timeout = Number(f.timeout.value) > 0 ? `${Math.round(Number(f.timeout.value))}m` : undefined;

  const others = (t.notifications || []).filter((n) => n.channel !== "system");
  t.notifications = f.notify.value ? [...others, { channel: "system", when: f.notify.value }] : others;
  if (t.notifications.length === 0) delete t.notifications;

  const env = {};
  for (const line of f.env.value.split("\n")) {
    if (!line.trim()) continue;
    const i = line.indexOf("=");
    if (i <= 0) throw new Error(`环境变量应写成 名=值,这一行不是:${line}`);
    env[line.slice(0, i).trim()] = line.slice(i + 1);
  }
  t.env = Object.keys(env).length ? env : undefined;
  return t;
}

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  try {
    const t = readTask();
    if (state.editing) await api("PUT", `/tasks/${encodeURIComponent(t.name)}`, t);
    else await api("POST", "/tasks", t);
    $("task-dialog").close();
    await refresh();
  } catch (err) {
    setError($("task-error"), err.message);
  }
});
form.addEventListener("change", (event) => {
  if (event.target.name === "preset" && event.target.value) form.elements.cron.value = event.target.value;
  syncForm();
});
form.elements.cron.addEventListener("input", () => {
  const f = form.elements;
  f.preset.value = [...f.preset.options].some((o) => o.value === f.cron.value.trim()) ? f.cron.value.trim() : "";
});
$("task-cancel").addEventListener("click", () => $("task-dialog").close());
$("new-task").addEventListener("click", () => openTaskDialog(null));

// ---- 运行记录 ----
async function loadRuns() {
  const filter = $("runs-filter");
  const tasks = await api("GET", "/tasks");
  if (changed("filter", tasks.map((t) => t.name))) {
    const selected = filter.value;
    filter.replaceChildren(h("option", { value: "" }, "全部"), ...tasks.map((t) => h("option", { value: t.name }, t.name)));
    filter.value = [...filter.options].some((o) => o.value === selected) ? selected : "";
  }

  const query = new URLSearchParams({ limit: "100" });
  if (filter.value) query.set("task", filter.value);
  const runs = await api("GET", "/runs?" + query);
  if (!changed("runs", runs)) return;
  $("runs-table").tBodies[0].replaceChildren(...runs.map(runRow));
  show($("runs-table"), runs.length > 0);
  show($("runs-empty"), runs.length === 0);
}

function runRow(r) {
  const [label, kind] = STATUS[r.status] || [r.status, ""];
  const open = () => openRun(r.id);
  return h("tr", { tabindex: "0", onclick: open, onkeydown: (e) => e.key === "Enter" && open() },
    h("td", {}, formatTime(r.started_at || r.created_at)),
    h("td", {}, r.task),
    h("td", {}, badge(label, kind)),
    h("td", {}, r.exit_code === undefined ? "-" : String(r.exit_code)),
    h("td", {}, r.trigger === "manual" ? "手动" : "定时"),
    h("td", { class: "wrap" }, r.note || (r.status === "failed" ? "" : r.error || "")));
}

async function openRun(id) {
  try {
    const [r, logs] = await Promise.all([api("GET", `/runs/${encodeURIComponent(id)}`), api("GET", `/runs/${encodeURIComponent(id)}/logs`)]);
    const [label] = STATUS[r.status] || [r.status];
    state.runId = id;
    state.runDone = r.status !== "queued" && r.status !== "running";
    $("run-title").textContent = `${r.task} · ${label}`;
    const rows = [
      ["运行编号", r.id],
      ["开始", formatTime(r.started_at || r.created_at)],
      ["结束", formatTime(r.finished_at)],
      ["退出码", r.exit_code === undefined ? "-" : String(r.exit_code)],
      ["命令", (r.command || []).join(" ") || "-"],
    ];
    if (r.scheduled_for) rows.push(["原定时间", formatTime(r.scheduled_for)]);
    if (r.note) rows.push(["说明", r.note]);
    if (r.error) rows.push(["原因", r.error]);
    $("run-meta").replaceChildren(...rows.flatMap(([k, v]) => [h("dt", {}, k), h("dd", {}, v)]));
    $("run-stdout").textContent = logs.stdout;
    $("run-stderr").textContent = logs.stderr;
    if (!$("run-dialog").open) $("run-dialog").showModal();
  } catch (err) {
    setError($("page-error"), err.message);
  }
}
$("run-close").addEventListener("click", () => $("run-dialog").close());
$("runs-filter").addEventListener("change", refresh);

// ---- 连接 ----
$("login-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  sessionStorage.setItem(TOKEN_KEY, $("login-token").value.trim());
  try {
    await api("GET", "/health");
    $("login-token").value = "";
    setError($("login-error"), "");
    showView("tasks");
  } catch (err) {
    sessionStorage.removeItem(TOKEN_KEY);
    setError($("login-error"), err.code === "unauthorized" ? "令牌无效。" : err.message);
  }
});

for (const tab of document.querySelectorAll(".tab")) tab.addEventListener("click", () => showView(tab.dataset.view));

// 桌面应用点通知时会带上 #run=<编号>,打开对应的结果。
function openRunFromHash() {
  const id = new URLSearchParams(location.hash.slice(1)).get("run");
  if (!id) return;
  history.replaceState(null, "", location.pathname);
  showView("runs");
  openRun(id);
}
window.addEventListener("hashchange", () => {
  takeTokenFromHash();
  if (!sessionStorage.getItem(TOKEN_KEY)) return;
  if (state.view === "login") showView("tasks");
  openRunFromHash();
});

takeTokenFromHash();
showView(sessionStorage.getItem(TOKEN_KEY) ? "tasks" : "login");
if (sessionStorage.getItem(TOKEN_KEY)) openRunFromHash();
// 页面可见且没有打开对话框时定期刷新,让状态和下次运行时间保持最新。
// 打开着的运行详情如果还没结束,也跟着更新。
setInterval(() => {
  if (document.hidden || $("task-dialog").open) return;
  refresh();
  if ($("run-dialog").open && !state.runDone) openRun(state.runId);
}, REFRESH_MS);

// 后台 JSON 接口的客户端和数据类型。前端只通过这些接口工作,应用窗口和浏览器里是同一份。
import { lang, t } from "./i18n";

const API = "/api/v1";
const TOKEN_KEY = "bugu.token";

export interface Trigger {
  cron?: string;
  at?: string;
  jitter?: string; // 随机偏移的范围,如 "10m"(S12)
}

export interface ScriptStep {
  command?: string;
  path?: string;
}

export interface AgentStep {
  runner: string;
  prompt: string;
  permission?: string;
}

export interface Step {
  name?: string;
  type: "script" | "agent" | "text";
  script?: ScriptStep;
  agent?: AgentStep;
  text?: string;
  timeout?: string;
}

export interface Notification {
  channel: string;
  when?: string;
  url?: string;
}

// Task 是任务定义。修改时以后台返回的原定义为底,前端不认识的字段原样带回。
export interface Task {
  name: string;
  trigger: Trigger;
  steps: Step[];
  cwd?: string;
  timeout?: string;
  env?: Record<string, string>;
  notifications?: Notification[];
  silent_if?: string;
  timezone?: string;
  on_missed?: string;
  missed_window?: string;
  env_file?: string;
  output?: { dir: string; name: string; from?: string };
  paused?: boolean;
  running?: boolean;
  next_run?: string;
  last_run?: Run; // 最近一次已经结束的运行,由后台填写
  [field: string]: unknown;
}

export interface Runner {
  name: string;
  title?: string;
  agent?: boolean;
  permissions?: string[];
}

export interface Run {
  id: string;
  task: string;
  status: string;
  trigger?: string;
  created_at?: string;
  started_at?: string;
  finished_at?: string;
  scheduled_for?: string;
  exit_code?: number;
  command?: string[];
  permission?: string;
  note?: string;
  error?: string;
  no_content?: boolean;
  summary?: string;
  artifacts?: string[];
  read_at?: string;
  steps?: StepRun[];
}

// Delivery 是一条通知的投递记录(N7)。
export interface Delivery {
  index: number;
  channel: string;
  target?: string;
  status: "pending" | "delivered" | "failed" | "dropped";
  attempts: number;
  error?: string;
  next_attempt?: string;
}

// Inbox 是收件箱的内容和计数(G3)。
export interface Inbox {
  unread: number;
  unread_failed: number;
  running: number;
  runs: Run[];
}

// StepRun 是一个步骤在一次运行里的结果,与任务的步骤一一对应。
export interface StepRun {
  name?: string;
  type: string;
  status: string;
  error?: string;
  exit_code?: number;
  nothing_to_do?: boolean;
}

export interface Output {
  stdout: string;
  stderr: string;
}

// Logs 是整次运行的输出,steps 是每个步骤各自的输出。
export interface Logs extends Output {
  steps?: Output[];
}

export class ApiError extends Error {
  constructor(
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

// ---- 令牌 ----
// 只保存在当前标签页的会话存储里(决策记录 0007)。

export const getToken = () => sessionStorage.getItem(TOKEN_KEY) ?? "";
export const setToken = (token: string) => sessionStorage.setItem(TOKEN_KEY, token);
export const clearToken = () => sessionStorage.removeItem(TOKEN_KEY);

// takeTokenFromHash 读取桌面应用放在网址片段里的令牌,读到后立即从地址栏移除。
export function takeTokenFromHash() {
  const params = new URLSearchParams(location.hash.slice(1));
  const token = params.get("token");
  if (!token) return;
  setToken(token);
  params.delete("token");
  const rest = params.toString();
  history.replaceState(null, "", location.pathname + (rest ? "#" + rest : ""));
}

// ---- 另一个 Bugu(G5)----
// 从本机打开另一台主机的面板时,网址片段里带着 home=<本机面板的地址>,那边的页面据此显示"返回本机"。
// 只接受本机回环地址,别人发来的链接不能借此把人引到任意网站。
const HOME_KEY = "bugu.home";
const NAME_KEY = "bugu.host-name"; // 从本机打开时,这台主机在本机登记的名字
const NAMES_KEY = "bugu.host-names"; // 以及本机登记的全部主机的名字(不含令牌),供切换用

export function takeHomeFromHash() {
  const params = new URLSearchParams(location.hash.slice(1));
  const home = params.get("home");
  if (!home) return;
  try {
    const u = new URL(home);
    if ((u.protocol === "http:" || u.protocol === "https:") && ["127.0.0.1", "localhost", "[::1]"].includes(u.hostname) && u.origin !== location.origin) {
      sessionStorage.setItem(HOME_KEY, u.origin);
    }
  } catch {
    // 不是网址,忽略
  }
  const name = params.get("name");
  const names = params.get("hosts");
  if (sessionStorage.getItem(HOME_KEY)) {
    if (name) sessionStorage.setItem(NAME_KEY, name.slice(0, 64));
    if (names !== null) sessionStorage.setItem(NAMES_KEY, names.slice(0, 4096));
  }
  params.delete("home");
  params.delete("name");
  params.delete("hosts");
  const rest = params.toString();
  history.replaceState(null, "", location.pathname + (rest ? "#" + rest : ""));
}

export const getHome = () => sessionStorage.getItem(HOME_KEY) ?? "";

// remoteName 是这台主机在本机登记的名字;不是从本机打开的,为空。
export const remoteName = () => sessionStorage.getItem(NAME_KEY) ?? "";

// remoteNames 是本机登记的全部主机的名字;不是从本机打开的,为空。
export const remoteNames = () => (sessionStorage.getItem(NAMES_KEY) ?? "").split(",").filter(Boolean);

// takeOpenFromHash 读取 #open=<主机名>:在另一台主机的面板里选了别的主机,先回到本机,由本机打开它,
// 令牌不离开本机。读到后从地址栏移除。
export function takeOpenFromHash(): string | null {
  const params = new URLSearchParams(location.hash.slice(1));
  const name = params.get("open");
  if (!name) return null;
  params.delete("open");
  const rest = params.toString();
  history.replaceState(null, "", location.pathname + (rest ? "#" + rest : ""));
  return name;
}

// isRemote 报告这个页面管理的是不是"另一台主机":从本机打开的,或者不在本机回环地址上的。
// 这时界面要明确标出主机,免得把操作做到了别的机器上(G5)。
export const isRemote = () => getHome() !== "" || !["127.0.0.1", "localhost", "[::1]"].includes(location.hostname);

// openHost 在当前窗口里打开另一台主机的面板。令牌放在网址片段里,不发给任何服务器(决策记录 0007)。
// 一并带上本机面板的地址和全部主机的名字(不含令牌),那边据此显示"返回本机"和切换主机的下拉框。
export function openHost(h: Host, names: string[]) {
  const fragment = new URLSearchParams({ token: h.token ?? "", home: location.origin, name: h.name, hosts: names.join(",") });
  location.href = h.url + "/#" + fragment.toString();
}

// Host 是登记过的另一台主机及其连接状态(G5、G14)。
export interface Host {
  name: string;
  url: string;
  token?: string;
  status?: "online" | "offline" | "unauthorized";
  version?: string;
  plain_public?: boolean;
}

// takeRunFromHash 读取桌面应用点通知时带上的 #run=<编号>,读到后从地址栏移除。
export function takeRunFromHash(): string | null {
  const id = new URLSearchParams(location.hash.slice(1)).get("run");
  if (id) history.replaceState(null, "", location.pathname);
  return id;
}

// ---- 页面地址 ----
// 新建和修改任务是单独的页面:#/new、#/edit/<任务名>。用 # 后紧跟 / 的写法,和 #token=、#open=、#run= 这些
// 参数片段区分开。打开表单时压入一条历史记录,浏览器后退即回到任务列表。

export type Route = { page: "new" } | { page: "edit"; name: string } | null;

export function currentRoute(): Route {
  const h = location.hash;
  if (h === "#/new") return { page: "new" };
  if (h.startsWith("#/edit/")) {
    try {
      const name = decodeURIComponent(h.slice("#/edit/".length));
      if (name) return { page: "edit", name };
    } catch {
      // 编码不对,当作没有
    }
  }
  return null;
}

const ROUTE_MARK = "bugu.route"; // 历史记录里由这个页面压入的表单页带着这个标记

// goRoute 打开表单页,压入一条历史记录。
export function goRoute(r: Exclude<Route, null>) {
  const hash = r.page === "new" ? "#/new" : "#/edit/" + encodeURIComponent(r.name);
  history.pushState({ [ROUTE_MARK]: true }, "", location.pathname + hash);
}

// leaveRoute 离开表单页回到列表。表单页是从列表打开的就后退一步,和浏览器的后退一样;
// 是直接打开的地址就把它换成列表,不留在历史里。返回值说明是否同步完成(后退要等 popstate)。
export function leaveRoute(): boolean {
  if ((history.state as Record<string, unknown> | null)?.[ROUTE_MARK]) {
    history.back();
    return false;
  }
  history.replaceState(null, "", location.pathname);
  return true;
}

export async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  // 后台按这个语言写错误信息,和界面一致(决策记录 0014)。
  const headers: Record<string, string> = { Authorization: "Bearer " + getToken(), "Accept-Language": lang() };
  const init: RequestInit = { method, headers };
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }
  let resp: Response;
  try {
    resp = await fetch(API + path, init);
  } catch {
    throw new ApiError("offline", t("连不上后台,它可能已经停止。"));
  }
  let data: unknown = null;
  try {
    data = await resp.json();
  } catch {
    // 不是 JSON 的响应按状态码处理
  }
  if (!resp.ok) {
    const e = (data as { error?: { code: string; message: string } } | null)?.error;
    throw new ApiError(e ? e.code : "http_" + resp.status, e ? e.message : t("后台返回 {status}", { status: resp.status }));
  }
  return data as T;
}

// Settings 是后台的设置(决策记录 0014)。effective_language 是后台实际说的语言。
export interface Settings {
  language: "auto" | "zh" | "en";
  effective_language?: string;
}

export const taskPath = (name: string, action = "") => `/tasks/${encodeURIComponent(name)}${action ? "/" + action : ""}`;
export const runPath = (id: string, action = "") => `/runs/${encodeURIComponent(id)}${action ? "/" + action : ""}`;

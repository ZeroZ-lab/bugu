// 后台 JSON 接口的客户端和数据类型。前端只通过这些接口工作,应用窗口和浏览器里是同一份。

const API = "/api/v1";
const TOKEN_KEY = "bugu.token";

export interface Trigger {
  cron?: string;
  at?: string;
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
  type: "script" | "agent";
  script?: ScriptStep;
  agent?: AgentStep;
}

export interface Notification {
  channel: string;
  when?: string;
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
  paused?: boolean;
  running?: boolean;
  next_run?: string;
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
}

export interface Logs {
  stdout: string;
  stderr: string;
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

// takeRunFromHash 读取桌面应用点通知时带上的 #run=<编号>,读到后从地址栏移除。
export function takeRunFromHash(): string | null {
  const id = new URLSearchParams(location.hash.slice(1)).get("run");
  if (id) history.replaceState(null, "", location.pathname);
  return id;
}

export async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { Authorization: "Bearer " + getToken() };
  const init: RequestInit = { method, headers };
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }
  let resp: Response;
  try {
    resp = await fetch(API + path, init);
  } catch {
    throw new ApiError("offline", "连不上后台,它可能已经停止。");
  }
  let data: unknown = null;
  try {
    data = await resp.json();
  } catch {
    // 不是 JSON 的响应按状态码处理
  }
  if (!resp.ok) {
    const e = (data as { error?: { code: string; message: string } } | null)?.error;
    throw new ApiError(e ? e.code : "http_" + resp.status, e ? e.message : "后台返回 " + resp.status);
  }
  return data as T;
}

export const taskPath = (name: string, action = "") => `/tasks/${encodeURIComponent(name)}${action ? "/" + action : ""}`;
export const runPath = (id: string, action = "") => `/runs/${encodeURIComponent(id)}${action ? "/" + action : ""}`;

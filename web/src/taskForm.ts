// 任务表单的内容与任务定义之间的转换。和界面分开,便于测试。
import type { AgentStep, Runner, ScriptStep, Task } from "./api";
import { durationToMinutes, toLocalInput } from "./format";

export const PRESETS = [
  { value: "0 2 * * *", label: "每天 02:00" },
  { value: "0 9 * * 1-5", label: "工作日 09:00" },
  { value: "0 * * * *", label: "每小时整点" },
  { value: "*/15 * * * *", label: "每 15 分钟" },
];

export interface TaskForm {
  name: string;
  trigger: "cron" | "at";
  cron: string;
  at: string;
  kind: "command" | "path" | "agent";
  command: string;
  path: string;
  runner: string;
  permission: string;
  prompt: string;
  cwd: string;
  timeout: string;
  notify: string;
  env: string;
}

// agentRunners 是可用于 AI 命令行步骤的运行器。
export const agentRunners = (runners: Runner[]) => runners.filter((r) => r.agent);

// permissionLevels 是所选运行器可选的权限级别,第一个是默认值。
export const permissionLevels = (runners: Runner[], runner: string) => runners.find((r) => r.name === runner)?.permissions ?? [];

// formFromTask 把任务定义填进表单。task 为 null 时是新建任务的初始内容。
export function formFromTask(task: Task | null, runners: Runner[]): TaskForm {
  const firstRunner = agentRunners(runners)[0]?.name ?? "";
  if (!task) {
    return {
      name: "", trigger: "cron", cron: PRESETS[0].value, at: "", kind: "command", command: "", path: "",
      runner: firstRunner, permission: permissionLevels(runners, firstRunner)[0] ?? "", prompt: "",
      cwd: "", timeout: "", notify: "on_content", env: "",
    };
  }
  const step = task.steps[0];
  const script = step?.script ?? {};
  const agent = step?.type === "agent" ? step.agent : undefined;
  const runner = agent ? agent.runner : firstRunner;
  const levels = permissionLevels(runners, runner);
  const system = (task.notifications ?? []).find((n) => n.channel === "system");
  return {
    name: task.name,
    trigger: task.trigger.cron ? "cron" : "at",
    cron: task.trigger.cron ?? "",
    at: task.trigger.at ? toLocalInput(task.trigger.at) : "",
    kind: agent ? "agent" : script.path ? "path" : "command",
    command: script.command ?? "",
    path: script.path ?? "",
    runner,
    permission: agent?.permission && levels.includes(agent.permission) ? agent.permission : (levels[0] ?? ""),
    prompt: agent?.prompt ?? "",
    cwd: task.cwd ?? "",
    timeout: durationToMinutes(task.timeout),
    notify: system ? system.when || "on_content" : "",
    env: Object.entries(task.env ?? {}).map(([k, v]) => `${k}=${v}`).join("\n"),
  };
}

// taskFromForm 把表单内容整理成任务定义。修改时以原定义为底,表单没有覆盖的字段保持不变。
// 内容不完整时抛出错误,错误信息直接显示给用户。
export function taskFromForm(f: TaskForm, editing: Task | null, runners: Runner[]): Task {
  const t: Task = editing ? structuredClone(editing) : { name: f.name.trim(), trigger: {}, steps: [] };
  delete t.next_run;
  delete t.running;

  if (f.trigger === "cron") {
    t.trigger = { cron: f.cron.trim() };
    if (!t.trigger.cron) throw new Error("请填写 cron 表达式。");
  } else {
    if (!f.at) throw new Error("请选择运行时间。");
    t.trigger = { at: new Date(f.at).toISOString() };
  }

  if (f.kind === "agent") {
    if (!f.runner) throw new Error("请选择运行器。");
    if (!f.prompt.trim()) throw new Error("请填写提示词。");
    const agent: AgentStep = { runner: f.runner, prompt: f.prompt };
    // 默认级别不写进任务,放开的才显式写出(决策记录 0008)。
    const levels = permissionLevels(runners, f.runner);
    if (f.permission && levels.length > 0 && f.permission !== levels[0]) agent.permission = f.permission;
    t.steps = [{ type: "agent", agent }];
  } else {
    const script: ScriptStep = f.kind === "command" ? { command: f.command.trim() } : { path: f.path.trim() };
    if (!script.command && !script.path) throw new Error(f.kind === "command" ? "请填写要运行的命令。" : "请填写脚本文件的路径。");
    t.steps = [{ type: "script", script }];
  }

  t.cwd = f.cwd.trim() || undefined;
  t.timeout = Number(f.timeout) > 0 ? `${Math.round(Number(f.timeout))}m` : undefined;

  const others = (t.notifications ?? []).filter((n) => n.channel !== "system");
  t.notifications = f.notify ? [...others, { channel: "system", when: f.notify }] : others;
  if (t.notifications.length === 0) delete t.notifications;

  const env: Record<string, string> = {};
  for (const line of f.env.split("\n")) {
    if (!line.trim()) continue;
    const i = line.indexOf("=");
    if (i <= 0) throw new Error(`环境变量应写成 名=值,这一行不是:${line}`);
    env[line.slice(0, i).trim()] = line.slice(i + 1);
  }
  t.env = Object.keys(env).length ? env : undefined;
  return t;
}

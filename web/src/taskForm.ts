// 任务表单的内容与任务定义之间的转换。和界面分开,便于测试。
import type { AgentStep, Runner, ScriptStep, Step, Task } from "./api";
import { durationToMinutes, toLocalInput } from "./format";
import { t } from "./i18n";
import { cronFromPick, pickFromCron, type Pick } from "./schedulePick";

// 新建任务时默认的定时。
const DEFAULT_CRON = "0 2 * * *";

export interface TaskForm {
  name: string;
  trigger: "cron" | "at";
  schedule: Pick; // 定时重复时选的重复方式和时间(S9),保存时换算成 cron
  at: string;
  // steps 表示用步骤卡片编辑(T9):多个步骤,或者含文本步骤。
  kind: "command" | "path" | "agent" | "steps";
  steps: FormStep[];
  command: string;
  path: string;
  runner: string;
  permission: string;
  prompt: string;
  cwd: string;
  timeout: string;
  notify: string;
  silent: string;
  env: string;
  envFile: string;
  timezone: string;
  onMissed: string;
  missedWindow: string; // 分钟
  jitter: string; // 随机偏移,分钟(S12)
  webhook: string;
  webhookWhen: string;
}

// FormStep 是一张步骤卡片的内容(T9)。
export interface FormStep {
  key: number; // 渲染用的标识,排序后不变
  name: string;
  type: "command" | "path" | "agent" | "text";
  command: string;
  path: string;
  runner: string;
  permission: string;
  prompt: string;
  text: string;
  timeout: string; // 分钟
  orig?: Step; // 原来的步骤:保存时以它为底,表单不认识的字段原样带回
}

let nextKey = 1;

// newStep 返回一张空白的步骤卡片。
export function newStep(runners: Runner[], type: FormStep["type"] = "command"): FormStep {
  const runner = agentRunners(runners)[0]?.name ?? "";
  return { key: nextKey++, name: "", type, command: "", path: "", runner, permission: permissionLevels(runners, runner)[0] ?? "", prompt: "", text: "", timeout: "" };
}

function stepToForm(s: Step, runners: Runner[]): FormStep {
  const f = newStep(runners);
  f.name = s.name ?? "";
  f.timeout = durationToMinutes(s.timeout);
  f.orig = s;
  if (s.type === "text") return { ...f, type: "text", text: s.text ?? "" };
  if (s.type === "agent" && s.agent) {
    const levels = permissionLevels(runners, s.agent.runner);
    const permission = s.agent.permission && levels.includes(s.agent.permission) ? s.agent.permission : (levels[0] ?? "");
    return { ...f, type: "agent", runner: s.agent.runner, prompt: s.agent.prompt, permission };
  }
  return s.script?.path ? { ...f, type: "path", path: s.script.path } : { ...f, type: "command", command: s.script?.command ?? "" };
}

// formToStep 把一张卡片整理成步骤。内容不完整时抛出错误,说明是第几步。
function formToStep(f: FormStep, i: number, runners: Runner[]): Step {
  const fail = (msg: string) => new Error(t("第 {n} 步:{msg}", { n: i + 1, msg }));
  const s: Step = { ...(f.orig ?? {}), type: f.type === "agent" ? "agent" : f.type === "text" ? "text" : "script" };
  delete s.script;
  delete s.agent;
  delete s.text;
  s.name = f.name.trim() || undefined;
  s.timeout = Number(f.timeout) > 0 ? `${Math.round(Number(f.timeout))}m` : undefined;
  if (f.type === "text") {
    if (!f.text.trim()) throw fail(t("请填写文本内容。"));
    s.text = f.text;
  } else if (f.type === "agent") {
    if (!f.runner) throw fail(t("请选择运行器。"));
    if (!f.prompt.trim()) throw fail(t("请填写提示词。"));
    s.agent = agentStep(f.runner, f.prompt, f.permission, runners);
  } else {
    const script: ScriptStep = f.type === "command" ? { command: f.command.trim() } : { path: f.path.trim() };
    if (!script.command && !script.path) throw fail(f.type === "command" ? t("请填写要运行的命令。") : t("请填写脚本文件的路径。"));
    s.script = script;
  }
  return s;
}

// agentStep 生成 AI 命令行步骤。默认级别不写进任务,放开的才显式写出(决策记录 0008)。
function agentStep(runner: string, prompt: string, permission: string, runners: Runner[]): AgentStep {
  const agent: AgentStep = { runner, prompt };
  const levels = permissionLevels(runners, runner);
  if (permission && levels.length > 0 && permission !== levels[0]) agent.permission = permission;
  return agent;
}

// stepsFromSimple 把表单里单步的内容变成一张卡片,切换到"多个步骤"时用。
export function stepsFromSimple(f: TaskForm, runners: Runner[]): FormStep[] {
  const s = newStep(runners, f.kind === "path" ? "path" : f.kind === "agent" ? "agent" : "command");
  return [{ ...s, command: f.command, path: f.path, runner: f.runner || s.runner, permission: f.permission || s.permission, prompt: f.prompt }];
}

// simpleSteps 报告任务的步骤能否在表单里修改:只有一个脚本或 AI 命令行步骤。
export const simpleSteps = (task: Task) => task.steps.length === 1 && (task.steps[0].type === "script" || task.steps[0].type === "agent");

// agentRunners 是可用于 AI 命令行步骤的运行器。
export const agentRunners = (runners: Runner[]) => runners.filter((r) => r.agent);

// permissionLevels 是所选运行器可选的权限级别,第一个是默认值。
export const permissionLevels = (runners: Runner[], runner: string) => runners.find((r) => r.name === runner)?.permissions ?? [];

// formFromTask 把任务定义填进表单。task 为 null 时是新建任务的初始内容。
export function formFromTask(task: Task | null, runners: Runner[]): TaskForm {
  const firstRunner = agentRunners(runners)[0]?.name ?? "";
  if (!task) {
    return {
      name: "", trigger: "cron", schedule: pickFromCron(DEFAULT_CRON), at: "", kind: "command", steps: [], command: "", path: "",
      runner: firstRunner, permission: permissionLevels(runners, firstRunner)[0] ?? "", prompt: "",
      cwd: "", timeout: "", notify: "on_content", silent: "", env: "",
      envFile: "", timezone: "", onMissed: "run_once", missedWindow: "", webhook: "", webhookWhen: "on_content", jitter: "",
    };
  }
  const step = task.steps[0];
  const script = step?.script ?? {};
  const agent = step?.type === "agent" ? step.agent : undefined;
  const runner = agent ? agent.runner : firstRunner;
  const levels = permissionLevels(runners, runner);
  const system = (task.notifications ?? []).find((n) => n.channel === "system");
  const webhook = (task.notifications ?? []).find((n) => n.channel === "webhook");
  return {
    name: task.name,
    trigger: task.trigger.cron ? "cron" : "at",
    schedule: pickFromCron(task.trigger.cron ?? DEFAULT_CRON),
    at: task.trigger.at ? toLocalInput(task.trigger.at) : "",
    kind: !simpleSteps(task) ? "steps" : agent ? "agent" : script.path ? "path" : "command",
    steps: simpleSteps(task) ? [] : task.steps.map((s) => stepToForm(s, runners)),
    command: script.command ?? "",
    path: script.path ?? "",
    runner,
    permission: agent?.permission && levels.includes(agent.permission) ? agent.permission : (levels[0] ?? ""),
    prompt: agent?.prompt ?? "",
    cwd: task.cwd ?? "",
    timeout: durationToMinutes(task.timeout),
    notify: system ? system.when || "on_content" : "",
    silent: task.silent_if ?? "",
    envFile: task.env_file ?? "",
    timezone: task.timezone ?? "",
    onMissed: task.on_missed || "run_once",
    missedWindow: durationToMinutes(task.missed_window),
    jitter: durationToMinutes(task.trigger.jitter),
    webhook: webhook?.url ?? "",
    webhookWhen: webhook?.when || "on_content",
    env: Object.entries(task.env ?? {}).map(([k, v]) => `${k}=${v}`).join("\n"),
  };
}

// taskFromForm 把表单内容整理成任务定义。修改时以原定义为底,表单没有覆盖的字段保持不变。
// 内容不完整时抛出错误,错误信息直接显示给用户。
export function taskFromForm(f: TaskForm, editing: Task | null, runners: Runner[]): Task {
  const out: Task = editing ? structuredClone(editing) : { name: f.name.trim(), trigger: {}, steps: [] };
  delete out.next_run;
  delete out.running;
  delete out.last_run;

  if (f.trigger === "cron") {
    out.trigger = {
      cron: cronFromPick(f.schedule, (what) => ({ time: t("请选择时间。"), days: t("每周至少选一天。"), cron: t("请填写 cron 表达式。") })[what]),
    };
    const jitter = Number(f.jitter);
    if (!(jitter >= 0)) throw new Error(t("随机偏移应是不小于 0 的分钟数。"));
    if (jitter > 0) {
      // 没改动时保留原来的写法,免得不足一分钟的偏移被四舍五入成整分钟。
      const old = editing?.trigger.jitter;
      out.trigger.jitter = old && durationToMinutes(old) === f.jitter.trim() ? old : `${Math.round(jitter)}m`;
    }
  } else {
    if (!f.at) throw new Error(t("请选择运行时间。"));
    out.trigger = { at: new Date(f.at).toISOString() };
  }

  if (f.kind === "steps") {
    if (f.steps.length === 0) throw new Error(t("至少要有一个步骤。"));
    out.steps = f.steps.map((s, i) => formToStep(s, i, runners));
  } else if (f.kind === "agent") {
    if (!f.runner) throw new Error(t("请选择运行器。"));
    if (!f.prompt.trim()) throw new Error(t("请填写提示词。"));
    out.steps = [{ type: "agent", agent: agentStep(f.runner, f.prompt, f.permission, runners) }];
  } else {
    const script: ScriptStep = f.kind === "command" ? { command: f.command.trim() } : { path: f.path.trim() };
    if (!script.command && !script.path) throw new Error(f.kind === "command" ? t("请填写要运行的命令。") : t("请填写脚本文件的路径。"));
    out.steps = [{ type: "script", script }];
  }

  out.cwd = f.cwd.trim() || undefined;
  out.timeout = Number(f.timeout) > 0 ? `${Math.round(Number(f.timeout))}m` : undefined;
  out.silent_if = f.silent.trim() || undefined;
  out.env_file = f.envFile.trim() || undefined;
  out.timezone = f.timezone.trim() || undefined;
  out.on_missed = f.onMissed === "run_once" ? undefined : f.onMissed;
  out.missed_window = undefined;
  if (f.onMissed === "window") {
    if (!(Number(f.missedWindow) > 0)) throw new Error(t("请填写补跑的宽限时长。"));
    out.missed_window = `${Math.round(Number(f.missedWindow))}m`;
  }

  // 表单只管一条系统通知和一条 webhook,其余渠道的设置原样保留。
  const others = (out.notifications ?? []).filter((n) => n.channel !== "system" && n.channel !== "webhook");
  out.notifications = [...others];
  if (f.notify) out.notifications.push({ channel: "system", when: f.notify });
  const hook = f.webhook.trim();
  if (hook) {
    if (!/^https?:\/\/\S+$/.test(hook)) throw new Error(t("webhook 地址应以 http:// 或 https:// 开头。"));
    out.notifications.push({ channel: "webhook", url: hook, when: f.webhookWhen });
  }
  if (out.notifications.length === 0) delete out.notifications;

  const env: Record<string, string> = {};
  for (const line of f.env.split("\n")) {
    if (!line.trim()) continue;
    const i = line.indexOf("=");
    if (i <= 0) throw new Error(t("环境变量应写成 名=值,这一行不是:{line}", { line }));
    env[line.slice(0, i).trim()] = line.slice(i + 1);
  }
  out.env = Object.keys(env).length ? env : undefined;
  return out;
}

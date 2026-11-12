import { useState, type FormEvent } from "react";
import { api, taskPath, type Runner, type Task } from "./api";
import { PERMISSION } from "./format";
import { agentRunners, formFromTask, permissionLevels, PRESETS, taskFromForm, type TaskForm } from "./taskForm";
import { Button, Choice, ErrorText, Field, Modal } from "./ui";

interface Props {
  task: Task | null; // null 表示新建
  runners: Runner[];
  onClose: () => void;
  onSaved: () => void;
}

const TRIGGERS = [
  ["cron", "定时重复"],
  ["at", "只运行一次"],
] as const;

const KINDS = [
  ["command", "一条命令"],
  ["path", "脚本文件"],
  ["agent", "AI 命令行"],
] as const;

export function TaskDialog({ task, runners, onClose, onSaved }: Props) {
  const [f, setForm] = useState<TaskForm>(() => formFromTask(task, runners));
  const [custom, setCustom] = useState(false); // 用户在"常用"里选了"自定义"
  const [error, setError] = useState("");
  const set = <K extends keyof TaskForm>(key: K, value: TaskForm[K]) => setForm((prev) => ({ ...prev, [key]: value }));

  const preset = custom ? "" : PRESETS.some((p) => p.value === f.cron.trim()) ? f.cron.trim() : "";
  const usable = agentRunners(runners);
  const runnerMissing = f.runner !== "" && !usable.some((r) => r.name === f.runner);
  const levels = permissionLevels(runners, f.runner);

  function chooseRunner(runner: string) {
    const next = permissionLevels(runners, runner);
    setForm((prev) => ({ ...prev, runner, permission: next.includes(prev.permission) ? prev.permission : (next[0] ?? "") }));
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    try {
      const t = taskFromForm(f, task, runners);
      if (task) await api("PUT", taskPath(t.name), t);
      else await api("POST", "/tasks", t);
      onSaved();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  return (
    <Modal onClose={onClose}>
      <form onSubmit={submit}>
        <h2>{task ? "修改任务" : "新建任务"}</h2>
        <Field label="名称" hint="字母、数字、- 和 _,创建后不能修改。">
          <input
            value={f.name}
            onChange={(e) => set("name", e.target.value)}
            disabled={task !== null}
            required
            maxLength={64}
            pattern="[A-Za-z0-9][A-Za-z0-9_\-]*"
            placeholder="backup"
          />
        </Field>

        <fieldset>
          <legend>什么时候运行</legend>
          <Choice name="trigger" value={f.trigger} options={TRIGGERS} onChange={(v) => set("trigger", v)} />
          {f.trigger === "cron" ? (
            <div>
              <Field label="常用">
                <select
                  value={preset}
                  onChange={(e) => {
                    setCustom(e.target.value === "");
                    if (e.target.value) set("cron", e.target.value);
                  }}
                >
                  {PRESETS.map((p) => (
                    <option key={p.value} value={p.value}>
                      {p.label}
                    </option>
                  ))}
                  <option value="">自定义</option>
                </select>
              </Field>
              <Field label="cron 表达式" hint="五段:分 时 日 月 周,按这台主机的时区。">
                <input
                  value={f.cron}
                  onChange={(e) => {
                    setCustom(false);
                    set("cron", e.target.value);
                  }}
                  placeholder="0 2 * * *"
                />
              </Field>
            </div>
          ) : (
            <Field label="时间">
              <input type="datetime-local" value={f.at} onChange={(e) => set("at", e.target.value)} />
            </Field>
          )}
        </fieldset>

        <fieldset>
          <legend>运行什么</legend>
          <Choice name="kind" value={f.kind} options={KINDS} onChange={(v) => set("kind", v)} />
          {f.kind === "command" && (
            <Field label="命令" hint="交给 /bin/sh 执行。">
              <input value={f.command} onChange={(e) => set("command", e.target.value)} placeholder="~/bin/backup.sh --all" />
            </Field>
          )}
          {f.kind === "path" && (
            <Field label="脚本文件的路径" hint="文件需要有执行权限。">
              <input value={f.path} onChange={(e) => set("path", e.target.value)} placeholder="~/bin/backup.sh" />
            </Field>
          )}
          {f.kind === "agent" && (
            <div>
              <Field
                label="运行器"
                hint={usable.length || runnerMissing ? "这台主机上已配置的 AI 命令行。能否启动可用 bugu runner check 检查。" : "这台主机上没有可用的运行器。"}
              >
                <select value={f.runner} onChange={(e) => chooseRunner(e.target.value)}>
                  {usable.map((r) => (
                    <option key={r.name} value={r.name}>
                      {r.title ? `${r.title}(${r.name})` : r.name}
                    </option>
                  ))}
                  {runnerMissing && <option value={f.runner}>{f.runner}(找不到这个运行器)</option>}
                </select>
              </Field>
              <Field label="提示词" hint="可以使用 {date}、{time}、{datetime}、{last_run}(上次成功运行的时间)、{task_name}、{run_dir}。">
                <textarea
                  rows={6}
                  value={f.prompt}
                  onChange={(e) => set("prompt", e.target.value)}
                  placeholder="列出 {last_run} 之后的新 issue,按 bug、需求、提问分类。只读,不要回复任何 issue。"
                />
              </Field>
              {levels.length > 0 && (
                <Field label="允许它做什么" hint="运行时没有人在场,除非任务确实需要修改文件,否则保持只读。">
                  <select value={f.permission} onChange={(e) => set("permission", e.target.value)}>
                    {levels.map((l, i) => (
                      <option key={l} value={l}>
                        {(PERMISSION[l] ?? l) + (i === 0 ? "(默认)" : "")}
                      </option>
                    ))}
                  </select>
                </Field>
              )}
            </div>
          )}
          <Field label="工作目录(可选)">
            <input value={f.cwd} onChange={(e) => set("cwd", e.target.value)} placeholder="~/projects/site" />
          </Field>
        </fieldset>

        <fieldset>
          <legend>其他</legend>
          <Field label="最长运行时间,分钟(可选)" hint="超过后终止并记为超时。">
            <input type="number" min={0} step={1} value={f.timeout} onChange={(e) => set("timeout", e.target.value)} placeholder="不限" />
          </Field>
          <Field label="通知" hint="系统通知由桌面应用发出。">
            <select value={f.notify} onChange={(e) => set("notify", e.target.value)}>
              <option value="on_content">失败时,或成功且有输出时</option>
              <option value="on_failure">仅失败时</option>
              <option value="always">每次运行后</option>
              <option value="">不通知</option>
            </select>
          </Field>
          <Field label="环境变量(可选,每行一个 名=值)">
            <textarea rows={2} value={f.env} onChange={(e) => set("env", e.target.value)} placeholder="TARGET=/data" />
          </Field>
        </fieldset>

        <ErrorText message={error} />
        <div className="actions">
          <Button onClick={onClose}>取消</Button>
          <Button type="submit" variant="primary">
            保存
          </Button>
        </div>
      </form>
    </Modal>
  );
}

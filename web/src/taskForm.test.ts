import { describe, expect, it } from "vitest";
import type { Runner, Task } from "./api";
import { durationToMinutes, formatTime, statusOf } from "./format";
import { formFromTask, taskFromForm } from "./taskForm";

const runners: Runner[] = [
  { name: "shell", agent: false },
  { name: "alpha", title: "Alpha", agent: true, permissions: ["read_only", "write_cwd", "full"] },
  { name: "beta", agent: true, permissions: ["read_only"] },
];

// G2:表单方式新建和修改任务。
describe("G2 新建任务的表单", () => {
  it("初始内容是每天 02:00 的命令,默认通知规则,运行器取第一个可用的", () => {
    const f = formFromTask(null, runners);
    expect(f).toMatchObject({ trigger: "cron", cron: "0 2 * * *", kind: "command", notify: "on_content", runner: "alpha", permission: "read_only" });
  });

  it("命令任务整理成单个脚本步骤", () => {
    const f = { ...formFromTask(null, runners), name: " backup ", command: " ~/bin/backup.sh ", timeout: "90", env: "A=1\nB=x=y\n" };
    expect(taskFromForm(f, null, runners)).toEqual({
      name: "backup",
      trigger: { cron: "0 2 * * *" },
      steps: [{ type: "script", script: { command: "~/bin/backup.sh" } }],
      cwd: undefined,
      timeout: "90m",
      notifications: [{ channel: "system", when: "on_content" }],
      env: { A: "1", B: "x=y" },
    });
  });

  it("内容不完整时给出说明", () => {
    const f = formFromTask(null, runners);
    expect(() => taskFromForm({ ...f, cron: " " }, null, runners)).toThrow("请填写 cron 表达式");
    expect(() => taskFromForm({ ...f, trigger: "at" }, null, runners)).toThrow("请选择运行时间");
    expect(() => taskFromForm(f, null, runners)).toThrow("请填写要运行的命令");
    expect(() => taskFromForm({ ...f, kind: "agent" }, null, runners)).toThrow("请填写提示词");
    expect(() => taskFromForm({ ...f, command: "x", env: "NOEQUALS" }, null, runners)).toThrow("环境变量应写成 名=值");
  });
});

// R7、决策记录 0008:默认级别不写进任务,放开的才显式写出。
describe("R7 AI 命令行步骤的权限级别", () => {
  const base = { ...formFromTask(null, runners), name: "digest", kind: "agent" as const, prompt: "总结" };

  it("选默认级别时不写 permission", () => {
    const t = taskFromForm(base, null, runners);
    expect(t.steps).toEqual([{ type: "agent", agent: { runner: "alpha", prompt: "总结" } }]);
  });

  it("放开的级别显式写出", () => {
    const t = taskFromForm({ ...base, permission: "write_cwd" }, null, runners);
    expect(t.steps[0].agent?.permission).toBe("write_cwd");
  });
});

describe("G2 修改任务的表单", () => {
  const existing: Task = {
    name: "digest",
    trigger: { cron: "30 18 * * 1-5" },
    steps: [{ type: "agent", agent: { runner: "alpha", prompt: "总结 {last_run}", permission: "write_cwd" } }],
    timeout: "1h30m0s",
    notifications: [{ channel: "webhook", when: "always" }, { channel: "system", when: "on_failure" }],
    created_at: "2026-11-12T02:00:00Z",
    next_run: "2026-11-12T10:30:00Z",
    running: false,
  };

  it("回填原定义", () => {
    expect(formFromTask(existing, runners)).toMatchObject({
      name: "digest", cron: "30 18 * * 1-5", kind: "agent", runner: "alpha", permission: "write_cwd",
      prompt: "总结 {last_run}", timeout: "90", notify: "on_failure",
    });
  });

  it("保存时保留表单不认识的字段,去掉只读的状态字段", () => {
    const t = taskFromForm({ ...formFromTask(existing, runners), notify: "" }, existing, runners);
    expect(t.created_at).toBe("2026-11-12T02:00:00Z");
    expect(t).not.toHaveProperty("next_run");
    expect(t).not.toHaveProperty("running");
    expect(t.notifications).toEqual([{ channel: "webhook", when: "always" }]);
  });
});

describe("显示格式", () => {
  it("时间按本地时区显示到分钟", () => {
    expect(formatTime(new Date(2026, 10, 12, 9, 5).toISOString())).toBe("2026-11-12 09:05");
    expect(formatTime(undefined)).toBe("-");
  });

  it("Go 的时长换算成分钟", () => {
    expect(durationToMinutes("1h30m0s")).toBe("90");
    expect(durationToMinutes("45s")).toBe("1");
    expect(durationToMinutes("")).toBe("");
  });

  // O3:运行状态,未知状态原样显示为空心圈。
  it("O3 每种运行状态都有文字和标记", () => {
    for (const s of ["queued", "running", "succeeded", "failed", "timed_out", "skipped", "missed", "canceled", "interrupted"]) {
      expect(statusOf(s)[0]).not.toBe(s);
    }
    expect(statusOf("weird")).toEqual(["weird", "ring"]);
  });
});

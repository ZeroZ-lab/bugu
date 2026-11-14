import { describe, expect, it } from "vitest";
import { setLang } from "./i18n";
import type { Runner, Task } from "./api";
import { describeCron, describeTrigger, durationToMinutes, formatTime, statusOf } from "./format";
import { formFromTask, newStep, stepsFromSimple, taskFromForm } from "./taskForm";

// 断言里的文字是中文原文;Node 自带的 navigator 可能说英文。
setLang("zh");

const runners: Runner[] = [
  { name: "shell", agent: false },
  { name: "alpha", title: "Alpha", agent: true, permissions: ["read_only", "write_cwd", "full"] },
  { name: "beta", agent: true, permissions: ["read_only"] },
];

// G2:表单方式新建和修改任务。
describe("G2 新建任务的表单", () => {
  it("初始内容是每天 02:00 的命令,默认通知规则,运行器取第一个可用的", () => {
    const f = formFromTask(null, runners);
    expect(f).toMatchObject({ trigger: "cron", schedule: { repeat: "daily", time: "02:00" }, kind: "command", notify: "on_content", runner: "alpha", permission: "read_only" });
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
    expect(() => taskFromForm({ ...f, schedule: { ...f.schedule, repeat: "cron", cron: " " } }, null, runners)).toThrow("请填写 cron 表达式");
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
    notifications: [{ channel: "webhook", url: "https://a.example/h", when: "always" }, { channel: "system", when: "on_failure" }],
    created_at: "2026-11-12T02:00:00Z",
    next_run: "2026-11-12T10:30:00Z",
    running: false,
    last_run: { id: "20261111T103000-000000", task: "digest", status: "succeeded", trigger: "schedule" } as Task["last_run"],
  };

  it("回填原定义", () => {
    expect(formFromTask(existing, runners)).toMatchObject({
      name: "digest", schedule: { repeat: "weekdays", time: "18:30" }, kind: "agent", runner: "alpha", permission: "write_cwd",
      prompt: "总结 {last_run}", timeout: "90", notify: "on_failure",
    });
  });

  it("保存时保留表单不认识的字段,去掉只读的状态字段", () => {
    const t = taskFromForm({ ...formFromTask(existing, runners), notify: "" }, existing, runners);
    expect(t.created_at).toBe("2026-11-12T02:00:00Z");
    expect(t).not.toHaveProperty("next_run");
    expect(t).not.toHaveProperty("running");
    expect(t).not.toHaveProperty("last_run");
    expect(t.notifications).toEqual([{ channel: "webhook", url: "https://a.example/h", when: "always" }]);
  });
});

// T1、T5:表单表示不了的多步骤任务,修改其他设置时步骤原样保留。O4:静默标记可填可清。
describe("多步骤任务和静默标记", () => {
  const multi: Task = {
    name: "triage",
    trigger: { cron: "0 9 * * 1-5" },
    steps: [
      { name: "检查", type: "script", script: { command: "~/bin/new-issues.sh" } },
      { type: "text", text: "按类型分类" },
      { type: "agent", agent: { runner: "alpha", prompt: "{prev}" } },
    ],
    silent_if: "NOTHING_NEW",
  };

  it("多步骤任务显示为步骤卡片,不改步骤时保存后原样", () => {
    const f = formFromTask(multi, runners);
    expect(f.kind).toBe("steps");
    expect(f.steps.map((s) => s.type)).toEqual(["command", "text", "agent"]);
    expect(f.silent).toBe("NOTHING_NEW");
    const t = taskFromForm({ ...f, schedule: { ...f.schedule, time: "10:00" }, silent: " " }, multi, runners);
    expect(t.steps).toEqual(multi.steps);
    expect(t.trigger).toEqual({ cron: "0 10 * * 1-5" });
    expect(t.silent_if).toBeUndefined();
  });

  // T9:卡片可以重新排序、删除、添加,每一步的内容和时长按卡片保存;原步骤里表单不认识的字段带回。
  it("T9 在卡片上修改步骤", () => {
    const withExtra: Task = { ...multi, steps: [{ ...multi.steps[0], future: 1 } as Task["steps"][0], ...multi.steps.slice(1)] };
    const f = formFromTask(withExtra, runners);
    const [check, text, agent] = f.steps;
    const steps = [text, { ...check, timeout: "5" }, { ...agent, permission: "write_cwd" }, { ...newStep(runners, "text"), name: "尾注", text: "完" }];
    const t = taskFromForm({ ...f, steps }, withExtra, runners);
    expect(t.steps).toEqual([
      { type: "text", text: "按类型分类" },
      { name: "检查", type: "script", script: { command: "~/bin/new-issues.sh" }, timeout: "5m", future: 1 },
      { type: "agent", agent: { runner: "alpha", prompt: "{prev}", permission: "write_cwd" } },
      { name: "尾注", type: "text", text: "完" },
    ]);
    expect(() => taskFromForm({ ...f, steps: [...steps, newStep(runners)] }, withExtra, runners)).toThrow("第 5 步:请填写要运行的命令");
    expect(() => taskFromForm({ ...f, steps: [] }, withExtra, runners)).toThrow("至少要有一个步骤");
  });

  it("T9 从单步切到多个步骤时,已填的内容成为第一步", () => {
    const f = { ...formFromTask(null, runners), name: "two", command: "echo 一" };
    const steps = [...stepsFromSimple(f, runners), { ...newStep(runners, "agent"), prompt: "总结" }];
    const t = taskFromForm({ ...f, kind: "steps", steps }, null, runners);
    expect(t.steps).toEqual([
      { type: "script", script: { command: "echo 一" } },
      { type: "agent", agent: { runner: "alpha", prompt: "总结" } },
    ]);
  });

  it("只有一个文本步骤的任务也原样保留", () => {
    const single: Task = { name: "memo", trigger: { cron: "0 9 * * *" }, steps: [{ type: "text", text: "喝水" }] };
    expect(formFromTask(single, runners).kind).toBe("steps");
  });

  it("新建任务时填写静默标记", () => {
    const f = { ...formFromTask(null, runners), name: "news", command: "true", silent: "NOTHING_NEW" };
    expect(taskFromForm(f, null, runners).silent_if).toBe("NOTHING_NEW");
  });
});

// E7 E8 S4 N3:时区、环境变量文件、补跑策略和 webhook。
describe("时区、环境变量文件、补跑和 webhook", () => {
  const base = { ...formFromTask(null, runners), name: "news", command: "true" };

  it("默认值不写进任务", () => {
    const t = taskFromForm(base, null, runners);
    expect(t).not.toHaveProperty("timezone", expect.anything());
    expect(t.on_missed).toBeUndefined();
    expect(t.notifications).toEqual([{ channel: "system", when: "on_content" }]);
  });

  it("填写后写进任务,修改时回填", () => {
    const f = { ...base, timezone: " Asia/Shanghai ", envFile: "~/news.env", onMissed: "window", missedWindow: "90", webhook: "https://example.com/h", webhookWhen: "always" };
    const t = taskFromForm(f, null, runners);
    expect(t).toMatchObject({ timezone: "Asia/Shanghai", env_file: "~/news.env", on_missed: "window", missed_window: "90m" });
    expect(t.notifications).toEqual([
      { channel: "system", when: "on_content" },
      { channel: "webhook", url: "https://example.com/h", when: "always" },
    ]);
    const back = formFromTask({ ...t, missed_window: "1h30m0s" }, runners);
    expect(back).toMatchObject({ timezone: "Asia/Shanghai", envFile: "~/news.env", onMissed: "window", missedWindow: "90", webhook: "https://example.com/h", webhookWhen: "always" });
  });

  it("清空 webhook 就去掉那条通知,其他渠道保留", () => {
    const existing: Task = {
      name: "news", trigger: { cron: "0 8 * * *" }, steps: [{ type: "script", script: { command: "true" } }],
      notifications: [{ channel: "telegram", when: "always" }, { channel: "webhook", url: "https://a.example/h" }],
    };
    const t = taskFromForm({ ...formFromTask(existing, runners), webhook: "" }, existing, runners);
    expect(t.notifications).toEqual([{ channel: "telegram", when: "always" }]);
  });

  it("内容不对时给出说明", () => {
    expect(() => taskFromForm({ ...base, webhook: "ftp://x" }, null, runners)).toThrow("webhook 地址应以");
    expect(() => taskFromForm({ ...base, onMissed: "window" }, null, runners)).toThrow("宽限时长");
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

// S9 与 docs/DESIGN.md:任务列表里的时间写成人读得懂的样子,认不出的 cron 原样显示。
describe("describeCron", () => {
  it("常见写法", () => {
    expect(describeCron("0 2 * * *")).toBe("每天 02:00");
    expect(describeCron("30 9 * * 1-5")).toBe("工作日 09:30");
    expect(describeCron("0 8,13,20 * * *")).toBe("每天 08:00、13:00、20:00");
    expect(describeCron("0 9 * * 1")).toBe("每周一 09:00");
    expect(describeCron("0 9 * * 7")).toBe("每周日 09:00");
    expect(describeCron("0 18 1 * *")).toBe("每月 1 日 18:00");
    expect(describeCron("0 * * * *")).toBe("每小时整点");
    expect(describeCron("*/15 * * * *")).toBe("每 15 分钟");
    expect(describeCron("*/1 * * * *")).toBe("每分钟");
    expect(describeCron("* * * * *")).toBe("每分钟");
    expect(describeCron("0 9-17 * * *")).toBe("0 9-17 * * *");
    expect(describeCron("0 9 * 3 *")).toBe("0 9 * 3 *");
  });
});

// S12:随机偏移按分钟填写;没改动时保留原来的写法;一次性任务没有偏移;列表里写在时间后面。
describe("随机偏移", () => {
  const daily: Task = { name: "post", trigger: { cron: "0 9 * * *", jitter: "10m0s" }, steps: [{ type: "script", script: { command: "true" } }] };

  it("回填、保存和清空", () => {
    const f = formFromTask(daily, runners);
    expect(f.jitter).toBe("10");
    expect(taskFromForm(f, daily, runners).trigger).toEqual({ cron: "0 9 * * *", jitter: "10m0s" });
    expect(taskFromForm({ ...f, jitter: "15" }, daily, runners).trigger).toEqual({ cron: "0 9 * * *", jitter: "15m" });
    expect(taskFromForm({ ...f, jitter: "" }, daily, runners).trigger).toEqual({ cron: "0 9 * * *" });
    expect(taskFromForm({ ...f, jitter: "0" }, daily, runners).trigger).toEqual({ cron: "0 9 * * *" });
    expect(() => taskFromForm({ ...f, jitter: "-5" }, daily, runners)).toThrow("随机偏移");
  });

  it("不足一分钟的偏移没改动时原样保留", () => {
    const odd: Task = { ...daily, trigger: { cron: "0 9 * * *", jitter: "1m30s" } };
    expect(taskFromForm(formFromTask(odd, runners), odd, runners).trigger.jitter).toBe("1m30s");
  });

  it("一次性任务不带偏移", () => {
    const f = { ...formFromTask(daily, runners), trigger: "at" as const, at: "2099-01-01T09:00" };
    expect(taskFromForm(f, daily, runners).trigger).not.toHaveProperty("jitter");
  });

  it("列表里写成 时间 ±偏移", () => {
    expect(describeTrigger({ cron: "0 9 * * *", jitter: "10m0s" })).toBe("每天 09:00 ±10 分钟");
    expect(describeTrigger({ cron: "0 9 * * *", jitter: "45s" })).toBe("每天 09:00 ±45 秒");
    expect(describeTrigger({ cron: "0 9 * * *" })).toBe("每天 09:00");
  });
});

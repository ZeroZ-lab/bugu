import { api, ApiError, taskPath, type Runner, type Task } from "./api";
import { Component, h, value, type Child } from "./dom";
import { n, t } from "./i18n";
import { describeCron, permissionText } from "./format";
import { StepCards } from "./StepCards";
import { agentRunners, formFromTask, permissionLevels, stepsFromSimple, taskFromForm, type TaskForm } from "./taskForm";
import { cronFromPick, EVERY, type Pick, type Repeat } from "./schedulePick";
import { Button, Choice, ErrorText, Field } from "./ui";

interface PageProps {
  name: string | null; // 要修改的任务名;null 表示新建
  onClose: () => void;
  onSaved: () => void;
  onError: (err: unknown) => void;
  onDirty: () => void; // 表单里有了没保存的修改
  onDelete: (name: string) => void; // 删除正在修改的任务,由外层确认
}

// TaskPage 是新建和修改任务的页面(#/new、#/edit/<任务名>)。先读运行器和要修改的任务,读到后再显示表单,
// 表单的初始值只在这时取一次。
export class TaskPage extends Component<PageProps> {
  static readonly tag = "bugu-task-page";
  private data: { task: Task | null; runners: Runner[] } | null = null;
  private missing = false;

  protected connected() {
    this.load();
  }

  protected propsChanged(prev: PageProps) {
    if (prev.name !== this.props.name) this.load();
  }

  private load() {
    const { name, onError } = this.props;
    const fresh = this.fresh();
    this.data = null;
    this.missing = false;
    Promise.all([api<Runner[]>("GET", "/runners"), name === null ? Promise.resolve(null) : api<Task>("GET", taskPath(name))]).then(
      ([runners, task]) => {
        if (!fresh()) return;
        this.data = { task, runners };
        this.update();
      },
      (err) => {
        if (!fresh()) return;
        if (err instanceof ApiError && err.code === "not_found") {
          this.missing = true;
          this.update();
        } else onError(err);
      },
    );
  }

  render(): Child {
    const { name, onClose, onSaved, onDirty, onDelete } = this.props;
    const title = name === null ? t("新建任务") : t("修改任务");
    if (this.missing || !this.data) {
      return h(
        "section",
        { class: "task-page" },
        h("div", { class: "head" }, h("h2", null, title)),
        this.missing && h("p", { class: "empty" }, t("没有名为 {name} 的任务,可能已被删除。", { name: name ?? "" }), h("br"), Button({ onclick: onClose }, t("返回任务列表"))),
      );
    }
    return h(TaskFormView, { key: name ?? "", title, task: this.data.task, runners: this.data.runners, onClose, onSaved, onDirty, onDelete });
  }
}

interface FormProps {
  title: string;
  task: Task | null; // null 表示新建
  runners: Runner[];
  onClose: () => void;
  onSaved: () => void;
  onDirty: () => void;
  onDelete: (name: string) => void;
}

const TRIGGERS = [
  ["cron", n("定时重复")],
  ["at", n("只运行一次")],
] as const;

const KINDS = [
  ["command", n("一条命令")],
  ["path", n("脚本文件")],
  ["agent", n("AI 命令行")],
  ["steps", n("多个步骤")],
] as const;

// 定时的重复方式(S9)。最后一项给会写 cron 的人直接填写。
const REPEATS = [
  ["daily", n("每天")],
  ["weekdays", n("工作日(周一到周五)")],
  ["weekly", n("每周")],
  ["monthly", n("每月")],
  ["hourly", n("每小时")],
  ["minutes", n("每隔几分钟")],
  ["cron", n("自定义(cron 表达式)")],
] as const;

// 星期从周一排到周日;值是 cron 里的写法,0 是周日。
const WEEK = [
  [1, n("周一")],
  [2, n("周二")],
  [3, n("周三")],
  [4, n("周四")],
  [5, n("周五")],
  [6, n("周六")],
  [0, n("周日")],
] as const;

const range = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => from + i);

// "其他"一栏里的各项。
const ADVANCED = ["timeout", "notify", "timezone", "onMissed", "missedWindow", "webhook", "webhookWhen", "silent", "envFile", "env"] as const;

class TaskFormView extends Component<FormProps> {
  static readonly tag = "bugu-task-form";
  private f!: TaskForm;
  private error = "";
  private more = false; // "其他"一栏一开始是否展开
  private dirty = false;

  protected connected() {
    if (this.f) return;
    const f = (this.f = formFromTask(this.props.task, this.props.runners));
    // 高级设置平时收起;修改的任务用到了其中某一项时展开,免得看不到已有的设置。
    const blank = formFromTask(null, this.props.runners);
    this.more = ADVANCED.some((key) => f[key] !== blank[key]);
  }

  private change(patch: Partial<TaskForm>) {
    this.f = { ...this.f, ...patch };
    if (!this.dirty) {
      this.dirty = true;
      this.props.onDirty();
    }
    this.update();
  }

  private pick(patch: Partial<Pick>) {
    this.change({ schedule: { ...this.f.schedule, ...patch } });
  }

  // 改成自定义时,先填上和刚才的选择等价的表达式,在它的基础上改。
  private setRepeat(repeat: Repeat) {
    const s = this.f.schedule;
    let cron = s.cron;
    if (repeat === "cron" && s.repeat !== "cron") {
      try {
        cron = cronFromPick(s, () => "");
      } catch {
        // 刚才的选择还不完整,沿用原来的表达式
      }
    }
    this.pick({ repeat, cron });
  }

  // scheduleFields 是定时重复的选项:先选重复方式,再按方式给出时间、星期、几号等(S9)。
  private scheduleFields(): Child {
    const s = this.f.schedule;
    const time = Field({ label: t("时间") }, h("input", { type: "time", value: s.time, required: true, oninput: (e: Event) => this.pick({ time: value(e) }) }));
    const repeat = Field(
      { label: t("重复") },
      h("select", { value: s.repeat, onchange: (e: Event) => this.setRepeat(value(e) as Repeat) }, REPEATS.map(([v, label]) => h("option", { value: v }, t(label)))),
    );
    // 重复方式总在第一行的同一个位置,换方式时下拉框还是那一个,键盘焦点不丢。
    const row = (...rest: Child[]) => h("div", { class: "pair" }, repeat, ...rest);
    switch (s.repeat) {
      case "daily":
      case "weekdays":
        return row(time);
      case "weekly":
        return [
          row(time),
          h(
            "div",
            { class: "days", role: "group", "aria-label": t("星期") },
            h("span", { class: "label" }, t("星期")),
            WEEK.map(([d, label]) =>
              h(
                "label",
                { class: "inline" },
                h("input", {
                  type: "checkbox",
                  checked: s.days.includes(d),
                  onchange: (e: Event) => this.pick({ days: (e.target as HTMLInputElement).checked ? [...s.days, d] : s.days.filter((x) => x !== d) }),
                }),
                t(label),
              ),
            ),
          ),
        ];
      case "monthly":
        return row(
          Field(
            { label: t("几号"), hint: t("没有这一天的月份不运行,比如 2 月没有 30 号。") },
            h("select", { value: String(s.dom), onchange: (e: Event) => this.pick({ dom: Number(value(e)) }) }, range(1, 31).map((d) => h("option", { value: String(d) }, t("{n} 号", { n: d })))),
          ),
          time,
        );
      case "hourly":
        return row(
          Field(
            { label: t("在第几分钟") },
            h("select", { value: String(s.minute), onchange: (e: Event) => this.pick({ minute: Number(value(e)) }) }, range(0, 59).map((m) => h("option", { value: String(m) }, `:${String(m).padStart(2, "0")}`))),
          ),
        );
      case "minutes":
        return row(
          Field(
            { label: t("间隔") },
            h("select", { value: String(s.every), onchange: (e: Event) => this.pick({ every: Number(value(e)) }) }, EVERY.map((m) => h("option", { value: String(m) }, t("{n} 分钟", { n: m })))),
          ),
        );
      case "cron": {
        const read = describeCron(s.cron);
        return [
          row(),
          Field(
            {
              label: t("cron 表达式"),
              hint: [t("五段:分 时 日 月 周,按下面的时区;没填时区就按这台主机的。"), s.cron.trim() && read !== s.cron.trim() ? " " + t("即:{text}", { text: read }) : ""],
            },
            h("input", { class: "mono", value: s.cron, oninput: (e: Event) => this.pick({ cron: value(e) }), placeholder: "0 9 * * 1-5", spellcheck: false }),
          ),
        ];
      }
    }
  }

  private chooseRunner(runner: string) {
    const next = permissionLevels(this.props.runners, runner);
    this.change({ runner, permission: next.includes(this.f.permission) ? this.f.permission : (next[0] ?? "") });
  }

  private submit = async (e: Event) => {
    e.preventDefault();
    const { task, runners, onSaved } = this.props;
    try {
      const out = taskFromForm(this.f, task, runners);
      if (task) await api("PUT", taskPath(out.name), out);
      else await api("POST", "/tasks", out);
      onSaved();
    } catch (err) {
      this.error = err instanceof Error ? err.message : String(err);
      this.update();
    }
  };

  render(): Child {
    const { title, task, runners, onClose, onDelete } = this.props;
    const f = this.f;
    const set = (key: keyof TaskForm) => (e: Event) => this.change({ [key]: value(e) });
    const usable = agentRunners(runners);
    const runnerMissing = f.runner !== "" && !usable.some((r) => r.name === f.runner);
    const levels = permissionLevels(runners, f.runner);
    const whenOptions = [
      h("option", { value: "on_content" }, t("失败时,或成功且有输出时")),
      h("option", { value: "on_result" }, t("仅有结果时,失败不发")),
      h("option", { value: "on_failure" }, t("仅失败时")),
      h("option", { value: "always" }, t("每次运行后")),
    ];

    return h(
      "section",
      { class: "task-page" },
      h(
        "form",
        { onsubmit: this.submit, "aria-labelledby": "task-page-title" },
        h("div", { class: "head" }, h("h2", { id: "task-page-title" }, title)),
        Field(
          { label: t("名称"), hint: t("字母、数字、- 和 _,创建后不能修改。") },
          h("input", { value: f.name, oninput: set("name"), disabled: task !== null, required: true, maxlength: 64, pattern: "[A-Za-z0-9][A-Za-z0-9_\\-]*", placeholder: "backup" }),
        ),

        h(
          "fieldset",
          null,
          h("legend", null, t("什么时候运行")),
          Choice({ name: "trigger", value: f.trigger, options: TRIGGERS.map(([v, label]) => [v, t(label)] as const), onChange: (v) => this.change({ trigger: v }) }),
          f.trigger === "cron"
            ? h(
                "div",
                { key: "cron" },
                this.scheduleFields(),
                Field(
                  { label: t("随机偏移,分钟(可选)"), hint: t("每次在原定时间前后这么多分钟内随机触发,不总在同一时刻;{date} 等仍按原定时间。") },
                  h("input", { type: "number", min: 0, step: 1, value: f.jitter, oninput: set("jitter"), placeholder: t("不偏移") }),
                ),
              )
            : Field({ label: t("时间") }, h("input", { type: "datetime-local", value: f.at, oninput: set("at") })),
        ),

        h(
          "fieldset",
          null,
          h("legend", null, t("运行什么")),
          Choice({
            name: "kind",
            value: f.kind,
            options: KINDS.map(([v, label]) => [v, t(label)] as const),
            // 切到多个步骤时,已经填的内容成为第一步。
            onChange: (v) => this.change({ kind: v, steps: v === "steps" && f.steps.length === 0 ? stepsFromSimple(f, runners) : f.steps }),
          }),
          f.kind === "steps" && h(StepCards, { steps: f.steps, runners, onChange: (steps) => this.change({ steps }) }),
          f.kind === "command" && Field({ label: t("命令"), hint: t("交给 /bin/sh 执行。") }, h("input", { value: f.command, oninput: set("command"), placeholder: "~/bin/backup.sh --all" })),
          f.kind === "path" && Field({ label: t("脚本文件的路径"), hint: t("文件需要有执行权限。") }, h("input", { value: f.path, oninput: set("path"), placeholder: "~/bin/backup.sh" })),
          f.kind === "agent" &&
            h(
              "div",
              null,
              Field(
                { label: t("运行器"), hint: usable.length || runnerMissing ? t("这台主机上已配置的 AI 命令行。能否启动可用 bugu runner check 检查。") : t("这台主机上没有可用的运行器。") },
                h(
                  "select",
                  { value: f.runner, onchange: (e: Event) => this.chooseRunner(value(e)) },
                  usable.map((r) => h("option", { value: r.name }, r.title ? `${r.title}(${r.name})` : r.name)),
                  runnerMissing && h("option", { value: f.runner }, t("{runner}(找不到这个运行器)", { runner: f.runner })),
                ),
              ),
              Field(
                { label: t("提示词"), hint: t("可以使用 {date}、{time}、{datetime}、{last_run}(上次成功运行的时间)、{task_name}、{run_dir}、{state_dir}(跨次运行保留的目录)。") },
                h("textarea", { rows: 6, value: f.prompt, oninput: set("prompt"), placeholder: t("列出 {last_run} 之后的新 issue,按 bug、需求、提问分类。只读,不要回复任何 issue。") }),
              ),
              levels.length > 0 &&
                Field(
                  { label: t("允许它做什么"), hint: t("运行时没有人在场,除非任务确实需要修改文件,否则保持只读。") },
                  h(
                    "select",
                    { value: f.permission, onchange: set("permission") },
                    levels.map((l, i) => h("option", { value: l }, i === 0 ? t("{level}(默认)", { level: permissionText(l) }) : permissionText(l))),
                  ),
                ),
            ),
          Field({ label: t("工作目录(可选)"), hint: t("不填时在这个任务的状态目录里运行,它跨次运行保留。") }, h("input", { value: f.cwd, oninput: set("cwd"), placeholder: "~/projects/site" })),
        ),

        h(
          "details",
          { class: "more", open: this.more },
          h("summary", null, t("其他")),
          Field(
            { label: t("最长运行时间,分钟(可选)"), hint: t("超过后终止并记为超时。") },
            h("input", { type: "number", min: 0, step: 1, value: f.timeout, oninput: set("timeout"), placeholder: t("不限") }),
          ),
          Field(
            { label: t("通知"), hint: t("系统通知由桌面应用发出。失败和结果可以分开:比如系统通知只管失败,结果用 webhook 发到别处。") },
            h("select", { value: f.notify, onchange: set("notify") }, whenOptions, h("option", { value: "" }, t("不通知"))),
          ),
          Field(
            { label: t("时区(可选)"), hint: t("IANA 时区名,如 Asia/Shanghai。cron 和 {date} 按它计算,不填用这台主机的时区。") },
            h("input", { value: f.timezone, oninput: set("timezone"), placeholder: "Asia/Shanghai" }),
          ),
          Field(
            { label: t("错过的触发"), hint: t("电脑睡眠或关机跨过了原定时间,醒来以后怎么办。") },
            h(
              "select",
              { value: f.onMissed, onchange: set("onMissed") },
              h("option", { value: "run_once" }, t("补跑最近一次")),
              h("option", { value: "window" }, t("只在宽限时间内补跑")),
              h("option", { value: "skip" }, t("不补跑")),
            ),
          ),
          f.onMissed === "window" &&
            Field(
              { label: t("宽限时间,分钟"), hint: t("原定时间过去不超过这么久才补跑。") },
              h("input", { type: "number", min: 1, step: 1, value: f.missedWindow, oninput: set("missedWindow"), placeholder: "120" }),
            ),
          Field(
            { label: t("Webhook 地址(可选)"), hint: t("运行结束后向这个地址 POST 一段 JSON。失败会自动重试。") },
            h("input", { type: "url", value: f.webhook, oninput: set("webhook"), placeholder: "https://example.com/hooks/bugu" }),
          ),
          f.webhook.trim() !== "" && Field({ label: t("Webhook 什么时候发") }, h("select", { value: f.webhookWhen, onchange: set("webhookWhen") }, whenOptions)),
          Field(
            { label: t("静默标记(可选)"), hint: t("输出包含这段文字时,本次记为无内容,不发通知。比如让 AI 在没有新内容时只输出 NOTHING_NEW。") },
            h("input", { value: f.silent, oninput: set("silent"), placeholder: "NOTHING_NEW" }),
          ),
          Field(
            { label: t("环境变量文件(可选)"), hint: t("每行一个 名=值,用来放密钥,不写进任务定义。其中的值在输出和通知里会被隐藏。") },
            h("input", { value: f.envFile, oninput: set("envFile"), placeholder: "~/.config/bugu/news.env" }),
          ),
          Field({ label: t("环境变量(可选,每行一个 名=值)") }, h("textarea", { rows: 2, value: f.env, oninput: set("env"), placeholder: "TARGET=/data" })),
        ),

        // 保存和取消贴在窗口底部,出错的说明也放在这一条里:表单很长,放在表单末尾会落在屏幕外。
        h(
          "div",
          { class: "actions" },
          task && Button({ variant: "danger", onclick: () => onDelete(task.name) }, t("删除任务")),
          h("div", { class: "grow" }, ErrorText(this.error)),
          Button({ onclick: onClose }, t("取消")),
          Button({ type: "submit", variant: "primary" }, t("保存")),
        ),
      ),
    );
  }
}

customElements.define(TaskPage.tag, TaskPage);
customElements.define(TaskFormView.tag, TaskFormView);

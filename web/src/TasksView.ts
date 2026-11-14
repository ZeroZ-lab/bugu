import { api, taskPath, type Task } from "./api";
import { Component, h, type Child } from "./dom";
import { t } from "./i18n";
import { describeTrigger, formatTime, friendlyTime, statusOf } from "./format";
import { getCollapsed, groupByDir, setCollapsed, type TaskGroup } from "./taskGroups";
import { Button, Mark } from "./ui";

interface Props {
  tick: number;
  onLoaded: () => void;
  onError: (err: unknown) => void;
  // act 执行一个操作,出错时显示后台的说明,之后刷新。
  act: (fn: () => Promise<unknown>) => void;
  onNew: () => void;
  onEdit: (task: Task) => void;
  onOpenRun: (runId: string) => void;
}

export class TasksView extends Component<Props> {
  static readonly tag = "bugu-tasks";
  private tasks: Task[] | null = null;
  private collapsed = getCollapsed(); // 折起来的组,按目录记

  protected connected() {
    this.load();
  }

  // 只在定期刷新或操作之后重新读取。
  protected propsChanged(prev: Props) {
    if (prev.tick !== this.props.tick) this.load();
  }

  private load() {
    const fresh = this.fresh();
    api<Task[]>("GET", "/tasks").then(
      (tasks) => {
        if (!fresh()) return;
        this.tasks = tasks;
        this.props.onLoaded();
        this.update();
      },
      (err) => fresh() && this.props.onError(err),
    );
  }

  private row(task: Task): Child {
    const { act, onEdit, onOpenRun } = this.props;
    const last = task.last_run;
    const [lastLabel, lastKind] = last ? statusOf(last.status) : ["", ""];
    return h(
      "tr",
      { key: task.name },
      h("td", { class: "mono name", title: task.name }, task.name),
      h("td", { class: "time sched", title: task.trigger.cron }, describeTrigger(task.trigger)),
      h("td", { class: "state" }, task.running ? Mark("spin ink", t("运行中")) : task.paused ? Mark("ring quiet", t("已暂停")) : Mark("ring", t("等待"))),
      // 上次结果:失败的任务在列表里就能看出来;点它打开那次运行。
      h(
        "td",
        { class: "last" },
        last
          ? h(
              "button",
              { type: "button", class: "link", title: formatTime(last.finished_at || last.started_at), onclick: () => onOpenRun(last.id) },
              h("span", { class: "narrow-only" }, t("上次"), " "),
              Mark(lastKind, lastLabel),
              h("span", { class: "time" }, " " + friendlyTime(last.finished_at || last.started_at)),
            )
          : h("span", { class: "muted" }, h("span", { class: "narrow-only" }, t("上次"), " "), "-"),
      ),
      h("td", { class: "time next", title: task.next_run && formatTime(task.next_run) }, h("span", { class: "narrow-only" }, t("下次"), " "), friendlyTime(task.next_run)),
      h(
        "td",
        { class: "ops-cell" },
        h(
          "div",
          { class: "ops" },
          Button(
            {
              disabled: task.running === true,
              title: task.running ? t("正在运行,结束后才能再次运行。") : undefined,
              onclick: () => act(async () => onOpenRun((await api<{ id: string }>("POST", taskPath(task.name, "run"))).id)),
            },
            t("立即运行"),
          ),
          // 两个字样叠在一起、只显示一个,按钮取较宽者的宽度:各行的按钮因此上下对齐,不随暂停与否错开。
          Button(
            { class: "swap", onclick: () => act(() => api("POST", taskPath(task.name, task.paused ? "resume" : "pause"))) },
            h("span", null, task.paused ? t("恢复") : t("暂停")),
            h("span", { "aria-hidden": "true" }, task.paused ? t("暂停") : t("恢复")),
          ),
          Button({ onclick: () => onEdit(task) }, t("编辑")),
        ),
      ),
    );
  }

  private toggle(key: string) {
    if (!this.collapsed.delete(key)) this.collapsed.add(key);
    setCollapsed(this.collapsed);
    this.update();
  }

  // group 是一组任务:组头写目录名、任务数和上次失败的个数,点组头折起或展开。
  private group(g: TaskGroup<Task>): Child {
    const open = !this.collapsed.has(g.key);
    // 失败和中断都算:组折起来时,出问题的组也要一眼看得出。
    const failed = g.tasks.filter((task) => {
      const last = task.last_run;
      return last !== undefined && statusOf(last.status)[1].includes("bad");
    }).length;
    return h(
      "tbody",
      { key: "dir:" + g.key },
      h(
        "tr",
        { class: "group" },
        h(
          "th",
          { colspan: 6, scope: "colgroup" },
          h(
            "button",
            { type: "button", class: "group-toggle", "aria-expanded": open ? "true" : "false", title: g.key || undefined, onclick: () => this.toggle(g.key) },
            h("span", { class: "chevron", "aria-hidden": "true" }),
            h("span", { class: g.key ? "mono" : undefined }, g.key ? g.label : t("未指定目录")),
            h("span", { class: "muted count" }, String(g.tasks.length)),
            failed > 0 && Mark("solid bad", t("{n} 个失败", { n: failed })),
          ),
        ),
      ),
      open && g.tasks.map((task) => this.row(task)),
    );
  }

  render(): Child {
    const tasks = this.tasks;
    return h(
      "section",
      null,
      h("div", { class: "head" }, h("h2", null, t("任务")), Button({ variant: "primary", onclick: this.props.onNew }, t("新建任务"))),
      tasks && tasks.length === 0 && h("p", { class: "empty" }, t("还没有任务。点\"新建任务\",让一条命令、一个脚本或一个 AI 命令行按时运行。")),
      tasks &&
        tasks.length > 0 &&
        h(
          "div",
          { class: "scroll" },
          h(
            "table",
            { class: "tasks" },
            h("thead", null, h("tr", null, h("th", null, t("名称")), h("th", null, t("触发")), h("th", null, t("状态")), h("th", null, t("上次结果")), h("th", null, t("下次运行")), h("th"))),
            // 只有一个目录时不分组,和原来一样平铺。
            ((groups) => (groups.length < 2 ? h("tbody", null, tasks.map((task) => this.row(task))) : groups.map((g) => this.group(g))))(groupByDir(tasks)),
          ),
        ),
    );
  }
}

customElements.define(TasksView.tag, TasksView);

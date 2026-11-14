import { api, type Run, type Task } from "./api";
import { Component, h, value, type Child } from "./dom";
import { t } from "./i18n";
import { formatTime, friendlyTime, statusOf } from "./format";
import { Mark } from "./ui";

interface Props {
  tick: number;
  onLoaded: () => void;
  onError: (err: unknown) => void;
  onOpen: (runId: string) => void;
}

export class RunsView extends Component<Props> {
  static readonly tag = "bugu-runs";
  private taskNames: string[] = [];
  private filter = "";
  private runs: Run[] | null = null;

  protected connected() {
    this.load();
  }

  protected propsChanged(prev: Props) {
    if (prev.tick !== this.props.tick) this.load();
  }

  private load() {
    const fresh = this.fresh();
    const filter = this.filter;
    const query = new URLSearchParams({ limit: "100" });
    if (filter) query.set("task", filter);
    Promise.all([api<Task[]>("GET", "/tasks"), api<Run[]>("GET", "/runs?" + query)]).then(
      ([tasks, data]) => {
        if (!fresh()) return;
        this.taskNames = tasks.map((task) => task.name);
        this.runs = data;
        this.props.onLoaded();
        this.update();
        // 筛选的任务已经被删除:回到全部。
        if (filter && !this.taskNames.includes(filter)) this.choose("");
      },
      (err) => fresh() && this.props.onError(err),
    );
  }

  private choose(filter: string) {
    this.filter = filter;
    this.update();
    this.load();
  }

  render(): Child {
    const { onOpen } = this.props;
    const runs = this.runs;
    return h(
      "section",
      null,
      h(
        "div",
        { class: "head" },
        h("h2", null, t("运行记录")),
        h(
          "label",
          { class: "inline" },
          t("按任务"),
          " ",
          h("select", { value: this.filter, onchange: (e: Event) => this.choose(value(e)) }, h("option", { value: "" }, t("全部")), this.taskNames.map((name) => h("option", { value: name }, name))),
        ),
      ),
      runs && runs.length === 0 && h("p", { class: "empty" }, t("还没有运行记录。")),
      runs &&
        runs.length > 0 &&
        h(
          "div",
          { class: "scroll" },
          h(
            "table",
            { class: "clickable" },
            h("thead", null, h("tr", null, h("th", null, t("开始时间")), h("th", null, t("任务名")), h("th", null, t("状态")), h("th", null, t("退出码")), h("th", null, t("来源")), h("th", null, t("说明")))),
            h(
              "tbody",
              null,
              runs.map((r) => {
                const [label, kind] = statusOf(r.status);
                // 单步任务失败时,后台的原因只是退出码,和旁边那一列重复;改用输出的开头,那里通常写着出了什么事。
                const why = r.status === "failed" && (r.steps?.length ?? 0) <= 1 ? (r.summary ?? "") : r.error || "";
                const note = [r.note || why, r.no_content ? t("无内容") : ""].filter(Boolean).join(t(";"));
                const started = r.started_at || r.created_at;
                return h(
                  "tr",
                  { key: r.id, tabindex: 0, onclick: () => onOpen(r.id), onkeydown: (e: KeyboardEvent) => e.key === "Enter" && onOpen(r.id) },
                  h("td", { class: "time", title: formatTime(started) }, friendlyTime(started)),
                  h("td", { class: "mono task", title: r.task }, r.task),
                  h("td", { class: "status" }, Mark(kind, label)),
                  h("td", { class: "extra" }, r.exit_code === undefined ? "-" : String(r.exit_code)),
                  h("td", { class: "extra" }, r.trigger === "manual" ? t("手动") : t("定时")),
                  h("td", { class: "wrap" }, note),
                );
              }),
            ),
          ),
        ),
    );
  }
}

customElements.define(RunsView.tag, RunsView);

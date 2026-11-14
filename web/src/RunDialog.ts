import { api, runPath, type Delivery, type Logs, type Run } from "./api";
import { Component, h, type Child } from "./dom";
import { t } from "./i18n";
import { formatTime, permissionText, statusOf, stepType } from "./format";
import { Markdown } from "./Markdown";
import { Button, Mark, Modal } from "./ui";

interface Props {
  id: string;
  tick: number;
  onClose: () => void;
  onError: (err: unknown) => void;
  onRead: () => void; // 未读的结果被打开、标为已读之后
}

const FOLLOW_MS = 1000;

// RunDialog 显示一次运行的详情和输出。运行还没结束时跟着定期刷新更新。
export class RunDialog extends Component<Props> {
  static readonly tag = "bugu-run-dialog";
  private data: { run: Run; logs: Logs; deliveries: Delivery[] } | null = null;
  private readAsked = false;
  private timer: number | undefined;

  private get done() {
    return this.data !== null && this.data.run.status !== "queued" && this.data.run.status !== "running";
  }

  protected connected() {
    this.load();
  }

  protected disconnected() {
    clearTimeout(this.timer);
  }

  protected propsChanged(prev: Props) {
    if (prev.tick !== this.props.tick) this.load();
  }

  private load() {
    if (this.done) return;
    const { id, onError, onClose } = this.props;
    const fresh = this.fresh();
    clearTimeout(this.timer);
    Promise.all([api<Run>("GET", runPath(id)), api<Logs>("GET", runPath(id, "logs")), api<Delivery[]>("GET", runPath(id, "deliveries"))]).then(
      ([run, logs, deliveries]) => {
        if (!fresh()) return;
        this.data = { run, logs, deliveries };
        this.update();
        // 还没结束时每秒再看一次,不等页面 5 秒一次的刷新:大多数任务几秒内就有结果。
        if (!this.done) this.timer = window.setTimeout(() => this.load(), FOLLOW_MS);
      },
      (err) => {
        if (!fresh()) return;
        onError(err);
        if (!this.data) onClose();
      },
    );
  }

  // 运行中的输出(G6)每次更新后滚到最新的一行;用户往上翻过就不再跟着滚,翻回底部后接着跟。
  protected rendered() {
    for (const el of this.querySelectorAll<HTMLElement>("pre.live")) if (el.dataset.follow !== "off") el.scrollTop = el.scrollHeight;
    this.markRead();
  }

  // 打开就算读过了(G3)。不在收件箱里的运行,后台会原样返回。
  private markRead() {
    if (this.readAsked || !this.done || this.data!.run.read_at) return;
    this.readAsked = true;
    api("POST", runPath(this.props.id, "read")).then(this.props.onRead, this.props.onError);
  }

  // showStep 展开原始输出,滚到这一步的完整输出。
  private showStep(i: number) {
    const raw = this.querySelector<HTMLDetailsElement>("details.raw");
    if (raw) raw.open = true;
    document.getElementById(`step-${i}`)?.scrollIntoView({ block: "start" });
  }

  render(): Child {
    if (!this.data) return null;
    const { onClose } = this.props;
    const { run: r, logs, deliveries } = this.data;
    const steps = (r.steps?.length ?? 0) > 1 ? r.steps! : null;
    const rows: [string, string][] = [
      [t("运行编号"), r.id],
      [t("开始"), formatTime(r.started_at || r.created_at)],
      [t("结束"), formatTime(r.finished_at)],
      [t("退出码"), r.exit_code === undefined ? "-" : String(r.exit_code)],
    ];
    // 多步骤的运行里,这一项只是其中一步的命令,容易被当成整个任务的;各步的内容看下面每一步。
    if (!steps) rows.push([t("命令"), (r.command ?? []).join(" ") || "-"]);
    if (r.permission) rows.push([t("权限"), permissionText(r.permission)]);
    // 成功且有输出时,先按 Markdown 显示结果,原始输出收起来(G3)。
    const asMarkdown = r.status === "succeeded" && !r.no_content && logs.stdout.trim() !== "";
    if (r.scheduled_for) rows.push([t("原定时间"), formatTime(r.scheduled_for)]);
    if (r.no_content) rows.push([t("结果"), noContentReason(r)]);
    if (r.artifacts?.length) rows.push([t("产物"), r.artifacts.join("\n")]);
    if (deliveries.length) rows.push([t("通知"), deliveries.map(describeDelivery).join("\n")]);
    if (r.note) rows.push([t("说明"), r.note]);
    if (r.error) rows.push([t("原因"), r.error]);

    const empty = t("(空)");
    const runFailed = r.status === "failed" || r.status === "timed_out" || r.status === "interrupted";
    // 运行中正在执行的那一步:它的输出随刷新增长(G6)。
    const running = r.status === "running";
    const pre = (text: string, isLive: boolean, cls?: string) =>
      h("pre", { class: [cls, isLive && "live"].filter(Boolean).join(" ") || undefined, "data-empty": empty, onscroll: isLive ? follow : undefined }, text);
    const stdout = [h("h3", null, t("标准输出")), pre(logs.stdout, running && !steps)];
    const stderr = [h("h3", null, t("标准错误")), pre(logs.stderr, running && !steps)];

    return Modal(
      { wide: true, onClose },
      h("div", { class: "head" }, h("h2", null, `${r.task} · `, Mark(statusOf(r.status)[1], statusOf(r.status)[0])), Button({ onclick: onClose }, t("关闭"))),
      h("dl", null, rows.map(([k, v]) => [h("dt", null, k), h("dd", null, v)])),
      // 每一步一张卡片(T9):状态和输出的第一行;运行中随刷新更新。点卡片跳到这一步的完整输出。
      steps &&
        h(
          "ol",
          { class: "run-steps", "aria-label": t("步骤") },
          steps.map((s, i) => {
            const [label, kind] = statusOf(s.status);
            const out = logs.steps?.[i];
            // 失败的步骤先看标准错误,那里通常写着原因;其余的看输出。
            const failed = s.status === "failed" || s.status === "timed_out";
            const first = (failed ? out?.stderr || s.error || out?.stdout : out?.stdout || out?.stderr || s.error) ?? "";
            const line = first.trim().split("\n")[0];
            return h(
              "li",
              null,
              h(
                "button",
                { type: "button", onclick: () => this.showStep(i) },
                h("strong", null, t("第 {n} 步", { n: i + 1 })),
                " ",
                Mark(kind, s.nothing_to_do ? t("无事可做") : label),
                h("span", { class: "first" }, (s.name ? `${s.name} · ` : "") + stepType(s.type)),
                line && h("span", { class: "first" }, line),
              ),
            );
          }),
        ),
      asMarkdown && h("section", { class: "result" }, h("h3", null, t("结果")), Markdown(logs.stdout)),
      h(
        "details",
        { class: "raw", open: !asMarkdown },
        h("summary", null, t("原始输出"), running && h("span", { class: "muted" }, " · " + t("运行中,每秒更新"))),
        steps
          ? steps.map((s, i) => {
              const [label, kind] = statusOf(s.status);
              const out = logs.steps?.[i];
              const isLive = running && s.status === "running";
              return h(
                "section",
                { id: `step-${i}`, class: "step" },
                h("h3", null, `${t("第 {n} 步", { n: i + 1 })} · ${s.name ? `${s.name} · ` : ""}${stepType(s.type)} `, Mark(kind, s.nothing_to_do ? t("无事可做") : label)),
                s.error && h("p", { class: "error" }, s.error),
                // 有标准错误时标明哪个是哪个;输出为空又有标准错误的,只显示标准错误。
                s.status !== "skipped" && (out?.stdout || !out?.stderr) && [out?.stderr && h("h4", null, t("标准输出")), pre(out?.stdout ?? "", isLive)],
                out?.stderr && [h("h4", null, t("标准错误")), pre(out.stderr, isLive, "stderr")],
              );
            })
          : // 失败时先看标准错误,那里通常写着原因。
            (runFailed ? [stderr, stdout] : [stdout, stderr]),
      ),
    );
  }
}

customElements.define(RunDialog.tag, RunDialog);

// follow 记下用户是否停在输出的底部:停在底部时更新后继续滚到最新,往上翻过就不动。
function follow(e: Event) {
  const el = e.currentTarget as HTMLElement;
  el.dataset.follow = el.scrollTop + el.clientHeight >= el.scrollHeight - 8 ? "" : "off";
}

// describeDelivery 用一行说明一条通知的投递情况(N7)。
function describeDelivery(d: Delivery): string {
  const target = d.channel === "webhook" ? `Webhook ${d.target ?? ""}` : t("系统通知");
  const error = d.error ?? "";
  switch (d.status) {
    case "delivered":
      return t("{target}:已送达", { target });
    case "pending":
      return t("{target}:第 {n} 次没有送达,{time} 重试。{error}", { target, n: d.attempts, time: formatTime(d.next_attempt), error });
    case "dropped":
      return t("{target}:没有发出。{error}", { target, error });
    default:
      return t("{target}:没有送达(试了 {n} 次)。{error}", { target, n: d.attempts, error });
  }
}

// noContentReason 说明这次运行为什么算作无内容(T7、O4)。
function noContentReason(r: Run): string {
  const i = r.steps?.findIndex((s) => s.nothing_to_do) ?? -1;
  if (i < 0) return t("无内容:输出里有静默标记,没有发通知");
  if (r.steps!.length > 1) return t("无内容:第 {n} 步声明无事可做,后面没有继续,也没有发通知", { n: i + 1 });
  return t("无内容:脚本声明无事可做,后面没有继续,也没有发通知");
}

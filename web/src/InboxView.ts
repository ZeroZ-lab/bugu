import { api, type Inbox } from "./api";
import { Component, h, type Child } from "./dom";
import { n, t } from "./i18n";
import { formatTime, friendlyTime, statusOf } from "./format";
import { Button, Choice, Mark } from "./ui";

interface Props {
  tick: number;
  onLoaded: () => void;
  onError: (err: unknown) => void;
  onOpen: (runId: string) => void;
  act: (fn: () => Promise<unknown>) => void;
}

type Show = "unread" | "all";

const SHOW = [
  ["unread", n("未读")],
  ["all", n("全部")],
] as const;

// InboxView 是收件箱:有结果要看的运行,未读的在前面加一个墨色实心点(G3)。点开即标为已读。
export class InboxView extends Component<Props> {
  static readonly tag = "bugu-inbox";
  private show: Show = "unread";
  private inbox: Inbox | null = null;

  protected connected() {
    this.load();
  }

  protected propsChanged(prev: Props) {
    if (prev.tick !== this.props.tick) this.load();
  }

  private load() {
    const fresh = this.fresh();
    api<Inbox>("GET", "/inbox" + (this.show === "all" ? "?all=1" : "")).then(
      (data) => {
        if (!fresh()) return;
        this.inbox = data;
        this.props.onLoaded();
        this.update();
      },
      (err) => fresh() && this.props.onError(err),
    );
  }

  private choose = (show: Show) => {
    this.show = show;
    this.update();
    this.load();
  };

  render(): Child {
    const { onOpen, act } = this.props;
    const { show, inbox } = this;
    return h(
      "section",
      null,
      h(
        "div",
        { class: "head" },
        h("h2", null, t("收件箱")),
        h(
          "span",
          { class: "inline-group" },
          Choice({ name: "inbox-show", value: show, options: SHOW.map(([v, label]) => [v, t(label)] as const), onChange: this.choose }),
          inbox !== null && inbox.unread > 0 && Button({ onclick: () => act(() => api("POST", "/inbox/read")) }, t("全部标为已读")),
        ),
      ),
      inbox && inbox.runs.length === 0 && h("p", { class: "empty" }, show === "unread" ? t("没有未读的结果。任务有了新结果或失败时会出现在这里。") : t("还没有结果。任务有了新结果或失败时会出现在这里。")),
      inbox &&
        inbox.runs.length > 0 &&
        h(
          "div",
          { class: "scroll" },
          h(
            "table",
            { class: "clickable inbox" },
            h("thead", null, h("tr", null, h("th", null, h("span", { class: "sr-only" }, t("未读"))), h("th", null, t("完成时间")), h("th", null, t("任务名")), h("th", null, t("状态")), h("th", null, t("摘要")))),
            h(
              "tbody",
              null,
              inbox.runs.map((r) => {
                const [label, kind] = statusOf(r.status);
                const unread = !r.read_at;
                const finished = r.finished_at || r.started_at;
                return h(
                  "tr",
                  { key: r.id, tabindex: 0, class: unread ? "unread" : undefined, onclick: () => onOpen(r.id), onkeydown: (e: KeyboardEvent) => e.key === "Enter" && onOpen(r.id) },
                  h("td", { class: "dot-cell" }, unread && h("span", { class: "unread-dot", "aria-label": t("未读") })),
                  h("td", { class: "time", title: formatTime(finished) }, friendlyTime(finished)),
                  h("td", { class: "mono task", title: r.task }, r.task),
                  h("td", { class: "status" }, Mark(kind, label)),
                  h("td", { class: "wrap" }, r.summary ?? ""),
                );
              }),
            ),
          ),
        ),
    );
  }
}

customElements.define(InboxView.tag, InboxView);

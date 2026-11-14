// 布谷的网页前端。只通过后台的 JSON 接口工作,应用窗口和浏览器里是同一份。
// 来自后台的内容都作为文本渲染,不拼接 HTML。
import { api, ApiError, taskPath, clearToken, currentRoute, getHome, goRoute, leaveRoute, getToken, isRemote, openHost, remoteName, setToken, takeHomeFromHash, takeOpenFromHash, takeRunFromHash, takeTokenFromHash, type Host, type Inbox, type Route, type Settings } from "./api";
import { Component, h, type Child } from "./dom";
import { browserLang, lang, n, setLang, t, tx } from "./i18n";
import { HostSwitcher } from "./HostSwitcher";
import { Hosts } from "./hosts";
import { InboxView } from "./InboxView";
import { RunDialog } from "./RunDialog";
import { RunsView } from "./RunsView";
import { SettingsView } from "./SettingsView";
import { TaskPage } from "./TaskPage";
import { TasksView } from "./TasksView";
import { Button, ErrorText, Field, Logo } from "./ui";

const REFRESH_MS = 5000;

type View = "tasks" | "inbox" | "runs" | "settings";

const VIEWS = [
  ["tasks", n("任务")],
  ["inbox", n("收件箱")],
  ["runs", n("运行记录")],
  ["settings", n("设置")],
] as const;

const message = (err: unknown) => (err instanceof Error ? err.message : String(err));

export class App extends Component {
  static readonly tag = "bugu-app";
  private authed = false;
  private loginError = "";
  private view: View = "tasks";
  private pageError = "";
  private online = false; // 最近一次读取成功
  private tick = 0;
  private runId: string | null = null;
  private unread = 0;
  private route: Route = null; // 新建或修改任务的页面
  private langSetting: Settings["language"] | null = null;
  // remote 是正在管理的另一台主机的名字:本机登记的名字,其次是它自己的主机名,再次是地址;本机时为空。
  private remote = "";
  private hosts = new Hosts(() => this.update()); // 顶栏的机器下拉框用;另一台主机的页面读不到登记表

  // 下面几项记录"上一次渲染时的情况",rendered 据此决定要不要读取、要不要开关定时刷新。
  private wasAuthed = false;
  private polled = -1;
  private hostsOn = false;
  private timer: number | undefined;
  private formDirty = false; // 任务表单里有没保存的修改
  private actionFailed = false; // 页面上的报错来自用户的操作:留着,不被下一次刷新清掉
  private reloading = false; // 点了顶栏的"刷新",还在等回应

  protected connected() {
    takeHomeFromHash();
    takeTokenFromHash();
    this.authed = getToken() !== "";
    this.remote = isRemote() ? remoteName() || location.host : "";
    this.readHash();
    this.syncRoute();
    window.addEventListener("hashchange", this.readHash);
    window.addEventListener("hashchange", this.syncRoute);
    window.addEventListener("popstate", this.syncRoute);
  }

  protected disconnected() {
    window.removeEventListener("hashchange", this.readHash);
    window.removeEventListener("hashchange", this.syncRoute);
    window.removeEventListener("popstate", this.syncRoute);
    clearInterval(this.timer);
    this.timer = undefined;
  }

  // 桌面应用会通过网址片段送来令牌(#token=)和要打开的运行(#run=,点通知时)。
  private readHash = () => {
    takeHomeFromHash();
    this.remote = isRemote() ? remoteName() || this.remote || location.host : "";
    takeTokenFromHash();
    this.update();
    if (!getToken()) return;
    this.authed = true;
    const id = takeRunFromHash();
    if (id) {
      // 点通知打开的:不在收件箱时切到运行记录,关掉详情后看到的是这次运行所在的列表。
      if (this.view !== "inbox") this.view = "runs";
      this.openRun(id);
    }
    // 在另一台主机的面板里选了别的主机:由本机打开它(令牌只在本机)。
    const open = takeOpenFromHash();
    if (open) {
      api<Host[]>("GET", "/hosts?probe=0").then((list) => {
        const host = list.find((x) => x.name === open);
        if (host) openHost(host, list.map((x) => x.name));
        else {
          this.pageError = t("没有登记名为 {name} 的主机。", { name: open });
          this.update();
        }
      }, this.onError);
    }
  };

  // 浏览器前进后退、手改地址时,页面跟着地址走。
  private syncRoute = () => {
    this.route = currentRoute();
    // 进入表单页时关掉运行详情:对话框是模态的,留着它表单就点不了。
    if (this.route) {
      this.view = "tasks";
      this.runId = null;
    } else this.formDirty = false;
    this.update();
  };

  private refresh = () => {
    this.tick++;
    this.update();
  };

  // reloadAll 是顶栏的"刷新":应用窗口里没有浏览器的刷新键,用它重新读取当前页面、未读条数和主机状态。
  // 只重新读数据,不重新载入页面,所以打开的运行详情、所在的页都留着。
  private reloadAll = () => {
    this.reloading = true;
    this.clearError();
    if (!this.remote) this.hosts.reload();
    this.refresh();
  };

  // 令牌失效时回到连接页;其他错误显示在页面上。
  private onError = (err: unknown) => {
    if (err instanceof ApiError && err.code === "unauthorized") {
      clearToken();
      this.authed = false;
      this.loginError = t("令牌无效,请重新填写。");
    } else {
      this.online = false;
      this.pageError = message(err);
    }
    this.update();
  };

  private onLoaded = () => {
    this.online = true;
    if (!this.actionFailed) this.pageError = "";
    this.update();
  };

  // act 执行用户的一个操作。失败的说明留在页面上,直到下一个操作或换页。
  private act = (fn: () => Promise<unknown>) => {
    this.clearError();
    fn().then(this.refresh, (err) => {
      this.onError(err);
      this.actionFailed = true;
    });
  };

  private clearError() {
    this.pageError = "";
    this.actionFailed = false;
    this.update();
  }

  private showView(v: View) {
    if (this.route && !this.closeRoute()) return;
    this.view = v;
    this.clearError();
  }

  private openRoute(r: Exclude<Route, null>) {
    goRoute(r);
    this.route = r;
    this.view = "tasks";
    this.runId = null;
    this.formDirty = false;
    this.clearError();
  }

  // closeRoute 离开表单页,返回是否离开了。有没保存的修改时先问一句,sure 为真时不问(刚保存或刚删除)。
  // 后退时由 popstate 更新页面。
  private closeRoute(sure = false): boolean {
    if (!sure && this.formDirty && !confirm(t("放弃没有保存的修改?"))) return false;
    this.formDirty = false;
    if (leaveRoute()) this.route = null;
    this.update();
    return true;
  }

  // deleteTask 删除正在修改的任务。管理的是另一台主机时,确认里写明是哪台(G5)。
  private deleteTask(name: string) {
    const host = this.remote;
    const question = host ? t("删除 {host} 上的任务 {name}?运行记录会保留。", { host, name }) : t("删除任务 {name}?运行记录会保留。", { name });
    if (!confirm(question)) return;
    this.act(async () => {
      await api("DELETE", taskPath(name));
      this.closeRoute(true);
    });
  }

  // openRun 打开一次运行的详情,留在当前这一页。
  private openRun = (id: string) => {
    this.runId = id;
    this.update();
  };

  // 界面语言跟着后台的设置走;设置为跟随系统时用浏览器的语言(决策记录 0014)。
  // 随刷新重新读取,在命令行或另一个窗口里改了设置,这里也会跟上。
  private applyLang = (s: Settings) => {
    this.langSetting = s.language;
    const l = s.language === "auto" ? browserLang() : s.language;
    if (l !== lang()) setLang(l);
    this.update();
  };

  // 选项先跟着变,保存失败时下一次刷新会读回后台的设置。
  private chooseLang = (language: Settings["language"]) => {
    this.langSetting = language;
    this.update();
    api<Settings>("PUT", "/settings", { language }).then(this.applyLang, this.onError);
  };

  private login = async (e: Event) => {
    e.preventDefault();
    const input = (e.currentTarget as HTMLFormElement).elements.namedItem("token") as HTMLInputElement;
    setToken(input.value.trim());
    try {
      await api("GET", "/health");
      input.value = "";
      this.loginError = "";
      this.authed = true;
      this.pageError = "";
      this.refresh();
    } catch (err) {
      clearToken();
      this.loginError = err instanceof ApiError && err.code === "unauthorized" ? t("令牌无效。") : message(err);
      this.update();
    }
  };

  // poll 读取导航上的未读条数和界面语言,每次刷新读一遍。出错由当前页面报告。
  private poll() {
    const inbox = this.fresh("inbox");
    api<Inbox>("GET", "/inbox").then(
      (data) => {
        if (!inbox()) return;
        this.unread = data.unread;
        this.reloading = false;
        this.update();
      },
      () => {
        if (!inbox()) return;
        this.reloading = false;
        this.update();
      },
    );
    const settings = this.fresh("settings");
    api<Settings>("GET", "/settings").then(
      (s) => settings() && this.applyLang(s),
      () => {},
    );
  }

  protected rendered() {
    const { authed, remote } = this;
    // 窗口和标签页的标题:另一台主机时以它的名字开头,在程序坞和标签上也能分辨。应用窗口的标题跟着它。
    document.title = remote ? `${remote} · ${t("布谷")}` : t("布谷");

    // 直接用浏览器打开的另一台主机没有登记的名字,用它自己报的主机名。
    if (authed && !this.wasAuthed && isRemote() && !remoteName()) {
      api<{ host?: string }>("GET", "/health").then(
        (health) => {
          if (!health.host) return;
          this.remote = health.host;
          this.update();
        },
        () => {},
      );
    }
    this.wasAuthed = authed;

    const tick = authed ? this.tick : -1;
    if (tick !== this.polled) {
      this.polled = tick;
      if (authed) this.poll();
    }

    const hostsOn = authed && !remote;
    if (hostsOn && !this.hostsOn) this.hosts.reload();
    this.hostsOn = hostsOn;

    // 页面可见且没有在编辑任务时定期刷新,让状态和下次运行时间保持最新。
    const timerOn = authed && !this.route;
    if (timerOn && this.timer === undefined) {
      this.timer = window.setInterval(() => {
        if (!document.hidden) this.refresh();
      }, REFRESH_MS);
    } else if (!timerOn && this.timer !== undefined) {
      clearInterval(this.timer);
      this.timer = undefined;
    }
  }

  private page(): Child {
    const { route, view, tick } = this;
    const { onLoaded, onError, act, openRun } = this;
    if (!this.authed) {
      return h(
        "section",
        null,
        h(
          "form",
          { class: "card narrow", onsubmit: this.login },
          h("h2", null, t("连接后台")),
          h(
            "p",
            { class: "muted" },
            tx("粘贴这台主机的访问令牌。本机的令牌保存在数据目录的 {file} 文件里,默认是 {path}。", { file: h("code", null, "token"), path: h("code", null, "~/.bugu/token") }),
          ),
          Field({ label: t("令牌") }, h("input", { name: "token", type: "password", autocomplete: "off", required: true })),
          ErrorText(this.loginError),
          Button({ type: "submit", variant: "primary" }, t("连接")),
        ),
      );
    }
    if (route) {
      return h(TaskPage, {
        name: route.page === "edit" ? route.name : null,
        onClose: () => this.closeRoute(),
        onSaved: () => {
          this.closeRoute(true);
          this.refresh();
        },
        onError,
        onDirty: () => (this.formDirty = true),
        onDelete: (name) => this.deleteTask(name),
      });
    }
    switch (view) {
      case "tasks":
        return h(TasksView, {
          tick,
          onLoaded,
          onError,
          act,
          onNew: () => this.openRoute({ page: "new" }),
          onEdit: (task) => this.openRoute({ page: "edit", name: task.name }),
          onOpenRun: openRun,
        });
      case "inbox":
        return h(InboxView, { tick, onLoaded, onError, onOpen: openRun, act });
      case "runs":
        return h(RunsView, { tick, onLoaded, onError, onOpen: openRun });
      case "settings":
        return h(SettingsView, { language: this.langSetting, onLanguage: this.chooseLang, onHostsChanged: this.hosts.reload });
    }
  }

  render(): Child {
    const { authed, remote, view, unread, runId } = this;
    return [
      h(
        "header",
        { class: "bar" },
        h("h1", { class: "brand", "aria-label": t("布谷") }, Logo(), "bugu"),
        authed && HostSwitcher({ remote, hosts: this.hosts, onManage: () => this.showView("settings") }),
        authed &&
          h(
            "nav",
            null,
            VIEWS.map(([v, label]) =>
              h(
                "button",
                { type: "button", class: "tab" + (view === v ? " active" : ""), onclick: () => this.showView(v) },
                t(label),
                v === "inbox" && unread > 0 && h("span", { class: "count", "aria-label": t("{n} 条未读", { n: unread }) }, unread),
              ),
            ),
          ),
        h("span", { class: "spacer" }),
        h("span", { class: "muted conn" }, authed && this.online ? t("已连接 {host}", { host: location.host }) : ""),
        authed && !this.route && Button({ class: "refresh", disabled: this.reloading, "aria-busy": this.reloading ? "true" : undefined, onclick: this.reloadAll }, t("刷新")),
      ),
      // 另一台主机的面板:整条醒目的标识,写明是哪台,免得把运行、删除做到了别的机器上(G5)。
      authed &&
        remote &&
        h(
          "div",
          { class: "remote-bar", role: "status" },
          h("span", null, t("正在管理另一台主机:"), h("strong", { class: "mono" }, remote), h("span", { class: "addr" }, " · " + location.host)),
          getHome() && h("a", { href: getHome() + "/", class: "home-link" }, t("返回本机")),
        ),
      // 报错放在内容上面:放在长列表下面会落在屏幕外。
      h("main", null, authed && ErrorText(this.pageError), this.page()),
      authed && runId && h(RunDialog, { key: runId, id: runId, tick: this.tick, onClose: () => this.close(), onError: this.onError, onRead: this.refresh }),
    ];
  }

  private close() {
    this.runId = null;
    this.update();
  }
}

customElements.define(App.tag, App);

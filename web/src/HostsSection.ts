import { api, openHost, type Host } from "./api";
import { Component, h, value, type Child } from "./dom";
import { Hosts } from "./hosts";
import { n, t } from "./i18n";
import { Button, ErrorText, Field, Mark } from "./ui";

// 连接状态的文字和标记。连不上只是"离线",不表示那台主机的任务出了问题(G14)。
// 没有状态表示还在检查。
const STATUS: Record<string, [string, string]> = {
  checking: [n("检查中"), "ring"],
  online: [n("在线"), "solid ok"],
  offline: [n("离线"), "ring quiet"],
  unauthorized: [n("令牌被拒绝"), "solid warn"],
};

// 连不上或令牌被拒时打开只会停在错误页,所以不让打开,并说明原因。
const BLOCKED: Record<string, string> = {
  offline: n("连不上这台主机,等它恢复在线再打开。"),
  unauthorized: n("那台主机拒绝了这个令牌,删除后用新令牌重新登记。"),
};

const isPlainPublic = (raw: string) => {
  try {
    const u = new URL(raw);
    if (u.protocol !== "http:") return false;
    const host = u.hostname.toLowerCase();
    if (["localhost"].includes(host) || /\.(local|lan|internal|home\.arpa|ts\.net)$/.test(host)) return false;
    const ip = host.split(".").map(Number);
    if (ip.length !== 4 || ip.some((x) => !Number.isInteger(x))) return true;
    const [a, b] = ip;
    return !(a === 10 || a === 127 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168) || (a === 169 && b === 254) || (a === 100 && b >= 64 && b <= 127));
  } catch {
    return false;
  }
};

const message = (err: unknown) => (err instanceof Error ? err.message : String(err));

// HostsSection 是设置页里的"其他主机"(G5):登记地址和令牌,在这个窗口里打开那台主机的面板。
// 登记表里有那些主机的令牌,只有本机令牌能看到(决策记录 0015)。
export class HostsSection extends Component<{ onChanged: () => void }> {
  static readonly tag = "bugu-hosts";
  private hosts = new Hosts(() => this.update());
  private error = "";
  private form = { name: "", url: "", token: "" };

  protected connected() {
    this.hosts.reload();
  }

  private changed() {
    this.hosts.reload();
    this.props.onChanged();
  }

  private add = async (e: Event) => {
    e.preventDefault();
    const { name, url, token } = this.form;
    try {
      await api("PUT", "/hosts/" + encodeURIComponent(name.trim()), { url: url.trim(), token: token.trim() });
      this.form = { name: "", url: "", token: "" };
      this.error = "";
      this.changed();
    } catch (err) {
      this.error = message(err);
    }
    this.update();
  };

  private async removeHost(name: string) {
    if (!confirm(t("删除主机 {name} 的登记?那台主机上的任务不受影响。", { name }))) return;
    try {
      await api("DELETE", "/hosts/" + encodeURIComponent(name));
      this.changed();
    } catch (err) {
      this.error = message(err);
      this.update();
    }
  }

  private set(key: keyof HostsSection["form"]) {
    return (e: Event) => {
      this.form = { ...this.form, [key]: value(e) };
      this.update();
    };
  }

  private row(host: Host, all: Host[]): Child {
    const [label, kind] = STATUS[host.status ?? "checking"] ?? STATUS.offline;
    const blocked = host.status ? BLOCKED[host.status] : undefined;
    return h(
      "li",
      { key: host.name },
      h("span", { class: "mono" }, host.name),
      h("span", { class: "muted mono url" }, host.url),
      Mark(kind, t(label)),
      host.plain_public && h("span", { class: "warn-text" }, t("明文传输")),
      h("span", { class: "spacer" }),
      Button({ onclick: () => openHost(host, all.map((x) => x.name)), disabled: blocked !== undefined, title: blocked && t(blocked) }, t("打开")),
      Button({ variant: "danger", onclick: () => this.removeHost(host.name) }, t("删除")),
    );
  }

  render(): Child {
    const { hosts, forbidden, error: loadError } = this.hosts;
    const form = this.form;
    return h(
      "fieldset",
      null,
      h("legend", null, t("其他主机")),
      forbidden
        ? h("p", { class: "muted" }, t("只有在这台主机本机打开的面板才能管理其他主机。"))
        : [
            hosts && hosts.length === 0 && h("p", { class: "muted" }, t("登记另一台主机上的布谷,就能在这个窗口里打开它的面板。")),
            hosts && hosts.length > 0 && h("ul", { class: "hosts" }, hosts.map((host) => this.row(host, hosts))),
            h(
              "form",
              { onsubmit: this.add, class: "host-form" },
              Field({ label: t("名称") }, h("input", { value: form.name, oninput: this.set("name"), required: true, placeholder: "nas" })),
              Field(
                { label: t("地址"), hint: t("那台主机上布谷的地址,如 https://bugu.example.com 或 http://100.64.0.8:7377。") },
                h("input", { type: "url", value: form.url, oninput: this.set("url"), required: true, placeholder: "http://100.64.0.8:7377" }),
              ),
              isPlainPublic(form.url) &&
                h("p", { class: "warn-text", role: "status" }, t("这是公网上的明文 HTTP 地址:令牌和任务内容会以明文经过网络。建议放在提供 HTTPS 的反向代理之后,或改用组网工具的内网地址。")),
              Field(
                { label: t("令牌"), hint: t("在那台主机上执行 bugu token create <名字> 得到。") },
                h("input", { type: "password", autocomplete: "off", value: form.token, oninput: this.set("token"), required: true }),
              ),
              Button({ type: "submit" }, t("登记")),
            ),
          ],
      ErrorText(this.error || loadError),
    );
  }
}

customElements.define(HostsSection.tag, HostsSection);

import { getHome, openHost, remoteName, remoteNames } from "./api";
import { h, value, type Child } from "./dom";
import { reachable, type Hosts } from "./hosts";
import { t } from "./i18n";

const LOCAL = "__local";
const MANAGE = "__manage";

interface Props {
  remote: string; // 正在管理的另一台主机的名字;本机时为空
  hosts: Hosts; // 本机页面读到的登记表;另一台主机的页面里用不到
  onManage: () => void; // 打开设置页里的"其他主机"
}

// HostSwitcher 是顶栏里的机器下拉框(G5):显示当前是哪台,选一台就切过去。
// 本机页面直接打开那台;另一台主机的页面里没有令牌,选了别的主机就先回到本机,由本机打开它。
export function HostSwitcher({ remote, hosts, onManage }: Props): Child {
  const home = getHome();
  if (remote) {
    // 不是从本机打开的(直接用浏览器打开的服务器),没有可以切去的地方。
    if (!home) return null;
    const current = remoteName();
    const names = remoteNames();
    return h(
      "select",
      {
        class: "host-switch",
        "aria-label": t("当前机器"),
        value: current,
        onchange: (e: Event) => {
          const v = value(e);
          location.href = v === LOCAL ? home + "/" : home + "/#" + new URLSearchParams({ open: v }).toString();
        },
      },
      h("option", { value: LOCAL }, t("本机")),
      (names.includes(current) ? names : [current, ...names]).map((name) => h("option", { value: name }, name)),
    );
  }

  const list = hosts.hosts ?? [];
  if (hosts.forbidden || list.length === 0) return null;
  return h(
    "select",
    {
      class: "host-switch",
      "aria-label": t("当前机器"),
      value: LOCAL,
      onchange: (e: Event) => {
        const v = value(e);
        if (v === MANAGE) {
          onManage();
          return;
        }
        const host = list.find((x) => x.name === v);
        if (host) openHost(host, list.map((x) => x.name));
      },
    },
    h("option", { value: LOCAL }, t("本机")),
    list.map((host) => h("option", { value: host.name, disabled: !reachable(host) }, reachable(host) ? host.name : t("{name}(离线)", { name: host.name }))),
    h("option", { value: MANAGE }, t("管理主机…")),
  );
}

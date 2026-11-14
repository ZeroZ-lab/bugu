import { api, type Settings } from "./api";
import { Component, h, type Child } from "./dom";
import { HostsSection } from "./HostsSection";
import { LANG_NAMES, n, t, type Lang } from "./i18n";
import { getTheme, setTheme, type Theme } from "./theme";
import { Choice } from "./ui";

interface Props {
  language: Settings["language"] | null;
  onLanguage: (language: Settings["language"]) => void;
  onHostsChanged: () => void; // 登记或删除了主机,顶栏的下拉框跟着更新
}

const THEMES = [
  ["auto", n("跟随系统")],
  ["light", n("浅色")],
  ["dark", n("深色")],
] as const;

// SettingsView 是设置页(G16):外观只记在这台设备上,语言存在后台、对所有界面生效(G15)。
export class SettingsView extends Component<Props> {
  static readonly tag = "bugu-settings";
  private theme: Theme = getTheme();
  private version = "";

  protected connected() {
    api<{ version?: string }>("GET", "/health").then(
      (health) => {
        this.version = health.version ?? "";
        this.update();
      },
      () => {}, // 版本号拿不到就不显示
    );
  }

  private chooseTheme = (next: Theme) => {
    setTheme(next);
    this.theme = next;
    this.update();
  };

  render(): Child {
    const { language, onLanguage, onHostsChanged } = this.props;
    const languages: (readonly ["auto" | Lang, string])[] = [["auto", t("跟随系统")], ...(Object.keys(LANG_NAMES) as Lang[]).map((l) => [l, LANG_NAMES[l]] as const)];
    return h(
      "section",
      null,
      h("div", { class: "head" }, h("h2", null, t("设置"))),
      h(
        "div",
        { class: "settings" },
        h(
          "fieldset",
          null,
          h("legend", null, t("外观")),
          Choice({ name: "theme", value: this.theme, options: THEMES.map(([v, label]) => [v, t(label)] as const), onChange: this.chooseTheme }),
          h("p", { class: "muted" }, t("只对这台设备上的这个窗口或浏览器生效。")),
        ),
        h(
          "fieldset",
          null,
          h("legend", null, t("语言")),
          language && Choice({ name: "language", value: language, options: languages, onChange: onLanguage }),
          h("p", { class: "muted" }, t("对所有窗口、菜单栏和系统通知生效。命令行用 --lang 单独指定。")),
        ),
        h(HostsSection, { onChanged: onHostsChanged }),
        h(
          "fieldset",
          null,
          h("legend", null, t("关于")),
          h("dl", null, h("dt", null, t("版本")), h("dd", { class: "mono" }, this.version || "-"), h("dt", null, t("后台")), h("dd", { class: "mono" }, location.host)),
        ),
      ),
    );
  }
}

customElements.define(SettingsView.tag, SettingsView);

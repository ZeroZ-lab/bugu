// 外观:跟随系统、浅色或深色(G16)。只记在这台设备上(这个浏览器或这个应用窗口),不存进后台:
// 深浅是"这块屏幕怎么看"的偏好,不同设备可以不同。
export type Theme = "auto" | "light" | "dark";

const THEME_KEY = "bugu.theme";

export function getTheme(): Theme {
  try {
    const v = localStorage.getItem(THEME_KEY);
    return v === "light" || v === "dark" ? v : "auto";
  } catch {
    return "auto";
  }
}

// applyTheme 让选择生效:auto 时去掉 data-theme,由样式表按系统设置取色。
export function applyTheme(theme: Theme) {
  if (theme === "auto") delete document.documentElement.dataset.theme;
  else document.documentElement.dataset.theme = theme;
}

export function setTheme(theme: Theme) {
  try {
    if (theme === "auto") localStorage.removeItem(THEME_KEY);
    else localStorage.setItem(THEME_KEY, theme);
  } catch {
    // 存不了就只在这次打开时生效
  }
  applyTheme(theme);
}

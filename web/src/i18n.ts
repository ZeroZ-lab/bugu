// 界面文字的中文和英文(决策记录 0014)。
// 界面上的文字用中文原文书写,原文就是查找英文译文的键,译文在 locales/en.json;找不到译文时显示原文。
// 原文只能写在 t、n、tx 的第一个参数里,且是双引号字符串,检查见 i18n.test.ts。
import type { Child } from "./dom";
import en from "./locales/en.json";

export type Lang = "zh" | "en";

// 语言在它自己里的名字,任何语言的界面里都这样写。
export const LANG_NAMES: Record<Lang, string> = { zh: "中文", en: "English" };

const LANG_KEY = "bugu.lang"; // 上次用的语言:下次打开时,第一屏不用等后台的设置就是对的
const catalog: Record<string, string> = en;

// browserLang 是浏览器偏好的语言:中文的各种写法是中文,其他语言说英文。
export function browserLang(): Lang {
  const tags = typeof navigator === "undefined" ? [] : (navigator.languages ?? [navigator.language]);
  for (const tag of tags) if (tag) return tag.toLowerCase().startsWith("zh") ? "zh" : "en";
  return "zh";
}

function saved(): Lang | null {
  try {
    const l = localStorage.getItem(LANG_KEY);
    return l === "zh" || l === "en" ? l : null;
  } catch {
    return null;
  }
}

let current: Lang = typeof navigator === "undefined" ? "zh" : (saved() ?? browserLang());

export const lang = () => current;

// setLang 改变界面语言。调用者负责重新渲染。
export function setLang(l: Lang) {
  current = l;
  if (typeof document !== "undefined") {
    document.documentElement.lang = l === "zh" ? "zh-CN" : "en";
    document.title = t("布谷");
  }
  try {
    localStorage.setItem(LANG_KEY, l);
  } catch {
    // 存不了就下次再按浏览器的语言
  }
}

// t 用当前语言说一段文字。{名字} 换成 vars 里的值,vars 里没有的原样保留(比如提示里的 {date})。
export function t(zh: string, vars?: Record<string, string | number>): string {
  const s = current === "en" ? (catalog[zh] ?? zh) : zh;
  return vars ? s.replace(/\{(\w+)\}/g, (m, k: string) => (k in vars ? String(vars[k]) : m)) : s;
}

// n 只标记原文,原样返回,用于模块级的表;显示时再用 t 说出来。
export const n = (zh: string) => zh;

// tx 和 t 一样,但 {名字} 可以换成元素,比如一段 code。
export function tx(zh: string, parts: Record<string, Child>): Child[] {
  const s = current === "en" ? (catalog[zh] ?? zh) : zh;
  return s.split(/(\{\w+\})/).map((piece) => {
    const k = /^\{(\w+)\}$/.exec(piece)?.[1];
    return k !== undefined && k in parts ? parts[k] : piece;
  });
}

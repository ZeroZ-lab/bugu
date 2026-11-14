/// <reference types="node" />
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import en from "./locales/en.json";
import { n, setLang, t } from "./i18n";

const dir = new URL(".", import.meta.url).pathname;
const cjk = /[　-〿一-鿿＀-￯]/;
// 原文:t、n、tx 的第一个参数,必须是双引号字符串。
const source = /\b(?:t|n|tx)\(\s*"((?:[^"\\]|\\.)*)"/g;
const placeholders = (s: string) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort().join(",");

function files(d: string): string[] {
  return readdirSync(d, { withFileTypes: true }).flatMap((e) => {
    const p = join(d, e.name);
    if (e.isDirectory()) return e.name === "locales" ? [] : files(p);
    return /\.tsx?$/.test(e.name) && !/\.test\.tsx?$/.test(e.name) && e.name !== "i18n.ts" ? [p] : [];
  });
}

// stripComments 去掉注释:整行的 //、行尾以空白开头的 //、/* */。
const stripComments = (code: string) =>
  code
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .replace(/^\s*\/\/.*$/gm, "")
    .replace(/\s\/\/\s.*$/gm, "");

// 决策记录 0014:给人看的中文都要能翻译。中文只能作为原文交给 t、n、tx;每段原文在 locales/en.json
// 里有译文,{名字} 占位一致;译文表里没有用不到的条目。
describe("界面文字", () => {
  const catalog: Record<string, string> = en;
  const used = new Set<string>();
  for (const file of files(dir)) {
    const code = stripComments(readFileSync(file, "utf8"));
    const name = file.slice(dir.length);
    const keys = [...code.matchAll(source)].map((m) => JSON.parse(`"${m[1]}"`) as string);
    keys.forEach((k) => used.add(k));
    it(`${name} 的中文都经过 t()`, () => {
      const rest = code.replace(source, "").split("\n").filter((line) => cjk.test(line));
      expect(rest).toEqual([]);
    });
    it(`${name} 的原文都有英文译文`, () => {
      const missing = keys.filter((k) => !(k in catalog));
      expect(missing).toEqual([]);
      for (const k of keys) {
        expect(placeholders(catalog[k]), k).toBe(placeholders(k));
        expect(cjk.test(catalog[k]), k).toBe(false);
      }
    });
  }
  it("译文表里没有用不到的条目", () => {
    expect(Object.keys(catalog).filter((k) => !used.has(k))).toEqual([]);
  });
});

describe("t", () => {
  it("按当前语言说,换掉给出的占位,其余原样保留", () => {
    setLang("en");
    try {
      expect(t("新建任务")).toBe("New task");
      expect(t("{n} 条未读", { n: 3 })).toBe("3 unread");
      expect(t("没有译文的文字 {x}", { x: 1 })).toBe("没有译文的文字 1");
      expect(t(n("每周"))).toBe("Every week");
    } finally {
      setLang("zh");
    }
    expect(t("提示词里的 {date} 原样保留", {})).toBe("提示词里的 {date} 原样保留");
  });
});

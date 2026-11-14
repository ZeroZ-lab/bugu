import { h, type Child, type VNode } from "./dom";
import { parseMarkdown, type Block, type Inline } from "./mdparse";

// 页面定期刷新时同一段输出会反复渲染,记住上一次的解析结果。
let last: { text: string; blocks: Block[] } | null = null;

// Markdown 把任务输出按 Markdown 显示(G3)。只生成元素,不插入 HTML。
export function Markdown(text: string): VNode {
  if (last?.text !== text) last = { text, blocks: parseMarkdown(text) };
  return h("div", { class: "md" }, last.blocks.map(block));
}

function block(b: Block): VNode {
  switch (b.type) {
    case "heading":
      // 结果显示在对话框里,标题从 h3 起,不和页面自己的标题抢层级。
      return h((["h3", "h4", "h5", "h6", "h6", "h6"] as const)[b.level - 1], null, inlines(b.children));
    case "paragraph":
      return h("p", null, lines(b.lines));
    case "quote":
      return h("blockquote", null, lines(b.lines));
    case "list":
      return h(b.ordered ? "ol" : "ul", null, b.items.map((item) => h("li", null, inlines(item))));
    case "code":
      return h("pre", { class: "md-code" }, h("code", null, b.text));
    case "rule":
      return h("hr");
  }
}

function lines(ls: Inline[][]): Child {
  return ls.map((l, i) => [i > 0 && h("br"), inlines(l)]);
}

function inlines(parts: Inline[]): Child {
  return parts.map((p) => {
    switch (p.type) {
      case "text":
        return p.text;
      case "code":
        return h("code", null, p.text);
      case "strong":
        return h("strong", null, inlines(p.children));
      case "em":
        return h("em", null, inlines(p.children));
      case "link":
        return h("a", { href: p.href, target: "_blank", rel: "noopener noreferrer" }, inlines(p.children));
    }
  });
}

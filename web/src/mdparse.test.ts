import { describe, expect, it } from "vitest";
import { parseInline, parseMarkdown } from "./mdparse";

const text = (t: string) => ({ type: "text", text: t });

// G3:收件箱按 Markdown 显示结果。
describe("G3 Markdown 解析", () => {
  it("标题、段落、列表、引用、分隔线和代码块", () => {
    const blocks = parseMarkdown("# 今日新闻\n\n第一行\n第二行\n\n- 甲\n- 乙\n\n1. 一\n2. 二\n\n> 引用\n\n---\n\n```\nls -l\n# 不是标题\n```");
    expect(blocks).toEqual([
      { type: "heading", level: 1, children: [text("今日新闻")] },
      { type: "paragraph", lines: [[text("第一行")], [text("第二行")]] },
      { type: "list", ordered: false, items: [[text("甲")], [text("乙")]] },
      { type: "list", ordered: true, items: [[text("一")], [text("二")]] },
      { type: "quote", lines: [[text("引用")]] },
      { type: "rule" },
      { type: "code", text: "ls -l\n# 不是标题" },
    ]);
  });

  it("行内代码、粗体、斜体和链接", () => {
    expect(parseInline("用 `bugu run` 跑 **一次**,看 *结果*:[文档](https://example.com/a) 或 https://example.com/b。")).toEqual([
      text("用 "),
      { type: "code", text: "bugu run" },
      text(" 跑 "),
      { type: "strong", children: [text("一次")] },
      text(",看 "),
      { type: "em", children: [text("结果")] },
      text(":"),
      { type: "link", href: "https://example.com/a", children: [text("文档")] },
      text(" 或 "),
      { type: "link", href: "https://example.com/b", children: [text("https://example.com/b")] },
      text("。"),
    ]);
  });

  it("不安全的链接和 HTML 都按文字显示", () => {
    expect(parseInline("[点我](javascript:alert(1)) <b>x</b>")).toEqual([text("[点我](javascript:alert(1)) <b>x</b>")]);
  });

  it("没有结束标记的代码块一直到末尾", () => {
    expect(parseMarkdown("```\na\nb")).toEqual([{ type: "code", text: "a\nb" }]);
  });
});

// 收件箱用的 Markdown 解析(G3)。只支持任务输出里常见的写法:标题、段落、列表、引用、分隔线、
// 代码块,以及行内的代码、粗体、斜体和链接。解析结果是数据,由 Markdown.ts 渲染成元素,
// 不拼接 HTML,所以输出里的 HTML 标签只会原样显示为文字。

export type Inline =
  | { type: "text"; text: string }
  | { type: "code"; text: string }
  | { type: "strong"; children: Inline[] }
  | { type: "em"; children: Inline[] }
  | { type: "link"; href: string; children: Inline[] };

export type Block =
  | { type: "heading"; level: number; children: Inline[] }
  | { type: "paragraph"; lines: Inline[][] }
  | { type: "list"; ordered: boolean; items: Inline[][] }
  | { type: "quote"; lines: Inline[][] }
  | { type: "code"; text: string }
  | { type: "rule" };

const HEADING = /^(#{1,6})\s+(.*?)\s*#*\s*$/;
const BULLET = /^\s*[-*+]\s+(.*)$/;
const NUMBERED = /^\s*\d+[.)]\s+(.*)$/;
const RULE = /^\s*([-*_])(\s*\1){2,}\s*$/;
const FENCE = /^\s*(```|~~~)/;

export function parseMarkdown(source: string): Block[] {
  const lines = source.replace(/\r\n?/g, "\n").split("\n");
  const blocks: Block[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (line.trim() === "") {
      i++;
      continue;
    }
    const fence = FENCE.exec(line);
    if (fence) {
      const body: string[] = [];
      for (i++; i < lines.length && !lines[i].trimStart().startsWith(fence[1]); i++) body.push(lines[i]);
      i++; // 结束的那行;没有结束标记时一直到末尾
      blocks.push({ type: "code", text: body.join("\n") });
      continue;
    }
    const heading = HEADING.exec(line);
    if (heading) {
      blocks.push({ type: "heading", level: heading[1].length, children: parseInline(heading[2]) });
      i++;
      continue;
    }
    if (RULE.test(line)) {
      blocks.push({ type: "rule" });
      i++;
      continue;
    }
    const listPattern = BULLET.test(line) ? BULLET : NUMBERED.test(line) ? NUMBERED : null;
    if (listPattern) {
      const items: Inline[][] = [];
      for (let m; i < lines.length && (m = listPattern.exec(lines[i])); i++) items.push(parseInline(m[1]));
      blocks.push({ type: "list", ordered: listPattern === NUMBERED, items });
      continue;
    }
    if (line.trimStart().startsWith(">")) {
      const quoted: Inline[][] = [];
      for (; i < lines.length && lines[i].trimStart().startsWith(">"); i++) quoted.push(parseInline(lines[i].trimStart().replace(/^>\s?/, "")));
      blocks.push({ type: "quote", lines: quoted });
      continue;
    }
    // 段落到空行或另一种块为止。段内换行保留:任务输出里的换行通常是有意的。
    const para: Inline[][] = [];
    for (; i < lines.length; i++) {
      const l = lines[i];
      if (l.trim() === "" || FENCE.test(l) || HEADING.test(l) || RULE.test(l) || BULLET.test(l) || NUMBERED.test(l) || l.trimStart().startsWith(">")) break;
      para.push(parseInline(l.trim()));
    }
    blocks.push({ type: "paragraph", lines: para });
  }
  return blocks;
}

// 只放行这几种链接,其他的(如 javascript:)按文字显示。
const SAFE_LINK = /^(https?:|mailto:)/i;

const INLINE = /`([^`]+)`|\*\*(.+?)\*\*|__(.+?)__|\*([^*\s][^*]*?)\*|\[([^\]]+)\]\(([^)\s]+)\)|(https?:\/\/[^\s<>()]+[^\s<>().,;:!?\uFF0C\u3002\uFF1B\uFF1A\uFF01\uFF1F\uFF09])/u;

export function parseInline(text: string): Inline[] {
  const out: Inline[] = [];
  let rest = text;
  for (let m; (m = INLINE.exec(rest)); ) {
    if (m.index > 0) out.push({ type: "text", text: rest.slice(0, m.index) });
    const [whole, code, strong, strong2, em, label, href, bare] = m;
    if (code !== undefined) out.push({ type: "code", text: code });
    else if (strong !== undefined || strong2 !== undefined) out.push({ type: "strong", children: parseInline(strong ?? strong2) });
    else if (em !== undefined) out.push({ type: "em", children: parseInline(em) });
    else if (label !== undefined) {
      if (SAFE_LINK.test(href)) out.push({ type: "link", href, children: parseInline(label) });
      else out.push({ type: "text", text: whole });
    } else out.push({ type: "link", href: bare, children: [{ type: "text", text: bare }] });
    rest = rest.slice(m.index + whole.length);
  }
  if (rest) out.push({ type: "text", text: rest });
  return merge(out);
}

// merge 合并相邻的文字片段,让结果更容易比较。
function merge(parts: Inline[]): Inline[] {
  const out: Inline[] = [];
  for (const p of parts) {
    const last = out[out.length - 1];
    if (p.type === "text" && last?.type === "text") last.text += p.text;
    else out.push(p);
  }
  return out;
}

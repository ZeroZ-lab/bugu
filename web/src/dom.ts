// 页面的渲染辅助,手写,没有第三方依赖(决策记录 0017)。
// h 描述一段界面;patch 把描述对到真实的元素上,标签相同的元素原地更新,所以重新渲染时输入框的焦点、
// 正在输入的内容、打开的对话框都保留。Component 是页面上各个自定义元素的基类。

export type Props = Record<string, unknown>;

// VNode 是对一个元素的描述。component 为真时它是一个自定义元素,内容由它自己渲染。
export interface VNode {
  tag: string;
  props: Props;
  kids: Kid[];
  component: boolean;
}

// null 是空位:条件不成立的内容留一个空注释占位,后面的兄弟元素不会因此错位。
type Kid = VNode | string | null;

export type Child = VNode | string | number | boolean | null | undefined | Child[];

export interface ComponentClass<P extends object> {
  readonly tag: string;
  new (): Component<P>;
}

// h 描述一个元素。属性里 on 开头的是事件处理函数,value 和 checked 对到元素的当前值,
// key 用来在列表里认出同一项,ref 在元素创建时调用一次,其余的是 HTML 属性(true 写成空属性,false 去掉)。
// 第一个参数是组件类时,属性整个交给那个组件。
export function h<P extends object>(tag: ComponentClass<P>, props: P & { key?: unknown }): VNode;
export function h(tag: string, props?: Props | null, ...children: Child[]): VNode;
export function h(tag: string | { readonly tag: string }, props?: object | null, ...children: Child[]): VNode {
  const component = typeof tag !== "string";
  return { tag: component ? tag.tag : tag, props: (props ?? {}) as Props, kids: component ? [] : flatten(children), component };
}

export function flatten(children: Child[], out: Kid[] = []): Kid[] {
  for (const c of children) {
    if (Array.isArray(c)) flatten(c, out);
    else if (c === null || c === undefined || typeof c === "boolean" || c === "") out.push(null);
    else out.push(typeof c === "number" ? String(c) : c);
  }
  return out;
}

const SVG = "http://www.w3.org/2000/svg";
const rendered = new WeakMap<Node, VNode>(); // 每个元素上一次是按哪个描述渲染的

// patch 让 parent 的子节点与 kids 一致。
export function patch(parent: Node, kids: Kid[], svg = false) {
  let cur = parent.firstChild;
  for (const kid of kids) {
    if (cur && matches(cur, kid)) {
      update(cur, kid, svg);
      cur = cur.nextSibling;
      continue;
    }
    const moved = findKeyed(cur, kid);
    if (moved) {
      parent.insertBefore(moved, cur);
      update(moved, kid, svg);
    } else {
      parent.insertBefore(create(kid, svg), cur);
    }
  }
  while (cur) {
    const next = cur.nextSibling;
    parent.removeChild(cur);
    cur = next;
  }
}

function matches(node: Node, kid: Kid): boolean {
  if (kid === null) return node.nodeType === Node.COMMENT_NODE;
  if (typeof kid === "string") return node.nodeType === Node.TEXT_NODE;
  const old = rendered.get(node);
  return old !== undefined && old.tag === kid.tag && old.props.key === kid.props.key;
}

// findKeyed 在后面的兄弟里找带同一个 key 的元素,列表调整顺序时把它移过来,而不是重建。
function findKeyed(from: Node | null, kid: Kid): Node | null {
  if (kid === null || typeof kid === "string" || kid.props.key === undefined) return null;
  for (let n = from; n; n = n.nextSibling) if (matches(n, kid)) return n;
  return null;
}

function create(kid: Kid, svg: boolean): Node {
  if (kid === null) return document.createComment("");
  if (typeof kid === "string") return document.createTextNode(kid);
  const inSvg = svg || kid.tag === "svg";
  const el = inSvg ? document.createElementNS(SVG, kid.tag) : document.createElement(kid.tag);
  rendered.set(el, kid);
  if (kid.component) {
    (el as Component<object>).props = kid.props;
    return el;
  }
  patch(el, kid.kids, inSvg);
  setProps(el, {}, kid.props);
  (kid.props.ref as ((el: Element) => void) | undefined)?.(el);
  return el;
}

function update(node: Node, kid: Kid, svg: boolean) {
  if (kid === null) return;
  if (typeof kid === "string") {
    if ((node as Text).data !== kid) (node as Text).data = kid;
    return;
  }
  const el = node as Element;
  const old = rendered.get(el)!;
  rendered.set(el, kid);
  if (kid.component) {
    (el as Component<object>).setProps(kid.props);
    return;
  }
  // 先内容后属性:select 的 value 要等选项都在了才能设置。
  patch(el, kid.kids, svg || kid.tag === "svg");
  setProps(el, old.props, kid.props);
}

function setProps(el: Element, old: Props, next: Props) {
  for (const k in old) if (!(k in next)) setProp(el, k, undefined, old[k]);
  for (const k in next) setProp(el, k, next[k], old[k]);
}

function setProp(el: Element, k: string, v: unknown, old: unknown) {
  if (k === "key" || k === "ref") return;
  const live = el as unknown as Record<string, unknown>;
  if (k === "value" || k === "checked") {
    // 与元素的当前值比较,而不是与上次的描述比较:用户刚输入的内容就是当前值,不用再写一遍。
    const want = v ?? (k === "value" ? "" : false);
    if (live[k] !== want) live[k] = want;
    return;
  }
  if (v === old) return;
  if (k.startsWith("on")) live[k] = v ?? null;
  else if (v === false || v === null || v === undefined) el.removeAttribute(k);
  else el.setAttribute(k, v === true ? "" : String(v));
}

// Component 是自定义元素的基类。内容直接渲染在元素里(不用 Shadow DOM),全局样式表照常生效。
// 状态是子类的普通字段,改完后调用 update();同一轮里的多次 update() 只渲染一次。
export abstract class Component<P extends object = object> extends HTMLElement {
  props = {} as P;
  #queued = false;
  #reads = new Map<string, number>();

  // render 返回这个元素此刻的内容。
  abstract render(): Child;

  // connected 在元素放进页面时调用,disconnected 在移走时调用。
  protected connected() {}
  protected disconnected() {}
  // propsChanged 在上层交来新的属性时调用,随后会重新渲染。
  protected propsChanged(_prev: P) {}
  // rendered 在每次渲染之后调用。
  protected rendered() {}

  connectedCallback() {
    this.connected();
    this.#render();
  }

  disconnectedCallback() {
    this.disconnected();
  }

  setProps(next: P) {
    const prev = this.props;
    this.props = next;
    if (!this.isConnected) return;
    this.propsChanged(prev);
    this.update();
  }

  update() {
    if (this.#queued) return;
    this.#queued = true;
    queueMicrotask(() => {
      this.#queued = false;
      if (this.isConnected) this.#render();
    });
  }

  // fresh 在发出一次读取前调用,返回的函数回答:这是不是同类读取里最新的一次,元素是不是还在页面上。
  // 过期的回应据此丢掉。
  protected fresh(kind = ""): () => boolean {
    const n = (this.#reads.get(kind) ?? 0) + 1;
    this.#reads.set(kind, n);
    return () => this.isConnected && this.#reads.get(kind) === n;
  }

  #render() {
    const active = document.activeElement;
    patch(this, flatten([this.render()]));
    // 列表里移动元素会让它失去焦点,移完还给它。
    if (active instanceof HTMLElement && active !== document.activeElement && this.contains(active)) active.focus();
    this.rendered();
  }
}

// value 取出输入事件里控件的当前值。
export const value = (e: Event) => (e.target as HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement).value;

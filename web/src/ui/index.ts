// 共用组件。写界面前先看这里有没有现成的;没有再加,并在 docs/DESIGN.md 的"组件"一节登记。
// 它们都是对原生元素的薄包装,所以是返回元素描述的函数,不是自定义元素:
// 按钮仍是 button,表单项仍是 label,键盘操作和读屏由浏览器处理。
import { h, type Child, type Props, type VNode } from "../dom";

// Button 默认 type="button",避免在表单里被当成提交按钮。提交按钮显式写 type="submit"。
// variant:primary 是墨色底,一屏最多一个;danger 是红色文字,用于删除这类操作。
export function Button(props: Props & { variant?: "default" | "primary" | "danger" }, ...children: Child[]): VNode {
  const { variant = "default", type = "button", class: cls, ...rest } = props;
  return h("button", { type, class: [variant !== "default" && variant, cls].filter(Boolean).join(" ") || undefined, ...rest }, ...children);
}

// Choice 是一组横排的单选项,用原生 radio,方向键切换由浏览器处理。
export function Choice<T extends string>({
  name,
  value,
  options,
  onChange,
}: {
  name: string;
  value: T;
  options: readonly (readonly [T, string])[];
  onChange: (value: T) => void;
}): VNode[] {
  return options.map(([v, label]) => h("label", { class: "inline" }, h("input", { type: "radio", name, checked: value === v, onchange: () => onChange(v) }), " ", label));
}

// ErrorText 显示一段错误说明,没有内容时不占位置。
export function ErrorText(message: string): Child {
  return message ? h("p", { class: "error", role: "alert" }, message) : null;
}

// Field 是表单里的一项:标题、控件、可选的说明。用 label 包住控件,点标题即可聚焦。
export function Field({ label, hint }: { label: Child; hint?: Child }, ...children: Child[]): VNode {
  return h("label", null, label, children, hint ? h("small", null, hint) : null);
}

// Logo 的几何与 resources/logo.svg 相同:左上实心圆,右下空心圆。颜色随文字颜色。
export function Logo(): VNode {
  return h(
    "svg",
    { viewBox: "0 0 48 48", "aria-hidden": "true" },
    h("circle", { cx: 17, cy: 19, r: 9, fill: "currentColor" }),
    h("circle", { cx: 33, cy: 31, r: 5.5, fill: "none", stroke: "currentColor", "stroke-width": 3.5 }),
  );
}

// Mark 是状态标记:圆点加文字(docs/DESIGN.md"运行状态标记")。
// kind 由形状和颜色组成:形状取 ring(空心)、spin(转动的虚线圈)、solid(实心),
// 颜色取 ink、ok、warn、bad,不写为灰色;quiet 让文字变淡。
export function Mark(kind: string, ...children: Child[]): VNode {
  return h("span", { class: "mark " + kind }, ...children);
}

// Modal 用原生的 dialog 元素:放进页面后以模态方式打开,焦点和 Esc 由浏览器处理。
// 按 Esc 或点关闭时调用 onClose,由外层把它移走。
export function Modal({ wide, onClose }: { wide?: boolean; onClose: () => void }, ...children: Child[]): VNode {
  const open = (el: Element) =>
    queueMicrotask(() => {
      const dialog = el as HTMLDialogElement;
      if (dialog.isConnected && !dialog.open) dialog.showModal();
    });
  return h("dialog", { class: wide ? "wide" : undefined, onclose: onClose, ref: open }, ...children);
}

import type { Runner } from "./api";
import { Component, h, value, type Child } from "./dom";
import { permissionText } from "./format";
import { n, t } from "./i18n";
import { agentRunners, newStep, permissionLevels, type FormStep } from "./taskForm";
import { Button, Field } from "./ui";

interface Props {
  steps: FormStep[];
  runners: Runner[];
  onChange: (steps: FormStep[]) => void;
}

const TYPES = [
  ["command", n("一条命令")],
  ["path", n("脚本文件")],
  ["agent", n("AI 命令行")],
  ["text", n("文本")],
] as const;

// StepCards 把任务的步骤显示为一排卡片(T9):可以添加、删除,拖动或用箭头按钮排序。
// 每一步的输出交给下一步;提示词和文本里可以用 {prev} 和 {steps.<名称>.output} 引用。
export class StepCards extends Component<Props> {
  static readonly tag = "bugu-step-cards";
  private dragging: number | null = null;
  private shown = 0; // 上次渲染时有几张卡片

  // 新加的卡片排在最后,可能在可见范围之外:滚过去。
  protected rendered() {
    const count = this.props.steps.length;
    if (this.shown > 0 && count > this.shown) this.querySelector(".step-card:last-child")?.scrollIntoView({ block: "nearest", inline: "end" });
    this.shown = count;
  }

  private patchStep(i: number, patch: Partial<FormStep>) {
    this.props.onChange(this.props.steps.map((s, j) => (j === i ? { ...s, ...patch } : s)));
  }

  private move(from: number, to: number) {
    const { steps, onChange } = this.props;
    if (to < 0 || to >= steps.length || from === to) return;
    const next = [...steps];
    const [s] = next.splice(from, 1);
    next.splice(to, 0, s);
    onChange(next);
  }

  private drag(i: number | null) {
    this.dragging = i;
    this.update();
  }

  private card(s: FormStep, i: number): Child {
    const { steps, runners, onChange } = this.props;
    const usable = agentRunners(runners);
    const levels = permissionLevels(runners, s.runner);
    const set = (key: keyof FormStep) => (e: Event) => this.patchStep(i, { [key]: value(e) });
    const nth = { n: i + 1 };
    return h(
      "li",
      {
        key: s.key,
        class: "step-card" + (this.dragging === i ? " dragging" : ""),
        "aria-label": t("第 {n} 步", nth),
        ondragover: (e: DragEvent) => this.dragging !== null && e.preventDefault(),
        ondrop: (e: DragEvent) => {
          e.preventDefault();
          if (this.dragging !== null) this.move(this.dragging, i);
          this.drag(null);
        },
      },
      h(
        "div",
        { class: "step-card-head", draggable: "true", ondragstart: () => this.drag(i), ondragend: () => this.drag(null), title: t("拖动可以调整顺序") },
        h("span", { class: "grip", "aria-hidden": "true" }, "⋮⋮"),
        h("strong", null, t("第 {n} 步", nth)),
        h("span", { class: "spacer" }),
        h("button", { type: "button", class: "icon", "aria-label": t("前移第 {n} 步", nth), disabled: i === 0, onclick: () => this.move(i, i - 1) }, "←"),
        h("button", { type: "button", class: "icon", "aria-label": t("后移第 {n} 步", nth), disabled: i === steps.length - 1, onclick: () => this.move(i, i + 1) }, "→"),
        h("button", { type: "button", class: "icon", "aria-label": t("删除第 {n} 步", nth), onclick: () => onChange(steps.filter((_, j) => j !== i)) }, "×"),
      ),
      Field({ label: t("类型") }, h("select", { value: s.type, onchange: set("type") }, TYPES.map(([v, label]) => h("option", { value: v }, t(label))))),
      Field({ label: t("名称(可选)"), hint: t("后面的步骤用 {steps.<名称>.output} 引用它的输出。") }, h("input", { value: s.name, oninput: set("name") })),
      s.type === "command" && Field({ label: t("命令"), hint: i > 0 ? t("上一步的输出从标准输入读到。") : undefined }, h("textarea", { rows: 2, value: s.command, oninput: set("command") })),
      s.type === "path" && Field({ label: t("脚本文件的路径") }, h("input", { value: s.path, oninput: set("path") })),
      s.type === "agent" && [
        Field(
          { label: t("运行器") },
          h(
            "select",
            {
              value: s.runner,
              onchange: (e: Event) => {
                const next = permissionLevels(runners, value(e));
                this.patchStep(i, { runner: value(e), permission: next.includes(s.permission) ? s.permission : (next[0] ?? "") });
              },
            },
            usable.map((r) => h("option", { value: r.name }, r.title ? `${r.title}(${r.name})` : r.name)),
            s.runner && !usable.some((r) => r.name === s.runner) && h("option", { value: s.runner }, t("{runner}(找不到这个运行器)", { runner: s.runner })),
          ),
        ),
        Field({ label: t("提示词"), hint: i > 0 ? t("没有用到 {prev} 时,上一步的输出附在提示词后面。") : undefined }, h("textarea", { rows: 4, value: s.prompt, oninput: set("prompt") })),
        levels.length > 0 &&
          Field(
            { label: t("允许它做什么") },
            h(
              "select",
              { value: s.permission, onchange: set("permission") },
              levels.map((l, j) => h("option", { value: l }, j === 0 ? t("{level}(默认)", { level: permissionText(l) }) : permissionText(l))),
            ),
          ),
      ],
      s.type === "text" && Field({ label: t("文本"), hint: t("原样交给下一步,可以写要求或固定的说明。") }, h("textarea", { rows: 3, value: s.text, oninput: set("text") })),
      s.type !== "text" &&
        Field({ label: t("这一步最长运行时间,分钟(可选)") }, h("input", { type: "number", min: 0, step: 1, value: s.timeout, oninput: set("timeout"), placeholder: t("不限") })),
    );
  }

  render(): Child {
    const { steps, runners, onChange } = this.props;
    return h(
      "div",
      null,
      h("ol", { class: "step-cards" }, steps.map((s, i) => this.card(s, i))),
      Button({ onclick: () => onChange([...steps, newStep(runners)]) }, t("添加步骤")),
    );
  }
}

customElements.define(StepCards.tag, StepCards);

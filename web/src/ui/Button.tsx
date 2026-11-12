import type { ButtonHTMLAttributes } from "react";

type Props = ButtonHTMLAttributes<HTMLButtonElement> & {
  // primary:墨色底,一屏最多一个;danger:红色文字,用于删除这类操作。
  variant?: "default" | "primary" | "danger";
};

// Button 默认 type="button",避免在表单里被当成提交按钮。提交按钮显式写 type="submit"。
export function Button({ variant = "default", type = "button", className, ...rest }: Props) {
  const cls = [variant !== "default" && variant, className].filter(Boolean).join(" ");
  return <button type={type} className={cls || undefined} {...rest} />;
}

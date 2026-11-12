import type { ReactNode } from "react";

// Mark 是状态标记:圆点加文字(docs/DESIGN.md"运行状态标记")。
// kind 由形状和颜色组成:形状取 ring(空心)、spin(转动的虚线圈)、solid(实心),
// 颜色取 ink、ok、warn、bad,不写为灰色;quiet 让文字变淡。
export function Mark({ kind, children }: { kind: string; children: ReactNode }) {
  return <span className={"mark " + kind}>{children}</span>;
}

import type { ReactNode } from "react";

// Field 是表单里的一项:标题、控件、可选的说明。用 label 包住控件,点标题即可聚焦。
export function Field({ label, hint, children }: { label: ReactNode; hint?: ReactNode; children: ReactNode }) {
  return (
    <label>
      {label}
      {children}
      {hint && <small>{hint}</small>}
    </label>
  );
}

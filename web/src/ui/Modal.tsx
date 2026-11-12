import { useEffect, useRef, type ReactNode } from "react";

// Modal 用原生的 dialog 元素:挂载时以模态方式打开,焦点和 Esc 由浏览器处理。
// 按 Esc 或点关闭时调用 onClose,由外层把它卸载。
export function Modal({ wide, onClose, children }: { wide?: boolean; onClose: () => void; children: ReactNode }) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const dialog = ref.current;
    if (dialog && !dialog.open) dialog.showModal();
  }, []);
  return (
    <dialog ref={ref} className={wide ? "wide" : undefined} onClose={onClose}>
      {children}
    </dialog>
  );
}

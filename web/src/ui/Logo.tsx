// Logo 的几何与 resources/logo.svg 相同:左上实心圆,右下空心圆。颜色随文字颜色。
export function Logo() {
  return (
    <svg viewBox="0 0 48 48" aria-hidden="true">
      <circle cx="17" cy="19" r="9" fill="currentColor" />
      <circle cx="33" cy="31" r="5.5" fill="none" stroke="currentColor" strokeWidth="3.5" />
    </svg>
  );
}

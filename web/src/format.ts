import type { Trigger } from "./api";

const pad = (n: number) => String(n).padStart(2, "0");

// formatTime 把接口返回的时间显示为本地时间,精确到分钟。
export function formatTime(iso?: string): string {
  if (!iso) return "-";
  const d = new Date(iso);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// toLocalInput 给 datetime-local 控件用:不带时区的本地时间。
export function toLocalInput(iso: string): string {
  return formatTime(iso).replace(" ", "T");
}

// durationToMinutes 把 Go 的时长字符串("1h30m0s")换算成分钟,用于表单回填。
export function durationToMinutes(s?: string): string {
  if (!s) return "";
  const unit: Record<string, number> = { h: 3600, m: 60, s: 1 };
  let seconds = 0;
  for (const [, n, u] of s.matchAll(/([0-9.]+)(h|m|s)/g)) seconds += Number(n) * unit[u];
  return seconds ? String(Math.round(seconds / 60)) : "";
}

export function describeTrigger(tr: Trigger): string {
  return tr.cron ? tr.cron : "一次 " + formatTime(tr.at);
}

// 运行状态的文字和标记。标记的画法见 docs/DESIGN.md:空心是还没发生或正在发生,实心是已有结果。
export const STATUS: Record<string, [label: string, mark: string]> = {
  queued: ["排队中", "ring"],
  running: ["运行中", "spin ink"],
  succeeded: ["成功", "solid ok"],
  failed: ["失败", "solid bad"],
  timed_out: ["超时", "solid warn"],
  skipped: ["已跳过", "solid quiet"],
  missed: ["已错过", "solid quiet"],
  canceled: ["已取消", "solid quiet"],
  interrupted: ["中断", "solid bad"],
};

export const statusOf = (status: string): [string, string] => STATUS[status] ?? [status, "ring"];

// 权限级别在界面上的说法(决策记录 0008)。
export const PERMISSION: Record<string, string> = {
  read_only: "只读,不修改文件",
  write_cwd: "可以修改工作目录里的文件",
  full: "不加限制",
};

import type { Trigger } from "./api";
import { n, t } from "./i18n";

const pad = (n: number) => String(n).padStart(2, "0");

// formatTime 把接口返回的时间显示为本地时间,精确到分钟。
export function formatTime(iso?: string): string {
  if (!iso) return "-";
  const d = new Date(iso);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// friendlyTime 把时间写成读得懂的样子,用在列表里:"今天 18:31""明天 09:00",今年的只写月日。
// 精确的时间用 formatTime,放在详情和悬停提示里。
export function friendlyTime(iso?: string, now = new Date()): string {
  if (!iso) return "-";
  const d = new Date(iso);
  const time = `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  const day = (x: Date) => Math.round(new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime() / 86400000);
  const diff = day(d) - day(now);
  if (diff === 0) return t("今天 {time}", { time });
  if (diff === 1) return t("明天 {time}", { time });
  if (diff === -1) return t("昨天 {time}", { time });
  const date = `${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  return d.getFullYear() === now.getFullYear() ? `${date} ${time}` : `${d.getFullYear()}-${date} ${time}`;
}

// toLocalInput 给 datetime-local 控件用:不带时区的本地时间。
export function toLocalInput(iso: string): string {
  return formatTime(iso).replace(" ", "T");
}

// durationToMinutes 把 Go 的时长字符串("1h30m0s")换算成分钟,用于表单回填。
export function durationToMinutes(s?: string): string {
  const seconds = durationSeconds(s);
  return seconds ? String(Math.round(seconds / 60)) : "";
}

// durationSeconds 把后台的时长写法("1h30m0s")换算成秒,没有时为 0。
function durationSeconds(s?: string): number {
  if (!s) return 0;
  const unit: Record<string, number> = { h: 3600, m: 60, s: 1 };
  let seconds = 0;
  for (const [, n, u] of s.matchAll(/([0-9.]+)(h|m|s)/g)) seconds += Number(n) * unit[u];
  return seconds;
}

export function describeTrigger(tr: Trigger): string {
  if (!tr.cron) return t("一次 {time}", { time: formatTime(tr.at) });
  const text = describeCron(tr.cron);
  // 随机偏移(S12)写在后面:"每天 09:00 ±10 分钟"。
  const s = durationSeconds(tr.jitter);
  if (!s) return text;
  return s % 60 === 0 ? t("{text} ±{n} 分钟", { text, n: s / 60 }) : t("{text} ±{n} 秒", { text, n: s });
}

const WEEKDAYS = [n("周日"), n("周一"), n("周二"), n("周三"), n("周四"), n("周五"), n("周六")];

// describeCron 把常见的 cron 写成人读得懂的样子("每天 02:00""工作日 09:00");
// 认不出的原样返回。列表里只显示这个,cron 本身留在编辑和详情里(docs/DESIGN.md)。
export function describeCron(cron: string): string {
  const fields = cron.trim().split(/\s+/);
  if (fields.length !== 5) return cron;
  const [mi, h, dom, mon, dow] = fields;
  const everyDay = dom === "*" && mon === "*" && dow === "*";
  if ((mi === "*" || mi === "*/1") && h === "*" && everyDay) return t("每分钟");
  if (/^\*\/\d+$/.test(mi) && h === "*" && everyDay) return t("每 {n} 分钟", { n: mi.slice(2) });
  if (!/^\d+$/.test(mi)) return cron;
  const m = Number(mi);
  if (mon !== "*") {
    // 每年一次:月和日都是确定的数字。
    if (!/^\d+$/.test(mon) || !/^\d+$/.test(dom) || dow !== "*" || !/^\d+$/.test(h)) return cron;
    return t("每年 {mon} 月 {d} 日 {times}", { mon, d: dom, times: `${pad(Number(h))}:${pad(m)}` });
  }
  if (h === "*" && everyDay) return m === 0 ? t("每小时整点") : t("每小时第 {m} 分", { m });
  if (!/^\d+(,\d+)*$/.test(h)) return cron;
  const times = h
    .split(",")
    .map((x) => `${pad(Number(x))}:${pad(m)}`)
    .join(t("、"));
  if (everyDay) return t("每天 {times}", { times });
  if (dom === "*" && dow === "1-5") return t("工作日 {times}", { times });
  if (dom === "*" && /^[0-7](,[0-7])+$/.test(dow)) {
    const days = dow.split(",").map((x) => t(WEEKDAYS[Number(x) % 7])).join(t("、"));
    return t("每{day} {times}", { day: days, times });
  }
  if (dom === "*" && /^[0-7]$/.test(dow)) return t("每{day} {times}", { day: t(WEEKDAYS[Number(dow) % 7]), times });
  if (dow === "*" && /^\d+$/.test(dom)) return t("每月 {d} 日 {times}", { d: dom, times });
  return cron;
}

// 运行状态的文字和标记。标记的画法见 docs/DESIGN.md:空心是还没发生或正在发生,实心是已有结果。
const STATUS: Record<string, [label: string, mark: string]> = {
  queued: [n("排队中"), "ring"],
  running: [n("运行中"), "spin ink"],
  succeeded: [n("成功"), "solid ok"],
  failed: [n("失败"), "solid bad"],
  timed_out: [n("超时"), "solid warn"],
  skipped: [n("已跳过"), "solid quiet"],
  missed: [n("已错过"), "solid quiet"],
  canceled: [n("已取消"), "solid quiet"],
  interrupted: [n("中断"), "solid bad"],
};

export const statusOf = (status: string): [string, string] => {
  const [label, mark] = STATUS[status] ?? [status, "ring"];
  return [t(label), mark];
};

// 步骤类型在界面上的说法。
const STEP_TYPE: Record<string, string> = {
  script: n("脚本"),
  agent: n("AI 命令行"),
  text: n("文本"),
};

export const stepType = (type: string) => (type in STEP_TYPE ? t(STEP_TYPE[type]) : type);

// 权限级别在界面上的说法(决策记录 0008)。
const PERMISSION: Record<string, string> = {
  read_only: n("只读,不修改文件"),
  write_cwd: n("可以修改工作目录里的文件"),
  full: n("不加限制"),
};

export const permissionText = (level: string) => (level in PERMISSION ? t(PERMISSION[level]) : level);

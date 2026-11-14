// 定时的选择方式(S9):多数人不会写 cron,表单里改成"重复方式 + 时间"这样的选择,
// 选的结果换算成 cron 交给后台;认不出的 cron 和想自己写的人用"自定义"直接填写。和界面分开,便于测试。

export type Repeat = "daily" | "weekdays" | "weekly" | "monthly" | "hourly" | "minutes" | "cron";

export interface Pick {
  repeat: Repeat;
  time: string; // HH:MM,用于 daily、weekdays、weekly、monthly
  days: number[]; // 星期几,0 是周日;用于 weekly
  dom: number; // 每月几号;用于 monthly
  minute: number; // 每小时的第几分钟;用于 hourly
  every: number; // 每隔几分钟;用于 minutes
  cron: string; // 自定义时的表达式
}

// 每隔几分钟能选的值:都能整除 60,这样每小时的节奏一样。
export const EVERY = [1, 2, 5, 10, 15, 20, 30] as const;

const defaults: Pick = { repeat: "daily", time: "09:00", days: [1], dom: 1, minute: 0, every: 15, cron: "" };

const num = (s: string, max: number) => (/^\d{1,2}$/.test(s) && Number(s) <= max ? Number(s) : null);

// pickFromCron 把 cron 认成一种选择;认不出的(多个时间点、范围和步长的组合等)归为自定义,原样保留。
export function pickFromCron(cron: string): Pick {
  const expr = cron.trim();
  const custom: Pick = { ...defaults, repeat: "cron", cron: expr };
  const f = expr.split(/\s+/);
  if (f.length !== 5) return custom;
  const [mi, h, dom, mon, dow] = f;
  if (mon !== "*") return custom;
  if (h === "*" && dom === "*" && dow === "*") {
    if (mi === "*") return { ...defaults, repeat: "minutes", every: 1, cron: expr };
    const step = /^\*\/(\d+)$/.exec(mi);
    if (step && (EVERY as readonly number[]).includes(Number(step[1]))) return { ...defaults, repeat: "minutes", every: Number(step[1]), cron: expr };
    const m = num(mi, 59);
    return m === null ? custom : { ...defaults, repeat: "hourly", minute: m, cron: expr };
  }
  const m = num(mi, 59);
  const hour = num(h, 23);
  if (m === null || hour === null) return custom;
  const time = `${String(hour).padStart(2, "0")}:${String(m).padStart(2, "0")}`;
  if (dom === "*" && dow === "*") return { ...defaults, repeat: "daily", time, cron: expr };
  if (dom === "*") {
    const days = weekdays(dow);
    if (!days) return custom;
    if (days.join() === "1,2,3,4,5") return { ...defaults, repeat: "weekdays", time, cron: expr };
    return { ...defaults, repeat: "weekly", time, days, cron: expr };
  }
  const d = num(dom, 31);
  if (dow === "*" && d !== null && d >= 1) return { ...defaults, repeat: "monthly", time, dom: d, cron: expr };
  return custom;
}

// weekdays 解析星期字段:数字、逗号分隔的列表和 a-b 范围;7 也表示周日。认不出返回 null。
function weekdays(field: string): number[] | null {
  const set = new Set<number>();
  for (const part of field.split(",")) {
    const r = /^(\d)(?:-(\d))?$/.exec(part);
    if (!r) return null;
    const from = Number(r[1]);
    const to = r[2] === undefined ? from : Number(r[2]);
    if (from > 7 || to > 7 || to < from) return null;
    for (let d = from; d <= to; d++) set.add(d % 7);
  }
  return [...set].sort((a, b) => a - b);
}

// cronFromPick 把选择换算成 cron。选择不完整时抛出错误,错误信息直接显示给用户。
export function cronFromPick(p: Pick, message: (key: "time" | "days" | "cron") => string): string {
  if (p.repeat === "cron") {
    if (!p.cron.trim()) throw new Error(message("cron"));
    return p.cron.trim();
  }
  if (p.repeat === "minutes") return p.every === 1 ? "* * * * *" : `*/${p.every} * * * *`;
  if (p.repeat === "hourly") return `${p.minute} * * * *`;
  const t = /^(\d{1,2}):(\d{2})$/.exec(p.time);
  if (!t || Number(t[1]) > 23 || Number(t[2]) > 59) throw new Error(message("time"));
  const [h, m] = [Number(t[1]), Number(t[2])];
  switch (p.repeat) {
    case "daily":
      return `${m} ${h} * * *`;
    case "weekdays":
      return `${m} ${h} * * 1-5`;
    case "weekly":
      if (p.days.length === 0) throw new Error(message("days"));
      return `${m} ${h} * * ${[...p.days].sort((a, b) => a - b).join(",")}`;
    case "monthly":
      return `${m} ${h} ${p.dom} * *`;
  }
}

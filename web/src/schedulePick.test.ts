import { describe, expect, it } from "vitest";
import { cronFromPick, pickFromCron, type Pick } from "./schedulePick";

const msg = (what: string) => what;
const roundTrip = (cron: string) => cronFromPick(pickFromCron(cron), msg);

// S9:常见的 cron 认成选择,选择换算回同样的 cron;认不出的归为自定义,原样保留。
describe("定时的选择", () => {
  it("认出常见的写法", () => {
    expect(pickFromCron("0 2 * * *")).toMatchObject({ repeat: "daily", time: "02:00" });
    expect(pickFromCron("30 18 * * 1-5")).toMatchObject({ repeat: "weekdays", time: "18:30" });
    expect(pickFromCron("0 9 * * 1,3,5")).toMatchObject({ repeat: "weekly", time: "09:00", days: [1, 3, 5] });
    expect(pickFromCron("0 10 * * 6,7")).toMatchObject({ repeat: "weekly", days: [0, 6] });
    expect(pickFromCron("0 9 * * 1-3")).toMatchObject({ repeat: "weekly", days: [1, 2, 3] });
    expect(pickFromCron("15 8 1 * *")).toMatchObject({ repeat: "monthly", time: "08:15", dom: 1 });
    expect(pickFromCron("10 * * * *")).toMatchObject({ repeat: "hourly", minute: 10 });
    expect(pickFromCron("*/15 * * * *")).toMatchObject({ repeat: "minutes", every: 15 });
    expect(pickFromCron("* * * * *")).toMatchObject({ repeat: "minutes", every: 1 });
  });

  it("认不出的归为自定义,表达式原样保留", () => {
    for (const cron of ["0 8,13,20 * * *", "*/7 * * * *", "0 9-17 * * *", "0 0 1 1 *", "0 9 * * MON", "0 9 1 * 1", "bad"]) {
      expect(pickFromCron(cron)).toMatchObject({ repeat: "cron", cron });
      expect(roundTrip(cron)).toBe(cron);
    }
  });

  it("选择换算回 cron", () => {
    for (const cron of ["0 2 * * *", "30 18 * * 1-5", "0 9 * * 1,3,5", "15 8 1 * *", "10 * * * *", "*/15 * * * *", "* * * * *"]) {
      expect(roundTrip(cron)).toBe(cron);
    }
    expect(roundTrip("0 10 * * 6,7")).toBe("0 10 * * 0,6");
  });

  it("选择不完整时说明缺什么", () => {
    const base: Pick = pickFromCron("0 9 * * 1");
    expect(() => cronFromPick({ ...base, days: [] }, msg)).toThrow("days");
    expect(() => cronFromPick({ ...base, time: "" }, msg)).toThrow("time");
    expect(() => cronFromPick({ ...base, repeat: "cron", cron: " " }, msg)).toThrow("cron");
  });
});

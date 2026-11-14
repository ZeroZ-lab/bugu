import { beforeAll, describe, expect, it } from "vitest";
import { describeCron, friendlyTime } from "./format";
import { setLang } from "./i18n";

// 断言里的文字是中文原文。
beforeAll(() => setLang("zh"));

// docs/DESIGN.md"文案":时间写成用户读得懂的样子,cron 表达式只在编辑和详情里出现。
describe("friendlyTime", () => {
  const now = new Date(2026, 10, 13, 17, 0);
  const at = (y: number, mo: number, d: number, h: number, mi: number) => new Date(y, mo - 1, d, h, mi).toISOString();
  it("今天、明天、昨天按日历上的日子算,不按相隔的小时数", () => {
    expect(friendlyTime(at(2026, 11, 13, 18, 31), now)).toBe("今天 18:31");
    expect(friendlyTime(at(2026, 11, 13, 0, 5), now)).toBe("今天 00:05");
    expect(friendlyTime(at(2026, 11, 14, 9, 0), now)).toBe("明天 09:00");
    expect(friendlyTime(at(2026, 11, 12, 23, 59), now)).toBe("昨天 23:59");
  });
  it("更远的写日期,不是今年的带上年份,没有时间时是一个短横", () => {
    expect(friendlyTime(at(2026, 12, 1, 3, 17), now)).toBe("12-01 03:17");
    expect(friendlyTime(at(2027, 1, 1, 0, 0), now)).toBe("2027-01-01 00:00");
    expect(friendlyTime(undefined, now)).toBe("-");
  });
});

describe("describeCron", () => {
  it("常见的写法说成人话", () => {
    expect(describeCron("0 2 * * *")).toBe("每天 02:00");
    expect(describeCron("0 9 * * 1-5")).toBe("工作日 09:00");
    expect(describeCron("*/15 * * * *")).toBe("每 15 分钟");
    expect(describeCron("0 8 * * 1")).toBe("每周一 08:00");
    expect(describeCron("30 8 * * 1,3,5")).toBe("每周一、周三、周五 08:30");
    expect(describeCron("17 3 1 * *")).toBe("每月 1 日 03:17");
    expect(describeCron("0 0 1 1 *")).toBe("每年 1 月 1 日 00:00");
  });
  it("认不出的原样返回", () => {
    expect(describeCron("5,35 8-18/2 * * 1,3,5")).toBe("5,35 8-18/2 * * 1,3,5");
    expect(describeCron("0 0 1 1-6 *")).toBe("0 0 1 1-6 *");
  });
});

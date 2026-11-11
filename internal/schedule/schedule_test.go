package schedule

import (
	"testing"
	"time"

	"bugu/internal/task"
)

func at(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

// S1: 标准 cron 表达式的各种写法。2026-11-11 是周三。
func TestS1CronNext(t *testing.T) {
	tests := []struct {
		expr, after, want string
	}{
		{"0 2 * * *", "2026-11-11 01:59", "2026-11-11 02:00"},
		{"0 2 * * *", "2026-11-11 02:00", "2026-11-12 02:00"}, // 严格晚于
		{"* * * * *", "2026-11-11 09:00", "2026-11-11 09:01"},
		{"*/15 * * * *", "2026-11-11 09:46", "2026-11-11 10:00"},
		{"0 8,13,20 * * *", "2026-11-11 13:00", "2026-11-11 20:00"},
		{"0 9 * * 1-5", "2026-11-13 09:00", "2026-11-16 09:00"}, // 周五之后是周一
		{"0 9 * * mon-fri", "2026-11-13 09:00", "2026-11-16 09:00"},
		{"0 0 * * 7", "2026-11-11 00:00", "2026-11-15 00:00"}, // 7 也是周日
		{"0 0 * * 0", "2026-11-11 00:00", "2026-11-15 00:00"},
		{"30 4 1 * *", "2026-11-11 00:00", "2026-12-01 04:30"},
		{"0 0 1 jan *", "2026-11-11 00:00", "2027-01-01 00:00"},
		{"0 0 31 * *", "2026-11-11 00:00", "2026-12-31 00:00"}, // 跳过没有 31 日的月份
		{"0 0 29 2 *", "2026-11-11 00:00", "2028-02-29 00:00"}, // 闰年
		{"10-40/10 3 * * *", "2026-11-11 03:20", "2026-11-11 03:30"},
		{"5/20 * * * *", "2026-11-11 09:25", "2026-11-11 09:45"},
		// 日和周都写了限制时,满足其一即可:13 日或周五。
		{"0 0 13 * 5", "2026-11-11 00:00", "2026-11-13 00:00"},
		{"0 0 13 * 5", "2026-11-13 00:00", "2026-11-20 00:00"},
	}
	for _, tt := range tests {
		c, err := Parse(tt.expr)
		if err != nil {
			t.Errorf("%s: %v", tt.expr, err)
			continue
		}
		if got := c.Next(at(tt.after)); !got.Equal(at(tt.want)) {
			t.Errorf("%q 在 %s 之后 = %s,期望 %s", tt.expr, tt.after, got.Format("2006-01-02 15:04"), tt.want)
		}
	}
}

// S1: 写错的表达式被拒绝,而不是悄悄按别的意思执行。
func TestS1CronRejectsInvalid(t *testing.T) {
	for _, expr := range []string{
		"", "0 2 * *", "0 2 * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *",
		"* * * * 8", "*/0 * * * *", "5-1 * * * *", "a * * * *", "1,,2 * * * *", "* * * foo *", "-1 * * * *",
	} {
		if _, err := Parse(expr); err == nil {
			t.Errorf("%q 应被拒绝", expr)
		}
	}
}

func TestCronThatNeverFiresReturnsZero(t *testing.T) {
	c, err := Parse("0 0 30 2 *")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Next(at("2026-11-11 00:00")); !got.IsZero() {
		t.Fatalf("got %v", got)
	}
}

// S3: 按墙上时间计算。夏令时跳过的时间点顺延,重复的那一小时不触发两次。
func TestS3DaylightSaving(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("没有时区数据:", err)
	}
	c, _ := Parse("30 2 * * *")
	// 2026-03-08 02:00 直接跳到 03:00,02:30 不存在。
	got := c.Next(time.Date(2026, 3, 8, 0, 0, 0, 0, ny))
	if want := time.Date(2026, 3, 8, 3, 30, 0, 0, ny); !got.Equal(want) {
		t.Errorf("跳过的时间点 = %v,期望 %v", got, want)
	}
	if next := c.Next(got); next.Day() != 9 {
		t.Errorf("顺延之后同一天又触发了一次: %v", next)
	}
	// 2026-11-01 01:30 出现两次,只触发第一次。
	c, _ = Parse("30 1 * * *")
	first := c.Next(time.Date(2026, 11, 1, 0, 0, 0, 0, ny))
	if next := c.Next(first); next.Day() != 2 {
		t.Errorf("重复的一小时里触发了两次: %v 之后是 %v", first, next)
	}
	// 即使从第二个 01:xx 里开始查,也不会倒退回第一个 01:30。
	second := first.Add(time.Hour + 10*time.Minute)
	if next := c.Next(second); !next.After(second) || next.Day() != 2 {
		t.Errorf("从重复的一小时里查询得到 %v", next)
	}
}

func cron(expr string) task.Trigger { return task.Trigger{Cron: expr} }

// S3/S7: 只触发区间内的时间点;评估点推进后,同一个时间点不会再触发。
func TestS3S7EvaluateFiresOncePerTimePoint(t *testing.T) {
	tr := cron("0 2 * * *")
	d, err := Evaluate(tr, at("2026-11-11 01:59"), at("2026-11-11 01:59"))
	if err != nil || d.Fire {
		t.Fatalf("时间没到就触发: %+v %v", d, err)
	}
	d, _ = Evaluate(tr, at("2026-11-11 01:59"), at("2026-11-11 02:00"))
	if !d.Fire || !d.ScheduledFor.Equal(at("2026-11-11 02:00")) || d.Missed != 0 {
		t.Fatalf("到点没有触发: %+v", d)
	}
	// 重启后带着已保存的评估点再问一次,不能重复触发。
	if d, _ = Evaluate(tr, at("2026-11-11 02:00"), at("2026-11-11 02:05")); d.Fire {
		t.Fatalf("同一个时间点触发了两次: %+v", d)
	}
}

// S3/S4: 睡眠或停机跨过多个时间点,醒来只补最近一次,其余计为错过。
func TestS3S4EvaluateAfterSleepRunsOnce(t *testing.T) {
	d, err := Evaluate(cron("0 8,13,20 * * *"), at("2026-11-11 07:00"), at("2026-11-12 09:00"))
	if err != nil || !d.Fire || !d.ScheduledFor.Equal(at("2026-11-12 08:00")) || d.Missed != 3 {
		t.Fatalf("d=%+v err=%v", d, err)
	}
}

// S2: 一次性任务只在指定时间点触发一次。
func TestS2OneShot(t *testing.T) {
	tr := task.Trigger{At: at("2026-11-12 02:00")}
	if next, _ := Next(tr, at("2026-11-11 00:00")); !next.Equal(tr.At) {
		t.Fatalf("next=%v", next)
	}
	if d, _ := Evaluate(tr, at("2026-11-11 00:00"), at("2026-11-12 01:59")); d.Fire {
		t.Fatal("时间没到就触发")
	}
	d, _ := Evaluate(tr, at("2026-11-11 00:00"), at("2026-11-12 02:00"))
	if !d.Fire || !d.ScheduledFor.Equal(tr.At) || d.Missed != 0 {
		t.Fatalf("d=%+v", d)
	}
	if d, _ := Evaluate(tr, at("2026-11-12 02:00"), at("2026-11-13 00:00")); d.Fire {
		t.Fatal("一次性任务触发了第二次")
	}
	if next, _ := Next(tr, at("2026-11-12 02:00")); !next.IsZero() {
		t.Fatalf("触发之后不应再有下次: %v", next)
	}
}

func TestCheckAndEvaluateReportBadCron(t *testing.T) {
	bad := cron("not a cron")
	if Check(bad) == nil {
		t.Fatal("Check 应拒绝")
	}
	if _, err := Evaluate(bad, at("2026-11-11 00:00"), at("2026-11-12 00:00")); err == nil {
		t.Fatal("Evaluate 应报错")
	}
	if err := Check(task.Trigger{At: at("2026-11-12 02:00")}); err != nil {
		t.Fatal(err)
	}
}

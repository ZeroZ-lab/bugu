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
	d, err := Evaluate(tr, "", at("2026-11-11 01:59"), at("2026-11-11 01:59"))
	if err != nil || d.Fire {
		t.Fatalf("时间没到就触发: %+v %v", d, err)
	}
	d, _ = Evaluate(tr, "", at("2026-11-11 01:59"), at("2026-11-11 02:00"))
	if !d.Fire || !d.ScheduledFor.Equal(at("2026-11-11 02:00")) || d.Missed != 0 {
		t.Fatalf("到点没有触发: %+v", d)
	}
	// 重启后带着已保存的评估点再问一次,不能重复触发。
	if d, _ = Evaluate(tr, "", at("2026-11-11 02:00"), at("2026-11-11 02:05")); d.Fire {
		t.Fatalf("同一个时间点触发了两次: %+v", d)
	}
}

// S3/S4: 睡眠或停机跨过多个时间点,醒来只补最近一次,其余计为错过。
func TestS3S4EvaluateAfterSleepRunsOnce(t *testing.T) {
	d, err := Evaluate(cron("0 8,13,20 * * *"), "", at("2026-11-11 07:00"), at("2026-11-12 09:00"))
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
	if d, _ := Evaluate(tr, "", at("2026-11-11 00:00"), at("2026-11-12 01:59")); d.Fire {
		t.Fatal("时间没到就触发")
	}
	d, _ := Evaluate(tr, "", at("2026-11-11 00:00"), at("2026-11-12 02:00"))
	if !d.Fire || !d.ScheduledFor.Equal(tr.At) || d.Missed != 0 {
		t.Fatalf("d=%+v", d)
	}
	if d, _ := Evaluate(tr, "", at("2026-11-12 02:00"), at("2026-11-13 00:00")); d.Fire {
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
	if _, err := Evaluate(bad, "", at("2026-11-11 00:00"), at("2026-11-12 00:00")); err == nil {
		t.Fatal("Evaluate 应报错")
	}
	if err := Check(task.Trigger{At: at("2026-11-12 02:00")}); err != nil {
		t.Fatal(err)
	}
}

// S4: 补跑策略。按时的触发三种策略都运行;睡眠醒来晚了,run_once 补最近一次,
// skip 不补,window 只在宽限时长内补。
func TestS4CatchupPolicies(t *testing.T) {
	tr := cron("0 8,13,20 * * *")
	onTime, _ := Evaluate(tr, "", at("2026-11-12 07:59"), at("2026-11-12 08:00"))
	late, _ := Evaluate(tr, "", at("2026-11-11 07:00"), at("2026-11-12 09:00")) // 最近的是 08:00,晚了一小时
	for _, tt := range []struct {
		d      Decision
		policy string
		window time.Duration
		run    bool
		missed int
	}{
		{onTime, task.MissedSkip, 0, true, 0},
		{onTime, task.MissedWindow, time.Minute, true, 0},
		{late, "", 0, true, 3},
		{late, task.MissedRunOnce, 0, true, 3},
		{late, task.MissedSkip, 0, false, 4},
		{late, task.MissedWindow, 2 * time.Hour, true, 3},
		{late, task.MissedWindow, 30 * time.Minute, false, 4},
		{Decision{}, task.MissedRunOnce, 0, false, 0},
	} {
		now := at("2026-11-12 09:00")
		if tt.d.ScheduledFor.Equal(at("2026-11-12 08:00")) && tt.d.Missed == 0 {
			now = at("2026-11-12 08:00") // 按时的那次
		}
		if run, missed := Catchup(tt.d, now, tt.policy, tt.window); run != tt.run || missed != tt.missed {
			t.Errorf("%s/%v 原定 %v: run=%v missed=%d", tt.policy, tt.window, tt.d.ScheduledFor, run, missed)
		}
	}
	// 晚到不超过一分钟仍算按时。
	if run, _ := Catchup(onTime, at("2026-11-12 08:00").Add(OnTime), task.MissedSkip, 0); !run {
		t.Error("一分钟之内应算按时")
	}
}

// E7: cron 按任务的时区计算:同一个"每天 9 点",上海和纽约的触发时刻不同。
func TestE7CronInTaskTimezone(t *testing.T) {
	sh, err1 := time.LoadLocation("Asia/Shanghai")
	ny, err2 := time.LoadLocation("America/New_York")
	if err1 != nil || err2 != nil {
		t.Skip("没有时区数据")
	}
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	a, _ := Next(cron("0 9 * * *"), now.In(sh))
	b, _ := Next(cron("0 9 * * *"), now.In(ny))
	if !a.Equal(time.Date(2026, 11, 12, 1, 0, 0, 0, time.UTC)) || !b.Equal(time.Date(2026, 11, 12, 14, 0, 0, 0, time.UTC)) {
		t.Fatalf("上海 %v,纽约 %v", a.UTC(), b.UTC())
	}
}

// S12: 随机偏移在 [-jitter, +jitter] 内、精确到秒,有早有晚、每天不同;同一个时间点每次算都一样,
// 换一个任务名得到另一组偏移。
func TestS12OffsetIsBoundedStableAndVaried(t *testing.T) {
	tr := cron("0 9 * * *")
	tr.Jitter = task.Duration(10 * time.Minute)
	seen := map[time.Duration]bool{}
	early, late, differ := 0, 0, 0
	for day := range 60 {
		s := at("2026-11-01 09:00").AddDate(0, 0, day)
		off := Offset(tr, "report", s)
		if off < -10*time.Minute || off > 10*time.Minute || off%time.Second != 0 {
			t.Fatalf("%v 的偏移 %v 超出范围", s, off)
		}
		if Offset(tr, "report", s) != off || Offset(tr, "report", s.In(time.FixedZone("x", 8*3600))) != off {
			t.Fatalf("%v 的偏移不稳定", s)
		}
		if Offset(tr, "other", s) != off {
			differ++
		}
		seen[off] = true
		if off < 0 {
			early++
		} else if off > 0 {
			late++
		}
	}
	if len(seen) < 50 || early < 15 || late < 15 || differ < 50 {
		t.Fatalf("偏移不够分散: %d 种,早 %d 晚 %d,与别的任务不同 %d", len(seen), early, late, differ)
	}
	if Offset(cron("0 9 * * *"), "report", at("2026-11-01 09:00")) != 0 {
		t.Fatal("没有设置偏移时应为 0")
	}
}

// S12 S7: 设置了偏移的任务在实际时间到了才触发,不早不晚;按短周期评估时每个时间点只触发一次,
// 先后顺序不变;补跑的迟到从实际时间算起。
func TestS12EvaluateAtJitteredTime(t *testing.T) {
	tr := cron("0 9 * * *")
	tr.Jitter = task.Duration(10 * time.Minute)
	since := at("2026-11-01 00:00")
	var fired []Decision
	for now := since.Add(15 * time.Second); now.Before(at("2026-11-08 00:00")); now = now.Add(15 * time.Second) {
		d, err := Evaluate(tr, "report", since, now)
		if err != nil {
			t.Fatal(err)
		}
		if d.Fire {
			if d.Missed != 0 || now.Sub(d.At) < 0 || now.Sub(d.At) >= 15*time.Second || !d.At.Equal(d.ScheduledFor.Add(Offset(tr, "report", d.ScheduledFor))) {
				t.Fatalf("触发 %+v 于 %v", d, now)
			}
			fired = append(fired, d)
		}
		since = now
	}
	if len(fired) != 7 {
		t.Fatalf("七天应触发 7 次,实际 %d 次", len(fired))
	}
	for i, d := range fired {
		if !d.ScheduledFor.Equal(at("2026-11-01 09:00").AddDate(0, 0, i)) {
			t.Fatalf("第 %d 次对应 %v", i, d.ScheduledFor)
		}
	}
	// NextOccurrence 给出的就是这些实际时间,任务列表的"下次运行"据此显示。
	o, _ := NextOccurrence(tr, "report", at("2026-11-01 00:00"))
	if !o.At.Equal(fired[0].At) || !o.ScheduledFor.Equal(fired[0].ScheduledFor) {
		t.Fatalf("next=%+v", o)
	}
	// 停机一天后醒来:迟到从实际时间算,宽限窗口按它判断。
	d, _ := Evaluate(tr, "report", at("2026-11-01 00:00"), fired[0].At.Add(30*time.Minute))
	if run, _ := Catchup(d, fired[0].At.Add(30*time.Minute), task.MissedWindow, 31*time.Minute); !run {
		t.Fatal("实际时间之后 30 分钟应在 31 分钟的宽限内")
	}
	if run, _ := Catchup(d, fired[0].At.Add(30*time.Minute), task.MissedWindow, 29*time.Minute); run {
		t.Fatal("实际时间之后 30 分钟应超出 29 分钟的宽限")
	}
}

// S12: 偏移要小于相邻两次触发最小间隔的一半;一次性任务和负的偏移不接受。
func TestS12CheckJitter(t *testing.T) {
	for _, tt := range []struct {
		cron   string
		jitter time.Duration
		ok     bool
	}{
		{"*/15 * * * *", 7 * time.Minute, true},
		{"*/15 * * * *", 450 * time.Second, false},
		{"*/15 * * * *", 10 * time.Minute, false},
		{"0 9 * * *", 10 * time.Minute, true},
		{"0 9 * * *", 11 * time.Hour, true},
		{"0 9 * * *", 12 * time.Hour, false},
		{"0,5 * * * *", 2 * time.Minute, true},
		{"0,5 * * * *", 3 * time.Minute, false},
	} {
		tr := cron(tt.cron)
		tr.Jitter = task.Duration(tt.jitter)
		if err := Check(tr); (err == nil) != tt.ok {
			t.Errorf("%s ±%v: %v", tt.cron, tt.jitter, err)
		}
	}
}

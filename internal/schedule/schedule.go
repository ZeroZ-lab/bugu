package schedule

import (
	"hash/fnv"
	"time"

	"bugu/internal/i18n"
	"bugu/internal/task"
)

// Check 报告任务的触发定义能否被调度。设置了随机偏移时,它必须小于相邻两次触发最小间隔的一半,
// 这样偏移之后触发的先后不变,不会两次挤在一起或颠倒(S12)。
func Check(tr task.Trigger) error {
	if tr.Cron == "" {
		return nil
	}
	c, err := Parse(tr.Cron)
	if err != nil || tr.Jitter <= 0 {
		return err
	}
	if gap := minGap(c); gap > 0 && 2*time.Duration(tr.Jitter) >= gap {
		return i18n.Errorf("随机偏移 %s 太大:相邻两次触发最近只隔 %s,偏移要小于它的一半", time.Duration(tr.Jitter), gap)
	}
	return nil
}

// minGap 返回相邻两个触发时间点的最小间隔:从一个固定日期起取前若干个时间点求出,永不触发时返回 0。
func minGap(c Cron) time.Duration {
	prev := c.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	var gap time.Duration
	for range 2000 {
		next := c.Next(prev)
		if prev.IsZero() || next.IsZero() {
			break
		}
		if g := next.Sub(prev); gap == 0 || g < gap {
			gap = g
		}
		prev = next
	}
	return gap
}

// Offset 返回原定时间点 at 的随机偏移,在 [-jitter, +jitter] 内,精确到秒(S12)。
// 它由 seed(任务名)和时间点散列得出而不是现抽的:同一个时间点无论重启或评估多少次都一样(决策记录 0018)。
func Offset(tr task.Trigger, seed string, at time.Time) time.Duration {
	j := int64(time.Duration(tr.Jitter) / time.Second)
	if j <= 0 || tr.Cron == "" {
		return 0
	}
	h := fnv.New64a()
	h.Write([]byte(seed))
	h.Write([]byte{0})
	h.Write([]byte(at.UTC().Format(time.RFC3339)))
	return time.Duration(int64(h.Sum64()%uint64(2*j+1))-j) * time.Second
}

// Occurrence 是一次触发:原定的时间点,以及加上随机偏移之后实际触发的时间。没有偏移时两者相同。
type Occurrence struct {
	ScheduledFor time.Time
	At           time.Time
}

// NextOccurrence 返回实际触发时间在 after 之后的下一次触发,没有则返回零值。
func NextOccurrence(tr task.Trigger, seed string, after time.Time) (Occurrence, error) {
	j := time.Duration(tr.Jitter)
	if tr.Cron == "" || j <= 0 {
		next, err := Next(tr, after)
		return Occurrence{next, next}, err
	}
	// 实际时间不早于原定时间减去偏移范围,所以从 after 往前一个范围找起;Check 保证偏移不改变先后,
	// 第一个实际时间落在 after 之后的就是。
	for from := after.Add(-j); ; {
		next, err := Next(tr, from)
		if err != nil || next.IsZero() {
			return Occurrence{}, err
		}
		if at := next.Add(Offset(tr, seed, next)); at.After(after) {
			return Occurrence{next, at}, nil
		}
		from = next
	}
}

// Next 返回任务在 after 之后的下一个触发时间,没有则返回零值。
func Next(tr task.Trigger, after time.Time) (time.Time, error) {
	if tr.Cron == "" {
		if tr.At.After(after) {
			return tr.At, nil
		}
		return time.Time{}, nil
	}
	c, err := Parse(tr.Cron)
	if err != nil {
		return time.Time{}, err
	}
	return c.Next(after), nil
}

// Decision 是一次评估的结论。
type Decision struct {
	Fire         bool      // 现在是否应该触发一次。
	ScheduledFor time.Time // 这次触发对应的原定时间点,即区间内最近的一个。
	At           time.Time // 这次触发实际该在的时间:原定时间点加上随机偏移(S12)。
	Missed       int       // 区间内更早的、不再补跑的时间点数量。
}

// OnTime 是"按时"的容差:原定时间过去不超过它,就不算错过。后台每 15 秒评估一次,
// 正常情况下触发总在这个范围内;超过它说明中间睡眠或停机了。
const OnTime = time.Minute

// Catchup 按任务的补跑策略(S4)决定一次评估要不要真的运行。missed 是这次一共错过、不再运行的时间点数。
// 按时的触发总是运行;晚了的,run_once 补跑最近一次,skip 不补,window 在宽限时长内才补。
func Catchup(d Decision, now time.Time, policy string, window time.Duration) (run bool, missed int) {
	if !d.Fire {
		return false, 0
	}
	late := now.Sub(d.At) // 设置了随机偏移时,从实际该触发的时间算起
	switch {
	case late <= OnTime, policy == task.MissedRunOnce, policy == "":
		run = true
	case policy == task.MissedWindow:
		run = late <= window
	}
	if run {
		return true, d.Missed
	}
	return false, d.Missed + 1
}

// Evaluate 判断在 (since, now] 这段时间里任务该不该触发。since 是上次评估到的时间点,
// 调用者要把它持久化,这样重启后不会重复触发(S7),睡眠或停机后也能知道错过了什么(S3)。
// seed 是任务名,决定随机偏移(S12);判断的是实际触发时间是否落在区间里。
//
// 区间内无论有多少个触发时间点,都只触发一次,对应最近的那个;更早的计入 Missed。
func Evaluate(tr task.Trigger, seed string, since, now time.Time) (Decision, error) {
	var d Decision
	for after := since; ; {
		o, err := NextOccurrence(tr, seed, after)
		if err != nil {
			return Decision{}, err
		}
		if o.At.IsZero() || o.At.After(now) {
			return d, nil
		}
		if d.Fire {
			d.Missed++
		}
		d.Fire, d.ScheduledFor, d.At, after = true, o.ScheduledFor, o.At, o.At
	}
}

package schedule

import (
	"time"

	"bugu/internal/task"
)

// Check 报告任务的触发定义能否被调度。
func Check(tr task.Trigger) error {
	if tr.Cron == "" {
		return nil
	}
	_, err := Parse(tr.Cron)
	return err
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
	Missed       int       // 区间内更早的、不再补跑的时间点数量。
}

// Evaluate 判断在 (since, now] 这段时间里任务该不该触发。since 是上次评估到的时间点,
// 调用者要把它持久化,这样重启后不会重复触发(S7),睡眠或停机后也能知道错过了什么(S3)。
//
// 区间内无论有多少个触发时间点,都只触发一次,对应最近的那个;更早的计入 Missed。
func Evaluate(tr task.Trigger, since, now time.Time) (Decision, error) {
	var d Decision
	for at := since; ; {
		next, err := Next(tr, at)
		if err != nil {
			return Decision{}, err
		}
		if next.IsZero() || next.After(now) {
			return d, nil
		}
		if d.Fire {
			d.Missed++
		}
		d.Fire, d.ScheduledFor, at = true, next, next
	}
}

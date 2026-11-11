package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron 是解析后的五段式 cron 表达式:分 时 日 月 周。每个字段是一个位集合。
type Cron struct {
	minute, hour, dom, month, dow uint64
	// 日和周是否写了限制。两者都限制时,满足任意一个即触发,这是 cron 的传统语义。
	domSet, dowSet bool
}

type field struct {
	name     string
	min, max int
	names    []string // 可用的英文缩写,下标加 min 即为数值
}

var (
	fields = [5]field{
		{"分", 0, 59, nil},
		{"时", 0, 23, nil},
		{"日", 1, 31, nil},
		{"月", 1, 12, []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}},
		{"周", 0, 7, []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}},
	}
)

// Parse 解析标准的五段式 cron 表达式(S1)。支持 *、列表(1,3)、范围(1-5)、
// 步长(*/15、10-40/10)以及月和周的英文缩写。周的 0 和 7 都表示周日。
func Parse(expr string) (Cron, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return Cron{}, fmt.Errorf("cron 表达式 %q 应有 5 段(分 时 日 月 周),实际有 %d 段", expr, len(parts))
	}
	var sets [5]uint64
	for i, part := range parts {
		set, err := fields[i].parse(part)
		if err != nil {
			return Cron{}, fmt.Errorf("cron 表达式 %q 的第 %d 段(%s): %w", expr, i+1, fields[i].name, err)
		}
		sets[i] = set
	}
	if sets[4]&(1<<7) != 0 { // 7 归并为 0
		sets[4] = sets[4]&^(1<<7) | 1
	}
	return Cron{
		minute: sets[0], hour: sets[1], dom: sets[2], month: sets[3], dow: sets[4],
		domSet: !strings.HasPrefix(parts[2], "*"), dowSet: !strings.HasPrefix(parts[4], "*"),
	}, nil
}

func (f field) parse(s string) (uint64, error) {
	var set uint64
	for _, item := range strings.Split(s, ",") {
		rng, stepText, hasStep := strings.Cut(item, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepText)
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("步长 %q 应为正整数", stepText)
			}
			step = n
		}
		lo, hi := f.min, f.max
		if rng != "*" {
			from, to, isRange := strings.Cut(rng, "-")
			var err error
			if lo, err = f.value(from); err != nil {
				return 0, err
			}
			hi = lo
			if isRange {
				if hi, err = f.value(to); err != nil {
					return 0, err
				}
			} else if hasStep {
				hi = f.max // "5/15" 表示从 5 开始每隔 15
			}
			if lo > hi {
				return 0, fmt.Errorf("范围 %q 的起点大于终点", rng)
			}
		}
		for v := lo; v <= hi; v += step {
			set |= 1 << v
		}
	}
	return set, nil
}

func (f field) value(s string) (int, error) {
	for i, name := range f.names {
		if strings.EqualFold(s, name) {
			return i + f.min, nil
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < f.min || n > f.max {
		return 0, fmt.Errorf("%q 不是 %d 到 %d 之间的数", s, f.min, f.max)
	}
	return n, nil
}

func (c Cron) matchDay(t time.Time) bool {
	if c.month&(1<<int(t.Month())) == 0 {
		return false
	}
	dom := c.dom&(1<<t.Day()) != 0
	dow := c.dow&(1<<int(t.Weekday())) != 0
	if c.domSet && c.dowSet {
		return dom || dow
	}
	return dom && dow
}

// 向后查找的上限。最稀疏的合法表达式是 2 月 29 日,相邻两次最多隔 8 年。
const searchDays = 366 * 9

// Next 返回严格晚于 after 的下一个触发时间,使用 after 所在的时区。
// 表达式永远不会触发(如 2 月 30 日)时返回零值。
//
// 夏令时:落在被跳过的那一小时里的时间点顺延到跳过之后;重复的那一小时只触发第一次。
func (c Cron) Next(after time.Time) time.Time {
	loc := after.Location()
	y, mo, d := after.Date()
	h0, m0 := after.Hour(), after.Minute()+1
	for day := range searchDays {
		// 用正午判断日期,避开夏令时在午夜切换的时区。
		date := time.Date(y, mo, d+day, 12, 0, 0, 0, loc)
		if !c.matchDay(date) {
			continue
		}
		for h := range 24 {
			if c.hour&(1<<h) == 0 || (day == 0 && h < h0) {
				continue
			}
			for m := range 60 {
				if c.minute&(1<<m) == 0 || (day == 0 && h == h0 && m < m0) {
					continue
				}
				if cand := wallTime(date, h, m); cand.After(after) {
					return cand
				}
			}
		}
	}
	return time.Time{}
}

// wallTime 返回 date 当天墙上时间 h:m 对应的时刻。这个墙上时间因夏令时被跳过时,
// time.Date 不保证落在切换的哪一侧,这里统一取切换之后的那一侧,即顺延。
func wallTime(date time.Time, h, m int) time.Time {
	t := time.Date(date.Year(), date.Month(), date.Day(), h, m, 0, 0, date.Location())
	diff := (h*60 + m) - (t.Hour()*60 + t.Minute())
	if diff > 12*60 { // 跨过午夜时按较短的方向算
		diff -= 24 * 60
	} else if diff < -12*60 {
		diff += 24 * 60
	}
	if diff > 0 {
		t = t.Add(time.Duration(diff) * time.Minute)
	}
	return t
}

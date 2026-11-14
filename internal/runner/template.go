package runner

import (
	"strings"
	"time"
)

// Vars 是一次运行里可用的模板变量(PRD 7.4)。
type Vars struct {
	TaskName string
	Now      time.Time // 本次运行开始的时间
	LastRun  time.Time // 上次成功运行的开始时间;没有时由调用者填入任务的创建时间
	RunDir   string    // 本次运行的存档目录
	StateDir string    // 本任务的状态目录
	// OutputFile 是本次应生成的产物文件的完整路径;任务没有设置产物时为空
	OutputFile string
	Prev       Output // 上一个步骤的输出
	Steps      map[string]Output
}

// Output 是一个步骤的输出:文本本身,以及保存它的文件。
type Output struct {
	Text string
	File string
}

// Expand 把 s 里的 {date}、{prev} 等模板变量换成本次运行的值,步骤的输出原样代入。
// 用于提示词和文本步骤。不认识的花括号内容原样保留,所以其中可以放心出现 JSON 或 shell 的花括号。
func (v Vars) Expand(s string) string {
	return v.expand(s, false)
}

// ExpandCommand 用于交给 /bin/sh 的命令。步骤的输出不直接拼进命令,
// 而是代入为从输出文件读取的命令替换,并按所在位置的引号加上引号:
// 输出里的引号、分号、$ 等都不会被 shell 解释。命令替换会去掉结尾的换行。
func (v Vars) ExpandCommand(s string) string {
	return v.expand(s, true)
}

func (v Vars) expand(s string, shell bool) string {
	if !strings.Contains(s, "{") {
		return s
	}
	var b strings.Builder
	q := quoteNone
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '{' {
			if end := strings.IndexByte(s[i:], '}'); end > 0 {
				key := s[i+1 : i+end]
				if text, out, ok := v.lookup(key); ok {
					switch {
					case out == nil:
						b.WriteString(text)
					case shell:
						b.WriteString(readFile(out.File, q))
					default:
						b.WriteString(out.Text)
					}
					i += end
					continue
				}
			}
		}
		b.WriteByte(c)
		if shell {
			q, i = q.next(s, i, &b)
		}
	}
	return b.String()
}

// lookup 返回变量的值。步骤输出返回 out,其他变量返回 text。
func (v Vars) lookup(key string) (text string, out *Output, ok bool) {
	switch key {
	case "date":
		return v.Now.Format("2006-01-02"), nil, true
	case "time":
		return v.Now.Format("15:04:05"), nil, true
	case "datetime":
		return v.Now.Format(time.RFC3339), nil, true
	case "last_run":
		return v.LastRun.Format(time.RFC3339), nil, true
	case "run_dir":
		return v.RunDir, nil, true
	case "state_dir":
		return v.StateDir, nil, true
	case "task_name":
		return v.TaskName, nil, true
	case "output_file":
		return v.OutputFile, nil, true
	case "prev":
		return "", &v.Prev, true
	}
	if name, ok := StepRef(key); ok {
		if o, found := v.Steps[name]; found {
			return "", &o, true
		}
	}
	return "", nil, false
}

// StepRef 报告花括号里的内容是不是 steps.<步骤名>.output,是则返回步骤名。
func StepRef(key string) (string, bool) {
	rest, ok := strings.CutPrefix(key, "steps.")
	if !ok {
		return "", false
	}
	name, ok := strings.CutSuffix(rest, ".output")
	return name, ok && name != ""
}

// References 列出 s 里引用的步骤输出:是否用到 {prev},以及 {steps.<步骤名>.output} 里的步骤名。
func References(s string) (prev bool, steps []string) {
	for rest := s; ; {
		start := strings.IndexByte(rest, '{')
		if start < 0 {
			return prev, steps
		}
		rest = rest[start+1:]
		end := strings.IndexByte(rest, '}')
		if end < 0 {
			return prev, steps
		}
		key := rest[:end]
		if key == "prev" {
			prev = true
		} else if name, ok := StepRef(key); ok {
			steps = append(steps, name)
		} else {
			continue // 不是变量,可能是 JSON 的花括号;从它后面接着找
		}
		rest = rest[end+1:]
	}
}

type quote int

const (
	quoteNone quote = iota
	quoteSingle
	quoteDouble
)

// next 根据刚写出的字符 s[i] 更新引号状态。反斜杠转义的下一个字符原样写出并跳过。
func (q quote) next(s string, i int, b *strings.Builder) (quote, int) {
	switch c := s[i]; {
	case q == quoteSingle:
		if c == '\'' {
			return quoteNone, i
		}
	case c == '\\' && i+1 < len(s):
		b.WriteByte(s[i+1])
		return q, i + 1
	case q == quoteNone && c == '\'':
		return quoteSingle, i
	case q == quoteNone && c == '"':
		return quoteDouble, i
	case q == quoteDouble && c == '"':
		return quoteNone, i
	}
	return q, i
}

// readFile 返回在当前引号状态下读取文件内容的 shell 写法。
func readFile(path string, q quote) string {
	sub := `$(cat ` + singleQuote(path) + `)`
	if path == "" {
		sub = ""
	}
	switch q {
	case quoteDouble:
		return sub
	case quoteSingle:
		return `'"` + sub + `"'` // 先结束单引号,用双引号包住替换,再接着单引号
	}
	return `"` + sub + `"`
}

func singleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

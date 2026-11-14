package exec

import (
	"strings"
	"sync"
)

// Live 让调用者在运行过程中读到正在执行的那一步已经产生的输出(G6)。
// 输出只在内存里,读的时候和存档一样脱敏(E10),所以密钥不会以明文落盘;
// 这一步结束后它的输出照常写进存档,Live 不再提供。零值可以直接使用。
type Live struct {
	mu      sync.Mutex
	step    int
	running bool
	hide    func(string) string
	whole   bool // 只给完整的行,见 Snapshot
	out     *capture
	err     *capture
}

// begin 在第 i 步启动之前调用。wholeLines 为真时运行中只给出完整的行,任务有要脱敏的值时这样用。
func (l *Live) begin(i int, hide func(string) string, wholeLines bool) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.step, l.running, l.hide, l.whole, l.out, l.err = i, true, hide, wholeLines, nil, nil
}

// attach 在这一步的进程启动后交出它的两路输出。
func (l *Live) attach(out, err *capture) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.out, l.err = out, err
}

// end 在这一步结束、输出已经写进存档之后调用。
func (l *Live) end() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.running, l.out, l.err = false, nil, nil
}

// Snapshot 返回正在执行的步骤序号(从 0 起)和它到目前为止的输出。
// 没有正在执行的步骤时 ok 为假;这一步还没启动进程(如文本步骤、预检中)时输出为空。
func (l *Live) Snapshot() (step int, stdout, stderr string, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.running {
		return 0, "", "", false
	}
	if l.out != nil {
		stdout, stderr = l.out.String(), l.err.String()
		// 进程可能正写到一个密钥的中间:只认得完整的值的脱敏会漏掉那半截。
		// 环境变量文件里的值都在一行之内,所以扣下最后不完整的一行,等它写完再给。
		if l.whole {
			stdout, stderr = wholeLines(stdout), wholeLines(stderr)
		}
		stdout, stderr = l.hide(stdout), l.hide(stderr)
	}
	return l.step, stdout, stderr, true
}

// wholeLines 去掉最后一个换行之后还没写完的部分。
func wholeLines(s string) string { return s[:strings.LastIndexByte(s, '\n')+1] }

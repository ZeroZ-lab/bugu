// fakeagent 是测试用的假 agent:一个按环境变量表现的小程序,用来在不调用真实
// AI 命令行的前提下测试执行、超时、卡死、等待确认和产物校验。
//
// 提示词取自命令行参数;没有参数时读取标准输入。行为由环境变量控制:
//
//	FAKEAGENT_OUTPUT   标准输出的内容。未设置时输出 "fakeagent: <提示词>"
//	FAKEAGENT_STDERR   标准错误的内容
//	FAKEAGENT_EXIT     退出码,默认 0
//	FAKEAGENT_SLEEP    输出后再等待的时长,如 "30s"
//	FAKEAGENT_HANG     设为 1 时输出后永不退出
//	FAKEAGENT_ASK      设为 1 时先提问并等待标准输入的一行;读不到则以 3 退出
//	FAKEAGENT_REPEAT   重复输出的次数，默认 1
//	FAKEAGENT_READY    就绪后向此路径写入父进程及子进程 PID (可为 FIFO)
//	FAKEAGENT_CHILD    设为 1 时启动持有输出管道且卡住的子进程
//	FAKEAGENT_PARENT_EXIT 设为 1 时在就绪握手后父进程退出
//	FAKEAGENT_CHILD_DETACH 设为 1 时上述子进程在新会话中启动,脱离父进程的进程组
//	FAKEAGENT_WRITE    要写入的文件路径,内容取自 FAKEAGENT_WRITE_CONTENT
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"time"
)

// exitNoInput 是等待确认却读不到输入时的退出码。
const exitNoInput = 3

type env struct {
	getenv func(string) string
	sleep  func(time.Duration)
	hang   func()
	ready  func()
}

func main() {
	var childPID int
	if os.Getenv("FAKEAGENT_CHILD") == "1" {
		r, w, err := os.Pipe()
		if err != nil {
			panic(err)
		}
		child := exec.Command(os.Args[0], "child")
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "FAKEAGENT_CHILD=") && !strings.HasPrefix(entry, "FAKEAGENT_READY=") && !strings.HasPrefix(entry, "FAKEAGENT_PARENT_EXIT=") {
				child.Env = append(child.Env, entry)
			}
		}
		child.Env = append(child.Env, "FAKEAGENT_READY_FD=3", "FAKEAGENT_HANG=1")
		child.ExtraFiles = []*os.File{w}
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if os.Getenv("FAKEAGENT_CHILD_DETACH") == "1" {
			detach(child)
		}
		if err := child.Start(); err != nil {
			panic(err)
		}
		childPID = child.Process.Pid
		w.Close()
		var b [1]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			panic(err)
		}
		r.Close()
	}
	ready := func() {
		if os.Getenv("FAKEAGENT_READY_FD") == "3" {
			f := os.NewFile(3, "ready")
			_, _ = f.Write([]byte{1})
			f.Close()
		}
		if path := os.Getenv("FAKEAGENT_READY"); path != "" {
			f, err := os.OpenFile(path, os.O_WRONLY, 0)
			if err != nil {
				panic(err)
			}
			fmt.Fprintf(f, "%d %d\n", os.Getpid(), childPID)
			f.Close()
		}
		if os.Getenv("FAKEAGENT_PARENT_EXIT") == "1" {
			os.Exit(0)
		}
	}

	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, env{
		getenv: os.Getenv,
		sleep:  time.Sleep,
		hang: func() {
			ch := make(chan os.Signal, 1)
			signal.Notify(ch, os.Interrupt)
			defer signal.Stop(ch)
			ready()
			<-ch
		},
		ready: func() {
			if os.Getenv("FAKEAGENT_HANG") != "1" || os.Getenv("FAKEAGENT_PARENT_EXIT") == "1" {
				ready()
			}
		},
	}))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, e env) int {
	in := bufio.NewReader(stdin)

	if e.getenv("FAKEAGENT_ASK") == "1" {
		fmt.Fprintln(stdout, "fakeagent: 是否允许执行此操作? [y/N]")
		if _, err := in.ReadString('\n'); err != nil {
			fmt.Fprintln(stderr, "fakeagent: 没有可用的输入,无法确认")
			return exitNoInput
		}
	}

	prompt := strings.Join(args, " ")
	if len(args) == 0 {
		b, _ := io.ReadAll(in)
		prompt = strings.TrimSpace(string(b))
	}

	if path := e.getenv("FAKEAGENT_WRITE"); path != "" {
		if err := os.WriteFile(path, []byte(e.getenv("FAKEAGENT_WRITE_CONTENT")), 0o644); err != nil {
			fmt.Fprintln(stderr, "fakeagent:", err)
			return 1
		}
	}

	repeat := 1
	if n, err := strconv.Atoi(e.getenv("FAKEAGENT_REPEAT")); err == nil && n > 0 {
		repeat = n
	}
	for range repeat {
		if out := e.getenv("FAKEAGENT_OUTPUT"); out != "" {
			fmt.Fprintln(stdout, out)
		} else {
			fmt.Fprintln(stdout, "fakeagent:", prompt)
		}
		if s := e.getenv("FAKEAGENT_STDERR"); s != "" {
			fmt.Fprintln(stderr, s)
		}

	}

	if e.ready != nil {
		e.ready()
	}

	if s := e.getenv("FAKEAGENT_SLEEP"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			fmt.Fprintln(stderr, "fakeagent: FAKEAGENT_SLEEP 无效:", err)
			return 1
		}
		e.sleep(d)
	}
	if e.getenv("FAKEAGENT_HANG") == "1" {
		e.hang()
	}

	if s := e.getenv("FAKEAGENT_EXIT"); s != "" {
		code, err := strconv.Atoi(s)
		if err != nil {
			fmt.Fprintln(stderr, "fakeagent: FAKEAGENT_EXIT 无效:", err)
			return 1
		}
		return code
	}
	return 0
}

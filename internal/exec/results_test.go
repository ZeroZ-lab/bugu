//go:build darwin || linux

package exec

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"bugu/internal/clock"
	"bugu/internal/runner"
	"bugu/internal/task"
)

func runTask(t *testing.T, input task.Task, info Info) (task.Run, error) {
	t.Helper()
	if input.Name == "" {
		input.Name = "x"
	}
	return (Executor{Clock: clock.NewFake(epoch)}).Run(context.Background(), input, info)
}

// writeEnvelope 是写结果信封的脚本步骤。
func writeEnvelope(json string) task.Step {
	return sh("", "cat > {run_dir}/result.json <<'EOF'\n"+json+"\nEOF")
}

// O12: 结果信封给出摘要、产物和通知请求;有信封时以它为准。
func TestO12EnvelopeIsAuthoritative(t *testing.T) {
	runDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(runDir, "report.md"), []byte("# 报告"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := runTask(t, task.Task{Steps: []task.Step{
		sh("", "echo 原始输出"),
		writeEnvelope(`{"status": "succeeded", "summary": " 两条新闻 ", "artifacts": ["report.md"], "notify": true, "message": "看看报告", "extra": 1}`),
	}}, Info{RunDir: runDir})
	if err != nil || r.Status != task.Succeeded || r.Summary != "两条新闻" || r.Message != "看看报告" || r.Notify == nil || !*r.Notify {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	if len(r.Artifacts) != 1 || r.Artifacts[0] != filepath.Join(runDir, "report.md") {
		t.Fatalf("artifacts=%v", r.Artifacts)
	}

	for want, json := range map[string]string{
		"结果信封声明本次失败:配额用完":     `{"status": "failed", "summary": "配额用完"}`,
		"不是合法的 JSON":          `{"status": `,
		"status \"done\" 不合法": `{"status": "done"}`,
		"产物":                  `{"artifacts": ["missing.md"]}`,
	} {
		r, err := runTask(t, task.Task{Steps: []task.Step{writeEnvelope(json)}}, Info{})
		if err == nil || r.Status != task.Failed || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: status=%s err=%v", want, r.Status, err)
		}
	}

	// 信封不能把失败的步骤改成成功。
	r, err = runTask(t, task.Task{Steps: []task.Step{sh("", `echo '{"status": "succeeded"}' > {run_dir}/result.json; exit 1`)}}, Info{})
	if err == nil || r.Status != task.Failed {
		t.Fatalf("result=%+v err=%v", r, err)
	}
}

// T7 O12: 任一步骤可以用结果信封声明无事可做,后面的步骤不执行。
func TestT7EnvelopeNothingToDo(t *testing.T) {
	dir := t.TempDir()
	r, err := runTask(t, task.Task{Cwd: dir, Steps: []task.Step{writeEnvelope(`{"status": "no_content"}`), sh("", "touch ran")}}, Info{})
	if err != nil || r.Status != task.Succeeded || !r.NoContent || r.Steps[1].Status != task.Skipped {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran")); err == nil {
		t.Fatal("声明无事可做之后的步骤被执行了")
	}
}

// O5 O6: 默认把最后一步的输出保存为产物;文件名可用模板变量;无内容时不落盘。
func TestO5O6OutputSavedFromStdout(t *testing.T) {
	cwd := t.TempDir()
	output := &task.Output{Dir: "reports/daily", Name: "{task_name}-{date}.md"}
	r, err := runTask(t, task.Task{Name: "news", Cwd: cwd, Output: output, Steps: []task.Step{sh("", "echo '# 今日新闻'; echo {output_file}")}}, Info{})
	file := filepath.Join(cwd, "reports", "daily", "news-2026-11-11.md")
	if err != nil || len(r.Artifacts) != 1 || r.Artifacts[0] != file {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	if b, _ := os.ReadFile(file); string(b) != "# 今日新闻\n"+file+"\n" {
		t.Fatalf("产物内容 %q", b)
	}

	r, err = runTask(t, task.Task{Name: "quiet", Cwd: cwd, Output: output, SilentIf: "NOTHING_NEW", Steps: []task.Step{sh("", "echo NOTHING_NEW")}}, Info{})
	if _, statErr := os.Stat(filepath.Join(cwd, "reports", "daily", "quiet-2026-11-11.md")); err != nil || !r.NoContent || statErr == nil || r.Artifacts != nil {
		t.Fatalf("无内容时不应落盘: result=%+v err=%v stat=%v", r, err, statErr)
	}
}

// O6 R6: 产物由任务自己写入 {output_file} 时,文件不存在就记为失败。
func TestO6R6OutputWrittenByTask(t *testing.T) {
	dir := t.TempDir()
	output := &task.Output{Dir: dir, Name: "out.txt", From: task.OutputFromTask}
	r, err := runTask(t, task.Task{Output: output, Steps: []task.Step{sh("", "echo 自己写 > {output_file}; echo 完成")}}, Info{})
	if err != nil || r.Status != task.Succeeded || len(r.Artifacts) != 1 {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "out.txt")); string(b) != "自己写\n" {
		t.Fatalf("产物内容 %q", b)
	}
	output.Name = "never.txt"
	r, err = runTask(t, task.Task{Output: output, Steps: []task.Step{sh("", "echo 忘了写")}}, Info{})
	if err == nil || r.Status != task.Failed || !strings.Contains(err.Error(), "没有生成产物文件") {
		t.Fatalf("result=%+v err=%v", r, err)
	}
}

// E6: 产物目录不可写时不启动,说明原因。
func TestE6OutputDirectoryMustBeWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 不受目录权限限制")
	}
	dir := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "started")
	r, err := runTask(t, task.Task{Output: &task.Output{Dir: dir, Name: "a.txt"}, Steps: []task.Step{sh("", "touch "+marker)}}, Info{})
	if err == nil || !strings.Contains(err.Error(), "不可写") || r.Status != task.Failed {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("预检未通过时不应启动")
	}
}

// O5: 产物设置有误时在保存任务时就被拒绝。
func TestO5ValidateOutput(t *testing.T) {
	steps := []task.Step{sh("", "true")}
	for want, input := range map[string]task.Task{
		"缺少目录":           {Steps: steps, Output: &task.Output{Name: "a"}},
		"缺少文件名":          {Steps: steps, Output: &task.Output{Dir: "/tmp"}},
		"不能包含 /":         {Steps: steps, Output: &task.Output{Dir: "/tmp", Name: "a/b"}},
		"产物来源":           {Steps: steps, Output: &task.Output{Dir: "/tmp", Name: "a", From: "agent"}},
		"没有设置产物(output)": {Steps: []task.Step{sh("", "echo > {output_file}")}},
	} {
		if err := Validate(input, nil); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err=%v", want, err)
		}
	}
}

// E8 E10: 环境变量文件里的值注入子进程,任务自己的 env 优先;这些值在输出、摘要和错误里脱敏。
func TestE8E10EnvFileInjectedAndRedacted(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "secrets.env")
	body := "# 密钥\nexport API_TOKEN=\"tok-123456\"\nSHORT=on\nREGION='cn north'\nOVERRIDDEN=from-file\n\n"
	if err := os.WriteFile(envFile, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	input := task.Task{Cwd: dir, EnvFile: "secrets.env", Env: map[string]string{"OVERRIDDEN": "from-task"}, Steps: []task.Step{
		sh("", `echo "token=$API_TOKEN short=$SHORT region=$REGION o=$OVERRIDDEN"; echo "$API_TOKEN" >&2`),
		sh("", `cat; printf '{"summary": "用了 %s"}' "$API_TOKEN" > {run_dir}/result.json`),
	}}
	runDir := t.TempDir()
	r, err := runTask(t, input, Info{RunDir: runDir})
	want := "token=[已隐藏 API_TOKEN] short=on region=[已隐藏 REGION] o=from-task\n"
	if err != nil || r.Stdout != want || r.Summary != "用了 [已隐藏 API_TOKEN]" || strings.Contains(r.Stderr, "tok-123456") {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	for i := range 2 {
		out, errOut, _ := StepOutput(runDir, i)
		if strings.Contains(out+errOut, "tok-123456") {
			t.Errorf("第 %d 步的存档里有明文: %q %q", i+1, out, errOut)
		}
	}

	// 中间步骤写了不合法的信封,错误里引用的内容同样脱敏。
	input.Steps = []task.Step{sh("", `printf '{"status": "%s"}' "$API_TOKEN" > {run_dir}/result.json`), sh("", "true")}
	if _, err := runTask(t, input, Info{}); err == nil || strings.Contains(err.Error(), "tok-123456") || !strings.Contains(err.Error(), "[已隐藏 API_TOKEN]") {
		t.Fatalf("信封错误: %v", err)
	}

	// 读不到文件时不启动;格式不对时指出第几行。
	input.EnvFile = "missing.env"
	if _, err := runTask(t, input, Info{}); err == nil || !strings.Contains(err.Error(), "环境变量文件") {
		t.Fatalf("err=%v", err)
	}
	if err := os.WriteFile(envFile, []byte("OK=1\nnot a pair\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input.EnvFile = envFile
	if _, err := runTask(t, input, Info{}); err == nil || !strings.Contains(err.Error(), "第 2 行") {
		t.Fatalf("err=%v", err)
	}
}

// E7: {date} 等按任务的时区显示。纪元是 UTC 2026-11-11 09:00,在奥克兰已是 11-11 22:00,在洛杉矶是 01:00。
func TestE7TemplateDatesInTaskTimezone(t *testing.T) {
	for tz, want := range map[string]string{"Pacific/Auckland": "2026-11-11 22:00:00", "America/Los_Angeles": "2026-11-11 01:00:00", "": "2026-11-11 09:00:00"} {
		r, err := runTask(t, task.Task{Timezone: tz, Steps: []task.Step{sh("", "echo {date} {time}")}}, Info{})
		if err != nil || r.Stdout != want+"\n" {
			t.Errorf("%q: %q %v", tz, r.Stdout, err)
		}
	}
}

// E2: 没有指定工作目录时,脚本和 AI 命令行步骤都在任务的状态目录里运行,
// 不取决于后台进程从哪个目录启动;指定了工作目录时照旧。
func TestE2DefaultCwdIsStateDir(t *testing.T) {
	state, runDir := t.TempDir(), t.TempDir()
	rn := runner.Runner{Name: "fake", Command: []string{agent}, PromptVia: runner.ViaArg}
	input := task.Task{Name: "x", Env: map[string]string{"FAKEAGENT_WRITE": "note.txt", "FAKEAGENT_WRITE_CONTENT": "记下"}, Steps: []task.Step{
		sh("", "pwd -P"),
		{Type: task.StepAgent, Agent: task.Agent{Runner: "fake", Prompt: "hi"}},
	}}
	if _, err := (Executor{Clock: clock.NewFake(epoch), Runners: lookup(rn)}).Run(context.Background(), input, Info{StateDir: state, RunDir: runDir}); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(state)
	if out, _, err := StepOutput(runDir, 0); err != nil || strings.TrimSpace(out) != real {
		t.Errorf("脚本步骤的工作目录: %q,应为 %q", out, real)
	}
	if b, err := os.ReadFile(filepath.Join(state, "note.txt")); err != nil || string(b) != "记下" {
		t.Errorf("AI 命令行步骤没有在状态目录里运行: %q %v", b, err)
	}

	other := t.TempDir()
	input.Cwd, input.Steps = other, input.Steps[:1]
	if r, err := runTask(t, input, Info{StateDir: state}); err != nil || !strings.Contains(r.Stdout, filepath.Base(other)) {
		t.Fatalf("指定的工作目录: %q %v", r.Stdout, err)
	}
}

// G6 E10: 运行中能读到正在执行的那一步已有的输出,和存档一样脱敏;这一步结束后不再从内存提供。
func TestG6LiveOutputOfRunningStep(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("API_TOKEN=tok-123456\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	readyPath, readyCh := ready(t)
	gate := filepath.Join(dir, "gate")
	if err := syscall.Mkfifo(gate, 0o600); err != nil {
		t.Fatal(err)
	}
	input := task.Task{Cwd: dir, EnvFile: "secrets.env", Steps: []task.Step{
		sh("", "echo one"),
		// 第二行先只写出密钥的前半截,停住,之后再写完。
		sh("", `echo "token=$API_TOKEN"; printf 'key=%s' "${API_TOKEN%3456}"; echo half >&2; echo go > `+readyPath+`; read x < `+gate+`; echo 3456; echo done`),
	}}
	live := &Live{}
	if _, _, _, ok := live.Snapshot(); ok {
		t.Fatal("还没开始就有输出")
	}
	done := make(chan outcome, 1)
	go func() { r, err := runTask(t, input, Info{Live: live}); done <- outcome{r, err} }()
	<-readyCh
	// 输出先于握手写进管道,但采集它的协程可能还没读到:按次数重试,不按时间等。
	var step int
	var out, errOut string
	var ok bool
	for range 1000 {
		if step, out, errOut, ok = live.Snapshot(); ok && strings.Contains(out, "\n") && errOut != "" {
			break
		}
		runtime.Gosched()
	}
	if !ok || step != 1 || out != "token=[已隐藏 API_TOKEN]\n" || errOut != "half\n" {
		t.Fatalf("运行中: step=%d out=%q err=%q ok=%v", step, out, errOut, ok)
	}
	if err := os.WriteFile(gate, []byte("go\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || got.run.Stdout != "token=[已隐藏 API_TOKEN]\nkey=[已隐藏 API_TOKEN]\ndone\n" {
		t.Fatalf("result=%+v err=%v", got.run, got.err)
	}
	if _, _, _, ok := live.Snapshot(); ok {
		t.Fatal("结束之后还在提供输出")
	}
}

// E10 O2: 输出过大省略中间时,截断处不留下半截密钥:任务有环境变量文件时只在行的边界上截断。
func TestE10O2CutDoesNotSplitSecrets(t *testing.T) {
	c := capture{limit: 40, whole: true}
	for _, s := range []string{"first line\n", "0123tok-123456 a\n", strings.Repeat("filler\n", 20), "6 b\n", "last tok-123456\n"} {
		c.Write([]byte(s))
	}
	got := c.String()
	head, rest, _ := strings.Cut(got, "\n[bugu: ")
	_, tail, _ := strings.Cut(rest, "\n")
	if head != "first line" { // 开头保留的 20 字节里第二行只有半截(含密钥的开头),整行省略
		t.Fatalf("截断处: %q", got)
	}
	for _, line := range strings.Split(strings.TrimSuffix(tail, "\n"), "\n") {
		if line != "filler" && line != "6 b" && line != "last tok-123456" {
			t.Fatalf("结尾里有不完整的行 %q: %q", line, got)
		}
	}

	// 整个运行:密钥正好跨在开头保留部分的末尾,存档里看不到它的任何一截。
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("API_TOKEN=tok-123456\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := task.Task{Name: "x", Cwd: dir, EnvFile: "secrets.env", Steps: []task.Step{
		sh("", `printf '0123456789012345%s\n' "$API_TOKEN"; i=0; while [ $i -lt 200 ]; do echo filler; i=$((i+1)); done`),
	}}
	r, err := (Executor{Clock: clock.NewFake(epoch), MaxOutput: 40}).Run(context.Background(), input, Info{})
	if err != nil || strings.Contains(r.Stdout, "tok") || !strings.Contains(r.Stdout, "[bugu: ") {
		t.Fatalf("result=%q err=%v", r.Stdout, err)
	}
}

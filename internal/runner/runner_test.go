package runner

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"bugu/internal/task"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// R2: 自带三份预设,每一份都自洽、能用于 AI 命令行步骤、声明了只读级别并以它为默认。
func TestR2PresetsAreUsable(t *testing.T) {
	list, err := Open("").List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("预设应有 3 份,实际 %d 份", len(list))
	}
	for _, r := range list {
		if !r.Preset || r.PromptVia == "" || len(r.Check) == 0 {
			t.Errorf("预设 %s 不完整: %+v", r.Name, r)
		}
		inv, err := r.Render(task.Agent{Runner: r.Name, Prompt: "p"}, "p", "")
		if err != nil || inv.Level != task.PermReadOnly {
			t.Errorf("预设 %s 默认应以只读级别渲染: %+v, %v", r.Name, inv, err)
		}
		if len(r.Levels()) < 2 {
			t.Errorf("预设 %s 应至少还有一个可放开的级别: %v", r.Name, r.Levels())
		}
	}
}

// R3: 用户目录里的配置与预设地位相同,同名时覆盖预设;有问题的配置不影响其余的。
func TestR3UserRunnersOverrideAndCoexist(t *testing.T) {
	presets, _ := Open("").List()
	dir := t.TempDir()
	write(t, dir, "stats.json", `{"command": ["stats", "run"], "prompt_via": "stdin"}`)
	write(t, dir, presets[0].Name+".json", `{"command": ["mine"], "prompt_via": "arg"}`)
	write(t, dir, "broken.json", `{"command": []}`)
	write(t, dir, "notes.txt", `不是配置`)

	set := Open(dir)
	list, err := set.List()
	if err == nil || !strings.Contains(err.Error(), "broken.json") {
		t.Fatalf("应报告有问题的配置: %v", err)
	}
	if len(list) != 4 {
		t.Fatalf("应有 3 份预设加 1 份自定义,实际 %d 份", len(list))
	}
	own, err := set.Get("stats")
	if err != nil || own.Preset || own.Name != "stats" {
		t.Fatalf("自定义运行器: %+v, %v", own, err)
	}
	over, err := set.Get(presets[0].Name)
	if err != nil || over.Preset || over.Command[0] != "mine" {
		t.Fatalf("同名配置应覆盖预设: %+v, %v", over, err)
	}
	if _, err := set.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的运行器: %v", err)
	}
	if _, err := set.Get("../etc/passwd"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("名字不能越出目录: %v", err)
	}
}

// R1: 配置里的 name 必须与文件名一致,不合法的字段被拒绝并说明原因。
func TestR1ConfigIsValidated(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"a.json": `{"name": "b", "command": ["x"]}`,
		"c.json": `{"command": ["x"], "prompt_via": "telepathy"}`,
		"d.json": `{"command": ["x"], "prompt_via": "file"}`,
		"e.json": `{"command": ["x", "{prompt_file}"], "prompt_via": "stdin"}`,
		"f.json": `{"command": ["x", "{prompt}"], "prompt_via": "stdin"}`,
		"g.json": `{"command": ["x"], "permissions": {"root": []}}`,
		"h.json": `{"command": ["x"], "max_permission": "everything"}`,
		"i.json": `not json`,
	} {
		write(t, dir, name, content)
		if _, err := Open(dir).Get(strings.TrimSuffix(name, ".json")); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("%s 应被拒绝: %v", name, err)
		}
	}
}

// R4: 提示词的三种传递方式。
func TestR4PromptDelivery(t *testing.T) {
	step := task.Agent{Prompt: "ignored"}
	for _, c := range []struct {
		r     Runner
		argv  []string
		stdin bool
	}{
		{Runner{Name: "a", Command: []string{"x", "-p"}, PromptVia: ViaArg}, []string{"x", "-p", "hello world"}, false},
		{Runner{Name: "a", Command: []string{"x", "--ask={prompt}", "-q"}, PromptVia: ViaArg}, []string{"x", "--ask=hello world", "-q"}, false},
		{Runner{Name: "a", Command: []string{"x", "-p"}, PromptVia: ViaStdin}, []string{"x", "-p"}, true},
		{Runner{Name: "a", Command: []string{"x", "--file", "{prompt_file}"}, PromptVia: ViaFile}, []string{"x", "--file", "/run/prompt.txt"}, false},
	} {
		inv, err := c.r.Render(step, "hello world", "/run/prompt.txt")
		if err != nil || !reflect.DeepEqual(inv.Argv, c.argv) || inv.Stdin != c.stdin || inv.Level != "" {
			t.Errorf("%+v: %+v, %v", c.r, inv, err)
		}
	}
	if _, err := (Runner{Name: "plain", Command: []string{"x"}}).Render(step, "p", ""); err == nil {
		t.Error("没有声明 prompt_via 的运行器不能用于 AI 命令行步骤")
	}
}

// R7 与决策记录 0008: 默认只读;只能选运行器声明过的级别;不能超过 max_permission;
// 没有声明权限参数的运行器不接受级别。
func TestR7PermissionLevels(t *testing.T) {
	r := Runner{Name: "a", Command: []string{"x"}, PromptVia: ViaArg, Permissions: map[string][]string{
		task.PermReadOnly: {"--ro"}, task.PermFull: {"--yolo"},
	}}
	inv, err := r.Render(task.Agent{}, "p", "")
	if err != nil || inv.Level != task.PermReadOnly || !reflect.DeepEqual(inv.Argv, []string{"x", "--ro", "p"}) {
		t.Fatalf("默认应为只读: %+v, %v", inv, err)
	}
	inv, err = r.Render(task.Agent{Permission: task.PermFull}, "p", "")
	if err != nil || inv.Level != task.PermFull || !reflect.DeepEqual(inv.Argv, []string{"x", "--yolo", "p"}) {
		t.Fatalf("显式放开: %+v, %v", inv, err)
	}
	if _, err := r.Level(task.PermWriteCwd); err == nil {
		t.Error("运行器没有声明的级别不能选")
	}
	if _, err := r.Level("root"); err == nil {
		t.Error("不认识的级别应被拒绝")
	}
	if got := r.Levels(); !reflect.DeepEqual(got, []string{task.PermReadOnly, task.PermFull}) {
		t.Errorf("可选级别: %v", got)
	}

	r.MaxPermission = task.PermWriteCwd
	if _, err := r.Level(task.PermFull); err == nil || !strings.Contains(err.Error(), "最高只允许") {
		t.Errorf("超过上限应被拒绝: %v", err)
	}
	if got := r.Levels(); !reflect.DeepEqual(got, []string{task.PermReadOnly}) {
		t.Errorf("上限之内的可选级别: %v", got)
	}

	plain := Runner{Name: "plain", Command: []string{"x"}, PromptVia: ViaStdin}
	if level, err := plain.Level(""); err != nil || level != "" {
		t.Errorf("不适用权限级别的运行器: %q, %v", level, err)
	}
	if _, err := plain.Level(task.PermReadOnly); err == nil {
		t.Error("不能给没有声明权限参数的运行器指定级别")
	}
}

// R6: 退出码之外的成功条件。
func TestR6SuccessConditions(t *testing.T) {
	r := Runner{Name: "a", Success: Success{Contains: "DONE", NotContains: "ERROR"}}
	if err := r.Succeeded("all DONE"); err != nil {
		t.Error(err)
	}
	if r.Succeeded("nothing") == nil || r.Succeeded("DONE with ERROR") == nil {
		t.Error("不满足成功条件时应说明原因")
	}
	if err := (Runner{}).Succeeded(""); err != nil {
		t.Error(err)
	}
}

// PRD 7.4: 模板变量按本次运行的值展开,不认识的花括号原样保留。
func TestTemplateVariables(t *testing.T) {
	zone := time.FixedZone("x", 8*3600)
	v := Vars{
		TaskName: "news", RunDir: "/data/runs/1",
		Now:     time.Date(2026, 11, 12, 9, 30, 5, 0, zone),
		LastRun: time.Date(2026, 11, 11, 9, 0, 0, 0, zone),
	}
	got := v.Expand(`{task_name} {date} {time} {datetime} since {last_run} in {run_dir}; {"json": true} ${HOME} {unknown}`)
	want := `news 2026-11-12 09:30:05 2026-11-12T09:30:05+08:00 since 2026-11-11T09:00:00+08:00 in /data/runs/1; {"json": true} ${HOME} {unknown}`
	if got != want {
		t.Fatalf("\n得到 %s\n应为 %s", got, want)
	}
}

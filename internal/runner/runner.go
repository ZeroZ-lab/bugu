package runner

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"bugu/internal/i18n"
	"bugu/internal/task"
	"bugu/runners"
)

// ErrNotFound 表示没有这个名字的运行器。
var ErrNotFound = errors.New("runner: not found")

// 提示词的传递方式(R4)。
const (
	ViaArg   = "arg"   // 作为命令行参数
	ViaStdin = "stdin" // 写入标准输入
	ViaFile  = "file"  // 写入文件,文件路径通过参数里的 {prompt_file} 传入
)

// 运行器命令里可以使用的占位符。
const (
	promptMark     = "{prompt}"
	promptFileMark = "{prompt_file}"
)

// Runner 是一份运行器配置,对应一个 JSON 文件,文件名(不含扩展名)即 Name。
type Runner struct {
	Name  string `json:"name"`
	Title string `json:"title,omitempty"` // 界面上显示的名字,为空时显示 Name。
	// Command 是启动命令及其固定参数,应包含让工具不进入交互界面的参数。
	Command []string `json:"command"`
	// PromptVia 为空的运行器只是一条普通命令,不能用于 AI 命令行步骤。
	PromptVia string `json:"prompt_via,omitempty"`
	// Permissions 是各权限级别对应的额外参数;没有声明的级别不能选(决策记录 0008)。
	Permissions map[string][]string `json:"permissions,omitempty"`
	// MaxPermission 是这台主机上允许的最高级别,为空表示不限制。
	MaxPermission string `json:"max_permission,omitempty"`
	// Success 是退出码为 0 之外的成功条件(R6)。
	Success Success `json:"success,omitzero"`
	// Check 是确认工具能启动的命令,例如查看版本(R8);为空时只检查命令是否存在。
	Check []string `json:"check,omitempty"`
	// Preset 表示这是自带的预设,由读取的一方填写。
	Preset bool `json:"preset,omitempty"`
}

// Success 里填写的条件都要满足,并且退出码为 0,运行才算成功。
type Success struct {
	Contains    string `json:"contains,omitempty"`     // 标准输出必须包含
	NotContains string `json:"not_contains,omitempty"` // 标准输出不能包含
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// Validate 检查一份运行器配置本身是否自洽。
func Validate(r Runner) error {
	if !namePattern.MatchString(r.Name) {
		return i18n.Errorf("运行器名 %q 不合法:只能包含字母、数字、- 和 _,以字母或数字开头,最长 64 个字符", r.Name)
	}
	if len(r.Command) == 0 || r.Command[0] == "" {
		return i18n.Errorf("command 不能为空,第一项是要启动的程序")
	}
	hasMark := func(mark string) bool {
		return slices.ContainsFunc(r.Command, func(a string) bool { return strings.Contains(a, mark) })
	}
	switch r.PromptVia {
	case "", ViaArg, ViaStdin:
		if hasMark(promptFileMark) {
			return i18n.Errorf("command 里用了 %s,prompt_via 应为 %s", promptFileMark, ViaFile)
		}
	case ViaFile:
		if !hasMark(promptFileMark) {
			return i18n.Errorf("prompt_via 为 %s 时,command 里必须有一个参数包含 %s", ViaFile, promptFileMark)
		}
	default:
		return i18n.Errorf("prompt_via %q 不合法,可用的是 %s、%s、%s", r.PromptVia, ViaArg, ViaStdin, ViaFile)
	}
	if r.PromptVia != ViaArg && hasMark(promptMark) {
		return i18n.Errorf("command 里用了 %s,prompt_via 应为 %s", promptMark, ViaArg)
	}
	for level := range r.Permissions {
		if _, ok := task.PermissionRank(level); !ok {
			return i18n.Errorf("permissions 里的级别 %q 不合法,可用的是 %s", level, strings.Join(task.Permissions, ", "))
		}
	}
	if r.MaxPermission != "" {
		if _, ok := task.PermissionRank(r.MaxPermission); !ok {
			return i18n.Errorf("max_permission %q 不合法,可用的是 %s", r.MaxPermission, strings.Join(task.Permissions, ", "))
		}
	}
	return nil
}

// Levels 返回任务可以为这个运行器选择的权限级别,由低到高:
// 运行器声明过、且不超过 MaxPermission 的那些。
func (r Runner) Levels() []string {
	limit := len(task.Permissions)
	if rank, ok := task.PermissionRank(r.MaxPermission); ok {
		limit = rank + 1
	}
	var levels []string
	for _, level := range task.Permissions[:limit] {
		if _, ok := r.Permissions[level]; ok {
			levels = append(levels, level)
		}
	}
	return levels
}

// Level 把任务里写的权限级别解析成实际使用的级别:为空时取 read_only。
// 没有声明任何权限参数的运行器不适用权限级别,此时只接受空值并返回空。
func (r Runner) Level(requested string) (string, error) {
	if len(r.Permissions) == 0 {
		if requested != "" {
			return "", i18n.Errorf("运行器 %s 没有声明权限参数,不能为它指定权限级别 %q", r.Name, requested)
		}
		return "", nil
	}
	level := requested
	if level == "" {
		level = task.PermReadOnly
	}
	rank, ok := task.PermissionRank(level)
	if !ok {
		return "", i18n.Errorf("权限级别 %q 不合法,可用的是 %s", level, strings.Join(task.Permissions, ", "))
	}
	if _, declared := r.Permissions[level]; !declared {
		return "", i18n.Errorf("运行器 %s 不支持权限级别 %s,它支持的是 %s", r.Name, level, strings.Join(r.Levels(), ", "))
	}
	if max, limited := task.PermissionRank(r.MaxPermission); limited && rank > max {
		return "", i18n.Errorf("运行器 %s 在这台主机上最高只允许 %s,任务要求的是 %s", r.Name, r.MaxPermission, level)
	}
	return level, nil
}

// Invocation 是渲染后的一次调用。
type Invocation struct {
	Argv []string
	// Stdin 为真时,提示词要写进子进程的标准输入。
	Stdin bool
	// Level 是实际使用的权限级别,运行器不适用权限级别时为空。
	Level string
}

// Render 把一个 AI 命令行步骤渲染成要执行的命令。prompt 是已经展开模板变量的提示词;
// promptFile 是它落盘后的路径,仅在提示词通过文件传递时使用。
func (r Runner) Render(step task.Agent, prompt, promptFile string) (Invocation, error) {
	if r.PromptVia == "" {
		return Invocation{}, i18n.Errorf("运行器 %s 没有声明提示词的传递方式(prompt_via),不能用于 AI 命令行步骤", r.Name)
	}
	level, err := r.Level(step.Permission)
	if err != nil {
		return Invocation{}, err
	}
	inv := Invocation{Level: level, Stdin: r.PromptVia == ViaStdin}
	placed := false
	for _, a := range r.Command {
		if strings.Contains(a, promptMark) {
			placed = true
		}
		a = strings.ReplaceAll(a, promptMark, prompt)
		inv.Argv = append(inv.Argv, strings.ReplaceAll(a, promptFileMark, promptFile))
	}
	inv.Argv = append(inv.Argv, r.Permissions[level]...)
	if r.PromptVia == ViaArg && !placed {
		inv.Argv = append(inv.Argv, prompt)
	}
	return inv, nil
}

// Succeeded 报告退出码为 0 的一次运行是否满足运行器的成功条件;不满足时说明原因。
func (r Runner) Succeeded(stdout string) error {
	if s := r.Success.Contains; s != "" && !strings.Contains(stdout, s) {
		return i18n.Errorf("输出里没有运行器 %s 约定的成功标记 %q", r.Name, s)
	}
	if s := r.Success.NotContains; s != "" && strings.Contains(stdout, s) {
		return i18n.Errorf("输出里出现了运行器 %s 约定的失败标记 %q", r.Name, s)
	}
	return nil
}

// Set 是一台主机上可用的全部运行器:自带的预设,加上 dir 目录下用户自己的配置。
// 同名时用户的配置优先。每次查询都重新读取文件,修改配置不需要重启后台。
type Set struct {
	dir string
}

// Open 返回以 dir 为用户配置目录的运行器集合。目录可以不存在。
func Open(dir string) Set { return Set{dir: dir} }

func parse(data []byte, file string) (Runner, error) {
	var r Runner
	if err := json.Unmarshal(data, &r); err != nil {
		return r, i18n.Errorf("运行器配置 %s 不是合法的 JSON: %w", file, err)
	}
	name := strings.TrimSuffix(filepath.Base(file), ".json")
	if r.Name == "" {
		r.Name = name
	}
	if r.Name != name {
		return r, i18n.Errorf("运行器配置 %s 里的 name 是 %q,应与文件名一致", file, r.Name)
	}
	r.Preset = false
	if err := Validate(r); err != nil {
		return r, i18n.Errorf("运行器配置 %s: %w", file, err)
	}
	return r, nil
}

// Get 返回指定名字的运行器;不存在时返回 ErrNotFound。
func (s Set) Get(name string) (Runner, error) {
	if !namePattern.MatchString(name) {
		return Runner{}, ErrNotFound
	}
	if s.dir != "" {
		file := filepath.Join(s.dir, name+".json")
		data, err := os.ReadFile(file)
		if err == nil {
			return parse(data, file)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return Runner{}, err
		}
	}
	data, err := runners.Files.ReadFile(name + ".json")
	if err != nil {
		return Runner{}, ErrNotFound
	}
	r, err := parse(data, name+".json")
	r.Preset = true
	return r, err
}

// List 返回全部运行器,按名字排序。个别配置有问题时仍返回其余的,问题汇总在 error 里。
func (s Set) List() ([]Runner, error) {
	names := map[string]bool{}
	collect := func(entries []fs.DirEntry) {
		for _, e := range entries {
			if name, ok := strings.CutSuffix(e.Name(), ".json"); ok && !e.IsDir() && namePattern.MatchString(name) {
				names[name] = true
			}
		}
	}
	presets, err := runners.Files.ReadDir(".")
	if err != nil {
		return nil, err
	}
	collect(presets)
	if s.dir != "" {
		own, err := os.ReadDir(s.dir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		collect(own)
	}
	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	var list []Runner
	var problems error
	for _, name := range sorted {
		r, err := s.Get(name)
		if err != nil {
			problems = errors.Join(problems, err)
			continue
		}
		list = append(list, r)
	}
	return list, problems
}

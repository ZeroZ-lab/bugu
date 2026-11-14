package exec

import (
	"path/filepath"

	"bugu/internal/api"
	"bugu/internal/i18n"
	"bugu/internal/runner"
	"bugu/internal/task"
)

// Plan 说明现在运行一次会执行什么,但不启动任何进程、不写任何文件(bugu run --dry-run)。
// 模板变量按现在的时间展开;上一步的输出要运行时才知道,用一个占位说明代替。
// 返回的命令是交给登录 shell 之前的样子。
func (e Executor) Plan(t task.Task, info Info) (api.Plan, error) {
	if err := Validate(t, e.Runners); err != nil {
		return api.Plan{}, err
	}
	cwd, err := expandHome(t.Cwd)
	if err != nil {
		return api.Plan{}, err
	}
	if cwd == "" {
		cwd = info.StateDir
	}
	if cwd != "" {
		if cwd, err = filepath.Abs(cwd); err != nil {
			return api.Plan{}, err
		}
	}
	runDir := info.RunDir
	if runDir == "" {
		runDir = i18n.T("<本次运行的存档目录>")
	}
	vars := runner.Vars{TaskName: t.Name, Now: t.In(e.Clock.Now()), LastRun: t.In(info.LastRun), RunDir: runDir, StateDir: info.StateDir, Steps: map[string]runner.Output{}}
	if info.LastRun.IsZero() {
		vars.LastRun = t.In(t.CreatedAt)
	}
	if t.Output != nil {
		if vars.OutputFile, err = outputFile(t, cwd, vars); err != nil {
			return api.Plan{}, err
		}
	}
	plan := api.Plan{Task: t.Name, Cwd: cwd, Steps: []api.PlannedStep{}}
	for i, s := range t.Steps {
		ps := api.PlannedStep{Name: s.Name, Type: string(s.Type)}
		switch s.Type {
		case task.StepText:
			ps.Text = vars.Expand(s.Text)
		case task.StepScript:
			if s.Script.Command != "" {
				ps.Command = []string{"/bin/sh", "-c", vars.ExpandCommand(s.Script.Command)}
				break
			}
			path, err := expandHome(s.Script.Path)
			if err != nil {
				return api.Plan{}, err
			}
			if !filepath.IsAbs(path) {
				path = filepath.Join(cwd, path)
			}
			ps.Command = []string{path}
		case task.StepAgent:
			rn, err := findRunner(e.Runners, s.Agent.Runner)
			if err != nil {
				return api.Plan{}, err
			}
			ps.Prompt = vars.Expand(s.Agent.Prompt)
			if prev, refs := runner.References(s.Agent.Prompt); i > 0 && !prev && len(refs) == 0 {
				ps.Prompt += "\n\n" + i18n.T("<上一步的输出>")
			}
			inv, err := rn.Render(s.Agent, ps.Prompt, filepath.Join(StepDir(runDir, i), promptFile))
			if err != nil {
				return api.Plan{}, err
			}
			ps.Command, ps.Permission = inv.Argv, inv.Level
		}
		plan.Steps = append(plan.Steps, ps)
		// 后面的步骤引用这一步的输出时,显示一个占位说明。
		out := runner.Output{Text: i18n.T("<第 %d 步的输出>", i+1), File: filepath.Join(StepDir(runDir, i), stdoutFile)}
		vars.Prev = out
		if s.Name != "" {
			vars.Steps[s.Name] = out
		}
	}
	return plan, nil
}

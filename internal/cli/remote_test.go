//go:build darwin || linux

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"bugu/internal/api"
	"bugu/internal/daemon"
	"bugu/internal/task"
)

// jsonOK 以 --json 调用一次并要求成功,把标准输出解析进 v。
func jsonOK(t *testing.T, v any, args ...string) {
	t.Helper()
	out := ok(t, append(args, "--json")...)
	if err := json.Unmarshal([]byte(out), v); err != nil {
		t.Fatalf("bugu %s --json 的输出不是 JSON: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// jsonFails 以 --json 调用一次并要求失败,返回标准错误里的错误代码和说明。
func jsonFails(t *testing.T, exit int, args ...string) api.Error {
	t.Helper()
	code, out, errOut := bugu(append(args, "--json")...)
	var body struct{ Error api.Error }
	if code != exit || out != "" || json.Unmarshal([]byte(errOut), &body) != nil || body.Error.Code == "" {
		t.Fatalf("bugu %s --json: 退出码 %d(期望 %d),标准输出 %q,标准错误 %q", strings.Join(args, " "), code, exit, out, errOut)
	}
	return body.Error
}

// A1:所有命令支持 --json;成功时标准输出是接口的 JSON,出错时标准错误是带错误代码的 JSON。
func TestA1JSONOutputAndErrors(t *testing.T) {
	d := backend(t)
	var info api.TaskInfo
	jsonOK(t, &info, "add", "backup", "--schedule", "0 2 * * *", "--command", "echo 备份")
	if info.Name != "backup" || info.NextRun.IsZero() {
		t.Fatalf("add: %+v", info)
	}
	var list []api.TaskInfo
	jsonOK(t, &list, "list")
	if len(list) != 1 || list[0].Name != "backup" {
		t.Fatalf("list: %+v", list)
	}
	var r task.Run
	jsonOK(t, &r, "run", "backup")
	d.Wait()
	var logs struct {
		Run  task.Run `json:"run"`
		Logs api.Logs `json:"logs"`
	}
	jsonOK(t, &logs, "logs", r.ID)
	if logs.Run.Status != task.Succeeded || logs.Logs.Stdout != "备份\n" {
		t.Fatalf("logs: %+v", logs)
	}
	var runs []task.Run
	jsonOK(t, &runs, "runs")
	var paused api.TaskInfo
	jsonOK(t, &paused, "pause", "backup")
	if !paused.Paused || len(runs) != 1 {
		t.Fatalf("pause: %+v runs=%d", paused, len(runs))
	}

	if e := jsonFails(t, 1, "show", "nope"); e.Code != api.CodeNotFound || !strings.Contains(e.Message, "nope") {
		t.Errorf("找不到任务: %+v", e)
	}
	if e := jsonFails(t, 1, "add", "bad", "--schedule", "61 * * * *", "--command", "true"); e.Code != api.CodeInvalid {
		t.Errorf("定义有误: %+v", e)
	}
	if e := jsonFails(t, 2, "add", "x", "--schedule", "0 2 * * *"); e.Code != "usage" {
		t.Errorf("用法有误: %+v", e)
	}
	if e := jsonFails(t, 2, "serve"); e.Code != "usage" {
		t.Errorf("serve 没有 JSON 结果: %+v", e)
	}
	var version map[string]string
	jsonOK(t, &version, "version")
	if version["version"] != "test" {
		t.Errorf("version: %v", version)
	}
}

// A1 G14:后台没在运行时,错误代码是 unavailable。
func TestA1Unavailable(t *testing.T) {
	t.Setenv("BUGU_HOME", t.TempDir())
	if e := jsonFails(t, 1, "list"); e.Code != "unavailable" {
		t.Fatalf("%+v", e)
	}
}

// R1 PRD 7.7:run --dry-run 列出将执行的命令而不运行;runner add 添加自定义运行器。
func TestDryRunAndRunnerAdd(t *testing.T) {
	backend(t)
	ok(t, "add", "greet", "--schedule", "0 9 * * *", "--command", "echo {task_name} on {date}")
	var plan api.Plan
	jsonOK(t, &plan, "run", "greet", "--dry-run")
	if len(plan.Steps) != 1 || !regexp.MustCompile(`^echo greet on \d{4}-\d{2}-\d{2}$`).MatchString(plan.Steps[0].Command[2]) {
		t.Fatalf("plan: %+v", plan)
	}
	if out := ok(t, "run", "greet", "--dry-run"); !strings.Contains(out, "没有真正运行") {
		t.Fatalf("dry-run: %q", out)
	}
	if runs := ok(t, "runs", "greet"); !strings.Contains(runs, "没有运行记录") {
		t.Fatalf("dry-run 不应真正运行: %q", runs)
	}

	file := filepath.Join(t.TempDir(), "echoer.json")
	os.WriteFile(file, []byte(`{"name": "echoer", "command": ["echo"], "prompt_via": "arg", "check": ["true"]}`), 0o600)
	var rn api.Runner
	jsonOK(t, &rn, "runner", "add", file)
	if rn.Name != "echoer" || !rn.Agent || rn.Preset {
		t.Fatalf("runner add: %+v", rn)
	}
	os.WriteFile(file, []byte(`{"name": "Bad Name", "command": []}`), 0o600)
	if e := jsonFails(t, 1, "runner", "add", file); e.Code != api.CodeInvalid {
		t.Fatalf("不合法的运行器: %+v", e)
	}
}

// C5 G5 C6 决策记录 0015:创建的令牌能管理任务,不能管理令牌;作废后立即失效。
// --host 对登记过的另一台主机执行命令;明文公网地址给出警告。
func TestC5TokensAndHosts(t *testing.T) {
	backend(t) // 本机
	remoteHome := filepath.Join(t.TempDir(), "remote")
	remote, err := daemon.Start(daemon.Options{Home: remoteHome, Listen: "127.0.0.1:0", Tick: 1 << 62})
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()

	// 在"远程主机"上创建一个令牌(用它自己的本机令牌)。
	home := os.Getenv("BUGU_HOME")
	t.Setenv("BUGU_HOME", remoteHome)
	var created api.NewToken
	jsonOK(t, &created, "token", "create", "laptop")
	ok(t, "add", "far", "--schedule", "0 3 * * *", "--command", "true")
	t.Setenv("BUGU_HOME", home)

	url := "http://" + remote.Addr()
	var h api.Host
	jsonOK(t, &h, "host", "add", "nas", url, "--token", created.Token)
	if h.Status != api.HostOnline || h.Token != "" || h.PlainPublic {
		t.Fatalf("host add: %+v", h)
	}
	var list []api.TaskInfo
	jsonOK(t, &list, "list", "--host", "nas")
	if len(list) != 1 || list[0].Name != "far" {
		t.Fatalf("--host list: %+v", list)
	}
	if e := jsonFails(t, 1, "token", "list", "--host", "nas"); e.Code != api.CodeForbidden {
		t.Fatalf("创建的令牌不能管理令牌: %+v", e)
	}
	if st, _ := os.Stat(filepath.Join(home, "hosts.json")); st.Mode().Perm() != 0o600 {
		t.Fatalf("hosts.json 的权限 %v", st.Mode().Perm())
	}

	// 作废后立即失效,登记表显示令牌被拒绝。
	t.Setenv("BUGU_HOME", remoteHome)
	ok(t, "token", "revoke", "laptop")
	fails(t, 1, "本机令牌不能作废", "token", "revoke", "local")
	t.Setenv("BUGU_HOME", home)
	if e := jsonFails(t, 1, "list", "--host", "nas"); e.Code != api.CodeUnauthorized {
		t.Fatalf("作废之后: %+v", e)
	}
	var hosts []api.Host
	jsonOK(t, &hosts, "host", "list")
	if len(hosts) != 1 || hosts[0].Status != api.HostUnauthorized {
		t.Fatalf("host list: %+v", hosts)
	}

	// C6:经公网的明文地址给出警告;HTTPS 不警告。登记的主机连不上时显示离线(G14)。
	if code, _, errOut := bugu("host", "add", "pub", "http://203.0.113.9:7377", "--token", "x"); code != 0 || !strings.Contains(errOut, "明文") {
		t.Fatalf("明文公网地址: %d %q", code, errOut)
	}
	jsonOK(t, &hosts, "host", "list")
	if len(hosts) != 2 || hosts[1].Name != "pub" || hosts[1].Status != api.HostOffline || !hosts[1].PlainPublic {
		t.Fatalf("host list: %+v", hosts)
	}
	ok(t, "host", "rm", "pub")
	fails(t, 2, "没有登记", "list", "--host", "pub")
	fails(t, 2, "只能在本机执行", "serve", "--host", "nas")
}

// A2:bugu skill 打印技能说明文件,按语言选择。
func TestA2Skill(t *testing.T) {
	if out := ok(t, "skill"); !strings.HasPrefix(out, "---\nname: bugu") || !strings.Contains(out, "--json") {
		t.Fatalf("skill: %.80q", out)
	}
	if out := ok(t, "skill", "--lang", "en"); !strings.Contains(out, "Managing scheduled tasks") {
		t.Fatalf("skill en: %.80q", out)
	}
}

// PRD 5.7:bugu doctor 列出各项检查,后台没在运行时这一项不通过。
func TestDoctor(t *testing.T) {
	backend(t)
	var checks []struct{ Name, Status, Detail string }
	jsonOK(t, &checks, "doctor")
	byName, runners := map[string]string{}, 0
	for _, c := range checks {
		byName[c.Name] = c.Status
		if strings.HasPrefix(c.Name, "运行器 ") {
			runners++
		}
	}
	if byName["后台"] != "ok" || byName["登录 shell"] == "" || runners == 0 {
		t.Fatalf("doctor: %+v", checks)
	}
	t.Setenv("BUGU_HOME", t.TempDir())
	fails(t, 1, "没有通过", "doctor")
}

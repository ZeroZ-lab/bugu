package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeSystem struct {
	calls  []string
	loaded bool
}

func (f *fakeSystem) run(name string, args ...string) (string, error) {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if strings.Contains(call, " print ") || strings.Contains(call, "is-enabled") {
		if !f.loaded {
			return "", os.ErrNotExist
		}
	}
	return "", nil
}

// PRD 5.2 5.3:bugu daemon install 写入服务定义并让 launchd 加载;卸载时停止并删除。
func TestLaunchdInstallUninstall(t *testing.T) {
	sys := &fakeSystem{}
	m := Manager{GOOS: "darwin", UserHome: t.TempDir(), UID: 501, Run: sys.run}
	spec := Spec{Exe: "/Applications/Bugu.app/Contents/MacOS/bugu", Shell: "/bin/zsh", Home: "/Users/a & b/.bugu", Log: "/tmp/serve.log"}
	file, err := m.Install(spec)
	if err != nil {
		t.Fatal(err)
	}
	if file != filepath.Join(m.UserHome, "Library/LaunchAgents/app.bugu.serve.plist") {
		t.Fatalf("file=%s", file)
	}
	b, _ := os.ReadFile(file)
	for _, want := range []string{"<string>/Applications/Bugu.app/Contents/MacOS/bugu</string>\n\t\t<string>serve</string>", "<key>SHELL</key>\n\t\t<string>/bin/zsh</string>", "/Users/a &amp; b/.bugu", "<key>RunAtLoad</key>"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("服务定义里没有 %q:\n%s", want, b)
		}
	}
	if len(sys.calls) != 1 || sys.calls[0] != "launchctl bootstrap gui/501 "+file {
		t.Fatalf("calls=%v", sys.calls)
	}
	sys.loaded = true
	if st, _ := m.Status(); !st.Installed || !st.Loaded {
		t.Fatalf("status=%+v", st)
	}
	if err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) || sys.calls[len(sys.calls)-1] != "launchctl bootout gui/501/app.bugu.serve" {
		t.Fatalf("卸载后: %v calls=%v", err, sys.calls)
	}
	if st, _ := m.Status(); st.Installed {
		t.Fatal("卸载后仍显示已安装")
	}
}

// PRD 5.2:服务器上注册为 systemd 用户服务;路径里的空格和 % 按单元文件的规则写。
func TestSystemdInstall(t *testing.T) {
	sys := &fakeSystem{}
	m := Manager{GOOS: "linux", UserHome: t.TempDir(), Run: sys.run}
	file, err := m.Install(Spec{Exe: "/opt/bugu dir/bugu", Home: "/srv/100%"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file)
	if !strings.Contains(string(b), "ExecStart=\"/opt/bugu dir/bugu\" serve\n") || !strings.Contains(string(b), "Environment=BUGU_HOME=/srv/100%%\n") {
		t.Fatalf("单元文件:\n%s", b)
	}
	if strings.Join(sys.calls, ";") != "systemctl --user daemon-reload;systemctl --user enable --now app.bugu.serve.service" {
		t.Fatalf("calls=%v", sys.calls)
	}
	// 没有 systemd(比如在容器里):不写服务定义,说明怎么办。
	bare := Manager{GOOS: "linux", UserHome: t.TempDir(), Run: sys.run, Has: func(string) bool { return false }}
	if _, err := bare.Install(Spec{Exe: "/usr/local/bin/bugu"}); !errors.Is(err, ErrNoManager) {
		t.Fatalf("没有 systemd: %v", err)
	}
	if st, _ := bare.Status(); st.Installed {
		t.Fatal("没装成却留下了服务定义")
	}
	// 服务管理器在,但加载失败:删掉刚写的服务定义。
	failing := Manager{GOOS: "linux", UserHome: t.TempDir(), Run: func(string, ...string) (string, error) { return "Failed to connect to bus", os.ErrPermission }}
	if _, err := failing.Install(Spec{Exe: "/usr/local/bin/bugu"}); err == nil || !strings.Contains(err.Error(), "Failed to connect to bus") {
		t.Fatalf("加载失败: %v", err)
	}
	if st, _ := failing.Status(); st.Installed {
		t.Fatal("加载失败后留下了服务定义")
	}
	if _, err := (Manager{GOOS: "windows"}).File(); err == nil {
		t.Fatal("不支持的系统应当报错")
	}
}

// Package service 把后台注册为当前用户的系统服务(PRD 5.2、5.3):macOS 上是 launchd 的
// LaunchAgent,Linux 上是 systemd 的用户服务。服务运行 `bugu serve`,开机或登录后自动启动,
// 异常退出时重启。桌面应用启动时发现已有后台在运行,就只做界面(G8)。
package service

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"bugu/internal/i18n"
)

// Label 是服务的名字:launchd 的 Label、systemd 的单元名(不含 .service)。
const Label = "app.bugu.serve"

// Spec 是服务要运行的程序和环境。
type Spec struct {
	Exe   string // bugu 程序的绝对路径
	Home  string // 数据目录;为空时用默认的 ~/.bugu
	Shell string // 用户的登录 shell,任务的子进程经它启动(E1)
	Log   string // 服务自己的输出写到哪里(launchd);systemd 交给 journal
}

// Manager 在一个系统上安装、卸载、查询服务。Run 执行 launchctl 或 systemctl,测试里换成假的。
type Manager struct {
	GOOS     string
	UserHome string
	UID      int
	Run      func(name string, args ...string) (output string, err error)
	// Has 报告系统上有没有某个命令,用来确认有服务管理器可用。为空时当作有。
	Has func(name string) bool
}

// ErrNoManager 表示这台机器上没有可用的服务管理器,比如容器里通常没有 systemd。
var ErrNoManager = i18n.Errorf("这台机器上没有可用的服务管理器(launchctl 或 systemctl),容器里通常没有 systemd。可以直接运行 bugu serve,交给容器的重启策略或别的进程管理器让它常驻")

// tool 是这个系统的服务管理命令。
func (m Manager) tool() string {
	if m.GOOS == "darwin" {
		return "launchctl"
	}
	return "systemctl"
}

// Status 是服务的安装情况。
type Status struct {
	Installed bool   `json:"installed"`
	File      string `json:"file"`
	// Loaded 表示系统的服务管理器已经加载了它(launchctl print / systemctl is-enabled 成功)。
	Loaded bool `json:"loaded"`
}

// ErrUnsupported 表示这个系统不支持安装为服务。
var ErrUnsupported = i18n.Errorf("这个系统不支持 bugu daemon,只支持 macOS(launchd)和 Linux(systemd)")

// File 返回服务定义文件的路径。
func (m Manager) File() (string, error) {
	switch m.GOOS {
	case "darwin":
		return filepath.Join(m.UserHome, "Library", "LaunchAgents", Label+".plist"), nil
	case "linux":
		return filepath.Join(m.UserHome, ".config", "systemd", "user", Label+".service"), nil
	}
	return "", ErrUnsupported
}

// Definition 返回服务定义文件的内容。
func (m Manager) Definition(s Spec) (string, error) {
	env := [][2]string{}
	if s.Shell != "" {
		env = append(env, [2]string{"SHELL", s.Shell})
	}
	if s.Home != "" {
		env = append(env, [2]string{"BUGU_HOME", s.Home})
	}
	switch m.GOOS {
	case "darwin":
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + Label + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + xmlEscape(s.Exe) + `</string>
		<string>serve</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ProcessType</key>
	<string>Background</string>
`)
		if len(env) > 0 {
			b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
			for _, kv := range env {
				b.WriteString("\t\t<key>" + kv[0] + "</key>\n\t\t<string>" + xmlEscape(kv[1]) + "</string>\n")
			}
			b.WriteString("\t</dict>\n")
		}
		if s.Log != "" {
			b.WriteString("\t<key>StandardOutPath</key>\n\t<string>" + xmlEscape(s.Log) + "</string>\n")
			b.WriteString("\t<key>StandardErrorPath</key>\n\t<string>" + xmlEscape(s.Log) + "</string>\n")
		}
		b.WriteString("</dict>\n</plist>\n")
		return b.String(), nil
	case "linux":
		var b strings.Builder
		b.WriteString("[Unit]\nDescription=Bugu scheduled tasks\nAfter=network-online.target\n\n[Service]\n")
		b.WriteString("ExecStart=" + unitQuote(s.Exe) + " serve\n")
		for _, kv := range env {
			b.WriteString("Environment=" + unitQuote(kv[0]+"="+kv[1]) + "\n")
		}
		b.WriteString("Restart=on-failure\nRestartSec=10\n\n[Install]\nWantedBy=default.target\n")
		return b.String(), nil
	}
	return "", ErrUnsupported
}

// Install 写入服务定义并让系统加载、启动它。已安装时先卸载旧的。
func (m Manager) Install(s Spec) (string, error) {
	file, err := m.File()
	if err != nil {
		return "", err
	}
	def, err := m.Definition(s)
	if err != nil {
		return "", err
	}
	if m.Has != nil && !m.Has(m.tool()) {
		return "", ErrNoManager
	}
	if st, _ := m.Status(); st.Installed {
		if err := m.Uninstall(); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(file, []byte(def), 0o644); err != nil {
		return "", err
	}
	switch m.GOOS {
	case "darwin":
		err = m.run("launchctl", "bootstrap", m.domain(), file)
	case "linux":
		if err = m.run("systemctl", "--user", "daemon-reload"); err == nil {
			err = m.run("systemctl", "--user", "enable", "--now", Label+".service")
		}
	}
	if err != nil {
		// 没装成就不留下服务定义,免得之后显示成"已安装但没有加载"。
		_ = os.Remove(file)
		return "", err
	}
	return file, nil
}

// Uninstall 停止服务并删除服务定义。没有安装时什么也不做。
func (m Manager) Uninstall() error {
	file, err := m.File()
	if err != nil {
		return err
	}
	if _, err := os.Stat(file); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	switch m.GOOS {
	case "darwin":
		_ = m.run("launchctl", "bootout", m.domain()+"/"+Label) // 没加载时会失败,不影响删除
	case "linux":
		_ = m.run("systemctl", "--user", "disable", "--now", Label+".service")
	}
	if err := os.Remove(file); err != nil {
		return err
	}
	if m.GOOS == "linux" {
		return m.run("systemctl", "--user", "daemon-reload")
	}
	return nil
}

// Status 查询服务是否已安装、是否已被加载。
func (m Manager) Status() (Status, error) {
	file, err := m.File()
	if err != nil {
		return Status{}, err
	}
	st := Status{File: file}
	if _, err := os.Stat(file); err != nil {
		return st, nil
	}
	st.Installed = true
	switch m.GOOS {
	case "darwin":
		st.Loaded = m.run("launchctl", "print", m.domain()+"/"+Label) == nil
	case "linux":
		st.Loaded = m.run("systemctl", "--user", "is-enabled", "--quiet", Label+".service") == nil
	}
	return st, nil
}

func (m Manager) domain() string { return "gui/" + strconv.Itoa(m.UID) }

func (m Manager) run(name string, args ...string) error {
	out, err := m.Run(name, args...)
	if err != nil {
		return i18n.Errorf("%s %s 失败: %v %s", name, strings.Join(args, " "), err, strings.TrimSpace(out))
	}
	return nil
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(s)
}

// unitQuote 按 systemd 单元文件的规则加引号;% 是 systemd 的占位符前缀,要写成 %%。
func unitQuote(s string) string {
	s = strings.ReplaceAll(s, "%", "%%")
	if !strings.ContainsAny(s, " \t\"\\") {
		return s
	}
	return fmt.Sprintf("%q", s)
}

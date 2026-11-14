//go:build darwin || linux

package cli

import (
	"strings"
	"testing"

	"bugu/internal/i18n"
)

// 测试里的"系统语言"固定为中文,结果不取决于运行测试的机器上的 LANG。
func init() { i18n.SetSystem([]string{"zh"}) }

// 决策记录 0014:--lang 可以写在任何位置,决定命令行自己说的语言,也决定后台返回的错误信息用哪种语言;
// bugu language 查看和设置后台的语言。
func TestLangFlagAndLanguageCommand(t *testing.T) {
	defer i18n.Set(i18n.Zh)
	backend(t)
	if out := ok(t, "list", "--lang", "en"); !strings.Contains(out, "No tasks yet") {
		t.Fatalf("list=%q", out)
	}
	fails(t, 1, "no task named", "--lang=en", "show", "nope")
	fails(t, 1, "没有名为", "show", "nope")
	fails(t, 2, "unknown command", "--lang", "en", "frobnicate")
	fails(t, 2, "--lang", "--lang", "fr", "list")
	if out := ok(t, "--lang", "en", "help"); !strings.Contains(out, "Usage:") {
		t.Fatalf("help=%q", out)
	}

	if out := ok(t, "language"); !strings.Contains(out, "跟随系统(现在是 中文)") {
		t.Fatalf("language=%q", out)
	}
	if out := ok(t, "language", "en"); !strings.Contains(out, "English") {
		t.Fatalf("language en=%q", out)
	}
	fails(t, 1, "auto", "language", "fr")
}

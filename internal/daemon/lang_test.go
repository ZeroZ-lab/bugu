//go:build darwin || linux

package daemon

import (
	"strings"
	"testing"

	"bugu/internal/api"
	"bugu/internal/clock"
	"bugu/internal/i18n"
	"bugu/internal/notify"
	"bugu/internal/task"
)

// 测试里的"系统语言"固定为中文,结果不取决于运行测试的机器上的 LANG。
func init() { i18n.SetSystem([]string{"zh"}) }

// 决策记录 0014:语言默认跟随系统;改成英文后,通知和错误信息随即说英文,重启后仍然有效;
// 不认识的语言不接受。
func TestLanguageSetting(t *testing.T) {
	defer i18n.Set(i18n.Zh)
	home, f := t.TempDir(), clock.NewFake(epoch)
	var shown []notify.Message
	d, err := Start(Options{Home: home, Listen: "127.0.0.1:0", Clock: f, Notify: func(m notify.Message) { shown = append(shown, m) }})
	if err != nil {
		t.Fatal(err)
	}
	if s, err := d.engine.Settings(); err != nil || s.Language != api.LanguageAuto || s.Effective != "zh" {
		t.Fatalf("默认设置: %+v %v", s, err)
	}
	if s, err := d.engine.UpdateSettings(api.Settings{Language: "en"}); err != nil || s.Effective != "en" {
		t.Fatalf("改成英文: %+v %v", s, err)
	}
	tk := command("report", "0 2 * * *", "exit 3")
	tk.Notifications = []task.Notification{{Channel: task.ChannelSystem}}
	create(t, d, tk)
	r, _ := d.engine.RunNow("report", "")
	d.Wait()
	if len(shown) != 1 || shown[0].Title != "report failed (exit code 3)" {
		t.Fatalf("通知: %+v", shown)
	}
	if _, err := d.engine.Run("nope"); err == nil || !strings.Contains(err.Error(), "no run with ID") {
		t.Fatalf("错误信息: %v", err)
	}
	_, err = d.engine.UpdateSettings(api.Settings{Language: "fr"})
	if code(err) != api.CodeInvalid {
		t.Fatalf("不认识的语言: %v", err)
	}
	d.Close()

	i18n.Set(i18n.Zh)
	d = start(t, home, f)
	if s, _ := d.engine.Settings(); s.Language != "en" || i18n.Current() != i18n.En {
		t.Fatalf("重启之后: %+v %q", s, i18n.Current())
	}
	if list := runs(t, d, "report"); len(list) != 1 || list[0].ID != r.ID {
		t.Fatalf("runs=%+v", list)
	}
	if s, _ := d.engine.UpdateSettings(api.Settings{Language: "auto"}); s.Effective != "zh" {
		t.Fatalf("改回跟随系统: %+v", s)
	}
}

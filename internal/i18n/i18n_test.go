package i18n

import (
	"errors"
	"io/fs"
	"testing"
)

// 决策记录 0014:各种写法的语言标记都能认出;中文以外的语言说英文。
func TestParse(t *testing.T) {
	for tag, want := range map[string]Lang{
		"zh_CN.UTF-8": Zh, "zh-Hans-CN": Zh, "zh-TW": Zh, "ZH": Zh,
		"en_US.UTF-8": En, "en-GB": En, "fr-FR": En, "de": En,
	} {
		if got, ok := Parse(tag); !ok || got != want {
			t.Errorf("%q: %q %v", tag, got, ok)
		}
	}
	for _, tag := range []string{"", "C", "POSIX", "C.UTF-8", "*"} {
		if _, ok := Parse(tag); ok {
			t.Errorf("%q 不应当算作语言", tag)
		}
	}
	if l, ok := ParseAccept("en-US,en;q=0.9,zh;q=0.8"); !ok || l != En {
		t.Errorf("Accept-Language: %q", l)
	}
}

// 决策记录 0014:没有设置系统语言时按 LC_ALL、LC_MESSAGES、LANG 的顺序;都没有时说中文。
func TestSystemFromEnvironment(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "en_GB.UTF-8")
	t.Setenv("LANG", "zh_CN.UTF-8")
	if l := System(); l != En {
		t.Errorf("LC_MESSAGES 应优先于 LANG: %q", l)
	}
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "C")
	if l := System(); l != Zh {
		t.Errorf("无从得知时应说中文: %q", l)
	}
}

// 决策记录 0014:错误在显示时才翻译,所以同一个错误可以按不同的语言说;
// 包在里面的错误也跟着翻译,并且仍能被 errors.Is 找到。
func TestErrorLocalizedWhenShown(t *testing.T) {
	inner := Errorf("后台返回 %s", "500")
	err := Errorf("读取运行记录 %s: %w", "r1", inner)
	if got := Localize(err, En); got != "read run record r1: the backend returned 500" {
		t.Errorf("英文: %q", got)
	}
	if got := Localize(err, Zh); got != "读取运行记录 r1: 后台返回 500" {
		t.Errorf("中文: %q", got)
	}
	if !errors.Is(Errorf("读取运行记录 %s: %w", "x", fs.ErrNotExist), fs.ErrNotExist) {
		t.Error("errors.Is 找不到包在里面的错误")
	}
	// 英文译文调换了参数的顺序,包括 %[4]w。
	got := In(En, "cron 表达式 %q 的第 %d 段(%s): %w", "* *", 2, "时", errors.New("bad"))
	if got != `field 2 (时) of cron expression "* *": bad` {
		t.Errorf("参数顺序: %q", got)
	}
}

// 决策记录 0014:改变语言时通知登记的函数;没有译文的文字说原文。
func TestSetAndFallback(t *testing.T) {
	defer Set(Zh)
	var heard []Lang
	OnChange(func(l Lang) { heard = append(heard, l) })
	Set(En)
	Set(En)
	if len(heard) != 1 || heard[0] != En || T("布谷") != "Bugu" {
		t.Errorf("heard=%v T=%q", heard, T("布谷"))
	}
	if T("没有译文的文字 %d", 1) != "没有译文的文字 1" {
		t.Error("没有译文时应说原文")
	}
	if (Text{Zh: "中", En: "en"}).In(En) != "en" || (Text{Zh: "中"}).In(En) != "中" {
		t.Error("Text")
	}
}

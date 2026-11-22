// Package i18n 让程序说中文或英文(决策记录 0014)。
//
// 代码里的文字用中文原文书写,原文本身就是查找英文译文的键,译文在 en.json。
// 找不到译文时说原文。哪些写法算"要翻译的文字"、译文是否齐全,由 internal/archlint 检查。
//
// 一个进程同一时间说一种语言(Current):命令行按 --lang 和环境变量,后台按设置。
// 错误在产生时只记下原文和参数,显示时才翻译,所以接口可以按请求的语言返回错误信息(Localize)。
package i18n

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
)

// Lang 是界面语言。
type Lang string

const (
	Zh Lang = "zh"
	En Lang = "en"
)

// Langs 是支持的语言,原文的语言在前。
var Langs = []Lang{Zh, En}

// Name 是语言用它自己说出的名字,在任何语言的界面里都这样写。
func (l Lang) Name() string {
	if l == En {
		return "English"
	}
	return "中文"
}

//go:embed en.json
var enJSON []byte

var en = func() map[string]string {
	m := map[string]string{}
	if err := json.Unmarshal(enJSON, &m); err != nil {
		panic("i18n: en.json: " + err.Error())
	}
	return m
}()

// English 返回全部英文译文,键是中文原文。供检查使用。
func English() map[string]string { return en }

var (
	current  atomic.Value // Lang
	system   atomic.Value // Lang
	mu       sync.Mutex
	watchers []func(Lang)
)

// Current 返回进程当前说的语言,默认中文。
func Current() Lang {
	if l, ok := current.Load().(Lang); ok {
		return l
	}
	return Zh
}

// Set 改变进程说的语言,并通知 OnChange 登记的函数。
func Set(l Lang) {
	if old := Current(); old == l {
		current.Store(l)
		return
	}
	current.Store(l)
	mu.Lock()
	fns := append([]func(Lang){}, watchers...)
	mu.Unlock()
	for _, fn := range fns {
		fn(l)
	}
}

// OnChange 登记一个函数,语言改变时调用。
func OnChange(fn func(Lang)) {
	mu.Lock()
	defer mu.Unlock()
	watchers = append(watchers, fn)
}

// Parse 把语言标记("zh-CN"、"en_US.UTF-8"、"zh-Hans")认作一种支持的语言:
// 中文的各种写法认作中文,其他语言一律说英文。空、C 和 POSIX 不算语言。
func Parse(tag string) (Lang, bool) {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(tag, ".@"); i >= 0 {
		tag = tag[:i]
	}
	switch {
	case tag == "" || tag == "c" || tag == "posix" || tag == "*":
		return "", false
	case tag == "zh" || strings.HasPrefix(tag, "zh-") || strings.HasPrefix(tag, "zh_"):
		return Zh, true
	}
	return En, true
}

// ParseAccept 取 Accept-Language 请求头里的第一种语言。
func ParseAccept(header string) (Lang, bool) {
	for _, part := range strings.Split(header, ",") {
		tag, _, _ := strings.Cut(part, ";")
		if l, ok := Parse(tag); ok {
			return l, true
		}
	}
	return "", false
}

// SetSystem 告诉程序系统偏好的语言,依次排列。桌面应用从系统设置里读到后调用;
// 没有调用时按环境变量 LC_ALL、LC_MESSAGES、LANG。
func SetSystem(tags []string) {
	for _, t := range tags {
		if l, ok := Parse(t); ok {
			system.Store(l)
			return
		}
	}
}

// System 返回系统的语言;无从得知时是中文。
func System() Lang {
	if l, ok := system.Load().(Lang); ok {
		return l
	}
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if l, ok := Parse(os.Getenv(k)); ok {
			return l
		}
	}
	return Zh
}

// Translate 返回原文 s 在语言 l 里的说法。
func Translate(l Lang, s string) string {
	if l == En {
		if t, ok := en[s]; ok {
			return t
		}
	}
	return s
}

// N 只标记 s 是要翻译的原文,原样返回,用于在包级别的表里登记文字;说出来时再用 T。
func N(s string) string { return s }

// T 用当前语言说一段文字,format 和 args 的用法同 fmt.Sprintf。
func T(format string, args ...any) string { return In(Current(), format, args...) }

// In 用指定的语言说一段文字。参数里的错误也用这种语言说。
func In(l Lang, format string, args ...any) string {
	f := Translate(l, format)
	if len(args) == 0 {
		return f
	}
	shown := make([]any, len(args))
	for i, a := range args {
		if err, ok := a.(error); ok && err != nil {
			a = Localize(err, l)
		}
		shown[i] = a
	}
	return fmt.Sprintf(wrapVerb.ReplaceAllString(f, "${1}v"), shown...)
}

// wrapVerb 匹配 %w 和 %[2]w:Sprintf 不认识 w,参数里的错误已经换成了文字,按 %v 说。
var wrapVerb = regexp.MustCompile(`(%(?:\[\d+\])?)w`)

// Error 是记下原文和参数、显示时才翻译的错误。参数里用 %w 的错误可以被 errors.Is 和 errors.As 找到。
type Error struct {
	format string
	args   []any
}

// Errorf 返回一个可翻译的错误,用法同 fmt.Errorf。
func Errorf(format string, args ...any) error { return &Error{format, args} }

func (e *Error) Error() string { return e.In(Current()) }

// In 用指定的语言说这个错误。
func (e *Error) In(l Lang) string { return In(l, e.format, e.args...) }

func (e *Error) Unwrap() []error {
	var errs []error
	for _, a := range e.args {
		if err, ok := a.(error); ok {
			errs = append(errs, err)
		}
	}
	return errs
}

// Localize 用指定的语言说一个错误。不是本包产生的错误按原样说。
func Localize(err error, l Lang) string {
	if err == nil {
		return ""
	}
	if s, ok := err.(interface{ In(Lang) string }); ok {
		return s.In(l)
	}
	return err.Error()
}

// Text 是一段较长的文字,两种语言写在一起,比如命令行的用法说明。
type Text struct{ Zh, En string }

func (t Text) String() string { return t.In(Current()) }

// In 用指定的语言说这段文字。
func (t Text) In(l Lang) string {
	if l == En && t.En != "" {
		return t.En
	}
	return t.Zh
}

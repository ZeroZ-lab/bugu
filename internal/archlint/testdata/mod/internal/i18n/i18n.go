package i18n

import "fmt"

type Text struct{ Zh, En string }

func N(s string) string                       { return s }
func T(format string, args ...any) string     { return fmt.Sprintf(format, args...) }
func In(l, format string, args ...any) string { return fmt.Sprintf(format, args...) }
func Errorf(format string, args ...any) error { return fmt.Errorf(format, args...) }

var 中文 = "i18n 包自己不受检查"

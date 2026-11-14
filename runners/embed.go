// Package runners 存放自带的运行器预设(R2)。厂商和产品的名字只出现在这个目录的配置文件里;
// 配置的格式见 internal/runner。
package runners

import "embed"

// Files 是本目录下全部预设配置,文件名即运行器名。
//
//go:embed *.json
var Files embed.FS

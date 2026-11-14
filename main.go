// bugu:桌面优先的定时任务应用。这里只是程序的起点,子命令在 internal/cli。
package main

import (
	"os"
	// 内嵌时区数据:任务可以指定时区(E7),服务器和精简容器上不一定装了系统的时区数据。
	_ "time/tzdata"

	"bugu/internal/cli"
)

// version 在构建时由 -ldflags "-X main.version=…" 设置,见 Makefile。
var version = "dev"

func main() {
	os.Exit(cli.Main(version, os.Args[1:], os.Stdout, os.Stderr))
}

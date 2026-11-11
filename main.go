// bugu:桌面优先的定时任务应用。这里只是程序的起点,子命令在 internal/cli。
package main

import (
	"os"

	"bugu/internal/cli"
)

// version 在构建时由 -ldflags "-X main.version=…" 设置,见 Makefile。
var version = "dev"

func main() {
	os.Exit(cli.Main(version, os.Args[1:], os.Stdout, os.Stderr))
}

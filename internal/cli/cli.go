// Package cli 实现 bugu 的子命令。命令通过 internal/client 访问后台,
// 只有启动后台的命令才引用 internal/daemon。
package cli

import (
	"fmt"
	"io"
)

const usage = `bugu:桌面优先的定时任务应用

用法:
  bugu version    显示版本

其余命令尚未实现,见 docs/PRD.md 第 11 章的里程碑。
`

// Main 执行一次命令行调用,返回进程退出码。
func Main(version string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return 0
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "bugu", version)
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "bugu: 未知命令 %q\n\n%s", args[0], usage)
		return 2
	}
}

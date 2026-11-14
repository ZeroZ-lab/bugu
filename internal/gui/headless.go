//go:build headless

package gui

import (
	"io"

	"bugu/internal/i18n"
)

// Available 报告这个构建是否包含桌面界面。
const Available = false

// Run 在无界面构建里总是失败。
func Run(home, version string, log io.Writer) error {
	return i18n.Errorf("这个构建不包含桌面界面,请用 bugu serve 启动后台")
}

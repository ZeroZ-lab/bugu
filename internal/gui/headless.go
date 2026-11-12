//go:build headless

package gui

import (
	"errors"
	"io"
)

// Available 报告这个构建是否包含桌面界面。
const Available = false

// Run 在无界面构建里总是失败。
func Run(home string, log io.Writer) error {
	return errors.New("这个构建不包含桌面界面,请用 bugu serve 启动后台")
}

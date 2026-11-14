// Package skills 存放教 AI agent 使用 bugu 的技能说明文件(A2),`bugu skill` 把它打印出来。
package skills

import _ "embed"

// Zh 和 En 是同一份说明的中文和英文。
var (
	//go:embed SKILL.md
	Zh string
	//go:embed SKILL.en.md
	En string
)

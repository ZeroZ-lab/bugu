package archtest

import (
	"testing"

	"bugu/internal/archlint"
)

// AGENTS.md 的硬规则 1 至 5、桌面框架的位置(PRD 5.6)、纯函数核心和可翻译文案(决策记录 0014)。
// 规则本身和各自的测试在 internal/archlint。
func TestArchitecture(t *testing.T) {
	findings, err := archlint.Run("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		t.Error(f)
	}
}

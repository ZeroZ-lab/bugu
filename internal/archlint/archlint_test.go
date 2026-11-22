package archlint

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// testdata/mod 是一个模块路径同为 bugu 的小模块,每条规则至少违反一次。
// 违反之处在行尾写 "// want <规则名>";Run 的结果必须与这些标注一一对应,不多不少。
// 覆盖:硬规则 1 至 5、PRD 5.6 的桌面框架、纯函数核心、决策记录 0014 的可翻译文案,
// 以及只在某个平台编译的文件和任何配置都不编译的文件。
func TestRunFindsEachMarkedViolation(t *testing.T) {
	const dir = "testdata/mod"
	want := []string{catalogFile + ":0: [i18n]"} // 译文表里没人用的条目
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		for i, line := range strings.Split(string(b), "\n") {
			if _, rule, ok := strings.Cut(line, "// want "); ok {
				want = append(want, fmt.Sprintf("%s:%d: [%s]", filepath.ToSlash(rel), i+1, strings.TrimSpace(rule)))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	findings, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range findings {
		got = append(got, fmt.Sprintf("%s:%d: [%s]", f.File, f.Line, f.Rule))
	}
	slices.Sort(want)
	slices.Sort(got)
	for _, w := range want {
		if i := slices.Index(got, w); i >= 0 {
			got = slices.Delete(got, i, i+1)
		} else {
			t.Errorf("没有报告 %s", w)
		}
	}
	for _, g := range got {
		t.Errorf("多报告了 %s", g)
	}
	if t.Failed() {
		for _, f := range findings {
			t.Log(f)
		}
	}
}

// excludedWhenHeadless 要求 headless 为真时约束对其他标签的任何取值都为假(PRD 5.6)。
func TestExcludedWhenHeadless(t *testing.T) {
	for src, want := range map[string]bool{
		"//go:build !headless":                                  true,
		"//go:build !headless && darwin":                        true,
		"//go:build darwin && !headless || ignore && !headless": true,
		"//go:build darwin || !headless":                        false,
		"//go:build headless":                                   false,
		"// 没有构建约束":                                             false,
	} {
		f, err := parseHeader(src)
		if err != nil {
			t.Fatal(err)
		}
		if got := excludedWhenHeadless(f); got != want {
			t.Errorf("%s: 得到 %v,应为 %v", src, got, want)
		}
	}
}

func parseHeader(comment string) (*ast.File, error) {
	return parser.ParseFile(token.NewFileSet(), "x.go", comment+"\n\npackage x\n", parser.ParseComments)
}

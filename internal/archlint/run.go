package archlint

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/checker"
	"golang.org/x/tools/go/packages"
)

// Finding 是一处违反规则的代码。File 相对模块根目录,使用正斜杠。
type Finding struct {
	File    string
	Line    int
	Rule    string
	Message string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: [%s] %s", f.File, f.Line, f.Rule, f.Message)
}

// configs 是检查时依次使用的构建配置。按类型检查的规则只看得到某个配置编译的文件,
// 所以覆盖每个平台和 headless 标签;结果合并去重,与运行检查的主机无关。
var configs = []struct {
	env   []string
	flags []string
}{
	{[]string{"GOOS=darwin"}, nil},
	{[]string{"GOOS=linux"}, nil},
	{[]string{"GOOS=windows"}, nil},
	{[]string{"GOOS=darwin"}, []string{"-tags=headless"}},
	{[]string{"GOOS=linux"}, []string{"-tags=headless"}},
}

// Run 在 root 处的模块上运行全部规则,返回按文件和行号排序的结果。
func Run(root string) ([]Finding, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(catalogFile)))
	if err != nil {
		return nil, err
	}
	var catalog map[string]string
	if err := json.Unmarshal(b, &catalog); err != nil {
		return nil, fmt.Errorf("%s: %w", catalogFile, err)
	}

	text := newTextAnalyzer(catalog)
	analyzers := []*analysis.Analyzer{importsAnalyzer, cgoAnalyzer, desktopAnalyzer, vendorAnalyzer, clockAnalyzer, pureAnalyzer, text}
	seen := map[Finding]bool{}
	used := map[string]bool{}
	for _, c := range configs {
		cfg := &packages.Config{
			Mode:       packages.LoadSyntax,
			Dir:        root,
			Env:        append(append(os.Environ(), "CGO_ENABLED=0"), c.env...),
			BuildFlags: c.flags,
			Tests:      true,
		}
		pkgs, err := packages.Load(cfg, "./...")
		if err != nil {
			return nil, err
		}
		var errs []error
		packages.Visit(pkgs, nil, func(p *packages.Package) {
			for _, e := range p.Errors {
				errs = append(errs, fmt.Errorf("%v: %s", c.env, e))
			}
		})
		if len(errs) > 0 {
			return nil, errors.Join(errs...)
		}
		pkgs = slices.DeleteFunc(pkgs, func(p *packages.Package) bool {
			return strings.HasSuffix(p.PkgPath, ".test") // go test 生成的入口
		})
		graph, err := checker.Analyze(analyzers, pkgs, nil)
		if err != nil {
			return nil, err
		}
		for _, act := range graph.Roots {
			if act.Err != nil {
				return nil, fmt.Errorf("%s: %w", act, act.Err)
			}
			if act.Analyzer == text {
				for s := range act.Result.(map[string]bool) {
					used[s] = true
				}
			}
			for _, d := range act.Diagnostics {
				pos := act.Package.Fset.Position(d.Pos)
				file, err := filepath.Rel(root, pos.Filename)
				if err != nil {
					file = pos.Filename
				}
				seen[Finding{filepath.ToSlash(file), pos.Line, act.Analyzer.Name, d.Message}] = true
			}
		}
	}
	for s := range catalog {
		if !used[s] {
			seen[Finding{catalogFile, 0, text.Name, fmt.Sprintf("%q 在代码里没有用到", s)}] = true
		}
	}

	out := make([]Finding, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b Finding) int {
		return cmp.Or(strings.Compare(a.File, b.File), a.Line-b.Line,
			strings.Compare(a.Rule, b.Rule), strings.Compare(a.Message, b.Message))
	})
	return out, nil
}

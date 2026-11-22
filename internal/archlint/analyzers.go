package archlint

import (
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/types"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// unit 返回导入路径所属的 internal 一级包名,外部测试包算作被测的包;不在 internal 下时返回空。
func unit(path string) string {
	rest, ok := strings.CutPrefix(path, module+"/internal/")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, "/")
	return strings.TrimSuffix(name, "_test")
}

// source 是一个待检查的文件。
type source struct {
	name string
	file *ast.File
	test bool
}

// sources 返回这个包当前配置下编译的文件,以及被构建约束排除的文件。
// 后者没有类型信息,只用于按语法检查的规则,这样任何配置都不编译的文件也逃不过检查。
func sources(pass *analysis.Pass) ([]source, error) {
	var out []source
	for _, f := range pass.Files {
		name := pass.Fset.File(f.FileStart).Name()
		out = append(out, source{name, f, strings.HasSuffix(name, "_test.go")})
	}
	for _, name := range pass.IgnoredFiles {
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		b, err := pass.ReadFile(name)
		if err != nil {
			return nil, err
		}
		f, err := parser.ParseFile(pass.Fset, name, b, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		out = append(out, source{name, f, strings.HasSuffix(name, "_test.go")})
	}
	return out, nil
}

func importPath(imp *ast.ImportSpec) string {
	p, _ := strconv.Unquote(imp.Path.Value)
	return p
}

// 硬规则 2:每个内部包都已登记,且只引用允许表里的包。
var importsAnalyzer = &analysis.Analyzer{
	Name: "imports",
	Doc:  "内部包只能按允许表引用其他内部包(硬规则 2)",
	Run: func(pass *analysis.Pass) (any, error) {
		self := unit(pass.Pkg.Path())
		if self == "" {
			return nil, nil
		}
		files, err := sources(pass)
		if err != nil || len(files) == 0 {
			return nil, err
		}
		deps, ok := Allowed[self]
		if !ok {
			pass.Reportf(files[0].file.Name.Pos(), "internal/%s 没有在允许表中登记", self)
			return nil, nil
		}
		for _, s := range files {
			for _, imp := range s.file.Imports {
				if dep := unit(importPath(imp)); dep != "" && dep != self && !slices.Contains(deps, dep) {
					pass.Reportf(imp.Pos(), "包 %s 不允许引用 %s", self, dep)
				}
			}
		}
		return nil, nil
	},
}

// 硬规则 1:不使用 cgo。CGO_ENABLED=0 时带 import "C" 的文件被排除在编译之外,所以也查被排除的文件。
var cgoAnalyzer = &analysis.Analyzer{
	Name: "nocgo",
	Doc:  "不使用 cgo(硬规则 1)",
	Run: func(pass *analysis.Pass) (any, error) {
		files, err := sources(pass)
		for _, s := range files {
			for _, imp := range s.file.Imports {
				if importPath(imp) == "C" {
					pass.Reportf(imp.Pos(), "使用了 cgo")
				}
			}
		}
		return nil, err
	},
}

// 桌面框架只允许出现在 gui 包里,而且必须能用 headless 构建标签整个排除(PRD 5.6)。
// 这样服务器上的构建不带它,将来更换框架也只动这一个包。
var desktopAnalyzer = &analysis.Analyzer{
	Name: "desktop",
	Doc:  "桌面框架只在 gui 里,且 headless 构建时排除(PRD 5.6)",
	Run: func(pass *analysis.Pass) (any, error) {
		files, err := sources(pass)
		for _, s := range files {
			for _, imp := range s.file.Imports {
				p := importPath(imp)
				if p != framework && !strings.HasPrefix(p, framework+"/") {
					continue
				}
				if unit(pass.Pkg.Path()) != "gui" {
					pass.Reportf(imp.Pos(), "只有 internal/gui 可以引用桌面框架")
				}
				if !excludedWhenHeadless(s.file) {
					pass.Reportf(imp.Pos(), "引用桌面框架的文件必须在 headless 构建时被排除,例如 //go:build !headless")
				}
			}
		}
		return nil, err
	},
}

// excludedWhenHeadless 报告文件的构建约束是否保证带 headless 标签时不编译它:
// 不论其他标签取什么值,约束在 headless 为真时都为假。
func excludedWhenHeadless(f *ast.File) bool {
	for _, cg := range f.Comments {
		if cg.Pos() > f.Package {
			break
		}
		for _, c := range cg.List {
			if !constraint.IsGoBuild(c.Text) {
				continue
			}
			expr, err := constraint.Parse(c.Text)
			if err != nil {
				return false
			}
			var tags []string
			collectTags(expr, &tags)
			if len(tags) > 16 {
				return false
			}
			for mask := 0; mask < 1<<len(tags); mask++ {
				on := func(tag string) bool {
					return tag == "headless" || mask&(1<<slices.Index(tags, tag)) != 0
				}
				if expr.Eval(on) {
					return false
				}
			}
			return true
		}
	}
	return false
}

func collectTags(e constraint.Expr, tags *[]string) {
	switch e := e.(type) {
	case *constraint.TagExpr:
		if e.Tag != "headless" && !slices.Contains(*tags, e.Tag) {
			*tags = append(*tags, e.Tag)
		}
	case *constraint.NotExpr:
		collectTags(e.X, tags)
	case *constraint.AndExpr:
		collectTags(e.X, tags)
		collectTags(e.Y, tags)
	case *constraint.OrExpr:
		collectTags(e.X, tags)
		collectTags(e.Y, tags)
	}
}

// 硬规则 3:厂商和产品的名字只出现在 runners/ 的配置里,不出现在 Go 代码中,注释也不行。
var vendorAnalyzer = &analysis.Analyzer{
	Name: "vendornames",
	Doc:  "Go 代码里不出现厂商名(硬规则 3)",
	Run: func(pass *analysis.Pass) (any, error) {
		if slices.Contains(vendorExempt, unit(pass.Pkg.Path())) {
			return nil, nil
		}
		files, err := sources(pass)
		if err != nil {
			return nil, err
		}
		for _, s := range files {
			b, err := pass.ReadFile(s.name)
			if err != nil {
				return nil, err
			}
			tf := pass.Fset.File(s.file.FileStart)
			for _, m := range vendorNames.FindAllIndex(b, -1) {
				pass.Reportf(tf.Pos(m[0]), "Go 代码中出现了厂商名 %q", b[m[0]:m[1]])
			}
		}
		return nil, nil
	},
}

// 硬规则 4 和 5:时间只通过 internal/clock 获取,测试也不依赖真实时间。
// 按类型信息判断,所以别名导入和不加括号的函数值(f := time.Now)也算。
var clockAnalyzer = &analysis.Analyzer{
	Name: "notime",
	Doc:  "当前时间、等待和定时只经 internal/clock(硬规则 4、5)",
	Run: func(pass *analysis.Pass) (any, error) {
		if slices.Contains(clockExempt, unit(pass.Pkg.Path())) {
			return nil, nil
		}
		for id, obj := range pass.TypesInfo.Uses {
			fn, ok := obj.(*types.Func)
			if !ok || fn.Pkg() == nil || fn.Signature().Recv() != nil {
				continue
			}
			if slices.Contains(clockFuncs[fn.Pkg().Path()], fn.Name()) {
				pass.Reportf(id.Pos(), "直接使用了 %s.%s,应改用 internal/clock", fn.Pkg().Name(), fn.Name())
			}
		}
		return nil, nil
	},
}

// 纯函数核心只算决定,不产生副作用;副作用由外壳负责(docs/ARCHITECTURE.md 的包表)。
var pureAnalyzer = &analysis.Analyzer{
	Name: "purecore",
	Doc:  "纯函数核心不引用会产生副作用的标准库包",
	Run: func(pass *analysis.Pass) (any, error) {
		self := unit(pass.Pkg.Path())
		if !slices.Contains(Pure, self) {
			return nil, nil
		}
		files, err := sources(pass)
		for _, s := range files {
			if s.test {
				continue
			}
			for _, imp := range s.file.Imports {
				p := importPath(imp)
				if slices.ContainsFunc(impure, func(x string) bool {
					return p == x || strings.HasSuffix(x, "/") && strings.HasPrefix(p, x)
				}) {
					pass.Reportf(imp.Pos(), "%s 是纯函数核心,不能引用 %s;副作用交给 daemon、exec 或 store", self, p)
				}
			}
		}
		return nil, err
	},
}

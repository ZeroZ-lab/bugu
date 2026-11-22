package archlint

import (
	"go/ast"
	"go/token"
	"go/types"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/types/typeutil"
)

// cjk 匹配中文:汉字和全角标点。
var cjk = regexp.MustCompile(`[\p{Han}\x{3000}-\x{303f}\x{ff00}-\x{ffef}]`)

// verbs 匹配 fmt 的格式动词。
var verbs = regexp.MustCompile(`%[-+# 0]*(\[\d+\])?\d*(\.\d+)?[a-zA-Z]`)

func verbList(s string) string {
	var vs []string
	for _, v := range verbs.FindAllString(strings.ReplaceAll(s, "%%", ""), -1) {
		v = v[len(v)-1:]
		if v == "w" {
			v = "v"
		}
		vs = append(vs, v)
	}
	slices.Sort(vs)
	return strings.Join(vs, "")
}

// literal 返回由字符串字面量(或它们相加)组成的表达式的值。
func literal(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		return s, err == nil
	case *ast.BinaryExpr:
		a, ok1 := literal(e.X)
		b, ok2 := literal(e.Y)
		return a + b, ok1 && ok2 && e.Op == token.ADD
	case *ast.ParenExpr:
		return literal(e.X)
	}
	return "", false
}

// isI18n 报告对象是否是 i18n 包里名为 name 的成员。
func isI18n(obj types.Object, name string) bool {
	return obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == i18nPath && obj.Name() == name
}

// newTextAnalyzer 检查决策记录 0014:给人看的中文都要能翻译。中文字面量只能作为原文交给
// i18n 的函数,或者写在 i18n.Text 里;每段原文在译文表里有译文,格式动词一致。
// 分析结果是这个包交给 i18n 的全部原文,Run 据此找出译文表里用不到的条目。
func newTextAnalyzer(catalog map[string]string) *analysis.Analyzer {
	return &analysis.Analyzer{
		Name:       "i18n",
		Doc:        "给人看的中文经过 i18n 且有英文译文(决策记录 0014)",
		ResultType: reflect.TypeFor[map[string]bool](),
		Run: func(pass *analysis.Pass) (any, error) {
			used := map[string]bool{}
			if slices.Contains(textExempt, unit(pass.Pkg.Path())) {
				return used, nil
			}
			for _, f := range pass.Files {
				if strings.HasSuffix(pass.Fset.File(f.FileStart).Name(), "_test.go") {
					continue
				}
				checkText(pass, f, catalog, used)
			}
			return used, nil
		},
	}
}

func checkText(pass *analysis.Pass, f *ast.File, catalog map[string]string, used map[string]bool) {
	ok := map[*ast.BasicLit]bool{} // 已经作为原文交出去的字面量
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			fn := typeutil.Callee(pass.TypesInfo, n)
			if fn == nil || fn.Pkg() == nil || fn.Pkg().Path() != i18nPath {
				return true
			}
			i, isTr := translators[fn.Name()]
			if !isTr || i >= len(n.Args) {
				return true
			}
			s, isLit := literal(n.Args[i])
			if !isLit {
				return true
			}
			ast.Inspect(n.Args[i], func(m ast.Node) bool {
				if lit, isBasic := m.(*ast.BasicLit); isBasic {
					ok[lit] = true
				}
				return true
			})
			used[s] = true
			if !cjk.MatchString(s) { // 没有中文的原文(比如 "%s")不需要译文
				return true
			}
			switch en, has := catalog[s]; {
			case !has:
				pass.Reportf(n.Args[i].Pos(), "%q 没有英文译文", s)
			case cjk.MatchString(en):
				pass.Reportf(n.Args[i].Pos(), "%q 的译文里有中文", s)
			case verbList(en) != verbList(s):
				pass.Reportf(n.Args[i].Pos(), "%q 的译文 %q 格式动词不一致", s, en)
			}
			return true // 其余参数照常检查
		case *ast.CompositeLit:
			named, _ := types.Unalias(pass.TypesInfo.TypeOf(n)).(*types.Named)
			if named == nil || !isI18n(named.Obj(), "Text") {
				return true
			}
			hasEn := false
			for _, el := range n.Elts {
				if kv, isKV := el.(*ast.KeyValueExpr); isKV {
					if k, _ := kv.Key.(*ast.Ident); k != nil && k.Name == "En" {
						en, _ := literal(kv.Value)
						hasEn = en != "" && !cjk.MatchString(en)
					}
				}
			}
			if !hasEn {
				pass.Reportf(n.Pos(), "i18n.Text 缺少英文")
			}
			return false // 中文写在 Zh 里是允许的
		case *ast.BasicLit:
			if s, isLit := literal(n); isLit && !ok[n] && cjk.MatchString(s) {
				pass.Reportf(n.Pos(), "中文字面量 %q 没有经过 i18n", s)
			}
		}
		return true
	})
}

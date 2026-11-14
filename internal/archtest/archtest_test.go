package archtest

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	root   = "../.."
	module = "bugu"
)

// allowed 是 internal 下每个包可以引用的其他内部包,即依赖方向的唯一定义。
// docs/ARCHITECTURE.md 的包表须与此一致。新增包必须先在这里登记。
var allowed = map[string][]string{
	"i18n":      {},
	"clock":     {},
	"task":      {"i18n"},
	"schedule":  {"clock", "task", "i18n"},
	"runner":    {"task", "i18n"},
	"exec":      {"clock", "task", "runner", "i18n", "api"},
	"store":     {"clock", "task", "i18n"},
	"notify":    {"clock", "task", "i18n"},
	"api":       {"task", "i18n"},
	"server":    {"api", "clock", "task", "schedule", "runner", "exec", "store", "notify", "i18n"},
	"daemon":    {"server", "api", "clock", "task", "schedule", "runner", "exec", "store", "notify", "deliver", "i18n", "hosts"},
	"client":    {"api", "task", "i18n"},
	"gui":       {"client", "api", "clock", "task", "daemon", "notify", "i18n"},
	"deliver":   {"task", "notify", "i18n"},
	"cli":       {"client", "api", "task", "daemon", "gui", "i18n", "hosts", "service"},
	"hosts":     {"i18n"},
	"service":   {"i18n"},
	"fakeagent": {},
	"archtest":  {},
}

// goFiles 返回仓库中的 Go 文件,路径相对仓库根目录并使用正斜杠。
func goFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bin", "node_modules", "repos":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// internalPkg 返回文件所属的 internal 一级包名;不在 internal 下时返回空。
func internalPkg(file string) string {
	parts := strings.Split(file, "/")
	if len(parts) >= 3 && parts[0] == "internal" {
		return parts[1]
	}
	return ""
}

func read(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 硬规则 2:每个内部包都已登记,且只引用允许表里的包。
func TestImportDirection(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join(root, "internal"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if _, ok := allowed[e.Name()]; e.IsDir() && !ok {
			t.Errorf("internal/%s 没有在允许表中登记", e.Name())
		}
	}

	prefix := module + "/internal/"
	for _, file := range goFiles(t) {
		pkg := internalPkg(file)
		if pkg == "" {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, filepath.FromSlash(file)), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if !strings.HasPrefix(path, prefix) {
				continue
			}
			dep := strings.Split(strings.TrimPrefix(path, prefix), "/")[0]
			if dep != pkg && !slices.Contains(allowed[pkg], dep) {
				t.Errorf("%s: 包 %s 不允许引用 %s", file, pkg, dep)
			}
		}
	}
}

// 硬规则 1:不使用 cgo。CGO_ENABLED=0 时带 import "C" 的文件会被静默跳过,所以单独检查。
func TestNoCgo(t *testing.T) {
	for _, file := range goFiles(t) {
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, filepath.FromSlash(file)), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			if imp.Path.Value == `"C"` {
				t.Errorf("%s: 使用了 cgo", file)
			}
		}
	}
}

// 桌面框架只允许出现在 gui 包里,而且必须能用 headless 构建标签整个排除(PRD 5.6)。
// 这样服务器上的构建不带它,将来更换框架也只动这一个包。
func TestDesktopFrameworkStaysInGUI(t *testing.T) {
	const framework = "github.com/egoist/mygo"
	for _, file := range goFiles(t) {
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, filepath.FromSlash(file)), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		uses := slices.ContainsFunc(f.Imports, func(imp *ast.ImportSpec) bool {
			path, _ := strconv.Unquote(imp.Path.Value)
			return path == framework || strings.HasPrefix(path, framework+"/")
		})
		if !uses {
			continue
		}
		if internalPkg(file) != "gui" {
			t.Errorf("%s: 只有 internal/gui 可以引用桌面框架", file)
		}
		if !strings.Contains(read(t, file), "//go:build !headless") {
			t.Errorf("%s: 引用桌面框架的文件必须带 //go:build !headless", file)
		}
	}
}

// 硬规则 3:厂商和产品的名字只出现在 runners/ 的配置里,不出现在 Go 代码中。
func TestNoVendorNamesInGoCode(t *testing.T) {
	vendor := regexp.MustCompile(`(?i)claude|codex|anthropic|openai|gemini|opencode`)
	for _, file := range goFiles(t) {
		if internalPkg(file) == "archtest" {
			continue // 本文件需要列出这些名字
		}
		for i, line := range strings.Split(read(t, file), "\n") {
			if m := vendor.FindString(line); m != "" {
				t.Errorf("%s:%d: Go 代码中出现了厂商名 %q", file, i+1, m)
			}
		}
	}
}

// 硬规则 4 和 5:时间只通过 internal/clock 获取,测试也不依赖真实时间。
func TestNoDirectTime(t *testing.T) {
	// context 的超时和截止时间内部使用真实时钟,同样会绕过注入。
	direct := regexp.MustCompile(`\b(time\.(Now|Sleep|After|AfterFunc|NewTimer|NewTicker|Tick|Since|Until)|context\.With(Timeout|Deadline)(Cause)?)\(`)
	exempt := []string{"clock", "fakeagent", "archtest"}
	for _, file := range goFiles(t) {
		if slices.Contains(exempt, internalPkg(file)) {
			continue
		}
		for i, line := range strings.Split(read(t, file), "\n") {
			if m := direct.FindString(line); m != "" {
				t.Errorf("%s:%d: 直接使用了 %s…),应改用 internal/clock", file, i+1, m)
			}
		}
	}
}

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

// translators 是接受原文的函数,以及原文是第几个参数(决策记录 0014)。
var translators = map[string]int{"i18n.T": 0, "i18n.Errorf": 0, "i18n.N": 0, "i18n.In": 1}

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

// 决策记录 0014:给人看的中文都要能翻译。中文字面量只能作为原文交给 i18n 的函数,
// 或者写在 i18n.Text 里;每段原文在 internal/i18n/en.json 里有译文,格式动词一致;
// 译文表里没有用不到的条目。
func TestTextIsTranslatable(t *testing.T) {
	var catalog map[string]string
	if err := json.Unmarshal([]byte(read(t, "internal/i18n/en.json")), &catalog); err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	exempt := []string{"archtest", "fakeagent", "i18n"}
	for _, file := range goFiles(t) {
		if strings.HasSuffix(file, "_test.go") || slices.Contains(exempt, internalPkg(file)) {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(file)), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ok := map[ast.Expr]bool{} // 已经作为原文交出去的表达式
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				sel, isSel := n.Fun.(*ast.SelectorExpr)
				if !isSel {
					return true
				}
				pkg, isIdent := sel.X.(*ast.Ident)
				if !isIdent {
					return true
				}
				i, isTr := translators[pkg.Name+"."+sel.Sel.Name]
				if !isTr || i >= len(n.Args) {
					return true
				}
				s, isLit := literal(n.Args[i])
				if !isLit {
					return true
				}
				ok[n.Args[i]] = true
				used[s] = true
				if !cjk.MatchString(s) { // 没有中文的原文(比如 "%s")不需要译文
					return false
				}
				pos := fset.Position(n.Pos())
				switch en, has := catalog[s]; {
				case !has:
					t.Errorf("%s:%d: %q 没有英文译文", file, pos.Line, s)
				case cjk.MatchString(en):
					t.Errorf("%s:%d: %q 的译文里有中文", file, pos.Line, s)
				case verbList(en) != verbList(s):
					t.Errorf("%s:%d: %q 的译文 %q 格式动词不一致", file, pos.Line, s, en)
				}
				return false
			case *ast.CompositeLit:
				sel, isSel := n.Type.(*ast.SelectorExpr)
				if !isSel || sel.Sel.Name != "Text" {
					return true
				}
				if pkg, _ := sel.X.(*ast.Ident); pkg == nil || pkg.Name != "i18n" {
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
					t.Errorf("%s:%d: i18n.Text 缺少英文", file, fset.Position(n.Pos()).Line)
				}
				return false
			case *ast.BasicLit:
				if s, isLit := literal(n); isLit && !ok[n] && cjk.MatchString(s) {
					t.Errorf("%s:%d: 中文字面量 %q 没有经过 i18n", file, fset.Position(n.Pos()).Line, s)
				}
			}
			return true
		})
	}
	for k := range catalog {
		if !used[k] {
			t.Errorf("en.json 里的 %q 在代码里没有用到", k)
		}
	}
}

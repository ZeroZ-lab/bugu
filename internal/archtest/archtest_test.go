package archtest

import (
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
	"clock":     {},
	"task":      {},
	"schedule":  {"clock", "task"},
	"runner":    {"task"},
	"exec":      {"clock", "task", "runner"},
	"store":     {"clock", "task"},
	"notify":    {"clock", "task"},
	"api":       {"task"},
	"server":    {"api", "clock", "task", "schedule", "runner", "exec", "store", "notify"},
	"daemon":    {"server", "api", "clock", "task", "schedule", "runner", "exec", "store", "notify"},
	"client":    {"api", "task"},
	"gui":       {"client", "api", "task", "daemon", "notify"},
	"cli":       {"client", "api", "task", "daemon", "gui"},
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

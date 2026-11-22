// Package archlint 把 AGENTS.md 的硬规则写成 go/analysis 分析器,由 Run 在几种构建配置下
// 对整个模块运行。internal/archtest 调用它,所以规则随 make test 和 make check 执行。
//
// 本包列出了厂商名、含中文的诊断信息和禁用的时间函数,因此自己不受这几条规则约束。
package archlint

import "regexp"

// module 是本仓库的模块路径。
const module = "bugu"

// Allowed 是 internal 下每个包可以引用的其他内部包,即依赖方向的唯一定义。
// docs/ARCHITECTURE.md 的包表须与此一致。新增包必须先在这里登记。
var Allowed = map[string][]string{
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
	"archlint":  {},
	"archtest":  {"archlint"},
}

// Pure 是纯函数核心:给定数据算出决定,不读写文件、不启动进程、不联网、不取随机数。
// 副作用由 daemon、exec、store 这些外壳负责。测试文件不受此限。
var Pure = []string{"schedule", "notify"}

// impure 是纯函数核心不能引用的标准库包;以 / 结尾的表示整个子树。
var impure = []string{
	"os", "os/", "io/fs", "io/ioutil", "net", "net/", "syscall", "plugin", "unsafe", "log",
	"math/rand", "math/rand/v2", "crypto/rand",
}

// framework 是桌面框架的模块路径,只有 gui 能引用(PRD 5.6)。
const framework = "github.com/egoist/mygo"

// vendorNames 匹配 AI 厂商和产品的名字(硬规则 3)。
var vendorNames = regexp.MustCompile(`(?i)claude|codex|anthropic|openai|gemini|opencode`)

// clockFuncs 是绕过 internal/clock 的函数(硬规则 4)。context 的超时和截止时间
// 内部使用真实时钟,同样算绕过。
var clockFuncs = map[string][]string{
	"time":    {"Now", "Sleep", "After", "AfterFunc", "NewTimer", "NewTicker", "Tick", "Since", "Until"},
	"context": {"WithTimeout", "WithDeadline", "WithTimeoutCause", "WithDeadlineCause"},
}

// 各条规则豁免的包。
var (
	clockExempt  = []string{"clock", "fakeagent", "archlint"}
	vendorExempt = []string{"archlint"}
	textExempt   = []string{"archlint", "archtest", "fakeagent", "i18n"}
)

// translators 是 i18n 包里接受原文的函数,以及原文是第几个参数(决策记录 0014)。
var translators = map[string]int{"T": 0, "Errorf": 0, "N": 0, "In": 1}

// i18nPath 是 i18n 包的导入路径,catalogFile 是相对模块根目录的英文译文表。
const (
	i18nPath    = module + "/internal/i18n"
	catalogFile = "internal/i18n/en.json"
)

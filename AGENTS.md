# Bugu

桌面优先的定时任务应用:一个 Go 程序,后台负责调度和执行,桌面窗口、浏览器、命令行都是它的 HTTP 客户端。软件自身不含 AI 功能,只负责按时启动脚本和 AI 命令行。

## 开工前读什么

- 任何改动:[LESSONS.md](LESSONS.md),已合并的工作犯过的错。
- 做哪个里程碑、做到哪了:[docs/PRD.md](docs/PRD.md) 第 11 章。
- 某条需求的含义:docs/PRD.md 第 7 章,按编号查(S4、R6、T7…)。术语见第 4 章。
- 包的职责和依赖方向:[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)。
- 某个设计为什么这样定:[docs/adr/](docs/adr/)。
- 改界面、图标、文案:[docs/DESIGN.md](docs/DESIGN.md)。组件自己写,先用原生元素和 `web/src/ui/` 里现成的组件。

## 命令

- `make check`:合并前的唯一关卡。格式、vet、全部测试、Linux 交叉编译、前端检查。需要 Node。
- `make test`:只跑 Go 测试。单个包用 `go test ./internal/schedule/...`。
- `make build`:生成 `bin/bugu`。
- `make web`:改完 `web/` 下的前端源码后构建,产物 `internal/server/static/` 要一起提交。`make web-dev` 启动带热更新的开发服务器。

## 硬规则

每条都有对应的检查,见 `internal/archtest`。

1. 构建时 `CGO_ENABLED=0`。
2. 包之间的依赖只能按 `internal/archtest` 里的允许表走。新增包要先登记。`cli` 通过 `client` 访问后台,只在启动后台时引用 `daemon`。
3. AI 厂商和产品的名字只出现在 `runners/` 的配置文件里。Go 代码按"运行器"这个抽象工作。
4. 需要当前时间、等待、定时的代码,一律通过 `internal/clock` 注入的时钟。
5. 测试的通过与否不取决于真实时间:用假时钟推进,用计数而不是耗时来衡量。

## 完成的标准

- `make check` 通过。
- 新行为有测试,测试名或其上方注释引用 PRD 的需求编号。
- 涉及 AI 命令行的行为用 `internal/fakeagent` 测试,不调用真实的 agent。
- 包的职责或依赖方向变了,同一次提交更新 docs/ARCHITECTURE.md 和允许表。
- 完成一个里程碑条目,在 PRD 第 11 章标注。

## 先停下来问人

- 需求范围变化,即需要修改 docs/PRD.md 第 1、2、3、7 章。
- 新增第三方依赖。
- HTTP 接口的不兼容改动。
- 认证、令牌、监听地址相关的任何改动。
- 做法与某篇决策记录冲突。此时不改旧记录,提议新增一篇。

## 文档约定

- 每个事实只写在一处,其他地方用链接。
- 文档只描述现状。提案写在 issue 或合并请求里。
- 决策记录只增不改。
- 过程和总结留在提交信息里。

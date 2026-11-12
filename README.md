# Bugu 布谷

桌面优先的定时任务应用。不用写配置文件,就能让 Claude Code、Codex、Pi 等命令行 agent 或普通脚本按时干活,结果送到你面前。

名字取自布谷鸟:到点报时。

**状态:开发早期。** 目前可以在桌面应用或命令行里管理和运行定时任务,任务可以是脚本,也可以交给一个 AI 命令行;多步骤和结果处理尚未实现。进度见需求文档第 11 章。

- 需求与里程碑:[docs/PRD.md](docs/PRD.md)
- 架构:[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
- 参与开发(人或 AI agent):[AGENTS.md](AGENTS.md)

## 构建

需要 Go 1.27 或更高版本。

```sh
make build    # 生成 bin/bugu
make check    # 格式、静态检查、测试、交叉编译
make app      # 打包桌面应用到 dist/(macOS)
```

## 试用

桌面应用:运行 `bin/bugu app`,或打开 `make app` 生成的应用包。关闭窗口后它留在菜单栏,任务照常运行。

命令行:

```sh
bin/bugu serve                # 启动后台,保持运行(桌面应用开着时不需要)
bin/bugu add backup --schedule "0 2 * * *" --command "~/bin/backup.sh"
bin/bugu list
bin/bugu run backup           # 不等到 2 点,立即试跑一次
bin/bugu runs
bin/bugu logs <运行编号>
```

让 AI 命令行按时干活:

```sh
bin/bugu runner check         # 本机装了哪些 AI 命令行、能否启动
bin/bugu add issues --schedule "0 9 * * 1-5" --runner claude --cwd ~/projects/site \
  --prompt "列出 {last_run} 之后的新 issue,按 bug、需求、提问分类。"
```

AI 命令行默认以只读方式运行;确实需要它修改文件时加 `--permission write_cwd`。

全部命令见 `bin/bugu help`。

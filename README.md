# Bugu 布谷

桌面优先的定时任务应用。不用写配置文件,就能让 Claude Code、Codex、Pi 等命令行 agent 或普通脚本按时干活,结果送到你面前。

名字取自布谷鸟:到点报时。

**状态:开发早期。** 目前可以在命令行下管理和运行定时脚本,桌面应用和 AI 命令行步骤尚未实现。进度见需求文档第 11 章。

- 需求与里程碑:[docs/PRD.md](docs/PRD.md)
- 架构:[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
- 参与开发(人或 AI agent):[AGENTS.md](AGENTS.md)

## 构建

需要 Go 1.27 或更高版本。

```sh
make build    # 生成 bin/bugu
make check    # 格式、静态检查、测试、交叉编译
```

## 试用

```sh
bin/bugu serve                # 启动后台,保持运行
bin/bugu add backup --schedule "0 2 * * *" --command "~/bin/backup.sh"
bin/bugu list
bin/bugu run backup           # 不等到 2 点,立即试跑一次
bin/bugu runs
bin/bugu logs <运行编号>
```

全部命令见 `bin/bugu help`。

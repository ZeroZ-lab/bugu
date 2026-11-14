---
name: bugu
description: 用 bugu 命令行建立和管理定时任务:让一条命令、一个脚本或一个 AI 命令行按时运行,查看结果。用户说"每天/每周/定时帮我做某事""以后每天早上看一下……"时使用。
---

# 用 bugu 管理定时任务

bugu 是本机(或一台服务器)上的定时任务后台。你通过 `bugu` 命令行管理它,不要直接改它的数据目录。

## 基本约定

- 每个命令都加 `--json`:成功时标准输出是 JSON,失败时标准错误是 `{"error": {"code": "...", "message": "..."}}`,退出码 2 表示用法错误,1 表示其他错误。按 `code` 判断错误种类,按 `message` 修正后重试。
- 常见的 `code`:`usage`(参数写错)、`invalid`(任务定义有问题,message 说明怎么改)、`not_found`、`conflict`(同名任务已存在且定义不同)、`unavailable`(后台没在运行或连不上)。
- `unavailable` 时告诉用户打开布谷应用,或执行 `bugu serve`;不要自己启动后台进程。
- 对另一台主机操作时加 `--host <名字>`(先由用户用 `bugu host add` 登记)。
- 某个命令有哪些参数,用 `bugu <命令> -h` 查看。

## 建任务的步骤

1. 先看现有的:`bugu list --json`,避免重名。任务名只能用字母、数字、- 和 _,以字母或数字开头,最长 64 个字符,不能用中文。
2. 时间写成 5 段 cron(分 时 日 月 周),按主机时区;用户没提时区就不要设置。需要别的时区用 `--timezone Asia/Shanghai`。只跑一次用 `--at "2026-11-12 02:00"`。用户希望不要每次都在同一时刻("大约 9 点""随机一点")时加 `--jitter 10m`:每次在原定时间前后 10 分钟内随机触发,`{date}` 等仍按原定时间;偏移要小于相邻两次触发间隔的一半。
3. 单步任务用参数建:
   - 一条命令:`bugu add backup --schedule "0 2 * * *" --command "~/bin/backup.sh" --notify on_failure --json`
   - 让 AI 命令行做:`bugu add triage --schedule "0 9 * * 1-5" --runner <运行器> --prompt "列出 {last_run} 之后的新 issue……只读,不要回复。" --json`。可用的运行器看 `bugu runner list --json`。
4. 多步骤任务写成 JSON,用 `bugu add --file task.json --json`(`-` 表示标准输入)。格式:

```json
{
  "name": "news",
  "trigger": {"cron": "0 8,13,20 * * *"},
  "silent_if": "NOTHING_NEW",
  "notifications": [{"channel": "system", "when": "on_content"}],
  "steps": [
    {"name": "抓取", "type": "script", "script": {"command": "~/bin/fetch.sh {state_dir}"}},
    {"name": "摘要", "type": "agent", "agent": {"runner": "<运行器>", "prompt": "把下面的新条目整理成简报:\n{prev}"}}
  ]
}
```

   任务的字段:`name`、`trigger`(`{"cron": "…"}` 或 `{"at": "2026-11-12T02:00:00+08:00"}`)、`steps` 必填;可选 `trigger.jitter`(如 `"10m"`)、`cwd`、`env`(`{"名": "值"}`)、`env_file`、`timezone`、`timeout`(如 `"2h"`)、`silent_if`、`on_missed`(`run_once`、`skip`、`window`)和 `missed_window`、`output`(`{"dir": "…", "name": "report-{date}.md", "from": "stdout"}`,`from` 也可以是 `task`)、`notifications`(`[{"channel": "system" 或 "webhook", "when": "…", "url": "webhook 的地址"}]`)。

   步骤的字段:`name`(供 `{steps.<名字>.output}` 引用)、`type`、`timeout`,再按 `type` 填一项:
   - `"type": "script"`:`"script": {"command": "…"}`(用 `/bin/sh -c` 执行)或 `"script": {"path": "脚本文件"}`
   - `"type": "agent"`:`"agent": {"runner": "…", "prompt": "…", "permission": "read_only"}`
   - `"type": "text"`:`"text": "…"`,展开模板变量后直接作为这一步的输出

5. 建完先演练:`bugu run <任务> --dry-run --json` 列出将执行的命令,不真正运行。用户同意后再 `bugu run <任务> --json` 试跑一次,用 `bugu logs <运行编号> --json` 看结果。
6. 把任务名、下次运行时间和试跑结果告诉用户。

## 写任务时的要点

- 模板变量:`{date}`(`2026-11-12`)、`{time}`(`09:00:00`)、`{datetime}`、`{last_run}`(上次成功运行的时间,这两个是 RFC 3339,如 `2026-11-12T09:00:00+08:00`)、`{task_name}`、`{run_dir}`(本次的存档目录)、`{state_dir}`(跨次保留的目录,用来记"看过什么")、`{prev}`(上一步的输出)、`{steps.<步骤名>.output}`、`{output_file}`。时间都按任务的时区。
- 不认识的花括号原样保留,命令里可以照常写 `${var}`、awk 的 `{print NR}` 和 JSON。命令里的 `{prev}` 等步骤输出会安全地代入,其中的引号、`$`、`;` 不会被 shell 解释。
- 没有设置工作目录(`--cwd`)时,任务在自己的状态目录里运行。
- 从第二步起,每一步的标准输入是上一步的输出,脚本可以直接 `cat` 读取;AI 命令行步骤的提示词没有引用任何步骤的输出时,上一步的输出会自动附在提示词后面。
- `{last_run}` 是上次成功运行的开始时间,第一次运行时是任务的创建时间。要精确记住处理到哪里(比如最后看过的提交),在 `{state_dir}` 里写个文件。
- 脚本以退出码 75 结束表示"这次没事可做":后面的步骤不执行,也不通知。输出包含 `--silent-if` 的标记时同样不通知。
- AI 命令行步骤默认只读。只有用户明确要求修改文件时才加 `--permission write_cwd`,并告诉用户这一点。
- 通知时机:`on_content`(失败或有结果时,默认)、`on_result`(只在有结果时)、`on_failure`(只在失败时)、`always`、`none`。也可以用 `--webhook <地址>` 推到别处。
- 密钥不要写进任务定义,放进 `--env-file` 指向的文件,其中的值在输出和通知里会被隐藏。
- 要保存成文件:`--output-dir <目录> --output-name "report-{date}.md"`,默认把最后一步的输出写成这个文件,同名文件会被覆盖;要追加或自己控制内容,用 `--output-from task`,由任务自己写 `{output_file}`,布谷只检查它存在。目录不存在时自动建立,没有内容的运行不生成文件。

## 其他常用命令

- `bugu show <任务> --json`:任务定义,改好后 `bugu edit <任务> --file - --json`。
- `bugu edit <任务> --schedule "…" --json`:只改给出的部分。
- `bugu pause / resume / rm <任务> --json`
- `bugu runs [任务] --json`、`bugu inbox --json`:运行历史和未读结果。
- `bugu doctor --json`:环境自检。

---
name: bugu
description: Create and manage scheduled tasks with the bugu CLI - run a command, a script or an AI CLI on a schedule and read the results. Use when the user says things like "every day/week do X" or "from now on, check ... every morning".
---

# Managing scheduled tasks with bugu

bugu is a scheduled-task backend on this machine (or a server). You manage it with the `bugu` command line; never edit its data directory directly.

## Conventions

- Add `--json` to every command. On success stdout is JSON; on failure stderr is `{"error": {"code": "...", "message": "..."}}`, with exit code 2 for usage errors and 1 for other errors. Branch on `code`, fix using `message`, and retry.
- Common codes: `usage` (wrong arguments), `invalid` (problem in the task definition; the message says how to fix it), `not_found`, `conflict` (a different task with that name exists), `unavailable` (the backend isn't running or can't be reached).
- On `unavailable`, tell the user to open the Bugu app or run `bugu serve`; don't start the backend yourself.
- To act on another host, add `--host <name>` (the user registers it first with `bugu host add`).
- To see a command's flags, run `bugu <command> -h`.

## Creating a task

1. Check what exists: `bugu list --json`, to avoid name clashes. Task names use only letters, digits, - and _, start with a letter or digit, and are at most 64 characters.
2. Schedules are 5-field cron (minute hour day month weekday) in the host's time zone; leave the time zone unset unless the user mentions one. Use `--timezone Asia/Shanghai` for another one. For a single run use `--at "2026-11-12 02:00"`. When the user doesn't want the same minute every time ("around 9", "somewhat random"), add `--jitter 10m`: each run fires at a random time within 10 minutes before or after the scheduled time, while `{date}` and friends still use the scheduled time; the jitter must be less than half the gap between two triggers.
3. Single-step tasks take flags:
   - A command: `bugu add backup --schedule "0 2 * * *" --command "~/bin/backup.sh" --notify on_failure --json`
   - An AI CLI: `bugu add triage --schedule "0 9 * * 1-5" --runner <runner> --prompt "List the new issues since {last_run}... read only, don't reply." --json`. See the available runners with `bugu runner list --json`.
4. Multi-step tasks are JSON, created with `bugu add --file task.json --json` (`-` reads standard input):

```json
{
  "name": "news",
  "trigger": {"cron": "0 8,13,20 * * *"},
  "silent_if": "NOTHING_NEW",
  "notifications": [{"channel": "system", "when": "on_content"}],
  "steps": [
    {"name": "fetch", "type": "script", "script": {"command": "~/bin/fetch.sh {state_dir}"}},
    {"name": "summary", "type": "agent", "agent": {"runner": "<runner>", "prompt": "Turn these new items into a brief:\n{prev}"}}
  ]
}
```

   Task fields: `name`, `trigger` (`{"cron": "…"}` or `{"at": "2026-11-12T02:00:00+08:00"}`) and `steps` are required. Optional: `trigger.jitter` (e.g. `"10m"`), `cwd`, `env` (`{"NAME": "value"}`), `env_file`, `timezone`, `timeout` (e.g. `"2h"`), `silent_if`, `on_missed` (`run_once`, `skip`, `window`) with `missed_window`, `output` (`{"dir": "…", "name": "report-{date}.md", "from": "stdout"}`; `from` can also be `task`), `notifications` (`[{"channel": "system" or "webhook", "when": "…", "url": "webhook URL"}]`).

   Step fields: `name` (referenced as `{steps.<name>.output}`), `type`, `timeout`, plus one of these by `type`:
   - `"type": "script"`: `"script": {"command": "…"}` (run with `/bin/sh -c`) or `"script": {"path": "script file"}`
   - `"type": "agent"`: `"agent": {"runner": "…", "prompt": "…", "permission": "read_only"}`
   - `"type": "text"`: `"text": "…"`, whose expanded text becomes the step's output

5. Rehearse first: `bugu run <task> --dry-run --json` lists the commands it would run without running them. With the user's go-ahead, try it once with `bugu run <task> --json` and read the result with `bugu logs <run id> --json`.
6. Tell the user the task name, the next run time and how the trial run went.

## Writing tasks

- Template variables: `{date}` (`2026-11-12`), `{time}` (`09:00:00`), `{datetime}`, `{last_run}` (time of the last successful run; these two are RFC 3339, like `2026-11-12T09:00:00+08:00`), `{task_name}`, `{run_dir}` (this run's archive folder), `{state_dir}` (a folder kept across runs, e.g. to remember what was seen), `{prev}` (the previous step's output), `{steps.<step name>.output}`, `{output_file}`. Times are in the task's time zone.
- Unknown braces are left as they are, so commands can use `${var}`, awk's `{print NR}` and JSON as usual. Step outputs such as `{prev}` are substituted safely into commands: quotes, `$` and `;` in them are not interpreted by the shell.
- Without a working directory (`--cwd`), a task runs in its own state folder.
- From the second step on, each step's standard input is the previous step's output, so a script can just `cat` it; when an AI CLI step's prompt references no step output, the previous output is appended to the prompt.
- `{last_run}` is when the last successful run started; on the first run it is the task's creation time. To track exactly where you left off (say, the last commit seen), write a file in `{state_dir}`.
- A script that exits with code 75 means "nothing to do this time": later steps don't run and nothing is sent. Output containing the `--silent-if` marker also sends nothing.
- AI CLI steps are read-only by default. Add `--permission write_cwd` only when the user explicitly wants files changed, and say so.
- Notification timing: `on_content` (failure or a result, the default), `on_result` (only results), `on_failure` (only failures), `always`, `none`. `--webhook <url>` sends elsewhere too.
- Keep secrets out of task definitions: put them in the file given by `--env-file`; its values are hidden in outputs and notifications.
- To save a file: `--output-dir <dir> --output-name "report-{date}.md"`; by default the last step's output is written to that file, replacing a file of the same name. To append or control the content yourself, use `--output-from task` and have the task write `{output_file}`; Bugu only checks that it exists. The folder is created if missing, and runs with no content write no file.

## Other commands

- `bugu show <task> --json` prints the definition; after editing, `bugu edit <task> --file - --json`.
- `bugu edit <task> --schedule "..." --json` changes only what you give.
- `bugu pause / resume / rm <task> --json`
- `bugu runs [task] --json` and `bugu inbox --json`: run history and unread results.
- `bugu doctor --json`: environment self-check.

package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestHelpVersionAndUnknown(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		code       int
		wantStdout string
		wantStderr string
	}{
		{"帮助", []string{"help"}, 0, "用法", ""},
		{"版本", []string{"version"}, 0, "bugu 1.2.3", ""},
		{"未知命令", []string{"nope"}, 2, "", `未知命令 "nope"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := Main("1.2.3", tt.args, &out, &errOut); code != tt.code {
				t.Fatalf("退出码 = %d,期望 %d", code, tt.code)
			}
			if !strings.Contains(out.String(), tt.wantStdout) {
				t.Errorf("标准输出 %q 不含 %q", out.String(), tt.wantStdout)
			}
			if !strings.Contains(errOut.String(), tt.wantStderr) {
				t.Errorf("标准错误 %q 不含 %q", errOut.String(), tt.wantStderr)
			}
		})
	}
}

// A1:agent 常用 bugu <命令> -h 查参数。它显示这个命令的用法和通用说明,退出码 0,不报用法错误;
// 带 --json 时输出 {"help": "…"}。
func TestA1CommandHelp(t *testing.T) {
	t.Setenv("BUGU_HOME", t.TempDir())
	tests := []struct {
		name    string
		args    []string
		want    []string
		notWant string
	}{
		{"add", []string{"--lang", "zh", "add", "--help"}, []string{"--schedule <cron>", "--prompt-file", "{state_dir}"}, "bugu list"},
		{"edit 带上 add 的选项", []string{"--lang", "zh", "edit", "-h"}, []string{"bugu edit <任务>", "--schedule <cron>"}, "bugu list"},
		{"子命令", []string{"--lang", "en", "runner", "list", "-h"}, []string{"bugu runner check", "bugu runner add"}, "bugu add"},
		{"json", []string{"--lang", "zh", "run", "-h", "--json"}, []string{`"help":`, "--dry-run"}, "bugu add"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := Main("1.2.3", tt.args, &out, &errOut); code != 0 || errOut.Len() != 0 {
				t.Fatalf("退出码 = %d,标准错误 %q;期望 0 且没有错误", code, errOut.String())
			}
			for _, w := range tt.want {
				if !strings.Contains(out.String(), w) {
					t.Errorf("用法里没有 %q:\n%s", w, out.String())
				}
			}
			if strings.Contains(out.String(), tt.notWant) {
				t.Errorf("用法里不该有别的命令 %q:\n%s", tt.notWant, out.String())
			}
		})
	}
}

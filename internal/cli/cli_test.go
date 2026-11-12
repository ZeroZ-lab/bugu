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

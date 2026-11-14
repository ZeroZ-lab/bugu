package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type result struct {
	code   int
	stdout string
	stderr string
	slept  []time.Duration
	hung   bool
}

func runWith(t *testing.T, vars map[string]string, stdin string, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	var r result
	r.code = run(args, strings.NewReader(stdin), &out, &errOut, env{
		getenv: func(k string) string { return vars[k] },
		sleep:  func(d time.Duration) { r.slept = append(r.slept, d) },
		hang:   func() { r.hung = true },
	})
	r.stdout, r.stderr = out.String(), errOut.String()
	return r
}

func TestEchoesPromptFromArgs(t *testing.T) {
	r := runWith(t, nil, "", "看一下", "issue")
	if r.code != 0 || r.stdout != "fakeagent: 看一下 issue\n" {
		t.Fatalf("code=%d stdout=%q", r.code, r.stdout)
	}
}

func TestReadsPromptFromStdin(t *testing.T) {
	r := runWith(t, nil, "来自标准输入\n")
	if r.stdout != "fakeagent: 来自标准输入\n" {
		t.Fatalf("stdout=%q", r.stdout)
	}
}

func TestOutputStderrAndExitCode(t *testing.T) {
	r := runWith(t, map[string]string{
		"FAKEAGENT_OUTPUT": "NOTHING_NEW",
		"FAKEAGENT_STDERR": "警告",
		"FAKEAGENT_EXIT":   "7",
	}, "", "x")
	if r.code != 7 || r.stdout != "NOTHING_NEW\n" || r.stderr != "警告\n" {
		t.Fatalf("%+v", r)
	}
}

func TestSleepAndHang(t *testing.T) {
	r := runWith(t, map[string]string{"FAKEAGENT_SLEEP": "30s", "FAKEAGENT_HANG": "1"}, "", "x")
	if len(r.slept) != 1 || r.slept[0] != 30*time.Second || !r.hung {
		t.Fatalf("%+v", r)
	}
}

// 对应需求 E9:等待人工确认而没有输入时,agent 以明确的错误退出。
func TestAskWithoutInputFails(t *testing.T) {
	r := runWith(t, map[string]string{"FAKEAGENT_ASK": "1"}, "", "x")
	if r.code != exitNoInput || !strings.Contains(r.stderr, "无法确认") {
		t.Fatalf("%+v", r)
	}
}

func TestAskWithInputContinues(t *testing.T) {
	r := runWith(t, map[string]string{"FAKEAGENT_ASK": "1"}, "y\n", "x")
	if r.code != 0 || !strings.Contains(r.stdout, "fakeagent: x") {
		t.Fatalf("%+v", r)
	}
}

func TestWritesArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.md")
	r := runWith(t, map[string]string{"FAKEAGENT_WRITE": path, "FAKEAGENT_WRITE_CONTENT": "# 日报"}, "", "x")
	got, err := os.ReadFile(path)
	if r.code != 0 || err != nil || string(got) != "# 日报" {
		t.Fatalf("code=%d err=%v got=%q", r.code, err, got)
	}
}

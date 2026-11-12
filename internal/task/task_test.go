package task

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// T1: 任务按"触发、步骤列表、通知列表"存储,序列化后能原样读回。
func TestT1TaskRoundTripsThroughJSON(t *testing.T) {
	in := Task{
		Name:    "backup",
		Trigger: Trigger{Cron: "0 2 * * *"},
		Steps:   []Step{{Type: StepScript, Script: Script{Command: "~/bin/backup.sh"}}},
		Env:     map[string]string{"A": "b"},
		Timeout: Duration(90 * time.Minute),
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"timeout":"1h30m0s"`) || strings.Contains(string(b), `"at"`) {
		t.Fatalf("序列化结果不符合预期: %s", b)
	}
	var out Task
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	again, _ := json.Marshal(out)
	if string(again) != string(b) {
		t.Fatalf("读回后不一致:\n%s\n%s", b, again)
	}
}

func TestDurationRejectsBadInput(t *testing.T) {
	var d Duration
	for _, in := range []string{`90`, `"soon"`} {
		if err := json.Unmarshal([]byte(in), &d); err == nil {
			t.Errorf("%s 应被拒绝", in)
		}
	}
	if err := json.Unmarshal([]byte(`""`), &d); err != nil || d != 0 {
		t.Errorf("空字符串应表示不设上限: %v %v", d, err)
	}
}

// T1/S1/S2: 名字要能安全地用作文件名;触发在 cron 和一次性时间点之间二选一。
func TestT1S1S2Validate(t *testing.T) {
	at := time.Date(2026, 11, 12, 2, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		task Task
		ok   bool
	}{
		{"cron", Task{Name: "backup", Trigger: Trigger{Cron: "0 2 * * *"}}, true},
		{"一次性", Task{Name: "once_1", Trigger: Trigger{At: at}}, true},
		{"没有触发", Task{Name: "backup"}, false},
		{"两种触发都填", Task{Name: "backup", Trigger: Trigger{Cron: "0 2 * * *", At: at}}, false},
		{"空名字", Task{Trigger: Trigger{Cron: "0 2 * * *"}}, false},
		{"路径穿越", Task{Name: "../x", Trigger: Trigger{Cron: "0 2 * * *"}}, false},
		{"带点", Task{Name: "a.b", Trigger: Trigger{Cron: "0 2 * * *"}}, false},
		{"过长", Task{Name: strings.Repeat("a", 65), Trigger: Trigger{Cron: "0 2 * * *"}}, false},
	}
	for _, tt := range tests {
		if err := Validate(tt.task); (err == nil) != tt.ok {
			t.Errorf("%s: err=%v,期望通过=%v", tt.name, err, tt.ok)
		}
	}
}

// N2: 通知设置只接受已支持的渠道和时机。
func TestN2ValidateNotifications(t *testing.T) {
	base := Task{Name: "backup", Trigger: Trigger{Cron: "0 2 * * *"}}
	for _, tt := range []struct {
		n  Notification
		ok bool
	}{
		{Notification{Channel: ChannelSystem}, true},
		{Notification{Channel: ChannelSystem, When: NotifyOnFailure}, true},
		{Notification{Channel: ChannelSystem, When: NotifyAlways}, true},
		{Notification{Channel: ChannelSystem, When: "sometimes"}, false},
		{Notification{Channel: "pager"}, false},
		{Notification{}, false},
	} {
		tk := base
		tk.Notifications = []Notification{tt.n}
		if err := Validate(tk); (err == nil) != tt.ok {
			t.Errorf("%+v: err=%v,期望通过=%v", tt.n, err, tt.ok)
		}
	}
}

// T2 与决策记录 0008: AI 命令行步骤能原样存取;没有放开权限时不写出权限字段;级别有高低次序。
func TestT2AgentStepRoundTripsAndPermissionsAreOrdered(t *testing.T) {
	in := Task{Name: "issues", Trigger: Trigger{Cron: "0 9 * * 1-5"}, Steps: []Step{{Type: StepAgent, Agent: Agent{Runner: "r", Prompt: "p"}}}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "permission") || strings.Contains(string(b), `"script"`) || strings.Contains(string(b), "created_at") {
		t.Fatalf("不应写出空字段: %s", b)
	}
	var out Task
	if err := json.Unmarshal(b, &out); err != nil || out.Steps[0].Agent != in.Steps[0].Agent {
		t.Fatalf("读回: %+v, %v", out, err)
	}
	ro, _ := PermissionRank(PermReadOnly)
	rw, _ := PermissionRank(PermWriteCwd)
	full, _ := PermissionRank(PermFull)
	if !(ro < rw && rw < full) {
		t.Fatalf("级别次序: %d %d %d", ro, rw, full)
	}
	if _, ok := PermissionRank("root"); ok {
		t.Fatal("不认识的级别")
	}
}

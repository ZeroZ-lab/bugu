package task

import (
	"strings"

	"bugu/internal/i18n"
	"bugu/internal/schedule" // want imports
)

var (
	_ = schedule.X
	_ = i18n.T("你好")
	_ = i18n.In("en", "好的 %s", "x")
	_ = i18n.T("坏的 %d", 1)  // want i18n
	_ = i18n.T("混的")        // want i18n
	_ = i18n.Errorf("没有译文") // want i18n
	_ = i18n.N("%s")
	_ = "裸中文"                                   // want i18n
	_ = i18n.T("好的 %s", strings.Join(nil, "、")) // want i18n
	_ = i18n.Text{Zh: "中", En: "Mid"}
	_ = i18n.Text{Zh: "中"} // want i18n
)

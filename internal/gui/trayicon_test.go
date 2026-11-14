package gui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"bugu/internal/api"
)

// G9 与 DESIGN.md:几种状态同时成立时,按"有失败 > 有新结果 > 运行中 > 平时"取第一个。
func TestG9TrayStatePriority(t *testing.T) {
	for _, tt := range []struct {
		in    api.Inbox
		state trayState
		count int
	}{
		{api.Inbox{}, trayIdle, 0},
		{api.Inbox{Running: 2}, trayRunning, 0},
		{api.Inbox{Unread: 3, Running: 1}, trayUnread, 3},
		{api.Inbox{Unread: 3, UnreadFailed: 1, Running: 1}, trayFailed, 1},
	} {
		if s, n := stateOf(tt.in); s != tt.state || n != tt.count {
			t.Errorf("%+v: state=%d count=%d", tt.in, s, n)
		}
	}
}

func decode(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// G9: 四种状态只改变右下那一声。右下圆心(27.5, 23.5)在空心圈和虚线圈里是透明的,
// 在"有新结果"和"有失败"里是实心的;"有失败"不是模板图标,右下是失败红,左上随菜单栏深浅。
func TestG9TrayIcons(t *testing.T) {
	center := func(img image.Image) color.NRGBA { return color.NRGBAModel.Convert(img.At(27, 23)).(color.NRGBA) }
	left := func(img image.Image) color.NRGBA { return color.NRGBAModel.Convert(img.At(11, 11)).(color.NRGBA) }

	for _, s := range []trayState{trayIdle, trayRunning} {
		b, template := drawTray(s, 0, false)
		if img := decode(t, b); !template || center(img).A != 0 || left(img).A != 0xff {
			t.Errorf("状态 %d: template=%v center=%v left=%v", s, template, center(img), left(img))
		}
	}
	b, template := drawTray(trayUnread, 0, false)
	if img := decode(t, b); !template || center(img) != (color.NRGBA{A: 0xff}) {
		t.Errorf("有新结果: template=%v center=%v", template, center(img))
	}
	for dark, want := range map[bool][2]color.NRGBA{
		false: {{A: 0xff}, failLight},
		true:  {{0xff, 0xff, 0xff, 0xff}, failDark},
	} {
		b, template := drawTray(trayFailed, 0, dark)
		if img := decode(t, b); template || left(img) != want[0] || center(img) != want[1] {
			t.Errorf("有失败(深色=%v): template=%v left=%v center=%v", dark, template, left(img), center(img))
		}
	}
	// 虚线圈转动:不同角度画出来的图不同,一整段之后回到原样。
	a, _ := drawTray(trayRunning, 0, false)
	c, _ := drawTray(trayRunning, 0.3, false)
	full, _ := drawTray(trayRunning, 2*3.141592653589793/8, false)
	if bytes.Equal(a, c) || !bytes.Equal(a, full) {
		t.Error("虚线圈的角度没有生效")
	}
}

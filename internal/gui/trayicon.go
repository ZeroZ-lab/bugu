package gui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"

	"bugu/internal/api"
)

// trayState 是菜单栏图标的状态,画法见 docs/DESIGN.md 的"菜单栏图标"(G9)。
type trayState int

const (
	trayIdle    trayState = iota // 平时:右下空心圈
	trayRunning                  // 运行中:右下虚线圈,缓慢转动
	trayUnread                   // 有新结果:右下实心,旁边是未读条数
	trayFailed                   // 有失败:右下失败红的实心,旁边是失败条数
)

// stateOf 按"有失败 > 有新结果 > 运行中 > 平时"取状态,并给出图标旁边显示的条数。
func stateOf(in api.Inbox) (trayState, int) {
	switch {
	case in.UnreadFailed > 0:
		return trayFailed, in.UnreadFailed
	case in.Unread > 0:
		return trayUnread, in.Unread
	case in.Running > 0:
		return trayRunning, 0
	}
	return trayIdle, 0
}

// 失败红,与网页的 --bad 相同;深色菜单栏上用亮一些的那个。
var (
	failLight = color.NRGBA{0xc2, 0x36, 0x2b, 0xff}
	failDark  = color.NRGBA{0xff, 0x7b, 0x6e, 0xff}
)

// drawTray 画一个状态的图标,返回 PNG 和它是否作为模板图标(由系统按菜单栏着色)。
// phase 是运行中那一圈虚线转过的角度(弧度)。"有失败"不能做模板图标,所以按 dark 画两种颜色。
// 几何取自 resources/logo.svg,平移后正好占满 36×36(菜单栏 18pt 的两倍像素)。
func drawTray(s trayState, phase float64, dark bool) (pngData []byte, template bool) {
	const size, sub = 36, 4 // 每个像素取 4×4 个采样点,边缘抗锯齿
	const ox, oy = 5.5, 7.5 // logo 画布到图标的平移
	const r, w = 5.5, 1.75  // 右下空心圈的半径和半个描边宽
	ink, accent := color.NRGBA{A: 0xff}, color.NRGBA{A: 0xff}
	if s == trayFailed {
		accent = failLight
		if dark {
			ink, accent = color.NRGBA{0xff, 0xff, 0xff, 0xff}, failDark
		}
	}
	// at 返回采样点落在哪一声上:0 不在图形上,1 左上,2 右下。
	at := func(x, y float64) int {
		if math.Hypot(x-(17-ox), y-(19-oy)) <= 9 {
			return 1
		}
		dx, dy := x-(33-ox), y-(31-oy)
		d := math.Hypot(dx, dy)
		switch s {
		case trayUnread, trayFailed:
			if d <= r+w {
				return 2
			}
		case trayRunning:
			// 八段虚线,每段占六成。
			seg := math.Mod((math.Atan2(dy, dx)+phase)/(2*math.Pi/8)+16, 1)
			if d >= r-w && d <= r+w && seg < 0.6 {
				return 2
			}
		default:
			if d >= r-w && d <= r+w {
				return 2
			}
		}
		return 0
	}
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			var hits [3]int
			for sy := range sub {
				for sx := range sub {
					hits[at(float64(x)+(float64(sx)+0.5)/sub, float64(y)+(float64(sy)+0.5)/sub)]++
				}
			}
			c := ink
			if hits[2] > hits[1] {
				c = accent
			}
			c.A = uint8((hits[1] + hits[2]) * 255 / (sub * sub))
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes(), s != trayFailed
}

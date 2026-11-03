package winapp

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// Status 是托盘图标要表达的状态。
type Status int

const (
	StatusOnline Status = iota
	StatusOffline
	StatusWorking // 检测/认证中
	StatusSuspend // 时段外挂起
)

func statusColor(s Status) color.RGBA {
	switch s {
	case StatusOnline:
		return color.RGBA{0x1f, 0xb5, 0x6b, 0xff} // 绿
	case StatusOffline:
		return color.RGBA{0xe0, 0x3e, 0x33, 0xff} // 红
	case StatusWorking:
		return color.RGBA{0xe8, 0x9a, 0x16, 0xff} // 琥珀
	default:
		return color.RGBA{0x6b, 0x74, 0x82, 0xff} // 灰
	}
}

// renderIcon 画图标：满幅圆角方块（状态色）+ 粗白信号弧。
//
// 设计取舍：
//   - 满幅而不是留白：托盘 16px 下留白会让图标显得"半透明"，糊在任务栏上。
//   - 白色弧线加粗到 0.125 宽：细线在 16px 会被抗锯齿吃成灰色。
//   - 外沿留一圈比底色略深的内描边，浅色任务栏上也有明确边界。
func renderIcon(size int, s Status) image.Image {
	const ss = 4
	w := size * ss
	img := image.NewRGBA(image.Rect(0, 0, w, w))

	base := statusColor(s)
	white := color.RGBA{0xff, 0xff, 0xff, 0xff}
	edge := shade(base, 0.78)

	fw := float64(w)
	pad := 0.02 * fw
	rad := 0.235 * fw
	box := [4]float64{pad, pad, fw - pad, fw - pad}
	inner := shrink(box, 0.038*fw)
	innerRad := math.Max(0, rad-0.038*fw)

	cx := 0.5 * fw
	cy := 0.745 * fw

	// 小尺寸（托盘 16/20px）只画一条弧：
	// 两条 0.125 粗的弧在 16px 下间距不足 1 像素，会糊成一块白斑。
	var arcs []struct{ r, t float64 }
	var dotR float64
	if size <= 24 {
		arcs = []struct{ r, t float64 }{{0.34 * fw, 0.165 * fw}}
		dotR = 0.125 * fw
	} else {
		arcs = []struct{ r, t float64 }{
			{0.30 * fw, 0.125 * fw},
			{0.50 * fw, 0.125 * fw},
		}
		dotR = 0.098 * fw
	}

	for y := 0; y < w; y++ {
		for x := 0; x < w; x++ {
			fx := float64(x) + 0.5
			fy := float64(y) + 0.5

			if !inRoundRect(fx, fy, box, rad) {
				continue
			}
			c := base
			if !inRoundRect(fx, fy, inner, innerRad) {
				c = edge
			}

			dx := fx - cx
			dy := cy - fy
			d := math.Hypot(dx, dy)
			ang := math.Atan2(dy, dx) * 180 / math.Pi

			if ang >= 38 && ang <= 142 {
				for _, a := range arcs {
					if math.Abs(d-a.r) <= a.t/2 {
						c = white
						break
					}
				}
			}
			if d <= dotR {
				c = white
			}
			img.SetRGBA(x, y, c)
		}
	}
	return downsample(img, size, ss)
}

func downsample(src *image.RGBA, size, ss int) image.Image {
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a uint32
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					pr, pg, pb, pa := src.At(x*ss+sx, y*ss+sy).RGBA()
					r += pr
					g += pg
					b += pb
					a += pa
				}
			}
			n := uint32(ss * ss)
			out.SetRGBA(x, y, color.RGBA{
				R: uint8(r / n >> 8), G: uint8(g / n >> 8),
				B: uint8(b / n >> 8), A: uint8(a / n >> 8),
			})
		}
	}
	return out
}

func inRoundRect(x, y float64, box [4]float64, r float64) bool {
	l, t, rr, b := box[0], box[1], box[2], box[3]
	if x < l || x > rr || y < t || y > b {
		return false
	}
	if r <= 0 {
		return true
	}
	cx := math.Min(math.Max(x, l+r), rr-r)
	cy := math.Min(math.Max(y, t+r), b-r)
	return math.Hypot(x-cx, y-cy) <= r
}

func shrink(box [4]float64, d float64) [4]float64 {
	return [4]float64{box[0] + d, box[1] + d, box[2] - d, box[3] - d}
}

func shade(c color.RGBA, k float64) color.RGBA {
	return color.RGBA{
		R: uint8(float64(c.R) * k),
		G: uint8(float64(c.G) * k),
		B: uint8(float64(c.B) * k),
		A: c.A,
	}
}

// makeICO 把多个尺寸打包成一个 ICO。
// 多尺寸的意义：任务栏 16/20/24、资源管理器 32/48、属性页 256 各取所需；
// 只塞一张 256 进去交给系统缩放，小尺寸会糊。
func makeICO(sizes []int, s Status) []byte {
	type entry struct {
		size int
		data []byte
	}
	items := make([]entry, 0, len(sizes))
	for _, sz := range sizes {
		var buf bytes.Buffer
		_ = png.Encode(&buf, renderIcon(sz, s))
		items = append(items, entry{sz, buf.Bytes()})
	}

	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, uint16(0)) // reserved
	binary.Write(&out, binary.LittleEndian, uint16(1)) // type = icon
	binary.Write(&out, binary.LittleEndian, uint16(len(items)))

	offset := 6 + 16*len(items)
	for _, it := range items {
		d := byte(it.size)
		if it.size >= 256 {
			d = 0
		}
		out.WriteByte(d)
		out.WriteByte(d)
		out.WriteByte(0)
		out.WriteByte(0)
		binary.Write(&out, binary.LittleEndian, uint16(1))
		binary.Write(&out, binary.LittleEndian, uint16(32))
		binary.Write(&out, binary.LittleEndian, uint32(len(it.data)))
		binary.Write(&out, binary.LittleEndian, uint32(offset))
		offset += len(it.data)
	}
	for _, it := range items {
		out.Write(it.data)
	}
	return out.Bytes()
}

// PNGFor 返回单张图标的 PNG 数据。
//
// CreateIconFromResourceEx 要的是「单张图标图像」，不是 ICO 容器，
// 所以托盘图标这里直接给 PNG。Vista 之后系统原生支持 PNG 图标。
func PNGFor(size int, s Status) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, renderIcon(size, s))
	return buf.Bytes()
}

// IconsForApp 生成 exe 资源用的多尺寸图标。
func IconsForApp() []byte {
	return makeICO([]int{16, 20, 24, 32, 40, 48, 64, 128, 256}, StatusOnline)
}

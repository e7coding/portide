//go:build ignore

// gen-icons 画出 Portide 的图标（A2「单条流线」，矢量稿见 site/public/assets/mark.svg）：深色圆角方块上
// 两个浅色端口（设备），中间一条薄荷绿流线（传输）。坐标和矢量稿一样按 64×64 计。
// 在 desktop/ 目录里运行：go run build/gen-icons.go
// 同时生成托盘图标（传输中的那一版右下角带一个点）、iOS 应用图标（M4）和网站的 apple-touch-icon：
// iOS 的是不带圆角、不透明的方块（App Store 不接受透明的图标，圆角由系统加上）。
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
)

var (
	ink   = color.NRGBA{0x10, 0x2A, 0x33, 0xFF} // 底色（#102A33）
	port  = color.NRGBA{0xF3, 0xF8, 0xF4, 0xFF} // 端口（#F3F8F4）
	flow  = color.NRGBA{0x58, 0xE0, 0xBC, 0xFF} // 流线（#58E0BC）
	white = color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
)

// rounded 为 false 时画不带圆角的方块（iOS 应用图标）；dot 为 true 时右下角加一个点（托盘：传输中）。
var rounded, dot = true, false

// 矢量稿里的形状（64 单位）：外框圆角 15；两个端口 15×18、圆角 4；
// 流线是三次贝塞尔 M23 30 c7-6 11 10 18 4，线宽 6.5，两端圆头。
const (
	outerR    = 15.0
	portR     = 4.0
	flowHalf  = 6.5 / 2
	flowSteps = 48
)

var ports = [][4]float64{{9, 23, 15, 18}, {40, 23, 15, 18}} // x, y, 宽, 高

// flowLine 是流线按 flowSteps 段取点得到的折线。
var flowLine = func() [][2]float64 {
	p0, p1, p2, p3 := [2]float64{23, 30}, [2]float64{30, 24}, [2]float64{34, 40}, [2]float64{41, 34}
	pts := make([][2]float64, 0, flowSteps+1)
	for i := 0; i <= flowSteps; i++ {
		t := float64(i) / flowSteps
		u := 1 - t
		a, b, c, d := u*u*u, 3*u*u*t, 3*u*t*t, t*t*t
		pts = append(pts, [2]float64{a*p0[0] + b*p1[0] + c*p2[0] + d*p3[0], a*p0[1] + b*p1[1] + c*p2[1] + d*p3[1]})
	}
	return pts
}()

// inRoundRect 报告点 (x, y) 是否落在左上角 (rx, ry)、宽 w、高 h、圆角 r 的圆角矩形里。
func inRoundRect(x, y, rx, ry, w, h, r float64) bool {
	if x < rx || y < ry || x > rx+w || y > ry+h {
		return false
	}
	cx := math.Max(rx+r-x, math.Max(0, x-(rx+w-r)))
	cy := math.Max(ry+r-y, math.Max(0, y-(ry+h-r)))
	return cx*cx+cy*cy <= r*r
}

// segDist 是点到线段的距离。
func segDist(x, y float64, a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	t := 0.0
	if l := dx*dx + dy*dy; l > 0 {
		t = math.Max(0, math.Min(1, ((x-a[0])*dx+(y-a[1])*dy)/l))
	}
	return math.Hypot(x-(a[0]+t*dx), y-(a[1]+t*dy))
}

func onFlow(x, y float64) bool {
	for i := 1; i < len(flowLine); i++ {
		if segDist(x, y, flowLine[i-1], flowLine[i]) <= flowHalf {
			return true
		}
	}
	return false
}

// shade 返回单位坐标 (x, y)（0 到 1）处的颜色；透明的地方返回 ok=false。
func shade(x, y float64) (color.NRGBA, bool) {
	if dot {
		// 右下角的点，外面一圈白边，和图标分开。
		if d := math.Hypot(x-0.8, y-0.8); d < 0.15 {
			return flow, true
		} else if d < 0.2 {
			return white, true
		}
	}
	gx, gy := x*64, y*64
	r := outerR
	if !rounded {
		r = 0
	}
	if !inRoundRect(gx, gy, 0, 0, 64, 64, r) {
		return color.NRGBA{}, false
	}
	// 流线画在端口上面（矢量稿里 path 在最后）。
	if onFlow(gx, gy) {
		return flow, true
	}
	for _, p := range ports {
		if inRoundRect(gx, gy, p[0], p[1], p[2], p[3], portR) {
			return port, true
		}
	}
	return ink, true
}

// draw 用 4x4 超采样画出 size×size 的图标。
func draw(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	const ss = 4
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/ss) / float64(size)
					y := (float64(py) + (float64(sy)+0.5)/ss) / float64(size)
					if c, ok := shade(x, y); ok {
						r, g, b, a = r+float64(c.R), g+float64(c.G), b+float64(c.B), a+1
					}
				}
			}
			if a == 0 {
				continue
			}
			img.SetNRGBA(px, py, color.NRGBA{uint8(r / a), uint8(g / a), uint8(b / a), uint8(a / (ss * ss) * 255)})
		}
	}
	return img
}

func encode(img image.Image) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		log.Fatal(err)
	}
	return buf.Bytes()
}

// ico 把几张 PNG 打包成 Windows 的 .ico（Vista 起支持 PNG 格式的图标项）。
func ico(sizes []int) []byte {
	var head, body bytes.Buffer
	binary.Write(&head, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for _, s := range sizes {
		data := encode(draw(s))
		dim := uint8(s)
		if s >= 256 {
			dim = 0
		}
		binary.Write(&head, binary.LittleEndian, struct {
			W, H, Colors, Reserved uint8
			Planes, Bits           uint16
			Size, Offset           uint32
		}{dim, dim, 0, 0, 1, 32, uint32(len(data)), uint32(offset)})
		body.Write(data)
		offset += len(data)
	}
	return append(head.Bytes(), body.Bytes()...)
}

func write(path string, data []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		log.Fatal(err)
	}
}

func main() {
	for _, s := range []int{16, 32, 48, 64, 128, 256, 512, 1024} {
		write(filepath.Join("build", "icons", strconv.Itoa(s)+".png"), encode(draw(s)))
	}
	write(filepath.Join("build", "windows", "icon.ico"), ico([]int{16, 32, 48, 256}))
	dot = true
	write(filepath.Join("build", "icons", "tray-active-64.png"), encode(draw(64)))
	dot = false
	// iOS：不透明的方块，Go 的 PNG 编码器对不透明的图像不写透明通道。
	rounded = false
	write(filepath.Join("..", "ios", "Portide", "Assets.xcassets", "AppIcon.appiconset", "AppIcon-1024.png"), encode(draw(1024)))
	// 网站：iOS 加到主屏幕时用的图标，同样不带圆角。
	write(filepath.Join("..", "site", "public", "assets", "apple-touch-icon.png"), encode(draw(180)))
}

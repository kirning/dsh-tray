package core

import (
	"bytes"
	"embed"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"

	xdraw "golang.org/x/image/draw"
)

//go:embed assets/running.png assets/stopped.png assets/starting-a.png assets/starting-b.png
var iconAssets embed.FS

// State 是托盘三态。
type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
)

var (
	badgeRunning  = color.RGBA{0x2E, 0xA0, 0x43, 0xFF}
	badgeStarting = color.RGBA{0xF0, 0x9D, 0x2E, 0xFF}
	badgeStopped  = color.RGBA{0x8C, 0x8C, 0x8C, 0xFF}
)

func loadSprite(name string) (image.Image, error) {
	b, err := iconAssets.ReadFile("assets/" + name)
	if err != nil {
		return nil, err
	}
	return png.Decode(bytes.NewReader(b))
}

// compose 把角色缩放到方形画布并画右下角状态点。
func compose(sprite image.Image, size int, badge color.RGBA) *image.RGBA {
	canvas := image.NewRGBA(image.Rect(0, 0, size, size))
	sb := sprite.Bounds()
	if sb.Dx() <= 0 || sb.Dy() <= 0 {
		return canvas
	}
	targetH := float64(size) * 0.94
	scale := targetH / float64(sb.Dy())
	targetW := float64(sb.Dx()) * scale
	x0 := int(math.Round((float64(size) - targetW) / 2))
	y0 := int(math.Round(float64(size) - targetH))
	dst := image.Rect(x0, y0, x0+int(math.Round(targetW)), y0+int(math.Round(targetH)))
	xdraw.CatmullRom.Scale(canvas, dst, sprite, sb, xdraw.Over, nil)

	r := float64(size) * 0.15
	if r < 2 {
		r = 2
	}
	inset := float64(size) * 0.035
	cx := float64(size) - r - inset
	cy := float64(size) - r - inset
	ring := float64(size) * 0.025
	if ring < 1 {
		ring = 1
	}
	fillCircle(canvas, cx, cy, r+ring, color.RGBA{255, 255, 255, 255})
	fillCircle(canvas, cx, cy, r, badge)
	return canvas
}

func fillCircle(img *image.RGBA, cx, cy, r float64, c color.RGBA) {
	x0 := int(math.Floor(cx - r - 1))
	x1 := int(math.Ceil(cx + r + 1))
	y0 := int(math.Floor(cy - r - 1))
	y1 := int(math.Ceil(cy + r + 1))
	b := img.Bounds()
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			if x < b.Min.X || x >= b.Max.X || y < b.Min.Y || y >= b.Max.Y {
				continue
			}
			dx := float64(x) + 0.5 - cx
			dy := float64(y) + 0.5 - cy
			d := math.Sqrt(dx*dx + dy*dy)
			a := 1.0
			if d > r {
				if d > r+1 {
					continue
				}
				a = r + 1 - d
			}
			blendPixel(img, x, y, c, a)
		}
	}
}

func blendPixel(img *image.RGBA, x, y int, c color.RGBA, a float64) {
	if a <= 0 {
		return
	}
	old := img.RGBAAt(x, y)
	oa := float64(old.A) / 255
	na := a + oa*(1-a)
	if na <= 0 {
		return
	}
	mix := func(src, dst uint8) uint8 {
		v := (float64(src)*a + float64(dst)*oa*(1-a)) / na
		if v > 255 {
			v = 255
		}
		if v < 0 {
			v = 0
		}
		return uint8(v)
	}
	img.SetRGBA(x, y, color.RGBA{
		R: mix(c.R, old.R), G: mix(c.G, old.G), B: mix(c.B, old.B),
		A: uint8(math.Round(na * 255)),
	})
}

// TrayIcon 返回指定状态的托盘图标（PNG 字节，Wails 各平台都接受）。
// frame 用于“启动中”的两帧动画：0 → starting-a，1 → starting-b。
func TrayIcon(state State, frame int) ([]byte, error) {
	sprite, badge := "stopped.png", badgeStopped
	switch state {
	case StateRunning:
		sprite, badge = "running.png", badgeRunning
	case StateStarting:
		badge = badgeStarting
		if frame%2 == 0 {
			sprite = "starting-a.png"
		} else {
			sprite = "starting-b.png"
		}
	}
	im, err := loadSprite(sprite)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, compose(im, 64, badge)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WriteAppIcon 生成构建期用的应用图标（不带状态角标）。
func WriteAppIcon(path string, size int) error {
	im, err := loadSprite("running.png")
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, composePlain(im, size)); err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("写入图标失败: %w", err)
	}
	return nil
}

func composePlain(sprite image.Image, size int) *image.RGBA {
	canvas := image.NewRGBA(image.Rect(0, 0, size, size))
	sb := sprite.Bounds()
	if sb.Dx() <= 0 || sb.Dy() <= 0 {
		return canvas
	}
	targetH := float64(size) * 0.96
	scale := targetH / float64(sb.Dy())
	targetW := float64(sb.Dx()) * scale
	x0 := int(math.Round((float64(size) - targetW) / 2))
	y0 := int(math.Round(float64(size) - targetH))
	xdraw.CatmullRom.Scale(canvas,
		image.Rect(x0, y0, x0+int(math.Round(targetW)), y0+int(math.Round(targetH))),
		sprite, sb, xdraw.Over, nil)
	return canvas
}

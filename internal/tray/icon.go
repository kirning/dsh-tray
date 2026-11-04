package tray

import (
	"bytes"
	"embed"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"

	"github.com/lxn/walk"
	xdraw "golang.org/x/image/draw"
)

//go:embed assets/running.png assets/stopped.png assets/starting-a.png assets/starting-b.png
var iconAssets embed.FS

// 托盘/任务栏会用到的尺寸，一次全打进去，Windows 按 DPI 自选。
var icoSizes = []int{16, 20, 24, 32, 40, 48, 64, 128, 256}

var (
	badgeRunning  = color.RGBA{0x2E, 0xA0, 0x43, 0xFF} // 绿：已就绪
	badgeStarting = color.RGBA{0xF0, 0x9D, 0x2E, 0xFF} // 橙：启动中
	badgeStopped  = color.RGBA{0x8C, 0x8C, 0x8C, 0xFF} // 灰：未运行
)

func loadEmbeddedSprite(name string) (image.Image, error) {
	b, err := iconAssets.ReadFile("assets/" + name)
	if err != nil {
		return nil, err
	}
	return png.Decode(bytes.NewReader(b))
}

// composeIcon 把角色按比例缩放铺到方形画布，并在右下角画状态圆点。
func composeIcon(sprite image.Image, size int, badge color.RGBA) *image.RGBA {
	canvas := image.NewRGBA(image.Rect(0, 0, size, size))
	sb := sprite.Bounds()
	if sb.Dx() <= 0 || sb.Dy() <= 0 {
		return canvas
	}

	// 高度占 94%，横向居中；给右下角标留出视觉空间
	targetH := float64(size) * 0.94
	scale := targetH / float64(sb.Dy())
	targetW := float64(sb.Dx()) * scale

	x0 := int(math.Round((float64(size) - targetW) / 2))
	y0 := int(math.Round(float64(size) - targetH))
	dst := image.Rect(x0, y0, x0+int(math.Round(targetW)), y0+int(math.Round(targetH)))
	xdraw.CatmullRom.Scale(canvas, dst, sprite, sb, xdraw.Over, nil)

	// 状态角标：小图标时也至少 4px，保证能看见
	r := float64(size) * 0.15
	if r < 2.0 {
		r = 2.0
	}
	inset := float64(size) * 0.035
	cx := float64(size) - r - inset
	cy := float64(size) - r - inset
	// 白色描边保证在深色角色/浅色任务栏上都清晰
	ring := float64(size) * 0.025
	if ring < 1 {
		ring = 1
	}
	fillCircle(canvas, cx, cy, r+ring, color.RGBA{255, 255, 255, 255})
	fillCircle(canvas, cx, cy, r, badge)

	return canvas
}

// fillCircle 画带抗锯齿边缘的实心圆。
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
			if a > 1 {
				a = 1
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
	oa := float64(old.A) / 255.0
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
		R: mix(c.R, old.R),
		G: mix(c.G, old.G),
		B: mix(c.B, old.B),
		A: uint8(math.Round(na * 255)),
	})
}

// encodeMultiICO 生成多尺寸 ICO（Vista+ 支持 PNG 负载）。
func encodeMultiICO(images []*image.RGBA) ([]byte, error) {
	type entry struct {
		size int
		data []byte
	}
	var entries []entry
	for _, im := range images {
		var buf bytes.Buffer
		if err := png.Encode(&buf, im); err != nil {
			return nil, err
		}
		entries = append(entries, entry{size: im.Bounds().Dx(), data: buf.Bytes()})
	}

	out := new(bytes.Buffer)
	binary.Write(out, binary.LittleEndian, uint16(0))            // reserved
	binary.Write(out, binary.LittleEndian, uint16(1))            // type = icon
	binary.Write(out, binary.LittleEndian, uint16(len(entries))) // count

	offset := 6 + 16*len(entries)
	for _, e := range entries {
		w := byte(e.size)
		if e.size >= 256 {
			w = 0
		}
		out.WriteByte(w)                                   // width
		out.WriteByte(w)                                   // height
		out.WriteByte(0)                                   // palette
		out.WriteByte(0)                                   // reserved
		binary.Write(out, binary.LittleEndian, uint16(1))  // planes
		binary.Write(out, binary.LittleEndian, uint16(32)) // bpp
		binary.Write(out, binary.LittleEndian, uint32(len(e.data)))
		binary.Write(out, binary.LittleEndian, uint32(offset))
		offset += len(e.data)
	}
	for _, e := range entries {
		out.Write(e.data)
	}
	return out.Bytes(), nil
}

// iconFiles 记录生成出来的四套 ICO 路径。
type iconFiles struct {
	running string
	startA  string
	startB  string
	stopped string
}

// writeIconSet 生成四套多尺寸 ICO：运行中 / 启动中两帧 / 已停止。
func writeIconSet(dir string) (iconFiles, error) {
	var out iconFiles
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return out, err
	}

	build := func(spriteName string, badge color.RGBA) ([]byte, error) {
		sprite, err := loadEmbeddedSprite(spriteName)
		if err != nil {
			return nil, err
		}
		imgs := make([]*image.RGBA, 0, len(icoSizes))
		for _, s := range icoSizes {
			imgs = append(imgs, composeIcon(sprite, s, badge))
		}
		return encodeMultiICO(imgs)
	}

	specs := []struct {
		file   string
		sprite string
		badge  color.RGBA
		dst    *string
	}{
		{"running.ico", "running.png", badgeRunning, &out.running},
		{"starting-a.ico", "starting-a.png", badgeStarting, &out.startA},
		{"starting-b.ico", "starting-b.png", badgeStarting, &out.startB},
		{"stopped.ico", "stopped.png", badgeStopped, &out.stopped},
	}

	for _, sp := range specs {
		data, err := build(sp.sprite, sp.badge)
		if err != nil {
			return out, fmt.Errorf("%s: %w", sp.sprite, err)
		}
		p := filepath.Join(dir, sp.file)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return out, err
		}
		*sp.dst = p
	}
	return out, nil
}

// exportPreview 把指定状态合成后的图标导出成 PNG，便于人工核对长相。
func exportPreview(dir, name, spriteName string, badge color.RGBA, size int) (string, error) {
	sprite, err := loadEmbeddedSprite(spriteName)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := png.Encode(f, composeIcon(sprite, size, badge)); err != nil {
		return "", err
	}
	return p, nil
}

// composePlainIcon 只放角色、不加状态角标（exe 图标是静态的，不该有状态点）。
func composePlainIcon(sprite image.Image, size int) *image.RGBA {
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
	dst := image.Rect(x0, y0, x0+int(math.Round(targetW)), y0+int(math.Round(targetH)))
	xdraw.CatmullRom.Scale(canvas, dst, sprite, sb, xdraw.Over, nil)
	return canvas
}

// WriteAppICO 生成 exe 自带的应用程序图标（RT_GROUP_ICON）。
// 由 tools/genicon 在构建前调用，产物是 resources/app.ico。
func WriteAppICO(path string) error {
	sprite, err := loadEmbeddedSprite("running.png")
	if err != nil {
		return err
	}
	imgs := make([]*image.RGBA, 0, len(icoSizes))
	for _, s := range icoSizes {
		imgs = append(imgs, composePlainIcon(sprite, s))
	}
	data, err := encodeMultiICO(imgs)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o644)
}

// initIcons 生成并加载托盘图标（运行中 / 启动中两帧 / 已停止）。
func initIcons(dir string) error {
	files, err := writeIconSet(dir)
	if err != nil {
		return err
	}
	if iconRunning, err = walk.NewIconFromFile(files.running); err != nil {
		return err
	}
	if iconStartingA, err = walk.NewIconFromFile(files.startA); err != nil {
		return err
	}
	if iconStartingB, err = walk.NewIconFromFile(files.startB); err != nil {
		return err
	}
	if iconStopped, err = walk.NewIconFromFile(files.stopped); err != nil {
		return err
	}
	if iconRunning == nil || iconStartingA == nil || iconStartingB == nil || iconStopped == nil {
		return fmt.Errorf("托盘图标加载结果为空")
	}
	return nil
}

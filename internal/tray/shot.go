package tray

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lxn/walk"
)

// settingsShot 是隐藏的界面自检（--shot）：真实创建设置窗口，
// 在两个尺寸下各截一张图，然后关掉窗口、检查宿主窗口没有被显出来。
//
// 之所以要真截图：walk 的 BoxLayout 会把「不支持横向拉伸」的控件
// 按偏好宽度摆放（见 boxlayout.go 里的 s2 = prefSizes2[i]），
// 各分区的右边界对不对齐只能看图确认，靠读代码容易猜错。
//
// 结果写到 %LOCALAPPDATA%\dsh-tray\settingsshot.txt，图在 settingsshot-*.png。
func settingsShot() {
	out := &strings.Builder{}
	writeResult := func() {
		_ = os.MkdirAll(dataDir(), 0o755)
		_ = os.WriteFile(filepath.Join(dataDir(), "settingsshot.txt"), []byte(out.String()), 0o644)
	}
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(out, "PANIC: %v\r\n", r)
			writeResult()
		}
	}()

	loadConfig()
	dsh = NewDsh(logFilePath())

	mw0, err := walk.NewMainWindow()
	if err != nil {
		fmt.Fprintf(out, "NewMainWindow FAIL: %v\r\n", err)
		writeResult()
		return
	}
	mw = mw0
	_ = mw.SetLayout(walk.NewHBoxLayout())
	mw.SetVisible(false)
	guardMainWindow()

	if err := openSettings(mw); err != nil {
		fmt.Fprintf(out, "openSettings FAIL: %v\r\n", err)
		writeResult()
		return
	}
	fmt.Fprintf(out, "版本 = %s\r\n", appVersion)

	shot := func(name string) bool {
		s := currentSettings()
		if s == nil || s.dlg == nil {
			fmt.Fprintf(out, "%s SKIP: 没有设置窗口\r\n", name)
			return true
		}
		img, err := s.dlg.Screenshot()
		if err != nil {
			fmt.Fprintf(out, "%s FAIL: %v\r\n", name, err)
			return false
		}
		p := filepath.Join(dataDir(), name)
		f, err := os.Create(p)
		if err != nil {
			fmt.Fprintf(out, "%s FAIL: %v\r\n", name, err)
			return false
		}
		err = png.Encode(f, img)
		_ = f.Close()
		if err != nil {
			fmt.Fprintf(out, "%s FAIL: %v\r\n", name, err)
			return false
		}
		b := img.Bounds()
		fmt.Fprintf(out, "%s OK %dx%d -> %s\r\n", name, b.Dx(), b.Dy(), p)
		return true
	}

	// 刚 Resize 完立刻 BitBlt 会 GetDIBits failed（布局还没落地），重试几次即可
	capture := func(name string) {
		ch := make(chan bool, 1)
		for attempt := 0; attempt < 4; attempt++ {
			if attempt > 0 {
				time.Sleep(250 * time.Millisecond)
			}
			syncUI(func() { ch <- shot(name) })
			if <-ch {
				return
			}
		}
	}

	// 逐层列出控件实际占的矩形，用来核对左右边界是否一致（截图里透明控件的
	// 文字会被父窗口背景盖掉，光看图会误判）
	dump := func(title string) {
		fmt.Fprintf(out, "\r\n--- %s ---\r\n", title)
		s := currentSettings()
		if s == nil || s.dlg == nil {
			fmt.Fprintf(out, "(没有设置窗口)\r\n")
			return
		}
		var walkTree func(w walk.Widget, depth int)
		walkTree = func(w walk.Widget, depth int) {
			b := w.BoundsPixels()
			fmt.Fprintf(out, "%s%s rect=(%d,%d %dx%d) %s\r\n",
				strings.Repeat("  ", depth), widgetKind(w), b.X, b.Y, b.Width, b.Height, widgetText(w))
			if c, ok := w.(walk.Container); ok {
				children := c.Children()
				for i := 0; i < children.Len(); i++ {
					walkTree(children.At(i), depth+1)
				}
			}
		}
		// Dialog 本身不是 Widget（没有 Alignment 之类），所以从它的子控件开始列
		children := s.dlg.Children()
		for i := 0; i < children.Len(); i++ {
			walkTree(children.At(i), 0)
		}
	}

	go func() {
		time.Sleep(1000 * time.Millisecond)
		capture("settingsshot-default.png")

		// 放大一档，观察各分区随窗口变宽时的拉伸是否一致
		syncUI(func() {
			if s := currentSettings(); s != nil && s.dlg != nil {
				_ = s.dlg.SetSize(walk.Size{Width: 920, Height: 760})
			}
		})
		time.Sleep(700 * time.Millisecond)
		capture("settingsshot-wide.png")
		syncUI(func() { dump("放大后的控件矩形") })

		// 关掉设置窗口：宿主窗口不该被显出来（walk 的 WM_CLOSE 会对 owner
		// SetWindowPos(..., SWP_SHOWWINDOW)，是我们手动藏回去的）
		time.Sleep(300 * time.Millisecond)
		syncUI(func() {
			if s := currentSettings(); s != nil && s.dlg != nil {
				s.dlg.Close(0)
			}
		})
		time.Sleep(500 * time.Millisecond)
		syncUI(func() {
			fmt.Fprintf(out, "关闭后 settingsPane 为空 = %v\r\n", currentSettings() == nil)
			fmt.Fprintf(out, "关闭后宿主窗口可见 = %v（期望 false）\r\n", mw.Visible())
			writeResult()
			quitApp()
		})
	}()

	mw.Run()
}

func widgetKind(w walk.Widget) string {
	return strings.TrimPrefix(fmt.Sprintf("%T", w), "*walk.")
}

func widgetText(w walk.Widget) string {
	var s string
	switch v := w.(type) {
	case *walk.Label:
		s = v.Text()
	case *walk.PushButton:
		s = v.Text()
	case *walk.CheckBox:
		s = v.Text()
	case *walk.LineEdit:
		s = v.Text()
	case *walk.TextEdit:
		s = fmt.Sprintf("(%d 字)", len([]rune(v.Text())))
	case *walk.ProgressBar:
		s = fmt.Sprintf("(progress %d)", v.Value())
	}
	if r := []rune(s); len(r) > 40 {
		s = string(r[:40]) + "…"
	}
	return s
}

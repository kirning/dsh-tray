package tray

import (
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lxn/walk"
)

// uitest 真实创建托盘图标与菜单，3 秒后自动退出，用于验证 walk 初始化与三态切换。
func uitest() {
	res := &strings.Builder{}
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(res, "PANIC: %v\r\n", r)
		}
		_ = os.MkdirAll(dataDir(), 0o755)
		_ = os.WriteFile(filepath.Join(dataDir(), "uitest.txt"), []byte(res.String()), 0o644)
	}()

	loadConfig()
	dsh = NewDsh(logFilePath())
	res.WriteString(fmt.Sprintf("应用版本 = %s\r\n", appVersion))
	res.WriteString(fmt.Sprintf("启动后自动打开web = %v，启动参数 = %v\r\n", autoOpenWeb(), startArgs()))

	// 1) 图标生成（嵌入序列帧 → 多尺寸 ICO）
	if err := initIcons(dataDir()); err != nil {
		fmt.Fprintf(res, "initIcons FAIL: %v\r\n", err)
	} else {
		res.WriteString("initIcons OK (running / starting-a / starting-b / stopped)\r\n")
	}
	for _, v := range []struct {
		file, sprite string
		badge        color.RGBA
	}{
		{"preview-running.png", "running.png", badgeRunning},
		{"preview-starting-a.png", "starting-a.png", badgeStarting},
		{"preview-starting-b.png", "starting-b.png", badgeStarting},
		{"preview-stopped.png", "stopped.png", badgeStopped},
	} {
		if p, err := exportPreview(dataDir(), v.file, v.sprite, v.badge, 256); err == nil {
			fmt.Fprintf(res, "%s OK -> %s\r\n", v.file, p)
		} else {
			fmt.Fprintf(res, "%s FAIL: %v\r\n", v.file, err)
		}
	}

	// 2) 窗口 + 托盘 + 菜单
	var err error
	mw, err = walk.NewMainWindow()
	if err != nil {
		fmt.Fprintf(res, "NewMainWindow FAIL: %v\r\n", err)
		return
	}
	res.WriteString("NewMainWindow OK\r\n")
	mw.SetVisible(false)

	ni, err = walk.NewNotifyIcon(mw)
	if err != nil {
		fmt.Fprintf(res, "NewNotifyIcon FAIL: %v\r\n", err)
		return
	}
	res.WriteString("NewNotifyIcon OK\r\n")
	defer ni.Dispose()

	if err := buildMenu(ni.ContextMenu()); err != nil {
		fmt.Fprintf(res, "buildMenu FAIL: %v\r\n", err)
		return
	}
	res.WriteString(fmt.Sprintf("buildMenu OK, actions=%d\r\n", ni.ContextMenu().Actions().Len()))
	res.WriteString(fmt.Sprintf("版本菜单项 = %q，可用=%v（应为 true，点击检查本程序更新）\r\n",
		actVersion.Text(), actVersion.Enabled()))
	res.WriteString(fmt.Sprintf("配置项「%s」勾选=%v\r\n", actAutoOpen.Text(), actAutoOpen.Checked()))
	res.WriteString(fmt.Sprintf("退出项 = %q（只退托盘）；%q（连 dsh 一起关）\r\n",
		actExit.Text(), actExitAll.Text()))

	if err := ni.SetVisible(true); err != nil {
		fmt.Fprintf(res, "SetVisible FAIL: %v\r\n", err)
		return
	}
	res.WriteString("SetVisible(true) OK\r\n")

	// 3) 三态切换 + 启动中动画两帧（注意：不真的打开浏览器，只检查菜单可用性）
	updateState(stStarting)
	res.WriteString(fmt.Sprintf("stStarting: tip=%q 启动=%v 打开web=%v 关闭=%v\r\n",
		ni.ToolTip(), actStart.Enabled(), actOpen.Enabled(), actStop.Enabled()))
	applyStartingFrame()
	animPhase = true
	applyStartingFrame()
	res.WriteString("starting 动画两帧切换 OK\r\n")

	updateState(stRunning)
	res.WriteString(fmt.Sprintf("stRunning: tip=%q 启动=%v 打开web=%v 关闭=%v\r\n",
		ni.ToolTip(), actStart.Enabled(), actOpen.Enabled(), actStop.Enabled()))

	updateState(stStopped)
	res.WriteString(fmt.Sprintf("stStopped: tip=%q 启动=%v 打开web=%v 关闭=%v\r\n",
		ni.ToolTip(), actStart.Enabled(), actOpen.Enabled(), actStop.Enabled()))

	// 打开web 的地址拼装（不发请求，只确认 URL 正确）
	res.WriteString(fmt.Sprintf("打开web 目标地址 = http://127.0.0.1:%d\r\n", dsh.Port()))

	// 4) 就绪判定：日志标志识别（用当前日志做一次真实扫描）
	if cur, err := CurrentVersion(); err == nil {
		res.WriteString(fmt.Sprintf("当前 dsh 版本 = %s\r\n", cur))
	}
	res.WriteString(fmt.Sprintf("日志里有就绪标志(全文件) = %v\r\n", logHasReadyMarker(logFilePath(), 0)))
	res.WriteString(fmt.Sprintf("当前实时状态 = %d (0=停止 1=启动中 2=运行中)\r\n", int(currentState())))

	go func() {
		time.Sleep(3 * time.Second)
		syncUI(func() {
			_ = ni.SetVisible(false)
			mw.Close()
		})
	}()

	mw.Run()
	res.WriteString("message loop exited cleanly\r\n")
}

package tray

import (
	"fmt"

	"os"
	"path/filepath"
	"strings"

	"time"

	"github.com/lxn/walk"
	"golang.org/x/sys/windows"
)

const appName = "dsh 托盘"
const pkgName = "@deepseek-ai/dsh"

// appVersion 应用自身版本（与被管理的 dsh 版本无关）。
// 用 var 而非 const，便于构建时用 -ldflags "-X main.appVersion=x.y.z" 覆盖。
var appVersion = "1.3.0"

var (
	dsh *Dsh
	ni  *walk.NotifyIcon
	mw  *walk.MainWindow

	actStart    *walk.Action
	actRestart  *walk.Action
	actOpen     *walk.Action
	actLog      *walk.Action
	actStop     *walk.Action
	actUpdate   *walk.Action
	actAuto     *walk.Action
	actAutoOpen *walk.Action
	actExit     *walk.Action
	actExitAll  *walk.Action
	actVersion  *walk.Action

	// 双击托盘的判定：两次左键抬起间隔小于该阈值算双击
	lastLeftClick time.Time

	iconRunning   *walk.Icon
	iconStartingA *walk.Icon
	iconStartingB *walk.Icon
	iconStopped   *walk.Icon

	singleMutex windows.Handle
)

// runState 三态：未运行 / 启动中 / 已就绪
type runState int

const (
	stStopped runState = iota
	stStarting
	stRunning
)

var (
	curState  = stStopped
	animPhase bool
)

// doubleClickWindow 双击托盘的判定阈值。
// walk 的 NotifyIcon 只暴露 MouseUp（单击与双击都会触发），
// 所以要自己按时间间隔判断"两次点击 = 双击"。单击本身没有动作，延迟判定无副作用。
const doubleClickWindow = 450 * time.Millisecond

func dataDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "dsh-tray")
}

func logFilePath() string {
	return filepath.Join(dataDir(), "dsh-web.txt")
}

func markerPath() string {
	return filepath.Join(dataDir(), "installed.flag")
}

// ensureSingleInstance 用命名互斥体保证只跑一个托盘实例。
func ensureSingleInstance() bool {
	name, _ := windows.UTF16PtrFromString(`Local\dsh-tray-single-instance`)
	h, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		if err == windows.ERROR_ALREADY_EXISTS {
			return false
		}
		// 其他错误不阻塞启动
		return true
	}
	singleMutex = h
	return true
}

// Run 是程序入口。args 为命令行参数（不含程序名）。
// 支持隐藏自检参数：--selftest / --uitest / --starttest。
func Run(args []string) {
	for _, a := range args {
		if a == "--selftest" {
			selftest()
			return
		}
		if a == "--uitest" {
			uitest()
			return
		}
		if a == "--starttest" {
			starttest()
			return
		}
	}

	if !ensureSingleInstance() {
		return
	}

	dir := dataDir()
	_ = os.MkdirAll(dir, 0o755)

	loadConfig() // 读「启动后自动打开web」等设置

	dsh = NewDsh(logFilePath())

	// 首次运行自动登记开机自启；之后尊重用户在菜单里的选择。
	// 已开启时每次启动重写一次，确保记录的是当前 exe 路径（程序可能被挪目录）。
	if _, err := os.Stat(markerPath()); err != nil {
		_ = setAutoStart(true)
		_ = os.WriteFile(markerPath(), []byte("1"), 0o644)
	} else if autoStartEnabled() {
		_ = setAutoStart(true)
	}

	if err := initIcons(dir); err != nil {
		return
	}
	var err error

	// 只认"本次启动之后"的日志里出现的就绪标志，避免把历史日志当成本次就绪
	dsh.SetReadyBaseline()

	mw, err = walk.NewMainWindow()
	if err != nil {
		return
	}
	mw.SetVisible(false)
	mw.SetTitle(appName)

	ni, err = walk.NewNotifyIcon(mw)
	if err != nil {
		return
	}
	defer ni.Dispose()

	// 托盘图标双击：未运行→启动，已就绪→打开 web，启动中不动
	ni.MouseUp().Attach(func(x, y int, button walk.MouseButton) {
		if button != walk.LeftButton {
			return
		}
		now := time.Now()
		if isDoubleClick(lastLeftClick, now) {
			lastLeftClick = time.Time{}
			doTrayDoubleClick()
			return
		}
		lastLeftClick = now
	})

	updateState(currentState())

	if err := buildMenu(ni.ContextMenu()); err != nil {
		return
	}
	if err := ni.SetVisible(true); err != nil {
		return
	}

	go monitor()
	go animate()

	mw.Run()
}

func newAction(text string, fn func()) *walk.Action {
	a := walk.NewAction()
	a.SetText(text)
	if fn != nil {
		a.Triggered().Attach(fn)
	}
	return a
}

func buildMenu(menu *walk.Menu) error {
	actStart = newAction("启动", func() { doStart() })
	menu.Actions().Add(actStart)

	actRestart = newAction("重启", func() { doRestart() })
	menu.Actions().Add(actRestart)

	actOpen = newAction("打开web", func() { doOpenWeb() })
	menu.Actions().Add(actOpen)

	actLog = newAction("查看日志", func() { doViewLog() })
	menu.Actions().Add(actLog)

	actStop = newAction("关闭", func() { doStop() })
	menu.Actions().Add(actStop)

	menu.Actions().Add(walk.NewSeparatorAction())

	actUpdate = newAction("检查更新", func() { doCheckUpdate() })
	menu.Actions().Add(actUpdate)

	actAuto = newAction("开机自启", func() { doToggleAutoStart() })
	actAuto.SetCheckable(true)
	actAuto.SetChecked(autoStartEnabled())
	menu.Actions().Add(actAuto)

	actAutoOpen = newAction("启动后自动打开web", func() { doToggleAutoOpenWeb() })
	actAutoOpen.SetCheckable(true)
	actAutoOpen.SetChecked(autoOpenWeb())
	menu.Actions().Add(actAutoOpen)

	menu.Actions().Add(walk.NewSeparatorAction())

	// 退出：只关掉本程序，dsh 服务继续跑
	actExit = newAction("退出", func() { doExit() })
	menu.Actions().Add(actExit)

	// 完全退出：先关 dsh 服务，再退出本程序
	actExitAll = newAction("完全退出", func() { doExitAll() })
	menu.Actions().Add(actExitAll)

	// 最后一项：版本号，纯展示，不可点击
	menu.Actions().Add(walk.NewSeparatorAction())
	actVersion = newAction("版本 "+appVersion, nil)
	actVersion.SetEnabled(false)
	menu.Actions().Add(actVersion)

	return nil
}

// monitor 每秒探测一次状态，刷新图标与菜单可用性。
func monitor() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s := currentState()
		syncUI(func() { updateState(s) })
	}
}

// currentState 结合"进程是否存活"和"是否已就绪"得出三态。
func currentState() runState {
	if !dsh.IsRunning() {
		dsh.ClearReady()
		return stStopped
	}
	if dsh.CheckReady() {
		return stRunning
	}
	return stStarting
}

// syncUI 把 UI 操作切回消息循环线程。
func syncUI(f func()) {
	if mw == nil {
		return
	}
	defer func() { _ = recover() }()
	mw.Synchronize(f)
}

func updateState(s runState) {
	prev := curState
	curState = s

	if actStart != nil {
		actStart.SetEnabled(s == stStopped)
	}
	if actStop != nil {
		actStop.SetEnabled(s != stStopped)
	}
	if actRestart != nil {
		actRestart.SetEnabled(true)
	}
	// 只有真正就绪（不是"启动中"）才允许打开 web
	if actOpen != nil {
		actOpen.SetEnabled(s == stRunning)
	}

	// 从"启动中"掉回"未运行" = 启动失败，别静默，把日志里的原因报出来
	if prev == stStarting && s == stStopped {
		go reportStartFailure()
	}

	if ni == nil {
		return
	}

	switch s {
	case stStopped:
		ni.SetToolTip(appName + " · 未运行")
	case stStarting:
		ni.SetToolTip(appName + " · 启动中…（等待 dsh web 就绪）")
	default:
		ni.SetToolTip(fmt.Sprintf("%s · 运行中 127.0.0.1:%d", appName, dshPort))
	}

	// 启动中由 animate 协程负责在两帧之间切换，这里只在刚进入该状态时定一下首帧
	if s == stStarting {
		if prev != stStarting {
			animPhase = false
			applyStartingFrame()
		}
		return
	}

	if s == stRunning {
		if iconRunning != nil {
			ni.SetIcon(iconRunning)
		}
	} else if iconStopped != nil {
		ni.SetIcon(iconStopped)
	}
}

// applyStartingFrame 按当前动画相位设置启动中图标。
func applyStartingFrame() {
	if ni == nil {
		return
	}
	ic := iconStartingA
	if animPhase {
		ic = iconStartingB
	}
	if ic != nil {
		ni.SetIcon(ic)
	}
}

// animate 在"启动中"状态下交替两帧图标，形成动图效果。
func animate() {
	const interval = 420 * time.Millisecond
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		syncUI(func() {
			if curState != stStarting {
				return
			}
			animPhase = !animPhase
			applyStartingFrame()
		})
	}
}

func doStart() {
	if dsh.IsRunning() {
		syncUI(func() { updateState(currentState()) })
		return
	}
	// 立刻切到"启动中"，让用户看到反馈，而不是等进程起来
	syncUI(func() { updateState(stStarting) })
	go func() {
		if err := dsh.Start(); err != nil {
			showError("启动失败", err)
			syncUI(func() { updateState(currentState()) })
			return
		}
		// 之后就交给 monitor：日志出现就绪标志/端口起来后自动转"运行中"
	}()
}

func doRestart() {
	syncUI(func() { updateState(stStarting) })
	go func() {
		if dsh.IsRunning() {
			_ = dsh.Stop()
			time.Sleep(400 * time.Millisecond)
		}
		if err := dsh.Start(); err != nil {
			showError("重启失败", err)
			syncUI(func() { updateState(currentState()) })
		}
	}()
}

func doStop() {
	if !dsh.IsRunning() {
		syncUI(func() { updateState(currentState()) })
		return
	}
	go func() {
		if err := dsh.Stop(); err != nil {
			showError("关闭失败", err)
		}
		syncUI(func() { updateState(currentState()) })
	}()
}

// doOpenWeb 用默认浏览器打开 dsh web 地址（仅"已就绪"状态可用）。
func doOpenWeb() {
	if curState != stRunning {
		return
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", dsh.Port())
	if err := shellOpen(url); err != nil {
		showError("打开浏览器失败", err)
	}
}

func doViewLog() {
	p := logFilePath()
	if _, err := os.Stat(p); err != nil {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("(还没有日志 —— dsh web 尚未通过本程序启动过)\r\n"), 0o644)
	}
	if err := shellOpen(p); err != nil {
		showError("打开文件失败", err)
	}
}

// shellOpen 用系统默认程序打开路径或 URL。
func shellOpen(target string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	t, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, t, nil, nil, 1)
}

func doCheckUpdate() {
	if actUpdate != nil {
		actUpdate.SetEnabled(false)
	}
	go func() {
		defer syncUI(func() {
			if actUpdate != nil {
				actUpdate.SetEnabled(true)
			}
		})

		current, newer, err := CheckUpdates(pkgName)

		var msg string
		switch {
		case err != nil:
			if current != "" {
				msg = fmt.Sprintf("当前版本: %s\r\n\r\n检查更新失败:\r\n%v", current, err)
			} else {
				msg = fmt.Sprintf("检查更新失败:\r\n%v", err)
			}
		case len(newer) == 0:
			msg = fmt.Sprintf("当前版本: %s\r\n\r\n已是最新版本，没有更高的版本。", current)
		default:
			list := newer
			extra := ""
			if len(list) > 25 {
				extra = fmt.Sprintf("\r\n… 另有 %d 个未列出", len(list)-25)
				list = list[len(list)-25:]
			}
			msg = fmt.Sprintf("当前版本: %s\r\n\r\n发现 %d 个更高的版本:\r\n%s%s\r\n\r\n(仅检查，不会自动更新)",
				current, len(newer), strings.Join(list, "\r\n"), extra)
		}

		syncUI(func() {
			walk.MsgBox(mw, "检查更新", msg, walk.MsgBoxIconInformation)
		})
	}()
}

// reportStartFailure 把"启动中→未运行"的失败原因从日志里捞出来提示用户。
func reportStartFailure() {
	msg := lastStartupFailure(logFilePath())
	if msg == "" {
		return
	}
	syncUI(func() {
		walk.MsgBox(mw, appName+" - 启动失败", msg+"\r\n\r\n完整日志：菜单 → 查看日志", walk.MsgBoxIconError)
	})
}

func doToggleAutoStart() {
	want := !autoStartEnabled()
	if err := setAutoStart(want); err != nil {
		showError("设置开机自启失败", err)
	}
	if actAuto != nil {
		actAuto.SetChecked(autoStartEnabled())
	}
}

// doToggleAutoOpenWeb 切换"启动后自动打开web"（即是否给 dsh 传 --no-open）。
func doToggleAutoOpenWeb() {
	want := !autoOpenWeb()
	if err := setAutoOpenWeb(want); err != nil {
		showError("保存配置失败", err)
	}
	if actAutoOpen != nil {
		actAutoOpen.SetChecked(autoOpenWeb())
	}
}

// isDoubleClick 判断"本次点击"是否构成双击（prev 为上一次左键抬起时间）。
// 抽成函数便于单测；单击没有绑定动作，所以不需要延迟等待就能判定。
func isDoubleClick(prev, now time.Time) bool {
	return !prev.IsZero() && now.Sub(prev) <= doubleClickWindow
}

// doTrayDoubleClick 托盘图标双击：未运行则启动，已就绪则打开 web，启动中忽略。
func doTrayDoubleClick() {
	switch curState {
	case stStopped:
		doStart()
	case stRunning:
		doOpenWeb()
	}
}

// doExit 只退出托盘程序，不动 dsh 服务（dsh 继续在后台提供 web）。
func doExit() {
	quitApp()
}

// doExitAll 先关闭 dsh 服务，再退出托盘程序。
func doExitAll() {
	go func() {
		dsh.Shutdown()
		syncUI(quitApp)
	}()
}

// quitApp 收起托盘图标并结束进程。
func quitApp() {
	if ni != nil {
		_ = ni.SetVisible(false)
		ni.Dispose()
	}
	os.Exit(0)
}

func showError(title string, err error) {
	msg := err.Error()
	syncUI(func() {
		walk.MsgBox(mw, appName+" - "+title, msg, walk.MsgBoxIconError)
	})
}

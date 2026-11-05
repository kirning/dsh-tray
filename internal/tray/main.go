package tray

import (
	"fmt"
	"os"
	"os/exec"
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
var appVersion = "1.5.0"

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
	actSettings *walk.Action
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
		if a == "--checkupdate" {
			checkUpdateReport()
			return
		}
		if a == "--settingstest" {
			settingstest()
			return
		}
	}

	if !ensureSingleInstance() {
		return
	}

	// 上次自更新可能留下 dsh-tray.old.exe，启动时顺手清掉
	cleanupOldBinary()

	dir := dataDir()
	_ = os.MkdirAll(dir, 0o755)

	loadConfig() // 读「启动后自动打开web」、代理等设置

	trayLogf("程序启动，版本 %s", appVersion)
	dsh = NewDsh(logFilePath())

	// 首次运行自动登记开机自启；之后尊重用户在菜单里的选择。
	// 已开启时每次启动重写一次，确保记录的是当前 exe 路径（程序可能被挪目录）。
	if _, err := os.Stat(markerPath()); err != nil {
		_ = setAutoStart(true)
		_ = os.WriteFile(markerPath(), []byte("1"), 0o644)
	} else if autoStartEnabled() {
		_ = setAutoStart(true)
	}
	// 把注册表里真实的状态同步一份到 config.json（副本，不反过来覆盖注册表）
	syncAutoStartMirror()

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
	// 裸 NewMainWindow 没有 Layout。弹出「设置」窗口时主窗口会收到
	// WM_WINDOWPOSCHANGED + SWP_SHOWWINDOW，walk 会去做布局，
	// 没有 Layout → ContainerBase.CreateLayoutItem 空指针崩溃。给个空布局即可。
	_ = mw.SetLayout(walk.NewHBoxLayout())
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

	actUpdate = newAction("检查dsh更新", func() { doCheckUpdate() })
	menu.Actions().Add(actUpdate)

	actAuto = newAction("开机自启", func() { doToggleAutoStart() })
	actAuto.SetCheckable(true)
	actAuto.SetChecked(autoStartEnabled())
	menu.Actions().Add(actAuto)

	actAutoOpen = newAction("启动后自动打开web", func() { doToggleAutoOpenWeb() })
	actAutoOpen.SetCheckable(true)
	actAutoOpen.SetChecked(autoOpenWeb())
	menu.Actions().Add(actAutoOpen)

	actSettings = newAction("设置…", func() { doOpenSettings() })
	menu.Actions().Add(actSettings)

	menu.Actions().Add(walk.NewSeparatorAction())

	// 退出：只关掉本程序，dsh 服务继续跑
	actExit = newAction("退出", func() { doExit() })
	menu.Actions().Add(actExit)

	// 完全退出：先关 dsh 服务，再退出本程序
	actExitAll = newAction("完全退出", func() { doExitAll() })
	menu.Actions().Add(actExitAll)

	// 最后一项：版本号，点击检查本程序更新（从 GitHub Release 拉取）
	menu.Actions().Add(walk.NewSeparatorAction())
	actVersion = newAction("版本 "+appVersion+"（点击检查更新）", func() { doCheckAppUpdate() })
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
		_ = os.WriteFile(p, []byte(formatTrayLog("还没有日志 —— dsh web 尚未通过本程序启动过", time.Now())), 0o644)
	}
	if err := shellOpen(p); err != nil {
		showError("打开文件失败", err)
	}
}

// doOpenSettings 打开设置窗口（非模态，主消息循环继续跑，后台更新才能刷新进度）。
func doOpenSettings() {
	if err := openSettings(mw); err != nil {
		trayLogf("打开设置窗口失败：%v", err)
		showError("打开设置失败", err)
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

		trayLogf("检查dsh更新：开始")
		current, newer, err := CheckUpdates(pkgName)
		if err != nil {
			trayLogf("检查dsh更新失败：%v", err)
		} else {
			trayLogf("检查dsh更新完成：当前 %s，更高版本 %d 个", current, len(newer))
		}

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

// msgBox 在 UI 线程弹提示（不等待用户操作）。
func msgBox(title, text string, style walk.MsgBoxStyle) {
	syncUI(func() {
		walk.MsgBox(mw, title, text, style)
	})
}

// askYesNo 在 UI 线程弹确认框并等待结果。
// 注意：walk 的 Synchronize 用 PostMessage 异步投递（不会阻塞等待），
// 所以必须靠 channel 把结果回传给后台 goroutine。
func askYesNo(title, text string) bool {
	ch := make(chan bool, 1)
	syncUI(func() {
		r := walk.MsgBox(mw, title, text, walk.MsgBoxYesNo|walk.MsgBoxIconQuestion)
		ch <- r == walk.DlgCmdYes
	})
	return <-ch
}

// doCheckAppUpdate 检查本程序自身更新：查 GitHub Release → 下载 → 替换 → 重启。
// dsh 服务是独立进程，更新托盘程序不会打断它。
func doCheckAppUpdate() {
	if actVersion != nil {
		actVersion.SetEnabled(false)
	}
	go func() {
		defer syncUI(func() {
			if actVersion != nil {
				actVersion.SetEnabled(true)
			}
		})

		tag, newer, asset, err := checkAppUpdate()
		if err != nil {
			msgBox(appName+" - 检查更新", "检查更新失败：\r\n\r\n"+err.Error(), walk.MsgBoxIconError)
			return
		}
		if !newer {
			msgBox(appName+" - 检查更新",
				fmt.Sprintf("当前版本：%s\r\n最新版本：%s\r\n\r\n已是最新版本。", appVersion, tag),
				walk.MsgBoxIconInformation)
			return
		}

		if !askYesNo(appName+" - 发现新版本",
			fmt.Sprintf("当前版本：%s\r\n最新版本：%s\r\n\r\n是否立即下载并更新？\r\n\r\n更新过程：下载 → 替换本程序 → 自动重启。\r\ndsh 服务不会中断。",
				appVersion, tag)) {
			return
		}

		if err := applyUpdate(asset); err != nil {
			msgBox(appName+" - 更新失败", err.Error(), walk.MsgBoxIconError)
			return
		}

		// 必须等用户点确定再重启 —— msgBox 是异步投递，直接用会立刻把窗口关掉
		showMsgBoxAndWait(appName+" - 更新完成",
			fmt.Sprintf("已更新到 %s。\r\n\r\n点击「确定」后自动重启本程序。\r\ndsh 服务会继续运行。", tag),
			walk.MsgBoxIconInformation)

		trayLogf("应用已更新到 %s，准备重启", tag)
		syncUI(func() {
			restartSelf()
			quitApp()
		})
	}()
}

// restartSelf 启动更新后的新进程。
// 必须先释放单实例互斥体，否则新进程启动时会判定"已有实例"而立即退出。
func restartSelf() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if exe, err = filepath.Abs(exe); err != nil {
		return
	}
	if singleMutex != 0 {
		_ = windows.CloseHandle(singleMutex)
		singleMutex = 0
	}
	cmd := exec.Command(exe)
	cmd.Dir = filepath.Dir(exe)
	_ = cmd.Start()
}

// reportStartFailure 把"启动中→未运行"的失败原因从日志里捞出来提示用户。
func reportStartFailure() {
	msg := lastStartupFailure(logFilePath())
	if msg == "" {
		trayLogf("启动失败：dsh 进程已退出，但日志里没有明确的 startup failed 原因")
		return
	}
	trayLogf("启动失败原因（摘自日志）：\n%s", msg)
	syncUI(func() {
		walk.MsgBox(mw, appName+" - 启动失败", msg+"\r\n\r\n完整日志：菜单 → 查看日志", walk.MsgBoxIconError)
	})
}

func doToggleAutoStart() {
	want := !autoStartEnabled()
	if err := setAutoStart(want); err != nil {
		showError("设置开机自启失败", err)
	}
	syncAutoStartMirror() // 同步一份到 config.json
	trayLogf("菜单：开机自启 = %v", want)
	syncSettingsToggles()
}

// doToggleAutoOpenWeb 切换"启动后自动打开web"（即是否给 dsh 传 --no-open）。
func doToggleAutoOpenWeb() {
	want := !autoOpenWeb()
	if err := setAutoOpenWeb(want); err != nil {
		showError("保存配置失败", err)
	}
	trayLogf("菜单：启动后自动打开web = %v（启动参数 %v）", want, startArgs())
	syncSettingsToggles()
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
	trayLogf("退出（dsh 服务保持运行）")
	quitApp()
}

// doExitAll 先关闭 dsh 服务，再退出托盘程序。
func doExitAll() {
	trayLogf("完全退出（同时关闭 dsh 服务）")
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
	trayLogf("%s：%s", title, msg)
	syncUI(func() {
		walk.MsgBox(mw, appName+" - "+title, msg, walk.MsgBoxIconError)
	})
}

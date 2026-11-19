package app

import (
	"fmt"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/kirning/dsh-tray/internal/core"
	"github.com/kirning/dsh-tray/internal/platform"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func platformName() string { return runtime.GOOS }

// currentState 结合“进程是否存活”和“是否已就绪”得出三态。
func currentState(d *core.Dsh) core.State {
	if !d.IsRunning() {
		d.ClearReady()
		return core.StateStopped
	}
	if d.CheckReady() {
		return core.StateRunning
	}
	return core.StateStarting
}

// CurrentState 导出给 main 的诊断参数使用。
func CurrentState(d *core.Dsh) core.State { return currentState(d) }

// Tray 管理托盘图标、右键菜单与 1 秒轮询。
type Tray struct {
	app  *application.App
	tray *application.SystemTray
	svc  *Service
	dsh  *core.Dsh

	actStart    *application.MenuItem
	actRestart  *application.MenuItem
	actOpen     *application.MenuItem
	actLog      *application.MenuItem
	actStop     *application.MenuItem
	actUpdate   *application.MenuItem
	actAuto     *application.MenuItem
	actAutoOpen *application.MenuItem
	actSettings *application.MenuItem
	actExit     *application.MenuItem
	actExitAll  *application.MenuItem
	actVersion  *application.MenuItem

	state core.State
	frame int
	done  chan struct{}

	// upgrading 为 true 时托盘显示“进行中”动画，让用户知道升级还没结束
	upgrading atomic.Bool
}

func NewTray(app *application.App, dsh *core.Dsh, svc *Service) *Tray {
	return &Tray{app: app, dsh: dsh, svc: svc, done: make(chan struct{})}
}

// Start 建立菜单与图标，并启动轮询/动画。
func (t *Tray) Start() {
	t.tray = t.app.SystemTray.New()
	t.tray.SetTooltip("dsh 托盘")

	menu := application.NewMenu()

	t.actStart = menu.Add("启动")
	t.actStart.OnClick(func(*application.Context) { t.guard(t.svc.StartDsh) })

	t.actRestart = menu.Add("重启")
	t.actRestart.OnClick(func(*application.Context) { t.guard(t.svc.RestartDsh) })

	t.actOpen = menu.Add("打开 web")
	t.actOpen.OnClick(func(*application.Context) { t.guard(t.svc.OpenWeb) })

	t.actLog = menu.Add("查看日志")
	t.actLog.OnClick(func(*application.Context) { t.guard(t.svc.OpenLogFile) })

	t.actStop = menu.Add("关闭")
	t.actStop.OnClick(func(*application.Context) { t.guard(t.svc.StopDsh) })

	menu.AddSeparator()

	t.actUpdate = menu.Add("检查 dsh 更新")
	t.actUpdate.OnClick(func(*application.Context) { go t.checkDshUpdate() })

	t.actAuto = menu.AddCheckbox("开机自启", autostart())
	t.actAuto.OnClick(func(*application.Context) {
		_ = t.svc.SetAutostart(!autostart())
		t.refresh(true)
	})

	t.actAutoOpen = menu.AddCheckbox("启动后自动打开 web", core.AutoOpenWeb())
	t.actAutoOpen.OnClick(func(*application.Context) {
		_ = core.UpdateConfig(func(c *core.Config) { c.AutoOpenWeb = !c.AutoOpenWeb })
		t.actAutoOpen.SetChecked(core.AutoOpenWeb())
		t.svc.notify()
	})

	t.actSettings = menu.Add("设置…")
	t.actSettings.OnClick(func(*application.Context) { ShowSettings(t.app) })

	menu.AddSeparator()

	t.actExit = menu.Add("退出（dsh 继续运行）")
	t.actExit.OnClick(func(*application.Context) { Quit() })

	t.actExitAll = menu.Add("完全退出（同时关闭 dsh）")
	t.actExitAll.OnClick(func(*application.Context) {
		go func() {
			_ = t.dsh.Stop()
			Quit()
		}()
	})

	menu.AddSeparator()
	t.actVersion = menu.Add("版本 " + core.AppVersion + "（点击检查更新）")
	t.actVersion.OnClick(func(*application.Context) {
		ShowSettings(t.app)
	})

	t.tray.SetMenu(menu)
	t.tray.OnDoubleClick(func() {
		switch currentState(t.dsh) {
		case core.StateStopped:
			t.guard(t.svc.StartDsh)
		case core.StateRunning:
			t.guard(t.svc.OpenWeb)
		}
	})

	t.refresh(true)
	go t.loop()
	go t.animate()
}

func autostart() bool {
	return platform.AutostartEnabled()
}

func (t *Tray) guard(fn func() error) {
	go func() {
		if err := fn(); err != nil {
			core.Logf("操作失败：%v", err)
		}
		t.refresh(true)
	}()
}

// loop 每秒刷新状态与菜单可用性。
func (t *Tray) loop() {
	tk := time.NewTicker(time.Second)
	defer tk.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-tk.C:
			t.refresh(false)
		}
	}
}

// animate 在“启动中”状态交替两帧图标。
func (t *Tray) animate() {
	tk := time.NewTicker(420 * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-tk.C:
			if t.state == core.StateStarting || t.upgrading.Load() {
				t.frame++
				t.applyIcon()
			}
		}
	}
}

func (t *Tray) refresh(force bool) {
	s := currentState(t.dsh)
	changed := s != t.state
	t.state = s
	if changed || force {
		t.applyIcon()
		t.applyMenu()
	}
}

func (t *Tray) applyIcon() {
	state := t.state
	upgrading := t.upgrading.Load()
	if upgrading {
		// 升级中用紫色角标的两帧动画，和“启动中”的橙色明确区分
		state = core.StateUpgrading
	}
	data, err := core.TrayIcon(state, t.frame)
	if err != nil {
		core.Logf("生成托盘图标失败：%v", err)
		return
	}
	t.tray.SetIcon(data)
	// 升级时 tooltip 由 upgradeDsh 单独设置，这里不要覆盖
	if !upgrading {
		t.tray.SetTooltip(fmt.Sprintf("dsh 托盘 · %s", stateLabel(t.state)))
	}
}

func stateLabel(s core.State) string {
	switch s {
	case core.StateRunning:
		return "已就绪"
	case core.StateStarting:
		return "启动中"
	case core.StateUpgrading:
		return "升级中"
	default:
		return "未运行"
	}
}

func (t *Tray) applyMenu() {
	t.actStart.SetEnabled(t.state == core.StateStopped)
	t.actRestart.SetEnabled(true)
	t.actOpen.SetEnabled(t.state == core.StateRunning)
	t.actStop.SetEnabled(t.state != core.StateStopped)
	t.actAuto.SetChecked(autostart())
	t.actAutoOpen.SetChecked(core.AutoOpenWeb())
	t.actVersion.SetLabel("版本 " + core.AppVersion + "（点击检查更新）")
	// 升级过程中不允许再次触发检查/升级
	t.actUpdate.SetEnabled(!t.upgrading.Load())
}

// checkDshUpdate 查 npm；有新版本时弹框并带上「升级」按钮。
func (t *Tray) checkDshUpdate() {
	info, err := t.svc.CheckDshUpdate()
	switch {
	case err != nil:
		t.app.Dialog.Error().SetTitle("检查 dsh 更新").
			SetMessage("检查更新失败：\n" + err.Error()).Show()
	case !info.HasUpdate:
		t.app.Dialog.Info().SetTitle("检查 dsh 更新").
			SetMessage(fmt.Sprintf("已是最新版本 %s", info.Current)).Show()
	default:
		// 必须用 Question 而不是 Info：Wails 在 Windows 上只有
		// QuestionDialogType + 自定义按钮才会走 TaskDialog；
		// Info 走 MessageBox，而 MessageBox 不支持自定义按钮文案（只会显示一个「确定」）。
		dlg := t.app.Dialog.Question().
			SetTitle("检查 dsh 更新").
			SetMessage(fmt.Sprintf(
				"发现新版本 %s（当前 %s）。\n\n"+
					"升级会先停止 dsh，再按当前安装方式执行全局安装命令，完成后自动重启（约 1~2 分钟）。",
				info.Latest, info.Current))
		upgrade := dlg.AddButton("升级到 " + info.Latest)
		upgrade.OnClick(func() { go t.upgradeDsh(info.Latest) })
		cancel := dlg.AddButton("暂不升级")
		cancel.OnClick(func() { core.Logf("用户选择暂不升级到 %s", info.Latest) })
		dlg.SetDefaultButton(upgrade)
		dlg.SetCancelButton(cancel)
		dlg.Show()
	}
}

// upgradeDsh 后台执行升级，完成后弹结果。
// 必须放 goroutine：npm/mise 安装要几十秒到几分钟，不能卡住托盘消息循环。
func (t *Tray) upgradeDsh(version string) {
	t.upgrading.Store(true)
	t.tray.SetTooltip("dsh 托盘 · 正在升级到 " + version + "…")
	t.applyIcon()
	t.applyMenu()
	defer func() {
		t.upgrading.Store(false)
		t.refresh(true)
	}()

	msg, err := t.svc.UpgradeDsh(version)
	if err != nil {
		detail := err.Error()
		if msg != "" {
			detail += "\n\n命令输出（末尾）：\n" + msg
		}
		t.app.Dialog.Error().SetTitle("升级 dsh 失败").SetMessage(detail).Show()
	} else {
		t.app.Dialog.Info().SetTitle("升级 dsh 完成").SetMessage(msg).Show()
	}
}

package app

import "github.com/wailsapp/wails/v3/pkg/application"

// SettingsWindowName 是设置窗口在 Wails 里的注册名。
const SettingsWindowName = "settings"

// settingsWindowOptions 是设置窗口的创建参数。
// hidden=true 用于启动时静默预创建，避免开机闪一下窗口。
func settingsWindowOptions(hidden bool) application.WebviewWindowOptions {
	return application.WebviewWindowOptions{
		Name:             SettingsWindowName,
		Title:            "设置 - dsh 托盘",
		Width:            860,
		Height:           900,
		MinWidth:         720,
		MinHeight:        640,
		Hidden:           hidden,
		URL:              "/",
		BackgroundColour: application.NewRGB(250, 249, 246),
	}
}

// EnsureSettingsWindow 返回设置窗口；被关闭过就按同样参数重建。
//
// 必须支持重建：Windows 上 Wails 关窗会真的销毁并移除窗口对象
// （见 webview_window.go 的默认 WindowClosing 处理器），
// 而我们又用 DisableQuitOnLastWindowClosed 阻止了“关窗即退出”，
// 所以下次点“设置”时窗口已经不在了，只能重建。
func EnsureSettingsWindow(a *application.App, hidden bool) application.Window {
	if w, ok := a.Window.GetByName(SettingsWindowName); ok {
		return w
	}
	return a.Window.NewWithOptions(settingsWindowOptions(hidden))
}

// ShowSettings 显示（必要时先重建）设置窗口。
func ShowSettings(a *application.App) {
	w := EnsureSettingsWindow(a, true)
	w.Show()
}

// SetEmitter 供 main 注入事件发送器。
// 刻意做成包级函数而不是 Service 的方法：导出方法带 func 参数会让 Wails
// 绑定生成器报 “function types are not supported by encoding/json”。
func SetEmitter(s *Service, fn func(string, any)) { s.setEmitter(fn) }

// SetRestarter 供 main 注入“重启自身”的实现。
func SetRestarter(s *Service, fn func()) { s.setRestarter(fn) }

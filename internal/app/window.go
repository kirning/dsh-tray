package app

import "github.com/wailsapp/wails/v3/pkg/application"

// SettingsWindowName 是设置窗口在 Wails 里的注册名。
const SettingsWindowName = "settings"

// ShowSettings 显示（或唤起）设置窗口。
// 窗口是 Hidden 创建的，关闭只是隐藏，程序继续常驻托盘。
func ShowSettings(a *application.App) {
	if w, ok := a.Window.GetByName(SettingsWindowName); ok {
		w.Show()
	}
}

// SetEmitter 供 main 注入事件发送器。
// 刻意做成包级函数而不是 Service 的方法：导出方法带 func 参数会让 Wails
// 绑定生成器报 “function types are not supported by encoding/json”。
func SetEmitter(s *Service, fn func(string, any)) { s.setEmitter(fn) }

// SetRestarter 供 main 注入“重启自身”的实现。
func SetRestarter(s *Service, fn func()) { s.setRestarter(fn) }

package app

import "sync/atomic"

var (
	quitting atomic.Bool
	quitFn   func()
)

// SetQuitter 由 main 注入 wailsApp.Quit。
func SetQuitter(fn func()) { quitFn = fn }

// ShouldQuit 供 Wails 的 Options.ShouldQuit 使用。
//
// 默认返回 false 是有意的：关掉设置窗口不应该结束整个托盘程序。
// 但要注意 Wails 的 windowsApp.destroy() 第一行就是
//   if !globalApplication.shouldQuit() { return }
// 所以任何想退出的路径都必须先置位，否则 Quit() 会静默失效
// （dsh 被停掉了、进程却还活着，正是这个坑）。
func ShouldQuit() bool { return quitting.Load() }

// Quit 请求退出：先置位，再调用 Wails 的 Quit。
func Quit() {
	quitting.Store(true)
	if quitFn != nil {
		quitFn()
	}
}

// MarkQuitting 只置位不退出，供“重启自身”流程使用。
func MarkQuitting() { quitting.Store(true) }

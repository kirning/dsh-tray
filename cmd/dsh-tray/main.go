// Command dsh-tray 是 dsh web 的托盘管理器。
//
// 它常驻 Windows 通知区，用右键菜单控制 dsh web 的启动/重启/关闭，
// 双击图标可在未运行时启动、已就绪时打开 Web UI。
//
// 用法：
//
//	dsh-tray.exe              正常启动（常驻托盘）
//	dsh-tray.exe --selftest   自检：路径解析、版本、更新查询
//	dsh-tray.exe --uitest     自检：创建托盘与菜单，3 秒后退出
//	dsh-tray.exe --starttest  自检：在 3999 端口起临时实例验证启停
package main

import (
	"os"

	"dshtray/internal/tray"
)

func main() {
	tray.Run(os.Args[1:])
}

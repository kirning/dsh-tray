// Command dsh-tray 是 dsh web 的跨平台托盘管理器。
//
// 本文件只负责装配：读取配置 → 单实例 → 创建 Wails 应用与隐藏的设置窗口
// → 启动托盘控制器 → 进入消息循环。具体实现分别在：
//
//	internal/app      托盘、菜单、暴露给前端的 Service
//	internal/core     平台无关业务（dsh 定位/启停/就绪/配置/日志/更新）
//	internal/platform 系统调用（Windows / Linux 各一套，build tag 隔离）
package main

import (
	"embed"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/kirning/dsh-tray/internal/app"
	"github.com/kirning/dsh-tray/internal/core"
	"github.com/kirning/dsh-tray/internal/platform"
	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	quitTest := false
	for _, a := range os.Args[1:] {
		switch a {
		case "--version", "-v":
			fmt.Println(core.AppVersion)
			return
		case "--quittest":
			// 自检：启动后 3 秒主动退出，用来验证退出链路（托盘菜单无法自动化点击）
			quitTest = true
		}
	}

	core.LoadConfig()

	release, ok := platform.AcquireSingleInstance()
	if !ok {
		core.Logf("已有实例在运行，本次启动退出")
		return
	}
	defer release()

	cleanupOldBinary()
	core.Logf("程序启动，版本 %s，平台 %s/%s", core.AppVersion, runtime.GOOS, runtime.GOARCH)

	dsh := core.NewDsh(core.LogPath())
	// 只认“本次启动之后”的日志，避免把历史日志里的旧标志当成本次就绪
	dsh.SetReadyBaseline()

	svc := app.NewService(dsh)

	wailsApp := application.New(application.Options{
		Name:        "dsh tray",
		Description: "dsh web tray manager",
		Services: []application.Service{
			application.NewService(svc),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		// 关掉设置窗口不能退出程序；只有显式 Quit() 置位后才允许真正退出
		ShouldQuit: app.ShouldQuit,
	})

	app.SetQuitter(wailsApp.Quit)
	app.SetEmitter(svc, func(name string, data any) {
		wailsApp.Event.Emit(name, data)
	})
	// 自更新重启：必须先释放单实例锁，否则新进程会判定“已有实例”直接退出
	app.SetRestarter(svc, func() {
		release()
		exe, err := os.Executable()
		if err != nil {
			return
		}
		cmd := exec.Command(exe)
		cmd.Dir = filepath.Dir(exe)
		_ = cmd.Start()
		app.Quit()
	})

	wailsApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             app.SettingsWindowName,
		Title:            "设置 - dsh 托盘",
		Width:            860,
		Height:           900,
		MinWidth:         720,
		MinHeight:        640,
		Hidden:           true,
		URL:              "/",
		BackgroundColour: application.NewRGB(250, 249, 246),
	})

	app.NewTray(wailsApp, dsh, svc).Start()

	if quitTest {
		go func() {
			time.Sleep(3 * time.Second)
			core.Logf("--quittest：主动退出（验证退出链路）")
			app.Quit()
		}()
	}

	if err := wailsApp.Run(); err != nil {
		core.Logf("运行失败：%v", err)
		log.Fatal(err)
	}
	core.Logf("程序退出")
}

// cleanupOldBinary 删除上次自更新留下的旧可执行文件（Windows 上改名后残留）。
func cleanupOldBinary() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	for _, name := range []string{"dsh-tray.old.exe", "dsh-tray.new.exe"} {
		_ = os.Remove(filepath.Join(filepath.Dir(exe), name))
	}
}

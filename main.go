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
	statusTest := false
	windowTest := false
	for _, a := range os.Args[1:] {
		switch a {
		case "--version", "-v":
			fmt.Println(core.AppVersion)
			return
		case "--quittest":
			// 自检：启动后 3 秒主动退出，用来验证退出链路（托盘菜单无法自动化点击）
			quitTest = true
		case "--statustest":
			// 自检：每 2 秒把三态判定的内部状态写进日志，10 秒后退出
			statusTest = true
		case "--windowtest":
			// 自检：关闭设置窗口，验证进程仍存活且窗口能重建
			windowTest = true
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
		Windows: application.WindowsOptions{
			// 关键：默认行为是“关掉最后一个窗口就 PostQuitMessage 退出”，
			// 而我们是托盘程序，设置窗口只是附属面板。关掉它必须继续常驻。
			DisableQuitOnLastWindowClosed: true,
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

	app.EnsureSettingsWindow(wailsApp, true)

	app.NewTray(wailsApp, dsh, svc).Start()

	if quitTest {
		go func() {
			time.Sleep(3 * time.Second)
			core.Logf("--quittest：主动退出（验证退出链路）")
			app.Quit()
		}()
	}
	if statusTest {
		go func() {
			for i := 0; i < 5; i++ {
				time.Sleep(2 * time.Second)
				core.Logf("statustest: state=%s running=%v ready=%v %s",
					app.CurrentState(dsh), dsh.IsRunning(), dsh.CheckReady(), dsh.DebugState())
			}
			core.Logf("statustest 结束")
			app.Quit()
		}()
	}
	if windowTest {
		go func() {
			time.Sleep(3 * time.Second)
			if w, ok := wailsApp.Window.GetByName(app.SettingsWindowName); ok {
				w.Close()
				core.Logf("windowtest: 已关闭设置窗口")
			} else {
				core.Logf("windowtest: 找不到设置窗口")
			}
			time.Sleep(3 * time.Second)
			core.Logf("windowtest: 关闭 3 秒后进程仍存活，开始重建窗口")
			app.ShowSettings(wailsApp)
			time.Sleep(2 * time.Second)
			if _, ok := wailsApp.Window.GetByName(app.SettingsWindowName); ok {
				core.Logf("windowtest: 窗口重建成功")
			} else {
				core.Logf("windowtest: 窗口重建失败")
			}
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

package tray

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/lxn/walk"
)

// settingstest 真实创建「设置」窗口，检查控件与配置联动，3 秒后自动关闭。
// 结果写到 %LOCALAPPDATA%\dsh-tray\settingstest.txt（GUI 程序没有 stdout）。
func settingstest() {
	res := &strings.Builder{}
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(res, "PANIC: %v\r\n%s\r\n", r, debug.Stack())
		}
		_ = os.MkdirAll(dataDir(), 0o755)
		_ = os.WriteFile(filepath.Join(dataDir(), "settingstest.txt"), []byte(res.String()), 0o644)
	}()

	loadConfig()
	dsh = NewDsh(logFilePath())
	res.WriteString(fmt.Sprintf("版本 = %s\r\n", appVersion))
	addr, scheme := proxyConfig()
	res.WriteString(fmt.Sprintf("配置代理 = %q / %q\r\n", addr, scheme))
	res.WriteString(fmt.Sprintf("开机自启 = %v，自动打开web = %v，启动参数 = %v\r\n",
		autoStartEnabled(), autoOpenWeb(), startArgs()))

	mw0, err := walk.NewMainWindow()
	if err != nil {
		fmt.Fprintf(res, "NewMainWindow FAIL: %v\r\n", err)
		return
	}
	mw = mw0
	res.WriteString("NewMainWindow OK\r\n")
	_ = mw.SetLayout(walk.NewHBoxLayout())
	mw.SetVisible(false)

	if err := openSettings(mw); err != nil {
		fmt.Fprintf(res, "openSettings FAIL: %v\r\n", err)
		return
	}
	s := currentSettings()
	if s == nil {
		res.WriteString("openSettings FAIL: settingsPane 为空\r\n")
		return
	}
	res.WriteString("openSettings OK\r\n")
	res.WriteString(fmt.Sprintf("  dsh 路径输入框 = %q（配置 = %q）\r\n", s.dshEdit.Text(), dshPathFromConfig()))
	res.WriteString(fmt.Sprintf("  dsh 解析结果 = %q\r\n", s.dshStatus.Text()))
	res.WriteString(fmt.Sprintf("  代理输入框 = %q\r\n", s.proxyEdit.Text()))
	res.WriteString(fmt.Sprintf("  版本标签 = %q\r\n", s.versionLabel.Text()))
	res.WriteString(fmt.Sprintf("  开机自启勾选 = %v（注册表 = %v）\r\n", s.chkAuto.Checked(), autoStartEnabled()))
	res.WriteString(fmt.Sprintf("  自动打开web勾选 = %v（配置 = %v）\r\n", s.chkAutoOpen.Checked(), autoOpenWeb()))
	res.WriteString(fmt.Sprintf("  日志查看长度 = %d 字节，只读 = %v\r\n", len(s.logEdit.Text()), s.logEdit.ReadOnly()))
	res.WriteString(fmt.Sprintf("  状态框 = %q\r\n", s.statusEdit.Text()))
	res.WriteString(fmt.Sprintf("  进度条 range = %d~%d，当前 = %d\r\n",
		s.progress.MinValue(), s.progress.MaxValue(), s.progress.Value()))

	type probe struct {
		progress int
		status   string
	}
	ch := make(chan probe, 1)

	go func() {
		// syncUI 是 PostMessage 异步投递，必须等 mw.Run() 起消息循环后才会执行，
		// 所以这三步按顺序投递，读取时就能看到更新后的值。
		time.Sleep(800 * time.Millisecond)
		setSettingsProgress(250, 1000)
		setSettingsStatus("自检：状态写入正常（含错误显示通道）")
		syncUI(func() {
			p := currentSettings()
			if p == nil {
				ch <- probe{-1, "(settingsPane 为空)"}
				return
			}
			ch <- probe{p.progress.Value(), p.statusEdit.Text()}
		})
		time.Sleep(2 * time.Second)
		syncUI(func() {
			if f := currentSettings(); f != nil && f.dlg != nil {
				f.dlg.Close(0)
			}
			mw.Close()
		})
	}()

	mw.Run()
	res.WriteString("message loop exited cleanly\r\n")

	select {
	case p := <-ch:
		res.WriteString(fmt.Sprintf("  进度回调后 = %d（期望 250）\r\n", p.progress))
		res.WriteString(fmt.Sprintf("  状态刷新后 = %q\r\n", p.status))
	case <-time.After(time.Second):
		res.WriteString("  异步 UI 探针超时\r\n")
	}
	res.WriteString(fmt.Sprintf("关闭后 settingsPane 为空 = %v\r\n", currentSettings() == nil))
}

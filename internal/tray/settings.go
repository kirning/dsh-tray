package tray

import (
	"fmt"
	"sync"
	"time"

	"github.com/lxn/walk"
	decl "github.com/lxn/walk/declarative"
)

// settingsForm 保存「设置」窗口里需要后续读写的控件。
type settingsForm struct {
	dlg *walk.Dialog

	proxyEdit   *walk.LineEdit
	proxyStatus *walk.Label
	btnProxy    *walk.PushButton

	chkAuto     *walk.CheckBox
	chkAutoOpen *walk.CheckBox

	versionLabel *walk.Label
	btnUpdate    *walk.PushButton
	progress     *walk.ProgressBar
	statusEdit   *walk.TextEdit

	logEdit *walk.TextEdit
}

var (
	settingsMu   sync.Mutex
	settingsPane *settingsForm
)

func currentSettings() *settingsForm {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	return settingsPane
}

// openSettings 创建（或唤起）非模态设置窗口。必须在 UI 线程调用。
func openSettings(owner walk.Form) error {
	settingsMu.Lock()
	if settingsPane != nil {
		dlg := settingsPane.dlg
		settingsMu.Unlock()
		if dlg != nil {
			dlg.Show()
		}
		return nil
	}
	settingsMu.Unlock()

	s := &settingsForm{}
	addr, scheme := proxyConfig()
	proxyNote := "未设置代理（直连）"
	if addr != "" {
		proxyNote = fmt.Sprintf("当前：%s://%s", scheme, addr)
	}

	var dlg *walk.Dialog
	err := (decl.Dialog{
		AssignTo: &dlg,
		Title:    "设置 - " + appName,
		Size:     decl.Size{Width: 780, Height: 720},
		MinSize:  decl.Size{Width: 720, Height: 600},
		Layout:   decl.VBox{Margins: decl.Margins{Left: 14, Top: 12, Right: 14, Bottom: 12}, Spacing: 10},
		Children: []decl.Widget{
			decl.GroupBox{
				Title:  "代理服务器",
				Layout: decl.VBox{Spacing: 6},
				Children: []decl.Widget{
					// 每行的收尾都放一个 HSpacer：它既把控件顶到左边、保持各行左边界一致，
					// 又让分组框带上 GrowableHorz —— 不然这个分组会被 walk 按偏好宽度居中，
					// 比其它分组窄一圈（BoxLayout 只铺满可横向拉伸的子项）。
					decl.Composite{
						Layout: decl.HBox{Spacing: 6},
						Children: []decl.Widget{
							decl.Label{Text: "地址（IP:端口）："},
							decl.LineEdit{
								AssignTo: &s.proxyEdit,
								Text:     addr,
								MinSize:  decl.Size{Width: 220, Height: 22},
							},
							decl.PushButton{
								AssignTo:  &s.btnProxy,
								Text:      "检测并保存",
								MaxSize:   decl.Size{Width: 100},
								OnClicked: func() {
									saveProxyFromSettings()
								},
							},
							decl.PushButton{
								Text:    "清空（直连）",
								MaxSize: decl.Size{Width: 100},
								OnClicked: func() {
									clearProxyFromSettings()
								},
							},
							decl.HSpacer{},
						},
					},
					decl.Label{AssignTo: &s.proxyStatus, Text: proxyNote},
					decl.Label{Text: "留空 = 直连。优先按 SOCKS5 判断，不通自动改用 HTTP；也可填 socks5:// 或 http:// 显式指定。"},
				},
			},

			decl.GroupBox{
				Title:  "启动配置（与右键菜单同步，config.json 另存一份副本）",
				Layout: decl.VBox{Spacing: 6},
				Children: []decl.Widget{
					decl.Composite{
						Layout: decl.HBox{Spacing: 18},
						Children: []decl.Widget{
							decl.CheckBox{
								Text:     "开机自启",
								AssignTo: &s.chkAuto,
								Checked:  autoStartEnabled(),
								OnClicked: func() {
									applyAutoStartFromSettings()
								},
							},
							decl.CheckBox{
								Text:     "启动后自动打开web",
								AssignTo: &s.chkAutoOpen,
								Checked:  autoOpenWeb(),
								OnClicked: func() {
									applyAutoOpenFromSettings()
								},
							},
							decl.HSpacer{},
						},
					},
				},
			},

			decl.GroupBox{
				Title:  "版本与更新",
				Layout: decl.VBox{Spacing: 6},
				Children: []decl.Widget{
					decl.Composite{
						Layout: decl.HBox{Spacing: 8},
						Children: []decl.Widget{
							decl.Label{AssignTo: &s.versionLabel, Text: "当前版本：" + appVersion},
							decl.PushButton{
								AssignTo:  &s.btnUpdate,
								Text:      "检查更新",
								MaxSize:   decl.Size{Width: 100},
								OnClicked: func() {
									settingsCheckUpdate()
								},
							},
							decl.HSpacer{},
						},
					},
					decl.ProgressBar{
						AssignTo: &s.progress,
						MinValue: 0,
						MaxValue: 1000,
						Value:    0,
						MinSize:  decl.Size{Height: 18},
					},
					decl.TextEdit{
						AssignTo: &s.statusEdit,
						ReadOnly: true,
						VScroll:  true,
						MinSize:  decl.Size{Height: 54},
						Text:     "就绪。点击「检查更新」检查并升级本程序。",
					},
				},
			},

			decl.GroupBox{
				Title:         "日志（只读，可选中复制）",
				Layout:        decl.VBox{Spacing: 6},
				StretchFactor: 1,
				Children: []decl.Widget{
					decl.Composite{
						Layout: decl.HBox{Spacing: 6},
						Children: []decl.Widget{
							decl.PushButton{Text: "刷新", MaxSize: decl.Size{Width: 80}, OnClicked: func() { refreshLogView() }},
							decl.PushButton{Text: "复制全部", MaxSize: decl.Size{Width: 96}, OnClicked: func() { copyLogView() }},
							decl.PushButton{Text: "用默认程序打开", MaxSize: decl.Size{Width: 120}, OnClicked: func() { doViewLog() }},
							decl.HSpacer{},
						},
					},
					decl.TextEdit{
						AssignTo:      &s.logEdit,
						ReadOnly:      true,
						VScroll:       true,
						HScroll:       true,
						Text:          readLogText(),
						MinSize:       decl.Size{Height: 180},
						StretchFactor: 2,
					},
				},
			},

			decl.Composite{
				Layout: decl.HBox{Spacing: 6},
				Children: []decl.Widget{
					decl.HSpacer{},
					decl.PushButton{Text: "关闭", MaxSize: decl.Size{Width: 100}, OnClicked: func() { dlg.Close(0) }},
				},
			},
		},
	}).Create(owner)
	if err != nil {
		return err
	}

	s.dlg = dlg
	settingsMu.Lock()
	settingsPane = s
	settingsMu.Unlock()

	dlg.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		settingsMu.Lock()
		settingsPane = nil
		settingsMu.Unlock()
		// walk 在 WM_CLOSE 里会给 owner（隐藏的 mw）发 SWP_SHOWWINDOW，
		// 不藏回去就会留下一个空白窗体（详见 guardMainWindow）。
		hideMainWindow()
		trayLogf("设置窗口已关闭")
	})

	trayLogf("设置窗口已打开（版本 %s，代理 %s）", appVersion, proxyNote)
	dlg.Show()
	return nil
}

// setSettingsStatus 把状态/错误显示在设置窗口里，同时写进日志。
func setSettingsStatus(text string) {
	trayLogf("设置：%s", text)
	syncUI(func() {
		if s := currentSettings(); s != nil && s.statusEdit != nil {
			_ = s.statusEdit.SetText(text)
		}
	})
}

// setSettingsProgress 更新进度条（done/total 用 0~1000 的刻度）。
func setSettingsProgress(done, total int64) {
	syncUI(func() {
		s := currentSettings()
		if s == nil || s.progress == nil {
			return
		}
		s.progress.SetRange(0, 1000)
		if total > 0 {
			p := int(done * 1000 / total)
			if p < 0 {
				p = 0
			}
			if p > 1000 {
				p = 1000
			}
			s.progress.SetValue(p)
		} else {
			s.progress.SetValue(0)
		}
	})
}

// showMsgBoxAndWait 弹提示并等待用户点确定（msgBox 是异步投递，不能用于"之后再重启"）。
func showMsgBoxAndWait(title, text string, style walk.MsgBoxStyle) {
	ch := make(chan struct{}, 1)
	syncUI(func() {
		walk.MsgBox(mw, title, text, style)
		ch <- struct{}{}
	})
	<-ch
}

// syncSettingsToggles 让右键菜单与设置窗口里的两个勾选项始终一致。
func syncSettingsToggles() {
	syncUI(func() {
		a, b := autoStartEnabled(), autoOpenWeb()
		if actAuto != nil {
			actAuto.SetChecked(a)
		}
		if actAutoOpen != nil {
			actAutoOpen.SetChecked(b)
		}
		if s := currentSettings(); s != nil {
			if s.chkAuto != nil {
				s.chkAuto.SetChecked(a)
			}
			if s.chkAutoOpen != nil {
				s.chkAutoOpen.SetChecked(b)
			}
		}
	})
}

// saveProxyFromSettings 解析输入 → 优先探测 SOCKS5、不通回退 HTTP → 保存。
func saveProxyFromSettings() {
	s := currentSettings()
	if s == nil || s.proxyEdit == nil {
		return
	}
	raw := s.proxyEdit.Text()
	addr, explicit, err := parseProxyInput(raw)
	if err != nil {
		setSettingsStatus("代理地址有误：" + err.Error())
		return
	}
	if addr == "" {
		clearProxyFromSettings()
		return
	}
	setSettingsStatus("正在检测代理协议（优先 SOCKS5）…")
	go func() {
		scheme, note, derr := detectProxyScheme(addr, explicit)
		if derr != nil {
			setSettingsStatus("代理不可用：" + derr.Error())
			return
		}
		if err := setProxyConfig(addr, scheme); err != nil {
			setSettingsStatus("保存代理失败：" + err.Error())
			return
		}
		trayLogf("代理已保存：%s://%s（%s）", scheme, addr, note)
		syncUI(func() {
			if f := currentSettings(); f != nil {
				_ = f.proxyEdit.SetText(addr)
				_ = f.proxyStatus.SetText("当前：" + scheme + "://" + addr + " · " + note)
			}
		})
		setSettingsStatus("代理已保存：" + scheme + "://" + addr + "（" + note + "）")
	}()
}

func clearProxyFromSettings() {
	if err := setProxyConfig("", ""); err != nil {
		setSettingsStatus("清空代理失败：" + err.Error())
		return
	}
	syncUI(func() {
		if f := currentSettings(); f != nil {
			_ = f.proxyEdit.SetText("")
			_ = f.proxyStatus.SetText("未设置代理（直连）")
		}
	})
	setSettingsStatus("已清空代理，改为直连")
}

func applyAutoStartFromSettings() {
	s := currentSettings()
	if s == nil || s.chkAuto == nil {
		return
	}
	want := s.chkAuto.Checked()
	if err := setAutoStart(want); err != nil {
		setSettingsStatus("设置开机自启失败：" + err.Error())
		return
	}
	syncAutoStartMirror() // 同步一份到 config.json
	syncSettingsToggles()
	trayLogf("设置：开机自启 = %v（菜单与 config.json 已同步）", want)
	setSettingsStatus(fmt.Sprintf("开机自启已设为 %v", want))
}

func applyAutoOpenFromSettings() {
	s := currentSettings()
	if s == nil || s.chkAutoOpen == nil {
		return
	}
	want := s.chkAutoOpen.Checked()
	if err := setAutoOpenWeb(want); err != nil {
		setSettingsStatus("保存配置失败：" + err.Error())
		return
	}
	syncSettingsToggles()
	trayLogf("设置：启动后自动打开web = %v（启动参数 %v）", want, startArgs())
	setSettingsStatus(fmt.Sprintf("启动后自动打开web已设为 %v（启动参数 %v）", want, startArgs()))
}

func refreshLogView() {
	text := readLogText()
	syncUI(func() {
		if s := currentSettings(); s != nil && s.logEdit != nil {
			_ = s.logEdit.SetText(text)
		}
	})
}

func copyLogView() {
	if err := walk.Clipboard().SetText(readLogText()); err != nil {
		setSettingsStatus("复制失败：" + err.Error())
		return
	}
	setSettingsStatus("日志已复制到剪贴板")
}

// settingsCheckUpdate 设置窗口里的「检查更新」：检查 → 下载 → 替换 → 重启，
// 全过程在状态框显示进度与错误。
func settingsCheckUpdate() {
	s := currentSettings()
	if s == nil {
		return
	}
	if s.btnUpdate != nil {
		s.btnUpdate.SetEnabled(false)
	}
	if s.progress != nil {
		s.progress.SetRange(0, 1000)
		s.progress.SetValue(0)
	}
	setSettingsStatus("正在检查更新…")

	// 确认框要挂在设置窗口上。挂到隐藏的 mw 上会跑到设置窗口背后、位置也奇怪。
	var owner walk.Form = mw
	if s.dlg != nil {
		owner = s.dlg
	}

	go func() {
		defer syncUI(func() {
			if f := currentSettings(); f != nil && f.btnUpdate != nil {
				f.btnUpdate.SetEnabled(true)
			}
		})

		tag, newer, asset, err := checkAppUpdate()
		if err != nil {
			setSettingsStatus("检查更新失败：" + err.Error())
			return
		}
		if !newer {
			// 本地版本可能比线上还新（刚打完包、Release 还没发出来），别只说"最新"
			setSettingsStatus(fmt.Sprintf("没有可用的新版本（当前 %s，GitHub 上最新 %s）", appVersion, tag))
			setSettingsProgress(1000, 1000)
			return
		}
		if !askYesNoFrom(owner, appName+" - 发现新版本",
			fmt.Sprintf("当前版本：%s\r\n最新版本：%s\r\n\r\n是否下载并更新？\r\n\r\n更新过程：下载 → 替换本程序 → 自动重启。dsh 服务不会中断。",
				appVersion, tag)) {
			setSettingsStatus("已取消更新")
			return
		}

		setSettingsStatus("正在下载 " + tag + " …")
		if err := applyUpdate(asset, setSettingsProgress); err != nil {
			setSettingsStatus("更新失败：" + err.Error())
			return
		}
		setSettingsStatus("更新完成，即将自动重启…")
		trayLogf("设置：应用已更新到 %s，准备重启", tag)
		time.Sleep(1200 * time.Millisecond)
		syncUI(func() {
			if f := currentSettings(); f != nil && f.dlg != nil {
				f.dlg.Close(0)
			}
			restartSelf()
			quitApp()
		})
	}()
}

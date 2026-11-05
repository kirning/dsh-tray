package tray

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/lxn/walk"
	decl "github.com/lxn/walk/declarative"
)

// settingsForm 保存「设置」窗口里需要后续读写的控件。
type settingsForm struct {
	dlg *walk.Dialog

	dshEdit   *walk.LineEdit
	dshStatus *walk.LineEdit

	proxyEdit   *walk.LineEdit
	proxyStatus *walk.Label
	btnProxy    *walk.PushButton

	chkAuto     *walk.CheckBox
	chkAutoOpen *walk.CheckBox

	tokenEdit *walk.LineEdit

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
		Size:     decl.Size{Width: 780, Height: 860},
		MinSize:  decl.Size{Width: 720, Height: 700},
		Layout:   decl.VBox{Margins: decl.Margins{Left: 14, Top: 12, Right: 14, Bottom: 12}, Spacing: 10},
		Children: []decl.Widget{
			// dsh 路径放在最前面：它是所有功能的前提。用 mise / volta 之类
			// 版本管理器时不手填一次就可能"找不到 dsh"（shims 目录不在 PATH 里）。
			decl.GroupBox{
				Title:  "dsh 程序路径",
				Layout: decl.VBox{Spacing: 6},
				Children: []decl.Widget{
					decl.Composite{
						Layout: decl.HBox{Spacing: 6},
						Children: []decl.Widget{
							decl.LineEdit{
								AssignTo:  &s.dshEdit,
								Text:      dshPathFromConfig(),
								MinSize:   decl.Size{Height: 22},
								CueBanner: `留空 = 自动探测。可直接粘贴路径，如 %LOCALAPPDATA%\mise\shims\dsh.exe`,
							},
							decl.PushButton{
								Text:      "浏览…",
								MaxSize:   decl.Size{Width: 80},
								OnClicked: func() { browseDshPath() },
							},
							decl.PushButton{
								Text:      "保存",
								MaxSize:   decl.Size{Width: 70},
								OnClicked: func() { saveDshPathFromSettings() },
							},
							decl.PushButton{
								Text:      "自动探测",
								MaxSize:   decl.Size{Width: 80},
								OnClicked: func() { clearDshPathFromSettings() },
							},
						},
					},
					// 只读输入框而不是 Label：路径太长时 Label 会被省略号截断，
					// 而这里可以横向滚动、也能选中复制给别的机器用。
					decl.LineEdit{
						AssignTo: &s.dshStatus,
						ReadOnly: true,
						Text:     describeDsh(),
						MinSize:  decl.Size{Height: 22},
					},
				},
			},

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
								AssignTo: &s.btnProxy,
								Text:     "检测并保存",
								MaxSize:  decl.Size{Width: 100},
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
								AssignTo: &s.btnUpdate,
								Text:     "检查更新",
								MaxSize:  decl.Size{Width: 100},
								OnClicked: func() {
									settingsCheckUpdate()
								},
							},
							decl.HSpacer{},
						},
					},
					// GitHub 匿名配额 60 次/小时且按出口 IP 算，公司网/代理出口极易被耗光，
					// 填了令牌提到 5000 次/小时；装了 gh 并登录过会自动复用，不用手填。
					decl.Composite{
						Layout: decl.HBox{Spacing: 6},
						Children: []decl.Widget{
							decl.Label{Text: "GitHub Token（可选）："},
							decl.LineEdit{
								AssignTo:     &s.tokenEdit,
								Text:         gitHubTokenFromConfig(),
								PasswordMode: true,
								MinSize:      decl.Size{Width: 260, Height: 22},
								CueBanner:    "留空则用匿名配额；装了 gh 且已登录时会自动复用",
							},
							decl.PushButton{
								Text:      "保存",
								MaxSize:   decl.Size{Width: 70},
								OnClicked: func() { saveTokenFromSettings() },
							},
							decl.PushButton{
								Text:      "清空",
								MaxSize:   decl.Size{Width: 70},
								OnClicked: func() { clearTokenFromSettings() },
							},
							decl.HSpacer{},
						},
					},
					decl.Label{Text: "匿名访问 60 次/小时（按出口 IP 算），带令牌 5000 次/小时。只用于查/下载本程序的 Release。"},
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
						MinSize:       decl.Size{Height: 150},
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

// describeDsh 用一句话说明"当前会怎么启动 dsh"，显示在设置窗口里。
// 只给关键信息（哪个 exe），完整启动方式放 describeDshFull 里当 ToolTip，
// 免得长路径被省略号截掉后又看不出全貌。
func describeDsh() string {
	l, err := resolveDshInstall()
	if err != nil {
		return "未找到 dsh：点「浏览…」选 dsh.exe，或把路径粘贴到上面"
	}
	src := "自动探测"
	if strings.TrimSpace(dshPathFromConfig()) != "" {
		src = "设置指定"
	}
	if len(l.prefix) > 0 {
		return src + "：" + l.exe + " + bin.js"
	}
	return src + "：" + l.exe
}

// refreshDshStatus 重新计算并刷新说明行。
func refreshDshStatus() {
	note := describeDsh()
	syncUI(func() {
		if f := currentSettings(); f != nil && f.dshStatus != nil {
			_ = f.dshStatus.SetText(note)
		}
	})
}

var winEnvRe = regexp.MustCompile(`%([^%]+)%`)

// expandWinEnv 展开 %LOCALAPPDATA% 这种 Windows 风格的变量（Go 的 ExpandEnv 只认 $VAR）。
func expandWinEnv(s string) string {
	return winEnvRe.ReplaceAllStringFunc(s, func(m string) string {
		if v := os.Getenv(m[1 : len(m)-1]); v != "" {
			return v
		}
		return m
	})
}

// normalizeDshPath 规整"粘贴进来的路径"：
// 去掉引号与首尾空白、展开 %VAR%、给目录补上 dsh.exe。
func normalizeDshPath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	p = strings.Trim(p, `"'`)
	if p == "" {
		return "", nil
	}
	p = expandWinEnv(p)
	p = os.ExpandEnv(p)
	st, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("路径不存在：%s", p)
	}
	if st.IsDir() {
		for _, name := range []string{"dsh.exe", "dsh.cmd", "dsh.bat"} {
			c := filepath.Join(p, name)
			if fileExists(c) {
				return c, nil
			}
		}
		return "", fmt.Errorf("这个目录里没有 dsh.exe / dsh.cmd：%s", p)
	}
	return p, nil
}

// browseDshPath 打开文件选择框，选完直接保存并验证。
func browseDshPath() {
	s := currentSettings()
	if s == nil || s.dlg == nil {
		return
	}
	dlg := new(walk.FileDialog)
	dlg.Title = "选择 dsh 启动器（dsh.exe / dsh.cmd）"
	dlg.Filter = "dsh 启动器 (*.exe;*.cmd;*.bat)|*.exe;*.cmd;*.bat|所有文件 (*.*)|*.*"
	// walk 的文件框没有单独的"初始文件"字段：文件名放 FilePath、目录放 InitialDirPath
	if cur := strings.TrimSpace(s.dshEdit.Text()); cur != "" {
		if p, err := normalizeDshPath(cur); err == nil && p != "" {
			dlg.InitialDirPath = filepath.Dir(p)
			dlg.FilePath = filepath.Base(p)
		}
	}
	if dlg.InitialDirPath == "" {
		for _, dir := range launcherDirs() {
			if fileExists(filepath.Join(dir, "dsh.exe")) || fileExists(filepath.Join(dir, "dsh.cmd")) {
				dlg.InitialDirPath = dir
				break
			}
		}
	}
	ok, err := dlg.ShowOpen(s.dlg)
	if err != nil {
		setSettingsStatus("打开文件选择框失败：" + err.Error())
		return
	}
	if !ok || dlg.FilePath == "" {
		return
	}
	_ = s.dshEdit.SetText(dlg.FilePath)
	saveDshPathFromSettings()
}

// saveDshPathFromSettings 保存手填/选中的 dsh 路径，并立刻跑一次 --version 验证。
func saveDshPathFromSettings() {
	s := currentSettings()
	if s == nil || s.dshEdit == nil {
		return
	}
	p, err := normalizeDshPath(s.dshEdit.Text())
	if err != nil {
		setSettingsStatus("dsh 路径无效：" + err.Error())
		return
	}
	if err := setDshPath(p); err != nil {
		setSettingsStatus("保存 dsh 路径失败：" + err.Error())
		return
	}
	syncUI(func() {
		if f := currentSettings(); f != nil {
			_ = f.dshEdit.SetText(p)
		}
	})
	refreshDshStatus()
	if p == "" {
		setSettingsStatus("已改为自动探测 → " + describeDsh())
		return
	}

	l, rerr := resolveDshInstall()
	if rerr != nil {
		setSettingsStatus("已保存，但仍无法解析：" + rerr.Error())
		return
	}
	setSettingsStatus("正在验证 " + p + " …")
	go func() {
		out, verr := runHiddenTimeout(30*time.Second, l.exe, l.argv("--version")...)
		v := firstSemver(out)
		if v == "" {
			msg := "已保存，但执行失败"
			if verr != nil {
				msg += "：" + verr.Error()
			}
			if t := strings.TrimSpace(out); t != "" {
				msg += "\r\n输出：" + t
			}
			setSettingsStatus(msg)
			return
		}
		trayLogf("dsh 路径已保存：%s（%s），版本 %s", p, l.how, v)
		setSettingsStatus("已保存并验证通过：dsh " + v + "（" + l.how + "）")
	}()
}

// clearDshPathFromSettings 清空手填路径，回到自动探测。
func clearDshPathFromSettings() {
	if err := setDshPath(""); err != nil {
		setSettingsStatus("清空 dsh 路径失败：" + err.Error())
		return
	}
	syncUI(func() {
		if f := currentSettings(); f != nil {
			_ = f.dshEdit.SetText("")
		}
	})
	refreshDshStatus()
	setSettingsStatus("已改为自动探测 → " + describeDsh())
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

// saveTokenFromSettings 保存手填的 GitHub 令牌（提高 API 配额）。
func saveTokenFromSettings() {
	s := currentSettings()
	if s == nil || s.tokenEdit == nil {
		return
	}
	token := strings.TrimSpace(s.tokenEdit.Text())
	if err := setGitHubToken(token); err != nil {
		setSettingsStatus("保存 GitHub Token 失败：" + err.Error())
		return
	}
	if token == "" {
		setSettingsStatus("已清空 GitHub Token，改为匿名访问（装了 gh 时会自动复用它的令牌）")
		return
	}
	// 立刻验证一次：令牌无效的话现在就说，别等到下次检查更新才发现
	go func() {
		ok, note, err := verifyGitHubToken()
		if err != nil {
			setSettingsStatus("GitHub 连通性检查失败：" + err.Error())
			return
		}
		if ok {
			setSettingsStatus("GitHub Token 已保存并验证通过（" + note + "）")
			return
		}
		setSettingsStatus("GitHub Token 已保存，但校验不通过：" + note)
	}()
}

func clearTokenFromSettings() {
	if err := setGitHubToken(""); err != nil {
		setSettingsStatus("清空 GitHub Token 失败：" + err.Error())
		return
	}
	syncUI(func() {
		if f := currentSettings(); f != nil && f.tokenEdit != nil {
			_ = f.tokenEdit.SetText("")
		}
	})
	setSettingsStatus("已清空 GitHub Token")
}

// verifyGitHubToken 用 /rate_limit 校验令牌：它免费（不占配额），能同时看出
// 令牌是否有效、以及当前限额（匿名 60 / 带令牌 5000）。
func verifyGitHubToken() (ok bool, note string, err error) {
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/rate_limit", nil)
	if err != nil {
		return false, "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "dsh-tray/"+appVersion)
	applyGitHubAuth(req)

	resp, err := doRoute(req, metaTimeout, proxyRoutes()[0]) // 先用配置的首选链路
	if err != nil {
		return false, "", err
	}
	if resp.StatusCode != http.StatusOK {
		// 换个链路再试（令牌失效时不需要换链路，但限流时换链路有意义）
		resp.Body.Close()
		for _, rt := range proxyRoutes()[1:] {
			r2, e2 := doRoute(req, metaTimeout, rt)
			if e2 != nil {
				continue
			}
			if r2.StatusCode == http.StatusOK {
				resp = r2
				break
			}
			r2.Body.Close()
		}
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return false, fmt.Sprintf("HTTP %d", resp.StatusCode), nil
	}
	defer resp.Body.Close()

	var rl struct {
		Resources struct {
			Core struct {
				Limit int `json:"limit"`
				Left  int `json:"remaining"`
			} `json:"core"`
		} `json:"resources"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rl); err != nil {
		return false, "", err
	}
	limit := rl.Resources.Core.Limit
	if limit <= 0 {
		return false, "响应里没有 rate limit 信息", nil
	}
	return limit > 60, fmt.Sprintf("限额 %d 次/小时，剩余 %d", limit, rl.Resources.Core.Left), nil
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

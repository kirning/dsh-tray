package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kirning/dsh-tray/internal/core"
	"github.com/kirning/dsh-tray/internal/platform"
)

// Status 是前端一次性拿到的全部状态。
type Status struct {
	State      string      `json:"state"`
	Port       int         `json:"port"`
	DshVersion string      `json:"dshVersion"`
	AppVersion string      `json:"appVersion"`
	LogPath    string      `json:"logPath"`
	DataDir    string      `json:"dataDir"`
	Config     core.Config `json:"config"`
	Autostart  bool        `json:"autostart"`
	OS         string      `json:"os"`
}

// UpdateInfo 是“检查更新”的统一返回。
type UpdateInfo struct {
	Current   string   `json:"current"`
	Latest    string   `json:"latest"`
	HasUpdate bool     `json:"hasUpdate"`
	Newer     []string `json:"newer,omitempty"`
	AssetName string   `json:"assetName,omitempty"`
	AssetSize int64    `json:"assetSize,omitempty"`
	Note      string   `json:"note,omitempty"`
}

// Service 是暴露给前端的 Go 侧 API（Wails 会生成对应 TS 绑定）。
type Service struct {
	dsh *core.Dsh

	mu       sync.Mutex
	verAt    time.Time
	verCache string
}

// svcEmitter 用包级变量而不是结构体字段：Wails 绑定生成器会遍历导出类型的字段，
// 遇到 func 类型会告警“function types are not supported by encoding/json”。
var (
	svcEmitter func(string, any)
	restartFn  func()
)

func NewService(dsh *core.Dsh) *Service { return &Service{dsh: dsh} }

// setEmitter 由 main 注入，用于向前端推送事件。
// 保持未导出：Wails 绑定生成器会遍历导出方法，func 参数会触发
// “function types are not supported by encoding/json” 警告。
func (s *Service) setEmitter(fn func(string, any)) { svcEmitter = fn }

// setRestarter 由 main 注入：释放单实例锁 → 启动新进程 → 退出当前进程。
func (s *Service) setRestarter(fn func()) { restartFn = fn }

// RestartApp 重启本程序（自更新完成后调用）。
func (s *Service) RestartApp() {
	if restartFn != nil {
		restartFn()
	}
}

func (s *Service) notify() {
	if svcEmitter == nil {
		return
	}
	svcEmitter("status", s.GetStatus())
}

// GetStatus 返回当前状态快照。
func (s *Service) GetStatus() Status {
	return Status{
		State:      string(currentState(s.dsh)),
		Port:       s.dsh.Port(),
		DshVersion: s.dshVersion(),
		AppVersion: core.AppVersion,
		LogPath:    core.LogPath(),
		DataDir:    platform.DataDir(),
		Config:     core.GetConfig(),
		Autostart:  platform.AutostartEnabled(),
		OS:         platformName(),
	}
}

func (s *Service) dshVersion() string {
	s.mu.Lock()
	if time.Since(s.verAt) < 30*time.Second && s.verCache != "" {
		v := s.verCache
		s.mu.Unlock()
		return v
	}
	s.mu.Unlock()

	v, err := core.CurrentVersion()
	if err != nil {
		v = ""
	}
	s.mu.Lock()
	s.verCache = v
	s.verAt = time.Now()
	s.mu.Unlock()
	return v
}

func (s *Service) invalidateVersion() {
	s.mu.Lock()
	s.verAt = time.Time{}
	s.mu.Unlock()
}

// StartDsh 启动 dsh web。
func (s *Service) StartDsh() error {
	if err := s.dsh.Start(); err != nil {
		return err
	}
	s.notify()
	return nil
}

// StopDsh 关闭 dsh（按端口找 PID，能收掉外部启动的实例）。
func (s *Service) StopDsh() error {
	err := s.dsh.Stop()
	s.notify()
	return err
}

// RestartDsh 重启 dsh。
func (s *Service) RestartDsh() error {
	_ = s.dsh.Stop()
	time.Sleep(300 * time.Millisecond)
	if err := s.dsh.Start(); err != nil {
		return err
	}
	s.notify()
	return nil
}

// OpenWeb 用默认浏览器打开 dsh web。
func (s *Service) OpenWeb() error {
	return platform.OpenBrowser(fmt.Sprintf("http://127.0.0.1:%d", s.dsh.Port()))
}

// GetConfig 返回配置。
func (s *Service) GetConfig() core.Config { return core.GetConfig() }

// SaveConfig 保存非代理类配置（自启副本、自动打开、Token、dsh 路径）。
func (s *Service) SaveConfig(c core.Config) error {
	err := core.UpdateConfig(func(cur *core.Config) {
		cur.AutoOpenWeb = c.AutoOpenWeb
		cur.GithubToken = strings.TrimSpace(c.GithubToken)
		cur.DshPath = strings.TrimSpace(c.DshPath)
	})
	if err != nil {
		return err
	}
	s.invalidateVersion()
	s.notify()
	return nil
}

// SaveProxy 解析并探测代理，成功才落盘；返回可读结论。
func (s *Service) SaveProxy(raw string) (string, error) {
	addr, scheme := core.ParseProxyInput(raw)
	if addr == "" {
		if err := core.UpdateConfig(func(c *core.Config) {
			c.Proxy, c.ProxyScheme = "", ""
		}); err != nil {
			return "", err
		}
		s.notify()
		return "已清空代理，改为直连", nil
	}
	if err := core.ProbeProxy(addr, scheme); err != nil {
		return "", fmt.Errorf("代理不可用：%w", err)
	}
	if err := core.UpdateConfig(func(c *core.Config) {
		c.Proxy, c.ProxyScheme = addr, scheme
	}); err != nil {
		return "", err
	}
	s.notify()
	return fmt.Sprintf("已保存并验证：%s://%s", scheme, addr), nil
}

// ProbeProxy 只探测不保存（设置页“测试”按钮）。
func (s *Service) ProbeProxy(raw string) (string, error) {
	addr, scheme := core.ParseProxyInput(raw)
	if addr == "" {
		return "留空表示直连", nil
	}
	if err := core.ProbeProxy(addr, scheme); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s://%s 可用", scheme, addr), nil
}

// SaveToken 保存并验证 GitHub Token。
func (s *Service) SaveToken(token string) (string, error) {
	token = strings.TrimSpace(token)
	note, err := core.VerifyToken(token)
	if err != nil {
		return "", err
	}
	if err := core.UpdateConfig(func(c *core.Config) { c.GithubToken = token }); err != nil {
		return "", err
	}
	s.notify()
	return note, nil
}

// SetDshPath 规范化并验证用户手填的 dsh 路径；空串表示恢复自动探测。
func (s *Service) SetDshPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if err := core.UpdateConfig(func(c *core.Config) { c.DshPath = "" }); err != nil {
			return "", err
		}
		s.invalidateVersion()
		s.notify()
		return "已恢复自动探测", nil
	}
	p, err := platform.NormalizeExecutablePath(raw, "dsh")
	if err != nil {
		return "", fmt.Errorf("路径无效：%w", err)
	}
	if err := core.UpdateConfig(func(c *core.Config) { c.DshPath = p }); err != nil {
		return "", err
	}
	s.invalidateVersion()
	s.notify()
	v, verr := core.CurrentVersion()
	if verr != nil {
		return "已保存：" + p + "（但执行 dsh --version 失败：" + verr.Error() + "）", nil
	}
	return "已保存：" + p + "（版本 " + v + "）", nil
}

// SetAutostart 开关开机自启。
func (s *Service) SetAutostart(enable bool) error {
	if err := platform.SetAutostart(enable); err != nil {
		return err
	}
	_ = core.UpdateConfig(func(c *core.Config) { v := enable; c.AutoStart = &v })
	s.notify()
	return nil
}

// ReadLog 返回日志全文（尾部 512 KB）。
func (s *Service) ReadLog() string { return core.ReadLog(512 * 1024) }

// OpenLogFile 用系统默认程序打开日志。
func (s *Service) OpenLogFile() error { return platform.OpenBrowser(core.LogPath()) }

// CheckDshUpdate 查 npm 上是否有更新的 dsh。
func (s *Service) CheckDshUpdate() (UpdateInfo, error) {
	cur, newer, err := core.CheckUpdates("@deepseek-ai/dsh")
	info := UpdateInfo{Current: cur, Newer: newer}
	if len(newer) > 0 {
		info.Latest = newer[len(newer)-1]
		info.HasUpdate = true
	}
	if err != nil {
		return info, err
	}
	if !info.HasUpdate {
		info.Note = "已是最新版本"
	}
	return info, nil
}

// CheckAppUpdate 查本程序的 GitHub Release。
func (s *Service) CheckAppUpdate() (UpdateInfo, error) {
	rel, route, err := core.LatestRelease()
	if err != nil {
		return UpdateInfo{}, err
	}
	info := UpdateInfo{
		Current: core.AppVersion,
		Latest:  strings.TrimPrefix(rel.TagName, "v"),
		Note:    "查询链路：" + route,
	}
	if core.CompareVersions(info.Latest, info.Current) > 0 {
		info.HasUpdate = true
		if a := core.PickAsset(rel); a != nil {
			info.AssetName = a.Name
			info.AssetSize = a.Size
		}
	}
	return info, nil
}

// ApplyAppUpdate 下载并替换自身。成功后由调用方重启。
func (s *Service) ApplyAppUpdate() error {
	rel, _, err := core.LatestRelease()
	if err != nil {
		return err
	}
	latest := strings.TrimPrefix(rel.TagName, "v")
	if core.CompareVersions(latest, core.AppVersion) <= 0 {
		return fmt.Errorf("已是最新版本（%s）", core.AppVersion)
	}
	a := core.PickAsset(rel)
	if a == nil {
		return fmt.Errorf("Release %s 没有当前平台的资产", rel.TagName)
	}
	tmp := filepath.Join(os.TempDir(), a.Name)
	_ = os.Remove(tmp)

	core.Logf("开始下载更新 %s（%d 字节）", a.Name, a.Size)
	err = core.DownloadAsset(a, tmp, func(done, total int64) {
		if svcEmitter != nil {
			svcEmitter("update-progress", map[string]int64{"done": done, "total": total})
		}
	})
	if err != nil {
		return err
	}
	old, err := platform.ReplaceSelf(tmp)
	if err != nil {
		return fmt.Errorf("替换程序失败：%w", err)
	}
	if old != "" {
		core.Logf("旧版本已改名，下次启动清理：%s", old)
	}
	core.Logf("已更新到 %s，等待重启", latest)
	return nil
}

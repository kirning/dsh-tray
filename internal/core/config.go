package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/kirning/dsh-tray/internal/platform"
)

// Config 是本程序的持久化配置。
type Config struct {
	// AutoOpenWeb 为 true 时启动 dsh 不带 --no-open（dsh 自己开浏览器）。
	AutoOpenWeb bool `json:"autoOpenWeb"`

	// Proxy 是用户输入的代理地址（ip:port），空串表示直连。
	Proxy string `json:"proxy,omitempty"`
	// ProxyScheme 是探测/指定出来的协议：socks5 / http / https。
	ProxyScheme string `json:"proxyScheme,omitempty"`

	// AutoStart 是「开机自启」的同步副本（真正生效的是系统自启项）。
	// 用指针区分“没写过”和“明确写了 false”。
	AutoStart *bool `json:"autoStart,omitempty"`

	// GithubToken 只用于查/下载本程序的 Release（匿名配额 60/h，带令牌 5000/h）。
	GithubToken string `json:"githubToken,omitempty"`

	// DshPath 是手工指定的 dsh 启动器路径，空串表示自动探测。
	DshPath string `json:"dshPath,omitempty"`
}

func defaultConfig() Config {
	return Config{AutoOpenWeb: true}
}

var (
	cfgMu sync.Mutex
	cfg   = defaultConfig()
)

// ConfigPath 返回配置文件路径。
func ConfigPath() string { return filepath.Join(platform.DataDir(), "config.json") }

// LoadConfig 读取配置；文件不存在或解析失败时保留默认值。
func LoadConfig() {
	raw, err := os.ReadFile(ConfigPath())
	if err != nil {
		return
	}
	c := defaultConfig()
	if err := json.Unmarshal(raw, &c); err != nil {
		return
	}
	cfgMu.Lock()
	cfg = c
	cfgMu.Unlock()
}

// GetConfig 返回当前配置副本。
func GetConfig() Config {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return cfg
}

// UpdateConfig 在持锁状态下修改并整体落盘。
func UpdateConfig(edit func(*Config)) error {
	cfgMu.Lock()
	edit(&cfg)
	snapshot := cfg
	cfgMu.Unlock()
	return saveConfig(snapshot)
}

func saveConfig(c Config) error {
	dir := platform.DataDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// 先写临时文件再 rename，避免断电/崩溃留下半截 JSON
	tmp := ConfigPath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, ConfigPath())
}

// AutoOpenWeb 返回「启动后自动打开 web」当前值。
func AutoOpenWeb() bool { return GetConfig().AutoOpenWeb }

// DshPathFromConfig 返回设置里手填的 dsh 路径。
func DshPathFromConfig() string { return GetConfig().DshPath }

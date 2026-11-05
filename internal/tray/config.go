package tray

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Config 是本程序的持久化配置，存在 %LOCALAPPDATA%\dsh-tray\config.json。
type Config struct {
	// AutoOpenWeb 为 true 时启动 dsh 不带 --no-open（dsh 自己会打开浏览器）；
	// 为 false 时带 --no-open，启动后不弹浏览器。
	AutoOpenWeb bool `json:"autoOpenWeb"`

	// Proxy 是用户输入的代理地址（ip:端口），空串表示直连。
	Proxy string `json:"proxy,omitempty"`
	// ProxyScheme 是探测/指定出来的协议：socks5 / http / https。
	ProxyScheme string `json:"proxyScheme,omitempty"`

	// AutoStart 是「开机自启」的一份同步副本（真正生效的是注册表 Run 键）。
	// 用指针区分"没写过"和"明确写了 false"。
	AutoStart *bool `json:"autoStart,omitempty"`

	// GithubToken 是可选的 GitHub 个人访问令牌，只用于查/下载本程序的 Release。
	// 匿名访问 api.github.com 只有 60 次/小时、按出口 IP 算，公司/代理出口常被用光；
	// 带上令牌是 5000 次/小时，基本不会再撞限流。留空则依次尝试环境变量与 gh CLI。
	GithubToken string `json:"githubToken,omitempty"`

	// DshPath 是用户手工指定的 dsh 启动器路径（可粘贴，也可点「浏览」选）。
	// 空串表示自动探测。用 mise / volta / scoop 等版本管理器的机器上，
	// 托盘从注册表 Run 键冷启动时 PATH 里没有 shims 目录，手填一次最省事。
	DshPath string `json:"dshPath,omitempty"`
}

func defaultConfig() Config {
	return Config{AutoOpenWeb: true} // 默认：启动后自动打开 web（与历史行为一致）
}

var (
	cfgMu sync.Mutex
	cfg   = defaultConfig()
)

func configPath() string {
	return filepath.Join(dataDir(), "config.json")
}

// loadConfig 读取配置；文件不存在或解析失败时保留默认值。
func loadConfig() {
	raw, err := os.ReadFile(configPath())
	if err != nil {
		return
	}
	c := defaultConfig() // 缺字段时按默认值处理
	if err := json.Unmarshal(raw, &c); err != nil {
		return
	}
	cfgMu.Lock()
	cfg = c
	cfgMu.Unlock()
}

// updateConfig 在持锁状态下修改配置，然后整体落盘。
func updateConfig(edit func(*Config)) error {
	cfgMu.Lock()
	edit(&cfg)
	snapshot := cfg
	cfgMu.Unlock()

	if err := os.MkdirAll(dataDir(), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), append(raw, '\n'), 0o644)
}

func autoOpenWeb() bool {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return cfg.AutoOpenWeb
}

func setAutoOpenWeb(v bool) error {
	return updateConfig(func(c *Config) { c.AutoOpenWeb = v })
}

// proxyConfig 返回用户配置的代理地址与协议（地址为空表示直连）。
func proxyConfig() (addr, scheme string) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return cfg.Proxy, cfg.ProxyScheme
}

// setProxyConfig 保存代理地址与探测出的协议。
func setProxyConfig(addr, scheme string) error {
	return updateConfig(func(c *Config) {
		c.Proxy = addr
		c.ProxyScheme = scheme
	})
}

// setProxyScheme 只更新协议（运行时从 SOCKS5 回退到 HTTP 时用）。
func setProxyScheme(scheme string) error {
	return updateConfig(func(c *Config) { c.ProxyScheme = scheme })
}

// gitHubTokenFromConfig 返回配置里手填的令牌（空串表示没填）。
func gitHubTokenFromConfig() string {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return cfg.GithubToken
}

// setGitHubToken 保存/清空手填的令牌。
func setGitHubToken(token string) error {
	return updateConfig(func(c *Config) { c.GithubToken = token })
}

// dshPathFromConfig 返回手填的 dsh 路径（空串表示自动探测）。
func dshPathFromConfig() string {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return cfg.DshPath
}

// setDshPath 保存/清空手填的 dsh 路径。
func setDshPath(p string) error {
	return updateConfig(func(c *Config) { c.DshPath = p })
}

// syncAutoStartMirror 把注册表里真实的开机自启状态同步一份到 config.json。
// 注册表始终是权威来源，这里只做副本，不用副本去覆盖用户的手动改动。
func syncAutoStartMirror() {
	v := autoStartEnabled()
	_ = updateConfig(func(c *Config) { c.AutoStart = &v })
}

// startArgs 根据配置拼出启动 dsh 的参数（抽出来便于单测）。
func startArgs() []string {
	if autoOpenWeb() {
		return []string{"web"}
	}
	return []string{"web", "--no-open"}
}

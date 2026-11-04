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
}

var (
	cfgMu sync.Mutex
	cfg   = Config{AutoOpenWeb: true} // 默认：启动后自动打开 web（与历史行为一致）
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
	c := Config{AutoOpenWeb: true} // 缺字段时也按 true 处理
	if err := json.Unmarshal(raw, &c); err != nil {
		return
	}
	cfgMu.Lock()
	cfg = c
	cfgMu.Unlock()
}

func autoOpenWeb() bool {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return cfg.AutoOpenWeb
}

func setAutoOpenWeb(v bool) error {
	cfgMu.Lock()
	cfg.AutoOpenWeb = v
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

// startArgs 根据配置拼出启动 dsh 的参数（抽出来便于单测）。
func startArgs() []string {
	if autoOpenWeb() {
		return []string{"web"}
	}
	return []string{"web", "--no-open"}
}

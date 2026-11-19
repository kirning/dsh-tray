package core

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/kirning/dsh-tray/internal/platform"
)

// UpgradePlan 描述“把已安装的 dsh 升到指定版本”要执行的命令。
type UpgradePlan struct {
	Exe  string
	Args []string
	How  string // 人类可读的命令行，写日志用
}

// PlanDshUpgrade 按 dsh 的实际安装方式选择升级命令：
//
//	· mise 管理的（shim 位于 mise/shims）→ mise use -g npm:@deepseek-ai/dsh@x
//	· volta 管理的（shim 位于 Volta/bin）→ volta install @deepseek-ai/dsh@x
//	· 其余（npm 全局）                    → npm install -g @deepseek-ai/dsh@x
//
// 不能一律用 npm -g：mise / volta 装出来的包不在 npm 全局前缀里，
// 用 npm 装会多出一份，PATH 里生效的仍是旧版本。
func PlanDshUpgrade(version string) (UpgradePlan, error) {
	version = strings.TrimSpace(strings.TrimPrefix(version, "v"))
	if version == "" {
		return UpgradePlan{}, fmt.Errorf("没有指定要升级到的版本")
	}
	pkg := "@deepseek-ai/dsh@" + version

	if shim, ok := findLauncher("dsh"); ok {
		dir := strings.ToLower(filepath.Dir(shim))
		switch {
		case strings.Contains(dir, "mise") && strings.Contains(dir, "shims"):
			if mise, ok := platform.FindOnPath("mise"); ok {
				return UpgradePlan{
					Exe:  mise,
					Args: []string{"use", "-g", "npm:" + pkg},
					How:  "mise use -g npm:" + pkg,
				}, nil
			}
		case strings.Contains(dir, "volta"):
			if volta, ok := platform.FindOnPath("volta"); ok {
				return UpgradePlan{
					Exe:  volta,
					Args: []string{"install", pkg},
					How:  "volta install " + pkg,
				}, nil
			}
		}
	}

	l, err := resolveNpmLaunch()
	if err != nil {
		return UpgradePlan{}, fmt.Errorf("找不到 npm，无法自动升级：%w", err)
	}
	return UpgradePlan{
		Exe:  l.exe,
		Args: l.argv("install", "-g", pkg),
		How:  "npm install -g " + pkg,
	}, nil
}

// UpgradeDsh 执行升级，返回命令输出（失败时也返回，便于在弹框里展示原因）。
// 超时给 10 分钟：npm 全局安装 + 解包在慢网络下可能要几分钟。
func UpgradeDsh(version string) (string, error) {
	plan, err := PlanDshUpgrade(version)
	if err != nil {
		return "", err
	}
	Logf("开始升级 dsh：%s", plan.How)
	c := GetConfig()
	out, runErr := platform.RunHiddenTimeoutEnv(10*time.Minute,
		ProxyEnv(c.Proxy, c.ProxyScheme), plan.Exe, plan.Args...)
	if runErr != nil {
		Logf("升级 dsh 失败：%v", runErr)
		return out, fmt.Errorf("%s 失败：%v", plan.How, runErr)
	}
	Logf("升级 dsh 完成：%s", plan.How)
	return out, nil
}

// TailText 截取字符串末尾 n 个字符（用于展示命令输出）。
func TailText(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

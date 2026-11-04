package tray

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------- semver ----------

type semver struct {
	major, minor, patch int
	pre                 []string
}

func parseSemver(s string) (semver, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var pre []string
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre = strings.Split(s[i+1:], ".")
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return semver{}, false
	}
	var nums [3]int
	for i := 0; i < 3; i++ {
		if i < len(parts) && parts[i] != "" {
			n, err := strconv.Atoi(parts[i])
			if err != nil {
				return semver{}, false
			}
			nums[i] = n
		}
	}
	return semver{nums[0], nums[1], nums[2], pre}, true
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// compareSemver 返回 -1 / 0 / 1，遵循 semver 2.0 的优先级规则。
func compareSemver(a, b semver) int {
	if a.major != b.major {
		return sign(a.major - b.major)
	}
	if a.minor != b.minor {
		return sign(a.minor - b.minor)
	}
	if a.patch != b.patch {
		return sign(a.patch - b.patch)
	}
	// 无预发布标识的版本更高
	if len(a.pre) == 0 && len(b.pre) == 0 {
		return 0
	}
	if len(a.pre) == 0 {
		return 1
	}
	if len(b.pre) == 0 {
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		x, y := a.pre[i], b.pre[i]
		xn, yn := isNumeric(x), isNumeric(y)
		switch {
		case xn && yn:
			xi, _ := strconv.Atoi(x)
			yi, _ := strconv.Atoi(y)
			if xi != yi {
				return sign(xi - yi)
			}
		case xn && !yn:
			return -1 // 数字标识低于字母标识
		case !xn && yn:
			return 1
		default:
			if c := strings.Compare(x, y); c != 0 {
				return c
			}
		}
	}
	if len(a.pre) != len(b.pre) {
		return sign(len(a.pre) - len(b.pre))
	}
	return 0
}

func sign(n int) int {
	if n > 0 {
		return 1
	}
	if n < 0 {
		return -1
	}
	return 0
}

// ---------- 版本查询 ----------

// CurrentVersion 读取本地已安装的 dsh 版本。
func CurrentVersion() (string, error) {
	dir := DshDir()
	if dir == "" {
		return "", fmt.Errorf("找不到已安装的 @deepseek-ai/dsh")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return "", err
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return "", err
	}
	if pkg.Version == "" {
		return "", fmt.Errorf("package.json 里没有 version 字段")
	}
	return pkg.Version, nil
}

// npmViewVersions 执行 npm view <pkg> versions --json。
// 用 node 直接跑 npm-cli.js，避免经 cmd.exe 起批处理壳。
func npmViewVersions(pkg string) (string, error) {
	nodeExe, _, err := ResolveDsh()
	if err != nil {
		return "", err
	}
	npmCli := filepath.Join(filepath.Dir(nodeExe), "node_modules", "npm", "bin", "npm-cli.js")
	if !fileExists(npmCli) {
		return "", fmt.Errorf("找不到 npm-cli.js: %s", npmCli)
	}
	return runHiddenTimeout(40*time.Second, nodeExe, npmCli, "view", pkg, "versions", "--json")
}

// CheckUpdates 返回当前版本，以及所有高于当前版本的可用版本（已升序去重）。
func CheckUpdates(pkg string) (current string, newer []string, err error) {
	current, err = CurrentVersion()
	if err != nil {
		return "", nil, err
	}
	raw, err := npmViewVersions(pkg)
	if err != nil {
		return current, nil, err
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return current, nil, fmt.Errorf("npm 没有返回任何版本")
	}
	var all []string
	if strings.HasPrefix(raw, "[") {
		if err := json.Unmarshal([]byte(raw), &all); err != nil {
			return current, nil, fmt.Errorf("解析 npm 输出失败: %v", err)
		}
	} else {
		// 只有一个版本时 npm 返回裸字符串
		all = []string{strings.Trim(raw, `"`)}
	}

	cur, ok := parseSemver(current)
	if !ok {
		return current, nil, fmt.Errorf("无法解析当前版本号 %q", current)
	}
	seen := map[string]bool{}
	for _, v := range all {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		sv, ok := parseSemver(v)
		if !ok {
			continue
		}
		if compareSemver(sv, cur) > 0 {
			seen[v] = true
			newer = append(newer, v)
		}
	}
	sort.Slice(newer, func(i, j int) bool {
		a, _ := parseSemver(newer[i])
		b, _ := parseSemver(newer[j])
		return compareSemver(a, b) < 0
	})
	return current, newer, nil
}

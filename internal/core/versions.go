package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kirning/dsh-tray/internal/platform"
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
			return -1
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

// CompareVersions 供外部比较两个版本号，无法解析时按字符串比较。
func CompareVersions(a, b string) int {
	x, ok1 := parseSemver(a)
	y, ok2 := parseSemver(b)
	if ok1 && ok2 {
		return compareSemver(x, y)
	}
	return strings.Compare(a, b)
}

// ---------- 当前版本 ----------

var semverRe = regexp.MustCompile(`\d+\.\d+\.\d+(?:-[0-9A-Za-z][0-9A-Za-z.-]*)?`)

// FirstSemver 返回文本里第一个像版本号的片段。
func FirstSemver(s string) string { return semverRe.FindString(StripANSI(s)) }

// DshDir 返回已安装 dsh 包目录（拿不到时返回空串）。
func DshDir() string {
	inst, err := ResolveDshInstall()
	if err != nil || len(inst.prefix) == 0 {
		return ""
	}
	bin := inst.prefix[0]
	if !strings.HasSuffix(bin, "bin.js") {
		return ""
	}
	// .../@deepseek-ai/dsh/lib/bin.js → .../@deepseek-ai/dsh
	return filepath.Dir(filepath.Dir(bin))
}

// CurrentVersion 读取本地已安装的 dsh 版本。
// 手填了路径就只认它，否则会读到另一份安装的 package.json。
func CurrentVersion() (string, error) {
	if strings.TrimSpace(DshPathFromConfig()) != "" || strings.TrimSpace(os.Getenv("DSH_TRAY_DSH")) != "" {
		return versionFromLauncher()
	}
	if dir := DshDir(); dir != "" {
		if raw, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
			var pkg struct {
				Version string `json:"version"`
			}
			if json.Unmarshal(raw, &pkg) == nil && pkg.Version != "" {
				return pkg.Version, nil
			}
		}
	}
	return versionFromLauncher()
}

// versionFromLauncher 执行 `dsh --version` 从输出里抠版本号。
func versionFromLauncher() (string, error) {
	inst, err := ResolveDshInstall()
	if err != nil {
		return "", fmt.Errorf("找不到已安装的 @deepseek-ai/dsh")
	}
	out, runErr := platform.RunHiddenTimeout(25*time.Second, inst.exe, inst.argv("--version")...)
	if v := FirstSemver(out); v != "" {
		return v, nil
	}
	if runErr != nil {
		return "", fmt.Errorf("执行 dsh --version 失败：%v", runErr)
	}
	return "", fmt.Errorf("dsh --version 没有输出可识别的版本号")
}

// ---------- npm ----------

// resolveNpmLaunch 定位怎么跑 npm：优先 node + npm-cli.js，退化为 npm 启动器。
func resolveNpmLaunch() (launch, error) {
	tryNode := func(ne string) (launch, bool) {
		candidates := []string{
			filepath.Join(filepath.Dir(ne), "node_modules", "npm", "bin", "npm-cli.js"),
			// Linux 全局安装：/usr/lib/node_modules/npm/bin/npm-cli.js
			filepath.Join(filepath.Dir(filepath.Dir(ne)), "lib", "node_modules", "npm", "bin", "npm-cli.js"),
		}
		for _, npmCli := range candidates {
			if st, err := os.Stat(npmCli); err == nil && !st.IsDir() {
				return launch{exe: ne, prefix: []string{npmCli}, how: "node " + npmCli}, true
			}
		}
		return launch{}, false
	}
	if ne, ok := platform.FindOnPath("node"); ok {
		if l, ok2 := tryNode(ne); ok2 {
			return l, nil
		}
	}
	for _, root := range platform.CandidateRoots() {
		if ne, _, ok := platform.ProbeRoot(root); ok {
			if l, ok2 := tryNode(ne); ok2 {
				return l, nil
			}
		}
	}
	if shim, ok := findLauncher("npm"); ok {
		if l, ok2 := launchFromLauncher(shim); ok2 {
			return l, nil
		}
	}
	return launch{}, fmt.Errorf("找不到 npm（既没有 npm-cli.js，也没有 npm 启动器）")
}

func npmViewVersions(pkg string) (string, error) {
	l, err := resolveNpmLaunch()
	if err != nil {
		return "", err
	}
	c := GetConfig()
	return platform.RunHiddenTimeoutEnv(40*time.Second, ProxyEnv(c.Proxy, c.ProxyScheme),
		l.exe, l.argv("view", pkg, "versions", "--json")...)
}

// CheckUpdates 返回当前版本，以及所有高于当前版本的可用版本（升序去重）。
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

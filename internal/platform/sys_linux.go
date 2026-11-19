//go:build linux

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var lockFile *os.File

// DataDir 运行期数据目录，遵循 XDG：$XDG_CONFIG_HOME/dsh-tray，默认 ~/.config/dsh-tray。
func DataDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		if h, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(h, ".config")
		} else {
			base = os.TempDir()
		}
	}
	return filepath.Join(base, "dsh-tray")
}

// WorkDir 启动 dsh 时用的工作目录（用户主目录）。
func WorkDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		if st, e := os.Stat(h); e == nil && st.IsDir() {
			return h
		}
	}
	return os.TempDir()
}

// DshHome 返回 dsh 配置根目录（尊重 DSH_HOME）。
func DshHome() string {
	if h := os.Getenv("DSH_HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".dsh")
	}
	return ""
}

// RunHidden Linux 没有“隐藏窗口”概念，保留函数名让上层代码保持平台无关。
func RunHidden(name string, args ...string) (string, error) {
	return RunHiddenTimeoutEnv(30*time.Second, nil, name, args...)
}

func RunHiddenTimeout(timeout time.Duration, name string, args ...string) (string, error) {
	return RunHiddenTimeoutEnv(timeout, nil, name, args...)
}

func RunHiddenTimeoutEnv(timeout time.Duration, extraEnv []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	// 独立进程组，便于超时/停止时连同子进程一起收掉
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	done := make(chan struct{})
	var out []byte
	var runErr error
	go func() {
		out, runErr = cmd.CombinedOutput()
		close(done)
	}()

	select {
	case <-done:
		return string(out), runErr
	case <-time.After(timeout):
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		<-done
		return string(out), fmt.Errorf("命令超时: %s", name)
	}
}

// ProcessAlive 用 signal 0 探测进程；EPERM 说明进程存在但无权操作，也算存活。
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return err == syscall.EPERM
}

// PidsByPort 先用 ss（iproute2），拿不到再退回 lsof。
func PidsByPort(port int) []int {
	if out, err := RunHiddenTimeout(5*time.Second, "ss", "-ltnp"); err == nil && out != "" {
		suffix := ":" + strconv.Itoa(port)
		var pids []int
		for _, line := range strings.Split(out, "\n") {
			f := strings.Fields(line)
			if len(f) < 4 || !strings.EqualFold(f[0], "LISTEN") {
				continue
			}
			// Local Address:Port 在第 4 列（State Recv-Q Send-Q Local:Port ...）
			if !strings.HasSuffix(f[3], suffix) {
				continue
			}
			for _, m := range pidRe.FindAllStringSubmatch(line, -1) {
				if pid, e := strconv.Atoi(m[1]); e == nil && !containsInt(pids, pid) {
					pids = append(pids, pid)
				}
			}
		}
		if len(pids) > 0 {
			return pids
		}
	}
	if out, err := RunHiddenTimeout(5*time.Second, "lsof", "-ti", fmt.Sprintf("tcp:%d", port), "-sTCP:LISTEN"); err == nil {
		var pids []int
		for _, line := range strings.Split(out, "\n") {
			if pid, e := strconv.Atoi(strings.TrimSpace(line)); e == nil {
				pids = append(pids, pid)
			}
		}
		return pids
	}
	return nil
}

var pidRe = regexp.MustCompile(`pid=(\d+)`)

func containsInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// KillTree 先 TERM 进程组，2 秒后仍在则 KILL。
func KillTree(pid int) error {
	if pid <= 0 {
		return nil
	}
	// 启动时用了 Setpgid，负 PID 表示整个进程组
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	for i := 0; i < 20; i++ {
		if !ProcessAlive(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	return nil
}

// OpenBrowser 用 xdg-open 打开默认浏览器。
func OpenBrowser(url string) error {
	cmd := exec.Command("xdg-open", url)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd.Start()
}

// ---------- 开机自启（XDG autostart） ----------

func autostartPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		if h, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(h, ".config")
		}
	}
	return filepath.Join(base, "autostart", "dsh-tray.desktop")
}

func AutostartEnabled() bool {
	_, err := os.Stat(autostartPath())
	return err == nil
}

func SetAutostart(enable bool) error {
	p := autostartPath()
	if !enable {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	desktop := "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=dsh tray\n" +
		"Comment=dsh web tray manager\n" +
		"Exec=\"" + exe + "\"\n" +
		"Terminal=false\n" +
		"X-GNOME-Autostart-enabled=true\n"
	return os.WriteFile(p, []byte(desktop), 0o644)
}

// ReplaceSelf Linux 允许直接 rename 覆盖正在运行的可执行文件
// （进程持有旧 inode，新文件成为新的目录项）。
func ReplaceSelf(newPath string) (oldPath string, err error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if err := os.Rename(newPath, exe); err != nil {
		// 跨文件系统时 rename 会失败，退化为复制 + 删源
		raw, rerr := os.ReadFile(newPath)
		if rerr != nil {
			return "", err
		}
		if werr := os.WriteFile(exe, raw, 0o755); werr != nil {
			return "", werr
		}
		_ = os.Remove(newPath)
	}
	return "", nil
}

// AcquireSingleInstance 用 flock 锁文件保证单实例。
func AcquireSingleInstance() (release func(), ok bool) {
	dir := DataDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return func() {}, true
	}
	f, err := os.OpenFile(filepath.Join(dir, "instance.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return func() {}, true
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return func() {}, false
	}
	lockFile = f
	return func() {
		if lockFile != nil {
			_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
			_ = lockFile.Close()
			lockFile = nil
		}
	}, true
}

// ---------- 安装路径探测 ----------

func executableExts() []string { return []string{""} }

// ProbeRoot 兼容两种 Linux 布局：
//
//	/usr/local      → bin/node + lib/node_modules/@deepseek-ai/dsh/lib/bin.js
//	~/.npm-global   → bin/node? 通常只有 lib/node_modules，node 可能不在同一 root
func ProbeRoot(root string) (nodeExe, binJS string, ok bool) {
	candidates := []string{
		filepath.Join(root, "bin", "node"),
		filepath.Join(root, "node"),
	}
	bins := []string{
		filepath.Join(root, "lib", "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js"),
		filepath.Join(root, "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js"),
	}
	var bj string
	for _, b := range bins {
		if fileExists(b) {
			bj = b
			break
		}
	}
	if bj == "" {
		return "", "", false
	}
	for _, n := range candidates {
		if fileExists(n) {
			return n, bj, true
		}
	}
	return "", "", false
}

// CandidateRoots 返回可能的 node 全局安装前缀。
func CandidateRoots() []string {
	home, _ := os.UserHomeDir()
	seen := map[string]bool{}
	var roots []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		roots = append(roots, p)
	}
	add("/usr/local")
	add("/usr")
	if ne, ok := FindOnPath("node"); ok {
		// /usr/bin/node → 前缀 /usr；/home/u/.nvm/.../bin/node → 往上两级
		d := filepath.Dir(ne)
		add(d)
		add(filepath.Dir(d))
	}
	add(filepath.Join(home, ".npm-global"))
	add(filepath.Join(home, ".local"))
	for _, base := range []string{
		filepath.Join(home, ".nvm", "versions", "node"),
		filepath.Join(home, ".local", "share", "mise", "installs", "node"),
		filepath.Join(home, ".local", "share", "fnm", "node-versions"),
		filepath.Join(home, ".volta", "tools", "image", "node"),
	} {
		for _, pattern := range []string{"v*", "*", filepath.Join("*", "installation")} {
			m, _ := filepath.Glob(filepath.Join(base, pattern))
			for i := len(m) - 1; i >= 0; i-- {
				add(m[i])
			}
		}
	}
	return roots
}

// LauncherDirs 返回可能放着 dsh / npm 启动器的目录。
func LauncherDirs() []string {
	home, _ := os.UserHomeDir()
	seen := map[string]bool{}
	var dirs []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		dirs = append(dirs, p)
	}
	add(filepath.Join(home, ".local", "share", "mise", "shims"))
	add(filepath.Join(home, ".volta", "bin"))
	add(filepath.Join(home, ".local", "bin"))
	add(filepath.Join(home, "bin"))
	add("/usr/local/bin")
	add("/usr/bin")
	add(filepath.Join(home, ".local", "share", "pnpm"))
	return dirs
}

// FindOnPath 在 PATH 里查找可执行文件（Linux 没有扩展名，检查可执行位）。
func FindOnPath(name string) (string, bool) {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, true
		}
	}
	return "", false
}

// ChildProcAttr 启动子进程时使用的平台属性（独立进程组，便于整组收掉）。
func ChildProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// WrapScript Linux 上脚本自带 shebang，直接执行即可。
func WrapScript(p string) (exe string, prefix []string, how string) {
	return p, nil, p
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

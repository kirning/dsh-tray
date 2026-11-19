//go:build windows

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// mutexName 用 -wails 后缀与旧版（walk 实现）区分，
// 便于两代程序并存对比；正式替换旧版后可改回。
const (
	createNoWindow = 0x08000000
	mutexName      = `Local\dsh-tray-wails-single-instance`
)

var singleMutex windows.Handle

// DataDir 运行期数据目录：%LOCALAPPDATA%\dsh-tray
func DataDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "dsh-tray")
}

// WorkDir 启动 dsh 时用的工作目录。
// 注意：绝不能是 dsh 安装目录（Program Files 只读，dsh 会卡住不监听端口）。
func WorkDir() string {
	if h := os.Getenv("USERPROFILE"); h != "" {
		if st, err := os.Stat(h); err == nil && st.IsDir() {
			return h
		}
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return os.TempDir()
}

// DshHome 返回 dsh 配置根目录（尊重 DSH_HOME）。
func DshHome() string {
	if h := os.Getenv("DSH_HOME"); h != "" {
		return h
	}
	if u := os.Getenv("USERPROFILE"); u != "" {
		return filepath.Join(u, ".dsh")
	}
	return ""
}

// RunHidden 执行命令并返回合并输出（隐藏窗口）。
func RunHidden(name string, args ...string) (string, error) {
	return RunHiddenTimeoutEnv(30*time.Second, nil, name, args...)
}

// RunHiddenTimeout 带超时版本；超时会强杀子进程。
func RunHiddenTimeout(timeout time.Duration, name string, args ...string) (string, error) {
	return RunHiddenTimeoutEnv(timeout, nil, name, args...)
}

// RunHiddenTimeoutEnv 在子进程环境里追加环境变量（用于给 npm 传代理）。
func RunHiddenTimeoutEnv(timeout time.Duration, extraEnv []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}

	done := make(chan struct{})
	var out []byte
	var runErr error
	go func() {
		out, runErr = cmd.CombinedOutput()
		close(done)
	}()

	select {
	case <-done:
		return decodeConsole(out), runErr
	case <-time.After(timeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		return decodeConsole(out), fmt.Errorf("命令超时: %s", name)
	}
}

// decodeConsole Windows 控制台默认 GBK，统一转 UTF-8。
func decodeConsole(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	ascii := true
	for _, c := range b {
		if c >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return string(b)
	}
	if s, err := simplifiedchinese.GBK.NewDecoder().Bytes(b); err == nil {
		return string(s)
	}
	return string(b)
}

// ProcessAlive 判断 PID 是否存活（STILL_ACTIVE = 259）。
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	const processQueryLimitedInformation = 0x1000
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	const stillActive = 259
	return code == stillActive
}

// PidsByPort 用 netstat 找监听指定端口的 PID。
func PidsByPort(port int) []int {
	out, err := RunHidden("netstat.exe", "-ano", "-p", "TCP")
	if err != nil && out == "" {
		return nil
	}
	suffix := ":" + strconv.Itoa(port)
	var pids []int
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || !strings.EqualFold(f[3], "LISTENING") {
			continue
		}
		if !strings.HasSuffix(f[1], suffix) {
			continue
		}
		if pid, err := strconv.Atoi(f[4]); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// KillTree 结束进程树（dsh 会再 fork 子进程）。
func KillTree(pid int) error {
	if pid <= 0 {
		return nil
	}
	return exec.Command("taskkill.exe", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}

// OpenBrowser 用默认浏览器打开 URL。
func OpenBrowser(url string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	target, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, target, nil, nil, 1)
}

// ---------- 开机自启（HKCU Run） ----------

const (
	runKeyPath   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValueName = "DshTray"
)

func AutostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runValueName)
	return err == nil && v != ""
}

func SetAutostart(enable bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !enable {
		if err := k.DeleteValue(runValueName); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue(runValueName, `"`+exe+`"`)
}

// ReplaceSelf 用新文件替换当前 exe。
// Windows 允许重命名“正在运行”的镜像，所以先把自身改名成 .old.exe，
// 再把新文件挪到原位；旧文件由调用方在下次启动时清理。
func ReplaceSelf(newPath string) (oldPath string, err error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	oldPath = filepath.Join(filepath.Dir(exe), "dsh-tray.old.exe")
	_ = os.Remove(oldPath)
	if err := os.Rename(exe, oldPath); err != nil {
		return "", fmt.Errorf("无法重命名当前程序: %w", err)
	}
	if err := os.Rename(newPath, exe); err != nil {
		_ = os.Rename(oldPath, exe) // 回滚
		return "", err
	}
	return oldPath, nil
}

// AcquireSingleInstance 用命名互斥体保证只跑一个实例。
// 返回的 release 必须在重启自身前调用，否则新进程会判定“已有实例”直接退出。
func AcquireSingleInstance() (release func(), ok bool) {
	name, _ := windows.UTF16PtrFromString(mutexName)
	h, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		if err == windows.ERROR_ALREADY_EXISTS {
			return func() {}, false
		}
		return func() {}, true
	}
	singleMutex = h
	return func() {
		if singleMutex != 0 {
			_ = windows.CloseHandle(singleMutex)
			singleMutex = 0
		}
	}, true
}

// ---------- 安装路径探测 ----------

func executableExts() []string { return []string{".exe", ".cmd", ".bat"} }

// ProbeRoot 检查某个根目录下是否同时有 node.exe 和 dsh 的 bin.js。
func ProbeRoot(root string) (nodeExe, binJS string, ok bool) {
	ne := filepath.Join(root, "node.exe")
	bj := filepath.Join(root, "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js")
	if fileExists(ne) && fileExists(bj) {
		return ne, bj, true
	}
	return "", "", false
}

// CandidateRoots 返回可能的 node 安装根目录（全局 npm 包所在处）。
func CandidateRoots() []string {
	seen := map[string]bool{}
	var roots []string
	add := func(p string) {
		if p == "" || seen[strings.ToLower(p)] {
			return
		}
		seen[strings.ToLower(p)] = true
		roots = append(roots, p)
	}
	local := os.Getenv("LOCALAPPDATA")
	appdata := os.Getenv("APPDATA")
	userProfile := os.Getenv("USERPROFILE")

	add(filepath.Join(os.Getenv("ProgramFiles"), "nodejs"))
	add(`C:\Program Files\nodejs`)
	add(filepath.Join(appdata, "npm"))
	if ne, ok := FindOnPath("node"); ok {
		add(filepath.Dir(ne))
	}
	for _, base := range []string{
		filepath.Join(appdata, "nvm"),
		filepath.Join(appdata, "fnm", "node-versions"),
		filepath.Join(local, "fnm", "node-versions"),
		filepath.Join(local, "mise", "installs", "node"),
		filepath.Join(userProfile, ".local", "share", "mise", "installs", "node"),
		filepath.Join(local, "Volta", "tools", "image", "node"),
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

// LauncherDirs 返回可能放着 dsh / npm 启动器（shim）的目录。
// mise / volta 装出来的命令只是转发器，冷启动时不在 PATH 里，必须主动找。
func LauncherDirs() []string {
	seen := map[string]bool{}
	var dirs []string
	add := func(p string) {
		if p == "" || seen[strings.ToLower(p)] {
			return
		}
		seen[strings.ToLower(p)] = true
		dirs = append(dirs, p)
	}
	local := os.Getenv("LOCALAPPDATA")
	add(filepath.Join(local, "mise", "shims"))
	add(filepath.Join(os.Getenv("USERPROFILE"), ".local", "share", "mise", "shims"))
	add(filepath.Join(local, "Volta", "bin"))
	add(filepath.Join(local, "pnpm"))
	add(filepath.Join(os.Getenv("APPDATA"), "npm"))
	add(filepath.Join(os.Getenv("USERPROFILE"), "scoop", "shims"))
	add(`C:\ProgramData\chocolatey\bin`)
	add(filepath.Join(local, "Yarn", "bin"))
	add(filepath.Join(os.Getenv("ProgramFiles"), "nodejs"))
	return dirs
}

// FindOnPath 在 PATH 里按 Windows 的可执行扩展名顺序找命令。
func FindOnPath(name string) (string, bool) {
	seen := map[string]bool{}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		dir = strings.Trim(strings.TrimSpace(dir), `"`)
		if dir == "" || seen[strings.ToLower(dir)] {
			continue
		}
		seen[strings.ToLower(dir)] = true
		for _, ext := range []string{".exe", ".cmd", ".bat", ""} {
			p := filepath.Join(dir, name+ext)
			if fileExists(p) {
				return p, true
			}
		}
	}
	return "", false
}

// ChildProcAttr 启动子进程时使用的平台属性（隐藏窗口 + 不创建控制台）。
func ChildProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

// WrapScript 把启动器包装成 (可执行文件, 参数前缀, 人类可读说明)。
// 批处理必须经 cmd.exe 才能执行。
func WrapScript(p string) (exe string, prefix []string, how string) {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".cmd", ".bat":
		comspec := os.Getenv("ComSpec")
		if comspec == "" {
			comspec = filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
		}
		return comspec, []string{"/c", p}, "cmd /c " + p
	default:
		return p, nil, p
	}
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

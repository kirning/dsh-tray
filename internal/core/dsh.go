package core

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kirning/dsh-tray/internal/platform"
)

// readyHTTPHits 是“外部实例”用 HTTP 探测判定就绪时需要连续命中的次数。
// 只对拿不到日志的场景生效（见 CheckReady）。
const readyHTTPHits = 2

// launch 描述“怎么启动一个 npm 全局命令”：
//
//   - node 模式：exe = node，prefix = [bin.js]
//   - shim 模式：exe = 启动器本身（mise/volta 的转发器），或 cmd /c 批处理
type launch struct {
	exe    string
	prefix []string
	how    string
}

func (l launch) argv(extra ...string) []string {
	out := make([]string, 0, len(l.prefix)+len(extra))
	out = append(out, l.prefix...)
	return append(out, extra...)
}

// Dsh 管理 dsh web 子进程与就绪状态。
type Dsh struct {
	mu   sync.Mutex
	cmd  *exec.Cmd
	pid  int
	port int

	logPath     string
	ready       bool
	readyOffset int64
	httpHits    int
}

func NewDsh(logPath string) *Dsh {
	return &Dsh{logPath: logPath, port: 3080}
}

// Port 返回 dsh web 监听端口（固定 3080，与本程序管理的实例保持一致）。
func (d *Dsh) Port() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.port == 0 {
		return 3080
	}
	return d.port
}

// SetPort 供自检使用（在独立端口起临时实例，避免打断正在使用的 3080）。
func (d *Dsh) SetPort(p int) { d.mu.Lock(); d.port = p; d.mu.Unlock() }

// Start 用默认参数启动（web [+ --no-open]）。
func (d *Dsh) Start() error {
	args := []string{"web"}
	if !AutoOpenWeb() {
		args = append(args, "--no-open")
	}
	return d.StartWithArgs(args)
}

// StartWithArgs 静默启动 dsh，args 为传给 bin.js 的完整参数（含子命令）。
func (d *Dsh) StartWithArgs(args []string) error {
	inst, err := ResolveDshInstall()
	if err != nil {
		Logf("启动失败：%v", err)
		return err
	}

	f, err := OpenFresh(d.logPath)
	if err != nil {
		Logf("启动失败（无法打开日志文件）：%v", err)
		return err
	}
	_, _ = f.WriteString(FreshLogHeader(args, time.Now()))

	if lockPath, removed := clearStaleCredLock(); removed {
		Logf("已清理僵尸锁: %s", lockPath)
	}

	d.mu.Lock()
	d.ready = false
	d.httpHits = 0
	d.readyOffset = FileSize(d.logPath)
	d.mu.Unlock()

	cmd := exec.Command(inst.exe, inst.argv(args...)...)
	// 工作目录必须是用户主目录：dsh 安装目录通常是只读的（Program Files / /usr/lib），
	// dsh 会把 cwd 当工作区，只读目录下会卡住不监听端口。
	cmd.Dir = platform.WorkDir()
	cmd.Stdout = f
	cmd.Stderr = f
	cmd.SysProcAttr = platform.ChildProcAttr()

	if err := cmd.Start(); err != nil {
		Logf("启动失败（无法创建进程）：%v（启动方式 %s）", err, inst.how)
		_ = f.Close()
		return err
	}
	Logf("已启动 dsh，PID=%d，工作目录=%s，启动方式=%s", cmd.Process.Pid, cmd.Dir, inst.how)
	_ = f.Close()

	d.mu.Lock()
	d.cmd = cmd
	d.pid = cmd.Process.Pid
	d.mu.Unlock()

	go func() {
		_ = cmd.Wait()
		d.mu.Lock()
		if d.cmd == cmd {
			d.cmd = nil
			d.pid = 0
		}
		d.mu.Unlock()
	}()
	return nil
}

// TrackedPid 返回本程序启动且仍存活的 PID（没有则 0）。
func (d *Dsh) TrackedPid() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pid > 0 && platform.ProcessAlive(d.pid) {
		return d.pid
	}
	return 0
}

// IsRunning 先看自己启动的进程，再探测端口（兼容外部启动的实例）。
func (d *Dsh) IsRunning() bool {
	if d.TrackedPid() > 0 {
		return true
	}
	return portListening(d.Port())
}

// Stop 关闭 dsh：优先收自己启动的进程，否则按端口找 PID。
func (d *Dsh) Stop() error {
	if pid := d.TrackedPid(); pid > 0 {
		return d.stopPid(pid)
	}
	pids := platform.PidsByPort(d.Port())
	if len(pids) == 0 {
		return fmt.Errorf("没有找到监听 %d 端口的 dsh 进程", d.Port())
	}
	var lastErr error
	for _, pid := range pids {
		if err := d.stopPid(pid); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// StopTracked 只关闭本程序启动的那个进程（自检用，避免误杀别的实例）。
func (d *Dsh) StopTracked() error {
	pid := d.TrackedPid()
	if pid == 0 {
		return fmt.Errorf("没有由本程序启动的实例")
	}
	return d.stopPid(pid)
}

func (d *Dsh) stopPid(pid int) error {
	_ = platform.KillTree(pid)
	d.mu.Lock()
	d.cmd = nil
	d.pid = 0
	d.ready = false
	d.httpHits = 0
	d.mu.Unlock()

	for i := 0; i < 50; i++ {
		if !platform.ProcessAlive(pid) {
			// 被强杀的 dsh 来不及删凭证锁，顺手清掉，避免下次启动被自己坑
			_, _ = clearStaleCredLock()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("进程 %d 未能结束", pid)
}

// SetReadyBaseline 把就绪判定的日志起点定在当前文件末尾。
//
// 只设起点，**不再用“端口在监听”直接判就绪**：dsh 绑端口后还要加载插件与 MCP，
// 那时访问并不通，提前变绿会误导。
func (d *Dsh) SetReadyBaseline() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ready = false
	d.httpHits = 0
	d.readyOffset = FileSize(d.logPath)
}

// CheckReady 判断 dsh web 是否真正可用。
//
// 唯一可靠判据是日志里出现 "dsh web: http..."——dsh 加载完插件、绑定端口后打印的访问地址。
// **明确不看“端口是否监听”**：端口先通、服务后好是常态，提前判绿会误导用户。
//
// 例外是“外部启动的实例”：它的 stdout 在别的终端里，我们拿不到那行日志，
// 只能退化为 HTTP 探测（连续 readyHTTPHits 次拿到响应才算就绪），否则会永远停在橙色。
func (d *Dsh) CheckReady() bool {
	d.mu.Lock()
	if d.ready {
		d.mu.Unlock()
		return true
	}
	offset := d.readyOffset
	pid := d.pid
	port := d.port
	d.mu.Unlock()

	if LogHasReadyMarker(d.logPath, offset) {
		d.mu.Lock()
		d.ready = true
		d.mu.Unlock()
		return true
	}

	// 本程序启动的实例：日志里还没出现就绪行，就是还没好，继续等
	if pid > 0 && platform.ProcessAlive(pid) {
		return false
	}

	// 外部实例：没有日志可读，用 HTTP 探测兜底
	if httpResponding(port) {
		d.mu.Lock()
		d.httpHits++
		hits := d.httpHits
		if hits >= readyHTTPHits {
			d.ready = true
		}
		d.mu.Unlock()
		return hits >= readyHTTPHits
	}
	d.mu.Lock()
	d.httpHits = 0
	d.mu.Unlock()
	return false
}

// ReadyByLog 只按日志标志判断（自检用）。
func (d *Dsh) ReadyByLog() bool {
	d.mu.Lock()
	offset := d.readyOffset
	d.mu.Unlock()
	return LogHasReadyMarker(d.logPath, offset)
}

// ClearReady 复位就绪缓存（进程已退出时调用）。
func (d *Dsh) ClearReady() {
	d.mu.Lock()
	d.ready = false
	d.httpHits = 0
	d.mu.Unlock()
}

// DebugState 返回内部状态快照，供 --statustest 诊断就绪判定用。
func (d *Dsh) DebugState() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return fmt.Sprintf("pid=%d ready=%v readyOffset=%d logSize=%d httpHits=%d port=%d",
		d.pid, d.ready, d.readyOffset, FileSize(d.logPath), d.httpHits, d.port)
}

// httpResponding 只用于“外部实例”的就绪探测：能拿到任意 HTTP 响应，
// 说明 dsh 的 HTTP 服务已经在处理请求。1.5s 超时，避免 dsh 卡住时拖死轮询。
func httpResponding(port int) bool {
	if port == 0 {
		port = 3080
	}
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}

// portListening 用 TCP 连接探测端口是否在监听（跨平台，不依赖 netstat/ss）。
func portListening(port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// ---------- dsh 定位 ----------

// ResolveDshInstall 定位“怎么启动 dsh”。
//
// 顺序：设置里手填 → 环境变量 → 常见 node 根目录里的 bin.js
// → PATH/shim 目录里的 dsh 启动器 → PATH 里 node 所在目录。
func ResolveDshInstall() (launch, error) {
	if p := strings.TrimSpace(DshPathFromConfig()); p != "" {
		if l, ok := launchFromLauncher(p); ok {
			return l, nil
		}
		Logf("设置里指定的 dsh 路径不可用，改回自动探测：%s", p)
	}
	if p := strings.TrimSpace(os.Getenv("DSH_TRAY_DSH")); p != "" {
		if l, ok := launchFromLauncher(p); ok {
			return l, nil
		}
		Logf("环境变量 DSH_TRAY_DSH 指向的文件不存在，改回自动探测：%s", p)
	}
	for _, root := range platform.CandidateRoots() {
		if ne, bj, ok := platform.ProbeRoot(root); ok {
			return launch{exe: ne, prefix: []string{bj}, how: "node " + bj}, nil
		}
	}
	if shim, ok := findLauncher("dsh"); ok {
		if ne, bj, ok2 := platform.ProbeRoot(filepath.Dir(shim)); ok2 {
			return launch{exe: ne, prefix: []string{bj}, how: "node " + bj}, nil
		}
		if bj := binJSFromShim(shim); bj != "" {
			if ne, ok2 := platform.FindOnPath("node"); ok2 {
				return launch{exe: ne, prefix: []string{bj}, how: "node " + bj}, nil
			}
			// bin.js 往上四层通常就是 node 安装根（Windows 的 nvm 布局）
			root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(bj))))
			if ne, _, ok2 := platform.ProbeRoot(root); ok2 {
				return launch{exe: ne, prefix: []string{bj}, how: "node " + bj}, nil
			}
		}
		if l, ok2 := launchFromLauncher(shim); ok2 {
			return l, nil
		}
	}
	if ne, ok := platform.FindOnPath("node"); ok {
		if _, bj, ok2 := platform.ProbeRoot(filepath.Dir(ne)); ok2 {
			return launch{exe: ne, prefix: []string{bj}, how: "node " + bj}, nil
		}
	}
	return launch{}, errDshNotFound()
}

// findLauncher 先查 PATH，再查常见启动器目录。
func findLauncher(name string) (string, bool) {
	if p, ok := platform.FindOnPath(name); ok {
		return p, true
	}
	for _, dir := range platform.LauncherDirs() {
		if p, ok := platform.FindExecutable(filepath.Join(dir, name)); ok {
			return p, true
		}
	}
	return "", false
}

// launchFromLauncher 把一个启动器文件包装成可执行方案。
// Windows 的 .cmd/.bat 必须经 cmd.exe；其他平台直接执行（脚本自带 shebang）。
func launchFromLauncher(p string) (launch, bool) {
	if p == "" {
		return launch{}, false
	}
	cand, ok := platform.FindExecutable(p)
	if !ok {
		return launch{}, false
	}
	exe, prefix, how := platform.WrapScript(cand)
	return launch{exe: exe, prefix: prefix, how: how}, true
}

// binJSFromShim 从 npm / 版本管理器生成的 shim 里抠出 bin.js 的绝对路径。
// Windows 的 dsh.cmd 形如 "%dp0%\node_modules\@deepseek-ai\dsh\lib\bin.js"；
// Linux 的 shell 包装通常包含基于 $basedir 的相对路径。
func binJSFromShim(shimPath string) string {
	raw, err := os.ReadFile(shimPath)
	if err != nil || len(raw) > 64*1024 {
		return ""
	}
	s := strings.ReplaceAll(string(raw), `\`, "/")
	const rel = "node_modules/@deepseek-ai/dsh/lib/bin.js"
	idx := strings.Index(s, rel)
	if idx < 0 {
		return ""
	}
	prefix := s[:idx]
	if j := strings.LastIndexAny(prefix, "\"' \t\r\n=;"); j >= 0 {
		prefix = prefix[j+1:]
	}
	prefix = strings.TrimSuffix(prefix, "/")

	var p string
	lower := strings.ToLower(prefix)
	switch {
	case strings.Contains(lower, "%dp0%") || strings.Contains(lower, "$basedir") || strings.Contains(lower, "${basedir}"):
		p = filepath.Join(filepath.Dir(shimPath), filepath.FromSlash(rel))
	case strings.HasPrefix(prefix, "/") || (len(prefix) > 2 && prefix[1] == ':'):
		p = filepath.FromSlash(prefix + "/" + rel)
	default:
		return ""
	}
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p
	}
	return ""
}

// errDshNotFound 是找不到 dsh 时给用户的指引。
func errDshNotFound() error {
	return errors.New(`找不到 dsh，无法启动。

本程序按以下顺序查找，都没找到：
  1. 「设置 → dsh 程序路径」里手填的路径
  2. 环境变量 DSH_TRAY_DSH
  3. 常见 node 安装目录（npm 全局前缀、nvm / fnm / mise / volta）
  4. PATH 与版本管理器的 shim 目录里的 dsh

请这样处理：
  · 终端执行：where dsh（Windows）/ which dsh（Linux）
  · 能看到路径 → 把该路径粘贴进「设置 → dsh 程序路径」并保存
  · 提示找不到 → 先在终端里执行 dsh --version 确认可用，
    再把它的安装目录加入 PATH 或设置 DSH_TRAY_DSH 环境变量`)
}

// ---------- 僵尸凭证锁 ----------

// clearStaleCredLock 清理“持有者已死”的凭证写锁。
//
// dsh 写 ~/.dsh/.credentials.yaml 时用 .credentials.yaml.lock（内容是 PID）。
// 进程被强杀来不及删锁，残留锁会让下一次启动直接失败：
//
//	startup failed: ... atomic-write: timed out waiting for the writer lock
//
// 判据必须是 PID 存活，而不是文件年龄——年龄阈值容易误删正在写入的锁。
func clearStaleCredLock() (path string, removed bool) {
	home := platform.DshHome()
	if home == "" {
		return "", false
	}
	lock := filepath.Join(home, ".credentials.yaml.lock")
	raw, err := os.ReadFile(lock)
	if err != nil {
		return lock, false
	}
	if pid, perr := strconv.Atoi(strings.TrimSpace(string(raw))); perr == nil {
		if platform.ProcessAlive(pid) {
			return lock, false
		}
	} else if st, e := os.Stat(lock); e != nil || time.Since(st.ModTime()) < 60*time.Second {
		return lock, false
	}
	if err := os.Remove(lock); err != nil {
		return lock, false
	}
	return lock, true
}

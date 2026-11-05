package tray

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const dshPort = 3080

const createNoWindow = 0x08000000

// Dsh 负责启动/停止 dsh web 进程并维护日志文件。
type Dsh struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	pid     int
	logPath string
	port    int

	// ready 表示"已就绪"：日志里出现过就绪标志，或端口监听后兜底超时
	ready bool
	// readyOffset 是这个实例启动时刻的日志偏移，只扫描这之后的内容
	readyOffset int64
	// portSince 记录端口首次被探测到监听的时间，用于兜底判定
	portSince time.Time
}

// readyPortGrace 是"端口已监听但日志迟迟没有就绪标志"的兜底等待时间。
// 正常情况下 dsh 绑定端口后会立刻打印 "dsh web: http..."，
// 只有当加 --no-open 或日志格式变化时才需要靠这个兜底。
const readyPortGrace = 10 * time.Second

func NewDsh(logPath string) *Dsh {
	return &Dsh{logPath: logPath, port: dshPort}
}

// SetPort 覆盖监听端口（自检会用临时端口，避免误判正式实例）。
func (d *Dsh) SetPort(p int) {
	d.mu.Lock()
	d.port = p
	d.mu.Unlock()
}

// Port 返回当前管理的端口。
func (d *Dsh) Port() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.port
}

// candidateRoots 返回可能的 node 安装根目录（全局 npm 包所在处）。
func candidateRoots() []string {
	seen := map[string]bool{}
	var roots []string
	add := func(p string) {
		if p == "" || seen[strings.ToLower(p)] {
			return
		}
		seen[strings.ToLower(p)] = true
		roots = append(roots, p)
	}
	add(filepath.Join(os.Getenv("ProgramFiles"), "nodejs"))
	add(`C:\Program Files\nodejs`)
	// npm 的按用户安装前缀：dsh 装在 %APPDATA%\npm\node_modules 下
	add(filepath.Join(os.Getenv("APPDATA"), "npm"))
	// node 本身在 PATH 里时，它的安装目录也可能就是全局包根
	if ne, ok := findOnPath("node"); ok {
		add(filepath.Dir(ne))
	}
	if appdata := os.Getenv("APPDATA"); appdata != "" {
		if m, _ := filepath.Glob(filepath.Join(appdata, "nvm", "v*")); len(m) > 0 {
			// 版本目录名按字符串倒序，尽量取新的
			for i := len(m) - 1; i >= 0; i-- {
				add(m[i])
			}
		}
	}
	return roots
}

// probeRoot 检查某个根目录下是否同时有 node.exe 和 dsh 的 bin.js。
func probeRoot(root string) (string, string, bool) {
	ne := filepath.Join(root, "node.exe")
	bj := filepath.Join(root, "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js")
	if fileExists(ne) && fileExists(bj) {
		return ne, bj, true
	}
	return "", "", false
}

// findOnPath 在 PATH 里按 Windows 的可执行扩展名顺序找命令。
func findOnPath(name string) (string, bool) {
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

// binJSFromShim 从 npm 生成的 dsh.cmd 里抠出 bin.js 的绝对路径。
// shim 里通常长这样：  "%dp0%\node_modules\@deepseek-ai\dsh\lib\bin.js"
func binJSFromShim(shimPath string) string {
	raw, err := os.ReadFile(shimPath)
	if err != nil {
		return ""
	}
	s := strings.ReplaceAll(string(raw), `\`, "/")
	const rel = "node_modules/@deepseek-ai/dsh/lib/bin.js"
	idx := strings.Index(s, rel)
	if idx < 0 {
		return ""
	}
	prefix := s[:idx]
	if j := strings.LastIndexAny(prefix, "\"' \t\r\n="); j >= 0 {
		prefix = prefix[j+1:]
	}
	prefix = strings.TrimSuffix(prefix, "/")
	if prefix == "" {
		return ""
	}
	var p string
	if strings.Contains(strings.ToLower(prefix), "%dp0%") {
		// 相对 shim 所在目录
		p = filepath.Join(filepath.Dir(shimPath), filepath.FromSlash(rel))
	} else {
		p = filepath.FromSlash(prefix + "/" + rel)
	}
	if fileExists(p) {
		return p
	}
	return ""
}

// errDshNotFound 是找不到 dsh 时给用户的指引（用户明确要求提示加到环境变量）。
func errDshNotFound() error {
	return fmt.Errorf(`找不到 dsh，无法启动。

本程序按以下顺序查找，都没找到：
  1. 常见安装目录：%s、%s、%%APPDATA%%\npm、nvm 各版本目录
  2. 系统环境变量 Path 里的 dsh 命令

请这样处理：
  · 打开命令提示符，执行：dsh --version
  · 如果能输出版本号 → 说明 dsh 已可用但本程序没识别到。
    请执行：where dsh
    把输出结果告诉我，或直接重装本程序的最新版本。
  · 如果提示「不是内部或外部命令」→ 说明 dsh 没加进 Path：
    把 npm 全局目录（一般是 %%APPDATA%%\npm，或 node 的安装目录）
    加入「系统属性 → 环境变量 → Path」，保存后重启本程序。`,
		filepath.Join(os.Getenv("ProgramFiles"), "nodejs"), `C:\Program Files\nodejs`)
}

// ResolveDsh 定位 node.exe 与 dsh 的 bin.js。
// 直接用 node 启动 bin.js，避免经过 cmd.exe 的批处理壳，窗口更干净。
//
// 查找顺序：常见安装根目录 → PATH 里的 dsh 启动器反推 → node 所在目录。
// 这样即使用户把 npm 前缀装到了非常规位置，只要 dsh 在 PATH 里就能用。
func ResolveDsh() (nodeExe, binJS string, err error) {
	// 1) 常见安装根目录
	for _, root := range candidateRoots() {
		if ne, bj, ok := probeRoot(root); ok {
			return ne, bj, nil
		}
	}

	// 2) PATH 里的 dsh：先看同目录能不能凑齐 node+bin.js，再从 shim 内容抠路径
	if shim, ok := findOnPath("dsh"); ok {
		if ne, bj, ok := probeRoot(filepath.Dir(shim)); ok {
			return ne, bj, nil
		}
		if bj := binJSFromShim(shim); bj != "" {
			if ne, ok := findOnPath("node"); ok {
				return ne, bj, nil
			}
			// node 不在 PATH，但 bin.js 的同级目录里通常有 node
			if ne := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(bj)))), "node.exe"); fileExists(ne) {
				return ne, bj, nil
			}
		}
	}

	// 3) node 在 PATH：用它的安装目录当全局包根
	if ne, ok := findOnPath("node"); ok {
		if _, bj, ok2 := probeRoot(filepath.Dir(ne)); ok2 {
			return ne, bj, nil
		}
	}

	return "", "", errDshNotFound()
}

// DshDir 返回已安装 dsh 包的目录。
func DshDir() string {
	if _, binJS, err := ResolveDsh(); err == nil {
		return filepath.Dir(filepath.Dir(binJS)) // bin.js → lib → dsh
	}
	for _, root := range candidateRoots() {
		dir := filepath.Join(root, "node_modules", "@deepseek-ai", "dsh")
		if fileExists(filepath.Join(dir, "package.json")) {
			return dir
		}
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// Start 静默启动 dsh web，输出追加写入日志。
// 是否带 --no-open 由配置项「启动后自动打开web」决定。
func (d *Dsh) Start() error {
	if d.IsRunning() {
		trayLogf("启动被忽略：dsh web 已在运行")
		return fmt.Errorf("dsh web 已在运行")
	}
	return d.StartWithArgs(startArgs())
}

// StartWithArgs 静默启动 dsh，args 为传给 bin.js 的完整参数（含子命令）。
func (d *Dsh) StartWithArgs(args []string) error {
	nodeExe, binJS, err := ResolveDsh()
	if err != nil {
		trayLogf("启动失败：%v", err)
		return err
	}
	// 日志策略：每次启动 dsh 都清空，只保留本次启动的内容（用户选定）。
	// dsh 的 stdout/stderr 直接继承这个句柄，之后的全部输出都落在本次日志里。
	f, err := openFreshLog(d.logPath)
	if err != nil {
		trayLogf("启动失败（无法打开日志文件）：%v", err)
		return err
	}
	_, _ = f.WriteString(freshLogHeader(args, time.Now()))

	// 上次异常退出可能留下僵尸凭证锁，先尝试清理，否则本次启动会直接失败
	if lockPath, removed := clearStaleCredLock(); removed {
		trayLogf("已清理僵尸锁: %s", lockPath)
	}

	// 就绪扫描只从本次启动之后的内容开始，避免把历史日志里的旧标志当成本次就绪
	d.mu.Lock()
	d.ready = false
	d.portSince = time.Time{}
	d.readyOffset = fileSize(d.logPath)
	d.mu.Unlock()

	cmd := exec.Command(nodeExe, append([]string{binJS}, args...)...)
	// 注意：工作目录不能用 dsh 安装目录（Program Files 只读，dsh 启动会卡住）。
	// 用用户主目录，与手动在终端里跑 dsh 的行为一致。
	cmd.Dir = workDir()
	cmd.Stdout = f
	cmd.Stderr = f
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}

	if err := cmd.Start(); err != nil {
		trayLogf("启动失败（无法创建进程）：%v", err)
		f.Close()
		return err
	}
	trayLogf("已启动 dsh，PID=%d，工作目录=%s", cmd.Process.Pid, cmd.Dir)
	// 子进程已复制句柄，父进程这边可以关掉。
	f.Close()

	d.mu.Lock()
	d.cmd = cmd
	d.pid = cmd.Process.Pid
	d.mu.Unlock()

	go func() {
		cmd.Wait()
		d.mu.Lock()
		if d.cmd == cmd {
			d.cmd = nil
			d.pid = 0
		}
		d.mu.Unlock()
	}()
	return nil
}

// TrackedPid 返回本程序启动的进程 PID（没有则 0）。
func (d *Dsh) TrackedPid() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pid > 0 && processAlive(d.pid) {
		return d.pid
	}
	return 0
}

// StopTracked 只关闭本程序启动的那个进程（用于自检，避免误杀其他实例）。
func (d *Dsh) StopTracked() error {
	pid := d.TrackedPid()
	if pid == 0 {
		return fmt.Errorf("没有由本程序启动的实例")
	}
	_ = killTree(pid) // taskkill 在进程已退出时返回非 0，不以它的返回值为准
	d.mu.Lock()
	d.cmd = nil
	d.pid = 0
	d.ready = false
	d.portSince = time.Time{}
	d.mu.Unlock()

	for i := 0; i < 50; i++ {
		if !processAlive(pid) {
			// 被强杀的 dsh 来不及删凭证锁，这里顺手清掉，避免下次启动被自己坑
			_, _ = clearStaleCredLock()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("进程 %d 未能结束", pid)
}

// workDir 返回启动 dsh 时使用的工作目录（用户主目录）。
func workDir() string {
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

// IsRunning 判断 dsh web 进程是否还活着：先看自己启动的进程，再探测端口。
func (d *Dsh) IsRunning() bool {
	d.mu.Lock()
	pid := d.pid
	d.mu.Unlock()
	if pid > 0 && processAlive(pid) {
		return true
	}
	return portListening(d.Port())
}

// SetReadyBaseline 把"就绪判定"的日志起点定在当前文件末尾。
// 用于程序刚启动、dsh 可能已经被外部启动的场景：不去扫描历史日志里的旧就绪标志。
func (d *Dsh) SetReadyBaseline() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ready = false
	d.portSince = time.Time{}
	d.readyOffset = fileSize(d.logPath)
}

// CheckReady 判断 dsh web 是否已经真正可用。
// 主判据：日志里出现 "dsh web: http..."（dsh 绑定端口后打印的访问地址）。
// 兜底：端口已监听超过 readyPortGrace 仍没有标志（例如用 --no-open 时 dsh 不打印该行）。
func (d *Dsh) CheckReady() bool {
	d.mu.Lock()
	if d.ready {
		d.mu.Unlock()
		return true
	}
	offset := d.readyOffset
	d.mu.Unlock()

	ready := logHasReadyMarker(d.logPath, offset)

	if !ready && portListening(d.Port()) {
		// 只记录"首次看到端口监听"的时刻；后续探测抖动不重置计时，
		// 否则 dsh 初始化期间端口闪断会把兜底时间无限往后推。
		d.mu.Lock()
		if d.portSince.IsZero() {
			d.portSince = time.Now()
		}
		gracePassed := time.Since(d.portSince) >= readyPortGrace
		d.mu.Unlock()
		ready = gracePassed
	}

	if ready {
		d.mu.Lock()
		d.ready = true
		d.mu.Unlock()
	}
	return ready
}

// ReadyByLog 只按"日志标志"判断是否就绪（自检用，便于区分是哪种信号触发的）。
func (d *Dsh) ReadyByLog() bool {
	d.mu.Lock()
	offset := d.readyOffset
	d.mu.Unlock()
	return logHasReadyMarker(d.logPath, offset)
}

// ClearReady 复位就绪缓存（进程已退出时调用）。
func (d *Dsh) ClearReady() {
	d.mu.Lock()
	d.ready = false
	d.portSince = time.Time{}
	d.mu.Unlock()
}

// dshHome 返回 dsh 配置根目录（尊重 DSH_HOME）。
func dshHome() string {
	if h := os.Getenv("DSH_HOME"); h != "" {
		return h
	}
	if u := os.Getenv("USERPROFILE"); u != "" {
		return filepath.Join(u, ".dsh")
	}
	return ""
}

// clearStaleCredLock 清理"持有者已死"的凭证写锁。
//
// 背景：dsh 写 ~/.dsh/.credentials.yaml 时用 .credentials.yaml.lock（内容是 PID）。
// 进程被强杀（taskkill /F）来不及删锁，残留锁会让下一次启动直接失败：
//
//	dsh: startup failed: 1 required plugin did not activate
//	  connection (required) ... atomic-write: timed out waiting for the writer lock
//
// 判据：锁里的 PID 已不存在才删；PID 还活着说明确有写入中，绝不碰。
func clearStaleCredLock() (path string, removed bool) {
	return clearStaleCredLockIn(dshHome())
}

func clearStaleCredLockIn(home string) (path string, removed bool) {
	if home == "" {
		return "", false
	}
	lock := filepath.Join(home, ".credentials.yaml.lock")
	raw, err := os.ReadFile(lock)
	if err != nil {
		return lock, false
	}
	if pid, perr := strconv.Atoi(strings.TrimSpace(string(raw))); perr == nil {
		if processAlive(pid) {
			return lock, false // 持有者还活着
		}
	} else {
		// 内容不是 PID：只在文件明显陈旧时才当残留
		if st, e := os.Stat(lock); e != nil || time.Since(st.ModTime()) < 60*time.Second {
			return lock, false
		}
	}
	if err := os.Remove(lock); err != nil {
		return lock, false
	}
	return lock, true
}

// logHasReadyMarker 从指定偏移开始扫描日志，找 dsh web 打印的就绪标志。
func logHasReadyMarker(path string, offset int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return false
		}
	}
	buf := make([]byte, 512*1024)
	n, _ := f.Read(buf)
	if n <= 0 {
		return false
	}
	text := stripANSI(string(buf[:n]))
	return strings.Contains(text, "dsh web: http") ||
		strings.Contains(text, "opening the default browser")
}

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;?]*[a-zA-Z]")

func stripANSI(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	return ansiRe.ReplaceAllString(s, "")
}

func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

func portListening(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 350*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// lastStartupFailure 从最近一次启动段落里提取失败原因（没有则返回空串）。
// 只认最后一个 "===== ... 启动 dsh ... =====" 之后的内容，避免把上次的旧错误报出来。
func lastStartupFailure(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	const tail = 160 * 1024
	if len(raw) > tail {
		raw = raw[len(raw)-tail:]
	}
	text := stripANSI(string(raw))

	if i := strings.LastIndex(text, "====="); i >= 0 {
		text = text[i:]
	}
	j := strings.Index(text, "startup failed")
	if j < 0 {
		return ""
	}
	msg := strings.TrimSpace(text[j:])
	// 只保留前面若干行，避免弹窗过长
	lines := strings.Split(msg, "\r\n")
	if len(lines) < 2 {
		lines = strings.Split(msg, "\n")
	}
	const maxLines = 16
	if len(lines) > maxLines {
		lines = append(lines[:maxLines], "…（完整内容见日志）")
	}
	return strings.Join(lines, "\r\n")
}

// Stop 关闭 dsh web（连同子进程树）。
func (d *Dsh) Stop() error {
	var targets []int

	d.mu.Lock()
	if d.pid > 0 && processAlive(d.pid) {
		targets = append(targets, d.pid)
	}
	d.mu.Unlock()

	// 端口上的监听进程（可能是用户手动启动的实例）
	for _, pid := range findPidsByPort(d.Port()) {
		if !containsInt(targets, pid) {
			targets = append(targets, pid)
		}
	}

	if len(targets) == 0 {
		return fmt.Errorf("dsh web 未在运行")
	}

	var lastErr error
	for _, pid := range targets {
		if err := killTree(pid); err != nil {
			lastErr = err
		}
	}

	// 等待端口释放
	for i := 0; i < 40; i++ {
		if len(findPidsByPort(d.Port())) == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	d.mu.Lock()
	d.cmd = nil
	d.pid = 0
	d.ready = false
	d.portSince = time.Time{}
	d.mu.Unlock()

	// 强杀后 dsh 来不及删凭证锁，顺手清理，免得下次启动被自己的残留锁挡住
	_, _ = clearStaleCredLock()

	if lastErr != nil && len(findPidsByPort(d.Port())) > 0 {
		trayLogf("停止 dsh 未完全成功：%v", lastErr)
		return lastErr
	}
	trayLogf("已停止 dsh web（目标 PID: %v）", targets)
	return nil
}

func containsInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func processAlive(pid int) bool {
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

// findPidsByPort 用 netstat 解析监听指定端口的进程 PID。
func findPidsByPort(port int) []int {
	out, err := runHidden("netstat.exe", "-ano", "-p", "TCP")
	if err != nil {
		return nil
	}
	needle := fmt.Sprintf(":%d", port)
	var pids []int
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "LISTENING") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		// 本地地址可能是 0.0.0.0:3080 / 127.0.0.1:3080 / [::]:3080
		if !strings.HasSuffix(fields[1], needle) {
			continue
		}
		if pid, err := strconv.Atoi(fields[len(fields)-1]); err == nil && pid > 0 {
			if !containsInt(pids, pid) {
				pids = append(pids, pid)
			}
		}
	}
	return pids
}

func killTree(pid int) error {
	_, err := runHidden("taskkill.exe", "/PID", strconv.Itoa(pid), "/T", "/F")
	return err
}

// Shutdown 在退出前兜底清理。
func (d *Dsh) Shutdown() {
	if d.IsRunning() {
		d.Stop()
	}
}

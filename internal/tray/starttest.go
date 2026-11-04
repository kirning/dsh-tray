package tray

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// starttest 在 3999 端口起一个临时 dsh web 实例，验证：
// 静默启动 → 日志出现就绪标志 → 状态转"运行中" → 能干净停止。
// 全程不触碰正式端口 3080 上的实例。
func starttest() {
	res := &strings.Builder{}
	w := func(format string, a ...any) {
		fmt.Fprintf(res, format+"\r\n", a...)
	}
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(res, "PANIC: %v\r\n", r)
		}
		_ = os.MkdirAll(dataDir(), 0o755)
		_ = os.WriteFile(filepath.Join(dataDir(), "starttest-report.txt"), []byte(res.String()), 0o644)
	}()

	const testPort = 3999
	w("=== starttest %s ===", time.Now().Format("2006-01-02 15:04:05"))

	d := NewDsh(filepath.Join(dataDir(), "starttest.txt"))
	d.SetPort(testPort)
	_ = os.Remove(d.logPath)

	t0 := time.Now()
	w("[ .. ] 启动 dsh web --port %d --no-open", testPort)
	if err := d.StartWithArgs([]string{"web", "--port", fmt.Sprint(testPort), "--no-open"}); err != nil {
		w("[FAIL] StartWithArgs: %v", err)
		return
	}
	pid := d.TrackedPid()
	w("[ OK ] 进程已启动 pid=%d", pid)

	// 先等端口监听（最多 120 秒：实测冷启动 8~110 秒不等）
	portAt := -1.0
	dead := false
	for i := 0; i < 400; i++ {
		if portListening(testPort) {
			portAt = time.Since(t0).Seconds()
			break
		}
		if !processAlive(pid) {
			dead = true
			break
		}
		time.Sleep(300 * time.Millisecond)
	}

	// 再等"就绪"判定成立（日志标志，或端口兜底宽限期）
	readyAt := -1.0
	if !dead && portAt >= 0 {
		for i := 0; i < 130; i++ {
			if d.CheckReady() {
				readyAt = time.Since(t0).Seconds()
				break
			}
			if !processAlive(pid) {
				dead = true
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
	markAt := -1.0
	if d.ReadyByLog() {
		markAt = readyAt
	}

	if dead {
		w("[FAIL] 进程提前退出")
	} else {
		if portAt >= 0 {
			w("[ OK ] 端口 %d 监听于 %.1fs", testPort, portAt)
		} else {
			w("[FAIL] 端口 %d 一直未监听", testPort)
		}
		if markAt >= 0 {
			w("[ OK ] 日志就绪标志出现于 %.1fs", markAt)
		} else {
			w("[说明] 日志里没有就绪标志 —— 加 --no-open 时 dsh 不打印那两行，属预期")
		}
		if readyAt >= 0 {
			w("[ OK ] CheckReady() 于 %.1fs 变为 true", readyAt)
		} else {
			w("[FAIL] CheckReady() 一直为 false")
		}
		w("[ .. ] ReadyByLog() = %v（--no-open 下预期 false）", d.ReadyByLog())
		w("[ .. ] IsRunning() = %v（应为 true）", d.IsRunning())
	}

	// 打印日志里那两行关键内容，确认格式
	if raw, err := os.ReadFile(d.logPath); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(line, "dsh web:") {
				w("   日志: %s", line)
			}
		}
	}

	w("[ .. ] 停止（只杀本程序启动的进程）")
	if err := d.StopTracked(); err != nil {
		w("[FAIL] StopTracked: %v", err)
	} else {
		w("[ OK ] 已停止")
	}

	time.Sleep(1500 * time.Millisecond)
	if _, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", testPort), 300*time.Millisecond); err == nil {
		w("[FAIL] 端口 %d 仍被占用", testPort)
	} else {
		w("[ OK ] 端口 %d 已释放", testPort)
	}
	if processAlive(pid) {
		w("[FAIL] pid %d 仍然存活", pid)
	} else {
		w("[ OK ] pid %d 已退出", pid)
	}
	if d.CheckReady() {
		w("[FAIL] 停止后 CheckReady 仍为 true")
	} else {
		w("[ OK ] 停止后 CheckReady 复位为 false")
	}
}

package tray

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 本程序自己产生的所有日志统一带这个前缀（dsh 自己的 stdout/stderr 不加）。
const trayLogPrefix = "[dsh-tray]"

var trayLogMu sync.Mutex

// trayLogf 往「查看日志」那个文件追加一行本程序日志。
// 多行消息会逐行加前缀，保证任何一行都能一眼看出是本程序写的。
func trayLogf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	line := formatTrayLog(msg, time.Now())

	trayLogMu.Lock()
	defer trayLogMu.Unlock()
	_ = os.MkdirAll(dataDir(), 0o755)
	f, err := os.OpenFile(logFilePath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}

// formatTrayLog 把一条（可能多行的）消息格式化成带 [dsh-tray] 前缀的日志行。
func formatTrayLog(msg string, at time.Time) string {
	stamp := at.Format("2006-01-02 15:04:05")
	msg = strings.ReplaceAll(msg, "\r\n", "\n")
	msg = strings.ReplaceAll(msg, "\r", "\n")
	var b strings.Builder
	for _, line := range strings.Split(msg, "\n") {
		fmt.Fprintf(&b, "%s %s %s\r\n", trayLogPrefix, stamp, line)
	}
	return b.String()
}

// openFreshLog 清空日志并打开文件，供 dsh 子进程继承 stdout/stderr。
//
// ⚠️ Windows 下**不能**把 O_TRUNC 和 O_APPEND 一起传：Go 的 syscall.Open
// 遇到这个组合会保留 GENERIC_WRITE（为了能截断），于是写操作按文件指针走、
// 不再追加到末尾。dsh 子进程和 trayLogf 是两个独立句柄，会互相覆盖日志。
// 所以分两步：先用一个句柄专门截断，再以纯 O_APPEND 重新打开。
func openFreshLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	trunc, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	if err := trunc.Close(); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

// freshLogHeader 生成每次启动 dsh 时写进日志的分隔行。
// 保留 "=====" 是为了让 lastStartupFailure 仍能定位"最近一次启动段落"。
func freshLogHeader(args []string, at time.Time) string {
	return formatTrayLog(fmt.Sprintf("===== 启动 dsh %s =====", strings.Join(args, " ")), at)
}

// readLogText 读取日志供设置窗口显示（只读）。
// 文件可能很大，只显示尾部，避免把 UI 卡住。
func readLogText() string {
	raw, err := os.ReadFile(logFilePath())
	if err != nil {
		return "(暂无日志 —— dsh web 尚未通过本程序启动过)"
	}
	const limit = 512 * 1024
	if len(raw) > limit {
		raw = append([]byte("…（内容较长，仅显示最后 512 KB）\r\n"), raw[len(raw)-limit:]...)
	}
	return stripANSI(string(raw))
}

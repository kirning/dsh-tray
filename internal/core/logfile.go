package core

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kirning/dsh-tray/internal/platform"
)

// LogPath 返回 dsh web 日志路径。
func LogPath() string { return filepath.Join(platform.DataDir(), "dsh-web.txt") }

// Logf 写一行本程序日志，统一带 [dsh-tray] 前缀。
func Logf(format string, args ...any) {
	p := LogPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(f, "[dsh-tray] %s %s\n", time.Now().Format("2006-01-02 15:04:05"), msg)
}

// OpenFresh 清空日志并返回以纯 O_APPEND 打开的句柄，供 dsh 子进程继承。
//
// ⚠️ Windows 下不能把 O_TRUNC 和 O_APPEND 一起传：Go 的 syscall.Open 会保留
// GENERIC_WRITE，写操作按文件指针走，两个句柄会互相覆盖。
// 所以分两步：先用一个句柄专门截断，再以纯 O_APPEND 重新打开。
// Linux 上同样安全，逻辑保持一份即可。
func OpenFresh(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	trunc, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	_ = trunc.Close()
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

// FileSize 返回文件大小（不存在时 0）。
func FileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

// ReadLog 读取日志全文（超过 maxBytes 时只保留尾部）。
func ReadLog(maxBytes int64) string {
	raw, err := os.ReadFile(LogPath())
	if err != nil {
		return ""
	}
	if maxBytes > 0 && int64(len(raw)) > maxBytes {
		raw = raw[int64(len(raw))-maxBytes:]
	}
	return string(raw)
}

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;?]*[a-zA-Z]")

// StripANSI 去掉终端颜色码。
func StripANSI(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	return ansiRe.ReplaceAllString(s, "")
}

// LogHasReadyMarker 从指定偏移开始扫描日志，找 dsh web 打印的就绪标志。
//
// dsh 绑定端口后会打印：
//
//	dsh web: http://127.0.0.1:3080/?token=...
//	dsh web: opening the default browser; pass --no-open to disable
func LogHasReadyMarker(path string, offset int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			return false
		}
	}
	buf := make([]byte, 512*1024)
	n, _ := f.Read(buf)
	if n <= 0 {
		return false
	}
	text := StripANSI(string(buf[:n]))
	return strings.Contains(text, "dsh web: http") ||
		strings.Contains(text, "opening the default browser")
}

// FreshLogHeader 生成每次启动写入的段落头，便于从日志里切分“本次启动”。
func FreshLogHeader(args []string, now time.Time) string {
	return fmt.Sprintf("\n===== %s 启动 dsh %s =====\n",
		now.Format("2006-01-02 15:04:05"), strings.Join(args, " "))
}

package tray

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// runHidden 执行外部命令，隐藏窗口并返回合并后的输出。
func runHidden(name string, args ...string) (string, error) {
	return runHiddenTimeoutEnv(30*time.Second, nil, name, args...)
}

// runHiddenTimeout 同上，带超时；超时会强杀进程，避免卡住 UI。
func runHiddenTimeout(timeout time.Duration, name string, args ...string) (string, error) {
	return runHiddenTimeoutEnv(timeout, nil, name, args...)
}

// runHiddenTimeoutEnv 在子进程环境里追加/覆盖若干变量（用于给 npm 设置代理）。
// env 元素格式是 "KEY=VALUE"；传 nil 表示完全继承当前环境。
func runHiddenTimeoutEnv(timeout time.Duration, env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
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
			cmd.Process.Kill()
		}
		<-done
		return decodeConsole(out), fmt.Errorf("命令超时: %s", name)
	}
}

// decodeConsole 处理 Windows 控制台默认 GBK 输出的解码。
func decodeConsole(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if s, ok := gbkToUTF8(b); ok {
		return s
	}
	return string(b)
}

func gbkToUTF8(b []byte) (string, bool) {
	if isASCII(b) {
		return string(b), true
	}
	dec := simplifiedchinese.GBK.NewDecoder()
	out, err := dec.Bytes(b)
	if err != nil {
		return "", false
	}
	return string(out), true
}

func isASCII(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 {
			return false
		}
	}
	return true
}

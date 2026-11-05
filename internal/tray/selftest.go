package tray

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// selftest 供构建后做无界面验证：把结果写到 dataDir/selftest.txt。
func selftest() {
	out := &strings.Builder{}
	w := func(format string, a ...any) {
		fmt.Fprintf(out, format+"\r\n", a...)
	}

	w("=== dsh-tray %s selftest %s ===", appVersion, time.Now().Format("2006-01-02 15:04:05"))

	l, err := resolveDshInstall()
	if err != nil {
		w("[FAIL] resolveDshInstall: %v", err)
	} else {
		w("[ OK ] 启动方式 = %s", l.how)
		w("[ OK ] exe     = %s", l.exe)
		if len(l.prefix) > 0 {
			w("[ OK ] 前缀参数 = %v", l.prefix)
		}
		if cfgPath := dshPathFromConfig(); cfgPath != "" {
			w("[ .. ] 设置里指定的路径 = %s", cfgPath)
		}
	}

	if cur, e := CurrentVersion(); e != nil {
		w("[FAIL] CurrentVersion: %v", e)
	} else {
		w("[ OK ] 当前版本 = %s", cur)
	}

	w("[ .. ] 查询 npm 版本中 ...")
	cur, newer, e := CheckUpdates(pkgName)
	if e != nil {
		w("[FAIL] CheckUpdates: %v", e)
	} else {
		w("[ OK ] 当前版本 = %s，更高版本 %d 个", cur, len(newer))
		if len(newer) > 0 {
			tail := newer
			if len(tail) > 10 {
				tail = tail[len(tail)-10:]
			}
			w("        最新几个: %s", strings.Join(tail, ", "))
		}
	}

	d := NewDsh(logFilePath())
	w("[ .. ] 运行状态 = %v", d.IsRunning())
	w("[ .. ] 日志路径 = %s", logFilePath())
	w("[ .. ] 开机自启 = %v", autoStartEnabled())

	_ = os.MkdirAll(dataDir(), 0o755)
	p := filepath.Join(dataDir(), "selftest.txt")
	if werr := os.WriteFile(p, []byte(out.String()), 0o644); werr != nil {
		return
	}
}

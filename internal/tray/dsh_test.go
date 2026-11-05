package tray

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 就绪标志识别：正常情况、带 ANSI 颜色、以及"只认本次启动之后"的偏移逻辑。
func TestLogHasReadyMarker(t *testing.T) {
	dir := t.TempDir()

	t.Run("识别 URL 行", func(t *testing.T) {
		p := filepath.Join(dir, "a.txt")
		os.WriteFile(p, []byte("hello\n\ndsh web: http://127.0.0.1:3080/?token=abc\n"), 0o644)
		if !logHasReadyMarker(p, 0) {
			t.Fatal("应识别出就绪标志")
		}
	})

	t.Run("识别 no-open 提示行", func(t *testing.T) {
		p := filepath.Join(dir, "b.txt")
		os.WriteFile(p, []byte("dsh web: opening the default browser; pass --no-open to disable\n"), 0o644)
		if !logHasReadyMarker(p, 0) {
			t.Fatal("应识别出 open 提示行")
		}
	})

	t.Run("ANSI 颜色码不干扰", func(t *testing.T) {
		p := filepath.Join(dir, "c.txt")
		os.WriteFile(p, []byte("\x1b[32mdsh web:\x1b[0m \x1b[1mhttp://127.0.0.1:3080/\x1b[0m\n"), 0o644)
		if !logHasReadyMarker(p, 0) {
			t.Fatal("去掉 ANSI 后应能识别")
		}
	})

	t.Run("偏移之前的不算", func(t *testing.T) {
		p := filepath.Join(dir, "d.txt")
		old := "dsh web: http://127.0.0.1:3080/?token=OLD\n"
		os.WriteFile(p, []byte(old), 0o644)
		off := fileSize(p)
		f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
		f.WriteString("still booting...\n")
		f.Close()
		if logHasReadyMarker(p, off) {
			t.Fatal("偏移之前的旧标志不应被当成这次就绪")
		}
	})

	t.Run("偏移之后的算", func(t *testing.T) {
		p := filepath.Join(dir, "e.txt")
		os.WriteFile(p, []byte("old stuff\n"), 0o644)
		off := fileSize(p)
		f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
		f.WriteString("dsh web: http://127.0.0.1:3080/?token=NEW\n")
		f.Close()
		if !logHasReadyMarker(p, off) {
			t.Fatal("偏移之后的新标志应被识别")
		}
	})

	t.Run("不存在的文件", func(t *testing.T) {
		if logHasReadyMarker(filepath.Join(dir, "nope.txt"), 0) {
			t.Fatal("文件不存在应返回 false")
		}
	})
}

// 僵尸凭证锁：持有者已死才清理，活着绝不碰。
func TestClearStaleCredLock(t *testing.T) {
	t.Run("PID 已死则清理", func(t *testing.T) {
		dir := t.TempDir()
		lock := filepath.Join(dir, ".credentials.yaml.lock")
		os.WriteFile(lock, []byte("999999\n"), 0o644) // 几乎不可能存在的 PID
		_, removed := clearStaleCredLockIn(dir)
		if !removed {
			t.Fatal("死 PID 的锁应被清理")
		}
		if _, err := os.Stat(lock); err == nil {
			t.Fatal("锁文件应已删除")
		}
	})

	t.Run("PID 存活则保留", func(t *testing.T) {
		dir := t.TempDir()
		lock := filepath.Join(dir, ".credentials.yaml.lock")
		os.WriteFile(lock, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644)
		_, removed := clearStaleCredLockIn(dir)
		if removed {
			t.Fatal("持有者存活的锁不能删")
		}
		if _, err := os.Stat(lock); err != nil {
			t.Fatal("锁文件应仍然存在")
		}
	})

	t.Run("目录不存在不报错", func(t *testing.T) {
		if _, removed := clearStaleCredLockIn(filepath.Join(t.TempDir(), "nope")); removed {
			t.Fatal("无锁文件时不应报告已清理")
		}
	})
}

// 启动失败原因提取：只认最后一次启动段落。
func TestLastStartupFailure(t *testing.T) {
	dir := t.TempDir()

	t.Run("能提取失败原因", func(t *testing.T) {
		p := filepath.Join(dir, "a.txt")
		os.WriteFile(p, []byte(
			"===== 2026-11-04 14:06:29  启动 dsh web =====\r\n"+
				"booting...\r\n"+
				"dsh: startup failed: 1 required plugin did not activate\r\n"+
				"Failed plugins (1):\r\n"+
				"  connection (required)\r\n"+
				"    Error: atomic-write: timed out waiting for the writer lock\r\n"), 0o644)
		msg := lastStartupFailure(p)
		if !strings.Contains(msg, "startup failed") || !strings.Contains(msg, "atomic-write") {
			t.Fatalf("提取内容不对: %q", msg)
		}
	})

	t.Run("旧段落的失败不算本次", func(t *testing.T) {
		p := filepath.Join(dir, "b.txt")
		os.WriteFile(p, []byte(
			"===== 旧的一次 =====\r\n"+
				"dsh: startup failed: old problem\r\n"+
				"===== 2026-11-04 17:44:20  启动 dsh web =====\r\n"+
				"booting...\r\n"+
				"dsh web: http://127.0.0.1:3080/\r\n"), 0o644)
		if msg := lastStartupFailure(p); msg != "" {
			t.Fatalf("不应该把旧失败当本次: %q", msg)
		}
	})

	t.Run("没有失败返回空", func(t *testing.T) {
		p := filepath.Join(dir, "c.txt")
		os.WriteFile(p, []byte("===== 2026-11-04 =====\r\nall good\r\n"), 0o644)
		if msg := lastStartupFailure(p); msg != "" {
			t.Fatalf("应返回空串，实际 %q", msg)
		}
	})
}

// 「启动后自动打开web」决定是否给 dsh 传 --no-open。
func TestStartArgs(t *testing.T) {
	old := cfg
	defer func() { cfg = old }()

	cfg = Config{AutoOpenWeb: true}
	if got := startArgs(); len(got) != 1 || got[0] != "web" {
		t.Fatalf("勾选时应为 [web]，实际 %v", got)
	}

	cfg = Config{AutoOpenWeb: false}
	got := startArgs()
	if len(got) != 2 || got[0] != "web" || got[1] != "--no-open" {
		t.Fatalf("取消勾选时应为 [web --no-open]，实际 %v", got)
	}
}

// 双击判定：间隔内算双击，间隔外不算，首次点击不算。
func TestIsDoubleClick(t *testing.T) {
	t0 := time.Now()
	if isDoubleClick(time.Time{}, t0) {
		t.Fatal("第一次点击不能算双击")
	}
	if !isDoubleClick(t0, t0.Add(120*time.Millisecond)) {
		t.Fatal("120ms 内应算双击")
	}
	if !isDoubleClick(t0, t0.Add(doubleClickWindow)) {
		t.Fatal("恰好等于阈值应算双击")
	}
	if isDoubleClick(t0, t0.Add(doubleClickWindow+50*time.Millisecond)) {
		t.Fatal("超过阈值不应算双击")
	}
}

// 双击判定阈值要落在合理区间，否则单击误触或双击失灵。
func TestDoubleClickWindowSane(t *testing.T) {
	if doubleClickWindow < 300*time.Millisecond {
		t.Fatalf("双击阈值 %v 过短，容易误判", doubleClickWindow)
	}
	if doubleClickWindow > time.Second {
		t.Fatalf("双击阈值 %v 过长，双击会失灵", doubleClickWindow)
	}
}

// 从 npm 的 dsh.cmd 里反推 bin.js 路径（PATH 兜底定位的关键）。
func TestBinJSFromShim(t *testing.T) {
	t.Run("dp0 相对形式", func(t *testing.T) {
		dir := t.TempDir()
		bj := filepath.Join(dir, "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js")
		if err := os.MkdirAll(filepath.Dir(bj), 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(bj, []byte("// stub"), 0o644)

		shim := filepath.Join(dir, "dsh.cmd")
		os.WriteFile(shim, []byte("@ECHO off\r\n\"%dp0%\\node_modules\\@deepseek-ai\\dsh\\lib\\bin.js\" %*\r\n"), 0o644)

		got := binJSFromShim(shim)
		if got == "" {
			t.Fatal("应从 shim 解析出 bin.js 路径")
		}
		if filepath.Clean(got) != filepath.Clean(bj) {
			t.Fatalf("解析结果不对:\n got %s\nwant %s", got, bj)
		}
	})

	t.Run("绝对路径形式", func(t *testing.T) {
		dir := t.TempDir()
		bj := filepath.Join(dir, "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js")
		os.MkdirAll(filepath.Dir(bj), 0o755)
		os.WriteFile(bj, []byte("// stub"), 0o644)

		shim := filepath.Join(dir, "dsh.cmd")
		// 用正斜杠的绝对路径（Git Bash 生成的 shim 常见形态）
		content := "\"" + filepath.ToSlash(filepath.Join(dir, "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js")) + "\" %*"
		os.WriteFile(shim, []byte(content), 0o644)

		if got := binJSFromShim(shim); got == "" {
			t.Fatal("绝对路径形式也应能解析")
		}
	})

	t.Run("bin.js 不存在则返回空", func(t *testing.T) {
		dir := t.TempDir()
		shim := filepath.Join(dir, "dsh.cmd")
		os.WriteFile(shim, []byte("\"%dp0%\\node_modules\\@deepseek-ai\\dsh\\lib\\bin.js\" %*\r\n"), 0o644)
		if got := binJSFromShim(shim); got != "" {
			t.Fatalf("bin.js 不存在时应返回空，实际 %q", got)
		}
	})
}

// 更新下载必须走 API 资产端点（github.com 域名在受限网络下不通）。
func TestDownloadURL(t *testing.T) {
	got := downloadURL(releaseAsset{ID: 123, URL: "https://github.com/x/y/releases/download/v1/a.exe"})
	want := "https://api.github.com/repos/" + updateRepo + "/releases/assets/123"
	if got != want {
		t.Fatalf("应优先用 API 资产端点\n got %s\nwant %s", got, want)
	}
	// 没有 id 时才回退到 browser_download_url
	got2 := downloadURL(releaseAsset{URL: "https://example.com/a.exe"})
	if got2 != "https://example.com/a.exe" {
		t.Fatalf("没有 id 时应回退，实际 %s", got2)
	}
}

func TestPortOverride(t *testing.T) {
	d := NewDsh(filepath.Join(t.TempDir(), "log.txt"))
	if d.Port() != dshPort {
		t.Fatalf("默认端口应为 %d，实际 %d", dshPort, d.Port())
	}
	d.SetPort(3999)
	if d.Port() != 3999 {
		t.Fatalf("SetPort 未生效")
	}
}

// semver 比较：预发布版本的先后关系要正确。
func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.2.1-rc.5", "0.2.1-rc.2", 1},
		{"0.2.2-alpha.2", "0.2.1-rc.5", 1},
		{"0.2.2-alpha.3", "0.2.2-alpha.2", 1},
		{"0.2.1", "0.2.1-rc.5", 1}, // 正式版高于预发布
		{"0.2.1-rc.5", "0.2.1", -1},
		{"0.2.0-rc.2", "0.2.1-rc.5", -1},
		{"0.2.1-rc.5", "0.2.1-rc.5", 0},
	}
	for _, c := range cases {
		av, ok1 := parseSemver(c.a)
		bv, ok2 := parseSemver(c.b)
		if !ok1 || !ok2 {
			t.Fatalf("解析失败: %s / %s", c.a, c.b)
		}
		if got := compareSemver(av, bv); got != c.want {
			t.Errorf("compare(%s,%s)=%d，期望 %d", c.a, c.b, got, c.want)
		}
	}
}

// 设置里粘贴的路径要能被接受：带引号、带 %VAR%、只给目录，都要归一化成可执行文件。
func TestNormalizeDshPath(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "dsh.exe")
	if err := os.WriteFile(exe, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DSH_TEST_ROOT", dir)

	t.Run("带引号的完整路径", func(t *testing.T) {
		got, err := normalizeDshPath(`"` + exe + `"`)
		if err != nil || got != exe {
			t.Fatalf("got %q, err %v", got, err)
		}
	})

	t.Run("展开百分号变量", func(t *testing.T) {
		got, err := normalizeDshPath(`%DSH_TEST_ROOT%\dsh.exe`)
		if err != nil || got != exe {
			t.Fatalf("got %q, err %v", got, err)
		}
	})

	t.Run("只给目录时补 dsh.exe", func(t *testing.T) {
		got, err := normalizeDshPath(dir)
		if err != nil || got != exe {
			t.Fatalf("got %q, err %v", got, err)
		}
	})

	t.Run("空串表示自动探测", func(t *testing.T) {
		got, err := normalizeDshPath("   ")
		if err != nil || got != "" {
			t.Fatalf("got %q, err %v", got, err)
		}
	})

	t.Run("路径不存在要报错", func(t *testing.T) {
		if _, err := normalizeDshPath(filepath.Join(dir, "nope.exe")); err == nil {
			t.Fatal("不存在的路径应报错")
		}
	})
}

// mise 之类版本管理器的启动器是 exe，直接执行；npm 的 .cmd 必须经命令行解释器。
func TestLaunchFromLauncher(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "dsh.exe")
	batch := filepath.Join(dir, "dsh.cmd")
	for _, p := range []string{exe, batch} {
		if err := os.WriteFile(p, []byte("stub"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	l, ok := launchFromLauncher(exe)
	if !ok || l.prefix != nil || l.exe != exe {
		t.Fatalf("exe 应直接执行，got %+v ok=%v", l, ok)
	}

	l, ok = launchFromLauncher(batch)
	if !ok {
		t.Fatal(".cmd 也应可用")
	}
	if len(l.prefix) != 2 || l.prefix[0] != "/c" || l.prefix[1] != batch {
		t.Fatalf("cmd 应经解释器 /c，got %+v", l)
	}
	if strings.EqualFold(filepath.Base(l.exe), filepath.Base(batch)) {
		t.Fatalf("exe 不应是批处理文件本身: %s", l.exe)
	}

	if _, ok := launchFromLauncher(filepath.Join(dir, "missing.exe")); ok {
		t.Fatal("文件不存在时不应可用")
	}
}

// mise 把命令装成 %LOCALAPPDATA%\mise\shims\dsh.exe，且只在交互 shell 里把
// 这个目录塞进 PATH；托盘冷启动时必须能自己找过去（用户机器上就是这么报"找不到 dsh"的）。
func TestFindLauncherInMiseShims(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "mise", "shims")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "dsh.exe")
	if err := os.WriteFile(exe, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCALAPPDATA", root)
	t.Setenv("PATH", "") // 确认不是碰巧从 PATH 里找到的

	got, ok := findLauncher("dsh")
	if !ok {
		t.Fatalf("应在 mise shims 目录里找到 dsh：%s", dir)
	}
	if filepath.Clean(got) != filepath.Clean(exe) {
		t.Fatalf("got %s, want %s", got, exe)
	}
}

// 版本号提取：mise 下拿不到 package.json，只能从 dsh --version 的输出里抠。
func TestFirstSemver(t *testing.T) {
	cases := map[string]string{
		"0.2.1-rc.5":                            "0.2.1-rc.5",
		"dsh 0.2.1-rc.5":                        "0.2.1-rc.5",
		"\x1b[32m@deepseek-ai/dsh\x1b[0m 0.2.1": "0.2.1",
		"node v24.19.0 / dsh 0.2.0":             "24.19.0",
		"没有版本号":                                 "",
		"v1.2.3-beta.1+build.5 已安装":             "1.2.3-beta.1",
	}
	for in, want := range cases {
		if got := firstSemver(in); got != want {
			t.Errorf("firstSemver(%q)=%q，期望 %q", in, got, want)
		}
	}
}

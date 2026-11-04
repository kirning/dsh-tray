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

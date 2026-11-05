package tray

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本程序产生的日志：每行都要有 [dsh-tray] 前缀（含多行消息）。
func TestFormatTrayLog(t *testing.T) {
	at := time.Date(2026, 11, 5, 10, 30, 0, 0, time.Local)
	got := formatTrayLog("第一行\r\n第二行\n第三行", at)
	lines := strings.Split(strings.TrimSpace(got), "\r\n")
	if len(lines) != 3 {
		t.Fatalf("多行消息应拆成 3 行，实际 %d: %q", len(lines), got)
	}
	for i, l := range lines {
		if !strings.HasPrefix(l, trayLogPrefix+" ") {
			t.Errorf("第 %d 行缺少前缀: %q", i+1, l)
		}
		if !strings.Contains(l, "2026-11-05 10:30:00") {
			t.Errorf("第 %d 行缺少时间戳: %q", i+1, l)
		}
	}
}

// 启动分隔行也要带前缀，且仍能被 lastStartupFailure 定位。
func TestFreshLogHeader(t *testing.T) {
	h := freshLogHeader([]string{"web", "--no-open"}, time.Now())
	if !strings.HasPrefix(h, trayLogPrefix) || !strings.Contains(h, "=====") {
		t.Fatalf("启动分隔行格式不对: %q", h)
	}
	p := filepath.Join(t.TempDir(), "log.txt")
	content := h + "booting\r\ndsh: startup failed: something bad\r\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg := lastStartupFailure(p); !strings.Contains(msg, "startup failed") {
		t.Fatalf("加了前缀后仍应能提取失败原因，实际 %q", msg)
	}
}

// 每次启动 dsh 都清空日志，只保留本次启动的内容。
func TestOpenFreshLogTruncates(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nested", "log.txt")
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte(strings.Repeat("上一次启动的旧内容\r\n", 20)), 0o644)

	f, err := openFreshLog(p)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(freshLogHeader([]string{"web"}, time.Now()))
	_ = f.Close()

	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "旧内容") {
		t.Fatal("旧日志没有被清掉")
	}
	if !strings.Contains(string(raw), trayLogPrefix) {
		t.Fatal("本次启动的日志没有前缀")
	}
}

// 两个独立句柄交替写日志时不能互相覆盖（子进程继承的句柄必须带 O_APPEND）。
func TestOpenFreshLogConcurrentWrites(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log.txt")
	f, err := openFreshLog(p)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("A")
	g, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = g.WriteString("B")
	_ = g.Close()
	_, _ = f.WriteString("C")
	_ = f.Close()

	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "ABC" {
		t.Fatalf("交叉写入被覆盖了（少了 O_APPEND）：实际 %q", raw)
	}
}

// 用户输入代理地址的各种写法。
func TestParseProxyInput(t *testing.T) {
	cases := []struct {
		in       string
		addr     string
		explicit string
		wantErr  bool
	}{
		{"", "", "", false},
		{"127.0.0.1:10808", "127.0.0.1:10808", "", false},
		{" 192.168.1.5:7890 ", "192.168.1.5:7890", "", false},
		{"127.0.0.1 10808", "127.0.0.1:10808", "", false},
		{"127.0.0.1：10808", "127.0.0.1:10808", "", false},
		{"socks5://127.0.0.1:10808", "127.0.0.1:10808", "socks5", false},
		{"socks5h://127.0.0.1:10808/", "127.0.0.1:10808", "socks5", false},
		{"http://127.0.0.1:7890", "127.0.0.1:7890", "http", false},
		{"https://proxy.example.com:443", "proxy.example.com:443", "https", false},
		{"[::1]:1080", "[::1]:1080", "", false},
		{"127.0.0.1", "", "", true},
		{"127.0.0.1:0", "", "", true},
		{"127.0.0.1:99999", "", "", true},
		{"127.0.0.1:abc", "", "", true},
		{"socks5://", "", "", true},
	}
	for _, c := range cases {
		addr, explicit, err := parseProxyInput(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("%q: err=%v, wantErr=%v", c.in, err, c.wantErr)
			continue
		}
		if err == nil && (addr != c.addr || explicit != c.explicit) {
			t.Errorf("%q: got (%q,%q), want (%q,%q)", c.in, addr, explicit, c.addr, c.explicit)
		}
	}
}

func TestProxyURLAndEnv(t *testing.T) {
	if u := proxyURL("", "socks5"); u != nil {
		t.Fatal("空地址不应生成代理 URL")
	}
	u := proxyURL("127.0.0.1:10808", "socks5")
	if u == nil || u.Scheme != "socks5" || u.Host != "127.0.0.1:10808" {
		t.Fatalf("socks5 URL 不对: %v", u)
	}
	u2 := proxyURL("127.0.0.1:7890", "")
	if u2 == nil || u2.Scheme != "http" {
		t.Fatalf("没写协议时应默认 http: %v", u2)
	}
	env := proxyEnv("127.0.0.1:10808", "socks5")
	if len(env) != 3 || !strings.Contains(strings.Join(env, " "), "socks5://127.0.0.1:10808") {
		t.Fatalf("子进程代理环境变量不对: %v", env)
	}
	if proxyEnv("", "") != nil {
		t.Fatal("没配代理时不应注入环境变量")
	}
}

// 本地假 SOCKS5 服务：优先判成 socks5。
func TestDetectProxySchemeSocks5(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				buf := make([]byte, 8)
				_, _ = conn.Read(buf) // SOCKS5 greeting: 05 01 00
				_, _ = conn.Write([]byte{0x05, 0x00})
			}(c)
		}
	}()

	scheme, note, err := detectProxyScheme(ln.Addr().String(), "")
	if err != nil {
		t.Fatal(err)
	}
	if scheme != "socks5" {
		t.Fatalf("应识别为 socks5，实际 %s（%s）", scheme, note)
	}
}

// 本地假 HTTP 代理：SOCKS5 握手不通（直接断开），第二次请求返回 HTTP 响应行。
func TestDetectProxySchemeFallbackHTTP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	n := 0
	go func() {
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			n++
			idx := n
			go func(conn net.Conn) {
				defer conn.Close()
				buf := make([]byte, 1024)
				_, _ = conn.Read(buf)
				if idx == 1 {
					return // 让 SOCKS5 探测失败
				}
				_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
			}(c)
		}
	}()

	scheme, note, err := detectProxyScheme(ln.Addr().String(), "")
	if err != nil {
		t.Fatal(err)
	}
	if scheme != "http" {
		t.Fatalf("SOCKS5 不通时应回退 http，实际 %s（%s）", scheme, note)
	}
}

// 连不上的代理要报错，不能悄悄写成 http。
func TestDetectProxySchemeUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	if _, _, err := detectProxyScheme(addr, ""); err == nil {
		t.Fatal("连不上代理时应返回错误")
	}
}

// 显式写出的协议优先于探测。
func TestDetectProxySchemeExplicit(t *testing.T) {
	scheme, _, err := detectProxyScheme("127.0.0.1:10808", "http")
	if err != nil || scheme != "http" {
		t.Fatalf("显式协议应被尊重，实际 %s / %v", scheme, err)
	}
	if scheme, note, err := detectProxyScheme("", ""); err != nil || scheme != "" || note == "" {
		t.Fatalf("空地址应视为直连，实际 %q / %q / %v", scheme, note, err)
	}
}

// 配置读写：代理字段与 config.json 往返一致。
func TestProxyConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir)

	old := cfg
	defer func() { cfg = old }()

	if err := setProxyConfig("127.0.0.1:10808", "socks5"); err != nil {
		t.Fatal(err)
	}
	addr, scheme := proxyConfig()
	if addr != "127.0.0.1:10808" || scheme != "socks5" {
		t.Fatalf("写入后读回不对：%q / %q", addr, scheme)
	}
	cfg = defaultConfig()
	loadConfig()
	addr, scheme = proxyConfig()
	if addr != "127.0.0.1:10808" || scheme != "socks5" {
		t.Fatalf("重新加载后不对：%q / %q", addr, scheme)
	}
	raw, err := os.ReadFile(configPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"proxy"`) {
		t.Fatalf("config.json 里没有 proxy 字段: %s", raw)
	}
}

// config.json 里缺字段时，默认值要保留（AutoOpenWeb 默认 true）。
func TestLoadConfigMissingFields(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir)
	old := cfg
	defer func() { cfg = old }()

	_ = os.MkdirAll(dataDir(), 0o755)
	_ = os.WriteFile(configPath(), []byte(`{"proxy":"1.2.3.4:8080"}`), 0o644)
	cfg = defaultConfig()
	loadConfig()
	if !autoOpenWeb() {
		t.Fatal("缺 autoOpenWeb 字段时应保持默认 true")
	}
	if addr, _ := proxyConfig(); addr != "1.2.3.4:8080" {
		t.Fatalf("代理字段没读到: %q", addr)
	}
}

package tray

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// parseProxyInput 规范用户输入的代理地址。
//
// 接受：127.0.0.1:10808 / 127.0.0.1 10808 / socks5://127.0.0.1:10808 /
// http://127.0.0.1:7890 / [::1]:1080。
// 返回 host:port 与用户显式写出的协议（没写则为空，交给 detectProxyScheme 探测）。
func parseProxyInput(s string) (addr, explicit string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", nil
	}
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "socks5h://"):
		explicit, s = "socks5", s[len("socks5h://"):]
	case strings.HasPrefix(lower, "socks5://"):
		explicit, s = "socks5", s[len("socks5://"):]
	case strings.HasPrefix(lower, "https://"):
		explicit, s = "https", s[len("https://"):]
	case strings.HasPrefix(lower, "http://"):
		explicit, s = "http", s[len("http://"):]
	}

	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "/"))
	s = strings.ReplaceAll(s, "：", ":") // 中文冒号
	if s == "" {
		return "", "", fmt.Errorf("代理地址为空")
	}

	host, port := "", ""
	if h, p, e := net.SplitHostPort(s); e == nil {
		host, port = h, p
	} else if fields := strings.Fields(s); len(fields) == 2 {
		host, port = fields[0], fields[1]
	} else {
		return "", "", fmt.Errorf("格式不对，应填 IP:端口（例：127.0.0.1:10808）")
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" {
		return "", "", fmt.Errorf("缺少 IP 或主机名")
	}
	p, e := strconv.Atoi(strings.TrimSpace(port))
	if e != nil || p < 1 || p > 65535 {
		return "", "", fmt.Errorf("端口不合法：%q（应为 1~65535）", port)
	}
	return net.JoinHostPort(host, strconv.Itoa(p)), explicit, nil
}

// proxyURL 把配置转成 http.Transport 用的代理 URL。
// Go 的 net/http 原生支持 socks5:// 与 http:// 两种 scheme。
func proxyURL(addr, scheme string) *url.URL {
	if addr == "" {
		return nil
	}
	if scheme == "" {
		scheme = "http"
	}
	return &url.URL{Scheme: scheme, Host: addr}
}

// proxyEnv 给子进程（npm）用的代理环境变量。没配代理时返回 nil（继承系统环境）。
func proxyEnv(addr, scheme string) []string {
	if addr == "" {
		return nil
	}
	if scheme == "" {
		scheme = "http"
	}
	v := scheme + "://" + addr
	return []string{"HTTP_PROXY=" + v, "HTTPS_PROXY=" + v, "ALL_PROXY=" + v}
}

// clientForProxy 构造按指定协议走代理的 http.Client。
// 不设整体 Timeout —— 下载 exe 时整个响应体读取可能远超 60s，
// 时长由调用方的 context 控制（见 doWithTimeout）。
func clientForProxy(addr, scheme string) *http.Client {
	proxyFn := http.ProxyFromEnvironment
	if u := proxyURL(addr, scheme); u != nil {
		proxyFn = http.ProxyURL(u)
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy: proxyFn,
			DialContext: (&net.Dialer{
				Timeout:   15 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

// probeSOCKS5 做一次最小的 SOCKS5 握手（无认证）。
// 服务端返回 0x05 开头的协商响应就认定是 SOCKS5。
func probeSOCKS5(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return false
	}
	buf := make([]byte, 2)
	if _, err := conn.Read(buf); err != nil {
		return false
	}
	return buf[0] == 0x05
}

// probeHTTPProxy 发一个 CONNECT 看是否返回 HTTP 响应行。
func probeHTTPProxy(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, err = fmt.Fprintf(conn,
		"CONNECT api.github.com:443 HTTP/1.1\r\nHost: api.github.com:443\r\n\r\n")
	if err != nil {
		return false
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return false
	}
	return strings.HasPrefix(strings.ToUpper(line), "HTTP/")
}

// detectProxyScheme 优先按 SOCKS5 判断，不对就改用 HTTP。
// 返回：协议、给用户看的说明、错误（连不上代理时才有）。
func detectProxyScheme(addr, explicit string) (scheme, note string, err error) {
	if addr == "" {
		return "", "未设置代理（直连）", nil
	}
	if explicit != "" {
		return explicit, "使用输入里指定的 " + strings.ToUpper(explicit) + " 协议", nil
	}
	if probeSOCKS5(addr) {
		return "socks5", "SOCKS5 握手成功", nil
	}
	if probeHTTPProxy(addr) {
		return "http", "SOCKS5 不通，已切换为 HTTP", nil
	}
	// 端口能连上但两种握手都不标准：按用户要求仍回退 HTTP，只是提示未经确认
	conn, e := net.DialTimeout("tcp", addr, 3*time.Second)
	if e != nil {
		return "", "", fmt.Errorf("连不上代理 %s：%v", addr, e)
	}
	conn.Close()
	return "http", "SOCKS5 不通，已回退 HTTP（未能确认 HTTP 握手）", nil
}

// autoFallbackProxy 在每次联网前调用：若配置的 SOCKS5 已不可用，自动改成 HTTP。
func autoFallbackProxy() {
	addr, scheme := proxyConfig()
	if addr == "" || scheme != "socks5" {
		return
	}
	if probeSOCKS5(addr) {
		return
	}
	if err := setProxyScheme("http"); err != nil {
		trayLogf("代理协议回退失败：%v", err)
		return
	}
	trayLogf("SOCKS5 代理 %s 握手失败，已自动切换为 HTTP", addr)
}

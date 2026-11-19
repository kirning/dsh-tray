package core

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ParseProxyInput 解析用户输入的代理地址。
// 支持：IP:端口、IP 端口、中文冒号、socks5:// / http:// 前缀。
// 返回 addr（host:port）与 scheme（socks5/http/https），空输入表示直连。
func ParseProxyInput(raw string) (addr, scheme string) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", ""
	}
	scheme = ""
	if i := strings.Index(s, "://"); i > 0 {
		scheme = strings.ToLower(s[:i])
		s = s[i+3:]
	}
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "：", ":")
	s = strings.ReplaceAll(s, " ", ":")
	s = strings.TrimSuffix(s, "/")
	if scheme == "" {
		scheme = "socks5"
	}
	return s, scheme
}

// ProxyEnv 生成给子进程（npm 等）用的代理环境变量。
func ProxyEnv(addr, scheme string) []string {
	if addr == "" {
		return nil
	}
	u := fmt.Sprintf("%s://%s", scheme, addr)
	return []string{
		"HTTP_PROXY=" + u,
		"HTTPS_PROXY=" + u,
		"ALL_PROXY=" + u,
		"http_proxy=" + u,
		"https_proxy=" + u,
		"all_proxy=" + u,
	}
}

// ProxyURL 把配置转成 *url.URL（给 http.Transport 用）。
func ProxyURL(addr, scheme string) (*url.URL, error) {
	if addr == "" {
		return nil, nil
	}
	return url.Parse(fmt.Sprintf("%s://%s", scheme, addr))
}

// HTTPClientFor 返回走指定代理的 http.Client；addr 为空时直连。
// 注意：不设 Client.Timeout（它把响应体读取也算进去，大文件必断）。
func HTTPClientFor(addr, scheme string) *http.Client {
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	}
	if addr != "" {
		if u, err := ProxyURL(addr, scheme); err == nil && u != nil {
			// Go 标准库原生支持 socks5:// 代理 URL，无需额外依赖
			tr.Proxy = http.ProxyURL(u)
		}
	}
	return &http.Client{Transport: tr}
}

// ProbeProxy 探测代理是否可用：SOCKS5 做一次无认证 CONNECT，HTTP 做一次 CONNECT。
func ProbeProxy(addr, scheme string) error {
	if addr == "" {
		return errors.New("未填写代理地址")
	}
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("连不上代理 %s：%w", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(6 * time.Second))

	if strings.HasPrefix(scheme, "socks") {
		return socks5Handshake(conn, "github.com:443")
	}
	return httpConnect(conn, "github.com:443")
}

func socks5Handshake(conn net.Conn, target string) error {
	// VER=5, NMETHODS=1, METHOD=0（无认证）
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return err
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return err
	}
	if resp[0] != 0x05 || resp[1] != 0x00 {
		return fmt.Errorf("SOCKS5 握手被拒绝（方法=%d）", resp[1])
	}
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	var port uint16
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		return err
	}
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, host...)
	var pb [2]byte
	binary.BigEndian.PutUint16(pb[:], port)
	req = append(req, pb[:]...)
	if _, err := conn.Write(req); err != nil {
		return err
	}
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return err
	}
	if head[1] != 0x00 {
		return fmt.Errorf("SOCKS5 CONNECT 失败（code=%d）", head[1])
	}
	return nil
}

func httpConnect(conn net.Conn, target string) error {
	req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		return err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.Contains(line, " 200") {
		return fmt.Errorf("HTTP 代理 CONNECT 失败：%s", strings.TrimSpace(line))
	}
	return nil
}

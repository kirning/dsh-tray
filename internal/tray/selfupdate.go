package tray

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// updateRepo 是本程序的发布仓库。GitHub API 在国内一般可直连；
// 若网络受限，可通过 HTTPS_PROXY 环境变量指定代理（http.ProxyFromEnvironment 会读）。
const updateRepo = "kirning/dsh-tray"

const releaseAPI = "https://api.github.com/repos/" + updateRepo + "/releases/latest"

type releaseAsset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// downloadURL 返回实际下载地址。
//
// 注意：不要用 browser_download_url —— 它在 github.com 域名下，
// 在部分网络（本机就是）根本连不通。改走 API 的资产端点：
//
//	https://api.github.com/repos/<repo>/releases/assets/<id>
//
// 它会 302 到 release-assets.githubusercontent.com（该域名可直连，无需代理）。
func downloadURL(a releaseAsset) string {
	if a.ID > 0 {
		return fmt.Sprintf("https://api.github.com/repos/%s/releases/assets/%d", updateRepo, a.ID)
	}
	return a.URL
}

type releaseInfo struct {
	Tag    string         `json:"tag_name"`
	Name   string         `json:"name"`
	Assets []releaseAsset `json:"assets"`
}

// updateClient 只限制"连不上/连上不响应"的时长，**不设整体 Timeout**。
// 原因：exe 有 8~9 MB，慢网络下读 body 很容易超过 60s，设整体超时会把下载掐断
// （实测：59s 时断了，只收到 2.9MB）。整体上限由各调用处的 context 控制。
func updateClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
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

// 各项操作的总体时长上限
const (
	metaTimeout     = 30 * time.Second
	downloadTimeout = 15 * time.Minute
)

func doWithTimeout(req *http.Request, d time.Duration) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	resp, err := updateClient().Do(req.WithContext(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	// 包装 Body，关闭时一并 cancel，避免 context 泄漏
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// latestRelease 查 GitHub 最新 release。
func latestRelease() (releaseInfo, error) {
	var rel releaseInfo
	req, err := http.NewRequest(http.MethodGet, releaseAPI, nil)
	if err != nil {
		return rel, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "dsh-tray/"+appVersion)

	resp, err := doWithTimeout(req, metaTimeout)
	if err != nil {
		return rel, fmt.Errorf("访问 GitHub 失败：%v\r\n\r\n若本机访问 GitHub 受限，可设置环境变量 HTTPS_PROXY 后重试。", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return rel, fmt.Errorf("仓库 %s 还没有发布任何 release。", updateRepo)
	default:
		return rel, fmt.Errorf("GitHub 返回 HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return rel, fmt.Errorf("解析 GitHub 响应失败：%v", err)
	}
	return rel, nil
}

// findExeAsset 在 release 里挑出 Windows 可执行文件。
func findExeAsset(rel releaseInfo) (releaseAsset, bool) {
	for _, a := range rel.Assets {
		if strings.HasSuffix(strings.ToLower(a.Name), ".exe") {
			return a, true
		}
	}
	return releaseAsset{}, false
}

// checkAppUpdate 比较本地版本与最新 release。
// 返回：最新版本号、是否有更新、可下载的 exe 资源。
func checkAppUpdate() (tag string, newer bool, asset releaseAsset, err error) {
	rel, err := latestRelease()
	if err != nil {
		return "", false, releaseAsset{}, err
	}
	tag = strings.TrimPrefix(strings.TrimSpace(rel.Tag), "v")
	if tag == "" {
		return "", false, releaseAsset{}, fmt.Errorf("release 没有 tag 信息")
	}

	remote, ok1 := parseSemver(tag)
	local, ok2 := parseSemver(appVersion)
	if !ok1 || !ok2 {
		return tag, false, releaseAsset{}, fmt.Errorf("版本号无法比较：本地 %s / 远端 %s", appVersion, tag)
	}
	if compareSemver(remote, local) <= 0 {
		return tag, false, releaseAsset{}, nil
	}

	asset, ok := findExeAsset(rel)
	if !ok {
		return tag, true, releaseAsset{}, fmt.Errorf("最新版本 %s 没有附带 exe 资源", tag)
	}
	return tag, true, asset, nil
}

// applyUpdate 下载新 exe 并替换自身。
// 做法：先把运行中的自身改名（Windows 允许重命名运行中的镜像），再把新文件挪到原位。
// 失败时会尽量回滚，调用方负责重启。
func applyUpdate(a releaseAsset) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.Abs(exe); err != nil {
		return err
	}
	dir := filepath.Dir(exe)
	newPath := filepath.Join(dir, "dsh-tray.new.exe")
	oldPath := filepath.Join(dir, "dsh-tray.old.exe")

	req, err := http.NewRequest(http.MethodGet, downloadURL(a), nil)
	if err != nil {
		return err
	}
	// API 资产端点必须带这个 Accept 才会返回二进制流而不是 JSON
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "dsh-tray/"+appVersion)

	resp, err := doWithTimeout(req, downloadTimeout)
	if err != nil {
		return fmt.Errorf("下载失败：%v\r\n\r\n若本机访问 GitHub 受限，可设置环境变量 HTTPS_PROXY 后重试。", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败：HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(newPath)
	if err != nil {
		return fmt.Errorf("无法写入新版本（目录可能没有写权限）：%v", err)
	}
	n, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("下载中断：%v", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("写入失败：%v", closeErr)
	}
	if a.Size > 0 && n != a.Size {
		_ = os.Remove(newPath)
		return fmt.Errorf("下载不完整（%d/%d 字节）", n, a.Size)
	}

	_ = os.Remove(oldPath)
	if err := os.Rename(exe, oldPath); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("无法替换旧程序（可能正在被占用或没有写权限）：%v", err)
	}
	if err := os.Rename(newPath, exe); err != nil {
		_ = os.Rename(oldPath, exe) // 回滚
		return fmt.Errorf("替换失败：%v", err)
	}
	return nil
}

// cleanupOldBinary 删除上次自更新留下的旧文件（启动时调用，失败无妨）。
func cleanupOldBinary() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	old := filepath.Join(filepath.Dir(exe), "dsh-tray.old.exe")
	if fileExists(old) {
		_ = os.Remove(old)
	}
}

// checkUpdateReport 是隐藏自检：查一次 GitHub 更新并把结果写到 updatecheck.txt。
// 会顺带把 release 里的 exe 真下载一遍（只统计字节数、随即删除），
// 用来验证"部分网络下 github.com 不通但 API 资产端点可下"这条链路真的可用。
func checkUpdateReport() {
	out := &strings.Builder{}
	fmt.Fprintf(out, "local   = %s\r\n", appVersion)
	fmt.Fprintf(out, "api     = %s\r\n", releaseAPI)

	rel, err := latestRelease()
	if err != nil {
		fmt.Fprintf(out, "ERROR   = %v\r\n", err)
		_ = os.MkdirAll(dataDir(), 0o755)
		_ = os.WriteFile(filepath.Join(dataDir(), "updatecheck.txt"), []byte(out.String()), 0o644)
		return
	}
	remote := strings.TrimPrefix(rel.Tag, "v")
	fmt.Fprintf(out, "remote  = %s\r\n", remote)

	if r, ok1 := parseSemver(remote); ok1 {
		if l, ok2 := parseSemver(appVersion); ok2 {
			fmt.Fprintf(out, "newer   = %v\r\n", compareSemver(r, l) > 0)
		}
	}

	asset, ok := findExeAsset(rel)
	if !ok {
		fmt.Fprintf(out, "asset   = (release 里没有 exe)\r\n")
	} else {
		fmt.Fprintf(out, "asset   = %s  size=%d\r\n", asset.Name, asset.Size)
		fmt.Fprintf(out, "dl-url  = %s\r\n", downloadURL(asset))

		tmp := filepath.Join(os.TempDir(), "dsh-tray-downloadtest.exe")
		_ = os.Remove(tmp)
		req, _ := http.NewRequest(http.MethodGet, downloadURL(asset), nil)
		req.Header.Set("Accept", "application/octet-stream")
		req.Header.Set("User-Agent", "dsh-tray/"+appVersion)
		resp, derr := doWithTimeout(req, downloadTimeout)
		if derr != nil {
			fmt.Fprintf(out, "download= 失败: %v\r\n", derr)
		} else {
			defer resp.Body.Close()
			f, ferr := os.Create(tmp)
			var n int64
			if ferr != nil {
				fmt.Fprintf(out, "download= 无法写入临时文件: %v\r\n", ferr)
			} else {
				n, derr = io.Copy(f, resp.Body)
				_ = f.Close()
				_ = os.Remove(tmp)
				if derr != nil {
					fmt.Fprintf(out, "download= 中断: %v（已收 %d 字节）\r\n", derr, n)
				} else {
					match := asset.Size == 0 || n == asset.Size
					fmt.Fprintf(out, "download= OK %d 字节，与 size 一致=%v\r\n", n, match)
				}
			}
		}
	}

	_ = os.MkdirAll(dataDir(), 0o755)
	_ = os.WriteFile(filepath.Join(dataDir(), "updatecheck.txt"), []byte(out.String()), 0o644)
}

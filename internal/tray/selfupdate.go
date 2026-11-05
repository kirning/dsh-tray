package tray

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// updateRepo 是本程序的发布仓库。GitHub API 在国内一般可直连；
// 若网络受限，可在「设置」里填代理地址（SOCKS5 优先，不通自动回退 HTTP）。
const updateRepo = "kirning/dsh-tray"

const releaseAPI = "https://api.github.com/repos/" + updateRepo + "/releases/latest"

// 各项操作的总体时长上限
const (
	metaTimeout     = 30 * time.Second
	downloadTimeout = 30 * time.Minute
)

type releaseAsset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

type releaseInfo struct {
	Tag    string         `json:"tag_name"`
	Name   string         `json:"name"`
	Assets []releaseAsset `json:"assets"`
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

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// doWithTimeoutClient 用一个带整体超时的 context 发请求，响应体关闭时释放 context。
func doWithTimeoutClient(req *http.Request, d time.Duration, client *http.Client) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// doWithTimeout 按「设置」里的代理发请求；若 SOCKS5 连不通，自动改用 HTTP 并记住结果。
func doWithTimeout(req *http.Request, d time.Duration) (*http.Response, error) {
	addr, scheme := proxyConfig()
	resp, err := doWithTimeoutClient(req, d, clientForProxy(addr, scheme))
	if err == nil || addr == "" || scheme != "socks5" {
		return resp, err
	}
	resp2, err2 := doWithTimeoutClient(req, d, clientForProxy(addr, "http"))
	if err2 != nil {
		return nil, err
	}
	_ = setProxyScheme("http")
	trayLogf("SOCKS5 请求失败，已自动切换为 HTTP：%v", err)
	return resp2, nil
}

// latestRelease 查 GitHub 最新 release。
func latestRelease() (releaseInfo, error) {
	var rel releaseInfo
	autoFallbackProxy()

	req, err := http.NewRequest(http.MethodGet, releaseAPI, nil)
	if err != nil {
		return rel, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "dsh-tray/"+appVersion)

	resp, err := doWithTimeout(req, metaTimeout)
	if err != nil {
		return rel, fmt.Errorf("访问 GitHub 失败：%v\r\n\r\n可在「设置」里配置代理后重试。", err)
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
	trayLogf("检查应用更新：本地版本 %s", appVersion)
	rel, err := latestRelease()
	if err != nil {
		trayLogf("检查应用更新失败：%v", err)
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
		trayLogf("检查应用更新：已是最新版本 %s", tag)
		return tag, false, releaseAsset{}, nil
	}

	asset, ok := findExeAsset(rel)
	if !ok {
		return tag, true, releaseAsset{}, fmt.Errorf("最新版本 %s 没有附带 exe 资源", tag)
	}
	trayLogf("检查应用更新：发现新版本 %s（%s，%d 字节）", tag, asset.Name, asset.Size)
	return tag, true, asset, nil
}

// progressReader 在读取响应体时按时间节流回调进度。
type progressReader struct {
	r        io.Reader
	base     int64 // 断点续传已下载的字节数
	done     int64 // 本轮已读
	total    int64
	cb       func(done, total int64)
	lastPush time.Time
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.done += int64(n)
		if p.cb != nil && (err != nil || time.Since(p.lastPush) >= 200*time.Millisecond) {
			p.lastPush = time.Now()
			p.cb(p.base+p.done, p.total)
		}
	}
	return n, err
}

// downloadAsset 把资产下载到 dst，支持断点续传与多轮重试。
//
// 慢网络实测（约 9 KB/s 时 9 MB 要十几分钟）单次请求必然被时长上限掐断，
// 所以每轮只追求"往前推进"，用 HTTP Range 从上次的字节数接着下，最多 attempts 轮。
// 服务端若不支持 Range（返回 200）则从头重下。
// onProgress 是可选的进度回调（设置窗口用来画进度条）。
func downloadAsset(dst, url string, expectSize int64, onProgress ...func(done, total int64)) error {
	const attempts = 8

	var cb func(done, total int64)
	if len(onProgress) > 0 {
		cb = onProgress[0]
	}

	for i := 0; i < attempts; i++ {
		have := fileSize(dst)
		if expectSize > 0 && have == expectSize {
			return nil
		}

		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		// API 资产端点必须带这个 Accept 才会返回二进制流而不是 JSON
		req.Header.Set("Accept", "application/octet-stream")
		req.Header.Set("User-Agent", "dsh-tray/"+appVersion)
		if have > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
		}

		resp, err := doWithTimeout(req, downloadTimeout)
		if err != nil {
			trayLogf("更新下载第 %d 轮失败：%v（已有 %d 字节）", i+1, err, have)
			if i == attempts-1 {
				return fmt.Errorf("下载失败：%v\r\n\r\n可在「设置」里配置代理后重试。", err)
			}
			continue
		}

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			resp.Body.Close()
			trayLogf("更新下载第 %d 轮返回 HTTP %d", i+1, resp.StatusCode)
			if i == attempts-1 {
				return fmt.Errorf("下载失败：HTTP %d", resp.StatusCode)
			}
			continue
		}

		// 服务端忽略 Range（返回 200）→ 已下的部分作废，从头写
		flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
		base := have
		if resp.StatusCode == http.StatusOK && have > 0 {
			flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
			base = 0
		}
		f, ferr := os.OpenFile(dst, flags, 0o644)
		if ferr != nil {
			resp.Body.Close()
			return fmt.Errorf("无法写入新版本（目录可能没有写权限）：%v", ferr)
		}
		pr := &progressReader{r: resp.Body, base: base, total: expectSize, cb: cb}
		_, cerr := io.Copy(f, pr)
		closeErr := f.Close()
		resp.Body.Close()

		if cerr == nil && closeErr == nil {
			if cb != nil {
				cb(base+pr.done, expectSize)
			}
			break // 这一轮完整结束
		}
		// 中断：已写入的部分留在文件里，下一轮用 Range 接着下
		trayLogf("更新下载第 %d 轮中断，已保存 %d 字节，准备续传", i+1, fileSize(dst))
	}

	if expectSize > 0 {
		if got := fileSize(dst); got != expectSize {
			return fmt.Errorf("下载不完整（%d/%d 字节），请检查网络后重试", got, expectSize)
		}
	}
	return nil
}

// applyUpdate 下载新 exe 并替换自身。
// 做法：先把运行中的自身改名（Windows 允许重命名运行中的镜像），再把新文件挪到原位。
// 失败时会尽量回滚，调用方负责重启。
func applyUpdate(a releaseAsset, onProgress ...func(done, total int64)) error {
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

	trayLogf("开始下载更新 %s（%d 字节）", a.Name, a.Size)
	if err := downloadAsset(newPath, downloadURL(a), a.Size, onProgress...); err != nil {
		trayLogf("更新下载失败：%v", err)
		return err
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
	trayLogf("更新完成，新程序已就位")
	return nil
}

// cleanupOldBinary 删除上次自更新留下的旧文件（启动时调用，失败无妨）。
func cleanupOldBinary() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	dir := filepath.Dir(exe)
	for _, name := range []string{"dsh-tray.old.exe", "dsh-tray.new.exe"} {
		p := filepath.Join(dir, name)
		if fileExists(p) {
			_ = os.Remove(p)
		}
	}
}

// checkUpdateReport 是隐藏自检：查一次 GitHub 更新并把结果写到 updatecheck.txt。
// 会顺带把 release 里的 exe 真下载一遍（保留部分文件，能验证断点续传）。
func checkUpdateReport() {
	out := &strings.Builder{}
	fmt.Fprintf(out, "local   = %s\r\n", appVersion)
	fmt.Fprintf(out, "api     = %s\r\n", releaseAPI)
	addr, scheme := proxyConfig()
	fmt.Fprintf(out, "proxy   = %s (%s)\r\n", addr, scheme)

	finish := func() {
		_ = os.MkdirAll(dataDir(), 0o755)
		_ = os.WriteFile(filepath.Join(dataDir(), "updatecheck.txt"), []byte(out.String()), 0o644)
		trayLogf("更新自检结束：%s", strings.ReplaceAll(strings.TrimSpace(out.String()), "\r\n", " | "))
	}

	rel, err := latestRelease()
	if err != nil {
		fmt.Fprintf(out, "ERROR   = %v\r\n", err)
		finish()
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
		finish()
		return
	}
	fmt.Fprintf(out, "asset   = %s  size=%d\r\n", asset.Name, asset.Size)
	fmt.Fprintf(out, "dl-url  = %s\r\n", downloadURL(asset))

	// 保留上次的部分文件，能验证断点续传
	tmp := filepath.Join(os.TempDir(), "dsh-tray-downloadtest.exe")
	fmt.Fprintf(out, "resume  = 已有 %d 字节\r\n", fileSize(tmp))

	t0 := time.Now()
	if derr := downloadAsset(tmp, downloadURL(asset), asset.Size); derr != nil {
		fmt.Fprintf(out, "download= 失败: %v（当前 %d 字节）\r\n", derr, fileSize(tmp))
	} else {
		fmt.Fprintf(out, "download= OK %d 字节，耗时 %.0fs，与 size 一致=%v\r\n",
			fileSize(tmp), time.Since(t0).Seconds(), asset.Size == 0 || fileSize(tmp) == asset.Size)
	}
	finish()
}

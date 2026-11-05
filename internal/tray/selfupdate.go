package tray

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

// ---------- GitHub 令牌 ----------

var (
	ghTokenOnce sync.Once
	ghTokenVal  string
)

// githubToken 找一个可用的 GitHub 令牌，顺序：设置里手填 → 环境变量 → gh CLI。
//
// 为什么要令牌：api.github.com 的匿名配额只有 **60 次/小时，按出口 IP 算**。
// 本机实测两个出口都是 0/60（代理出口 203.10.98.186 被共用、公司出口 218.13.14.224
// 也一样），所以"回退直连"并不能保证查得通；带上令牌是 5000 次/小时，基本不会撞限流。
// gh 的令牌存在系统凭据管理器里，只能问 `gh auth token`。
func githubToken() string {
	if t := strings.TrimSpace(gitHubTokenFromConfig()); t != "" {
		return t
	}
	for _, k := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	ghTokenOnce.Do(func() { ghTokenVal = readGhToken() })
	return ghTokenVal
}

// ghCandidates 返回可能的 gh 可执行文件路径（PATH 里没有时兜底常见安装位置）。
func ghCandidates() []string {
	var list []string
	if p, err := exec.LookPath("gh"); err == nil {
		list = append(list, p)
	}
	if base := os.Getenv("LOCALAPPDATA"); base != "" {
		p := filepath.Join(base, "Programs", "gh", "gh.exe")
		if fileExists(p) {
			list = append(list, p)
		}
	}
	if base := os.Getenv("ProgramFiles"); base != "" {
		p := filepath.Join(base, "GitHub CLI", "gh.exe")
		if fileExists(p) {
			list = append(list, p)
		}
	}
	return list
}

// readGhToken 问一次 gh 要令牌；没装、没登录、超时都当没有（不打扰用户）。
func readGhToken() string {
	for _, exe := range ghCandidates() {
		out, err := runHiddenTimeout(8*time.Second, exe, "auth", "token")
		if err != nil {
			continue
		}
		if t := strings.TrimSpace(out); t != "" && !strings.ContainsAny(t, " \r\n") {
			trayLogf("已从 gh CLI 取得 GitHub 令牌（用于提高 API 配额）")
			return t
		}
	}
	return ""
}

// applyGitHubAuth 给请求带上令牌（没有就匿名）。
func applyGitHubAuth(req *http.Request) {
	if t := githubToken(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
}

// ---------- 出网链路（代理 / 直连）----------

// proxyRoute 是一条可尝试的出网链路。
type proxyRoute struct {
	label  string // 给日志和报错用
	addr   string // "" = 直连
	scheme string
}

// proxyRoutes 返回本次请求要依次尝试的链路。
//
// 为什么配了代理还要试直连：GitHub API 的匿名配额（60 次/小时）是按**出口 IP**
// 算的。代理出口常被一堆人共用，于是「代理能连上、握手也成功，但 GitHub 回
// HTTP 403 rate limit exceeded」——而本机直连反而还有额度（实测直连 200）。
// 反过来若直连被墙，代理就派上用场。所以两条都备着，谁通用谁。
func proxyRoutes() []proxyRoute {
	addr, scheme := proxyConfig()
	if addr == "" {
		return []proxyRoute{{label: "直连"}}
	}
	routes := []proxyRoute{{label: strings.ToUpper(scheme) + " 代理 " + addr, addr: addr, scheme: scheme}}
	if scheme != "http" {
		// 同一个端口先按配的协议试，不行再按 HTTP 代理试（很多客户端两种都收）
		routes = append(routes, proxyRoute{label: "HTTP 代理 " + addr, addr: addr, scheme: "http"})
	}
	return append(routes, proxyRoute{label: "直连"})
}

// doRoute 用指定链路发一次请求。req 会被 Clone，可以重复用。
func doRoute(req *http.Request, d time.Duration, rt proxyRoute) (*http.Response, error) {
	return doWithTimeoutClient(req.Clone(req.Context()), d, clientForProxy(rt.addr, rt.scheme))
}

// apiProblem 把非 2xx 响应翻成一句人话。
// GitHub 限流时 body 是 {"message":"API rate limit exceeded for ..."}，
// 头部还有 X-RateLimit-Remaining / Reset，一起带上，用户才知道该等还是该换链路。
func apiProblem(resp *http.Response, body []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &e)
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	if r := []rune(msg); len(r) > 200 {
		msg = string(r[:200]) + "…"
	}
	s := fmt.Sprintf("HTTP %d", resp.StatusCode)
	if msg != "" {
		s += "：" + msg
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			s += fmt.Sprintf("（匿名配额已用尽 %s/%s",
				resp.Header.Get("X-RateLimit-Remaining"), resp.Header.Get("X-RateLimit-Limit"))
			if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
				if sec, err := strconv.ParseInt(reset, 10, 64); err == nil {
					if left := time.Until(time.Unix(sec, 0)); left > 0 {
						s += "，约 " + left.Round(time.Minute).String() + "后恢复"
					}
				}
			}
			s += "）"
		}
	}
	return s
}

// latestRelease 查 GitHub 最新 release，按 proxyRoutes 依次尝试。
func latestRelease() (releaseInfo, error) {
	var rel releaseInfo
	autoFallbackProxy()

	req, err := http.NewRequest(http.MethodGet, releaseAPI, nil)
	if err != nil {
		return rel, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "dsh-tray/"+appVersion)
	applyGitHubAuth(req)

	var problems []string
	for _, rt := range proxyRoutes() {
		resp, rerr := doRoute(req, metaTimeout, rt)
		if rerr != nil {
			problems = append(problems, rt.label+" → "+rerr.Error())
			trayLogf("查询 release 失败（%s）：%v", rt.label, rerr)
			continue
		}
		if resp.StatusCode == http.StatusOK {
			derr := json.NewDecoder(resp.Body).Decode(&rel)
			resp.Body.Close()
			if derr != nil {
				problems = append(problems, rt.label+" → 响应解析失败："+derr.Error())
				continue
			}
			trayLogf("查询 release 成功（%s，版本 %s）", rt.label, rel.Tag)
			return rel, nil
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return rel, fmt.Errorf("仓库 %s 还没有发布任何 release。", updateRepo)
		}
		problem := apiProblem(resp, body)
		problems = append(problems, rt.label+" → "+problem)
		trayLogf("查询 release 失败（%s）：%s", rt.label, problem)
	}
	return rel, fmt.Errorf("访问 GitHub 失败（已依次尝试 %d 条链路）：\r\n%s\r\n\r\n"+
		"提示：GitHub 匿名接口限速 60 次/小时且按出口 IP 算，公司网/代理出口很容易被别人用光。"+
		"可在「设置」里清空代理走直连，或填一个 GitHub Token（限速提高到 5000 次/小时；"+
		"本机装了 gh 并登录过时会自动复用，无需填写）。",
		len(problems), strings.Join(problems, "\r\n"))
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

// openAsset 请求资产地址，按 proxyRoutes 依次尝试，返回 200/206 的响应。
// 第 i 轮续传要知道 shouldRetry 之外的信息，所以顺手把用的链路 label 也带出来。
func openAsset(url string, have int64) (*http.Response, string, error) {
	var problems []string
	for _, rt := range proxyRoutes() {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, "", err
		}
		// API 资产端点必须带这个 Accept 才会返回二进制流而不是 JSON
		req.Header.Set("Accept", "application/octet-stream")
		req.Header.Set("User-Agent", "dsh-tray/"+appVersion)
		applyGitHubAuth(req)
		if have > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
		}

		resp, rerr := doRoute(req, downloadTimeout, rt)
		if rerr != nil {
			problems = append(problems, rt.label+" → "+rerr.Error())
			continue
		}
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
			return resp, rt.label, nil
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		problems = append(problems, rt.label+" → "+apiProblem(resp, body))
		trayLogf("下载资产失败（%s）：%s", rt.label, apiProblem(resp, body))
	}
	return nil, "", fmt.Errorf("%s", strings.Join(problems, "\r\n"))
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

	var lastErr error
	for i := 0; i < attempts; i++ {
		have := fileSize(dst)
		if expectSize > 0 && have == expectSize {
			return nil
		}

		resp, route, err := openAsset(url, have)
		if err != nil {
			lastErr = fmt.Errorf("下载失败：%v", err)
			trayLogf("更新下载第 %d 轮失败：%v（已有 %d 字节）", i+1, err, have)
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
		if have == 0 {
			trayLogf("更新下载第 %d 轮开始，链路=%s", i+1, route)
		} else {
			trayLogf("更新下载第 %d 轮续传，链路=%s，已有 %d 字节", i+1, route, have)
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
		lastErr = cerr
		trayLogf("更新下载第 %d 轮中断，已保存 %d 字节，准备续传", i+1, fileSize(dst))
	}

	if expectSize > 0 {
		if got := fileSize(dst); got != expectSize {
			msg := fmt.Sprintf("下载不完整（%d/%d 字节），请检查网络后重试", got, expectSize)
			if lastErr != nil {
				msg += "\r\n最后一次错误：" + lastErr.Error()
			}
			return fmt.Errorf("%s", msg)
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
	loadConfig() // 自检也要按真实配置走代理，否则测不到代理链路
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

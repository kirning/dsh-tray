package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kirning/dsh-tray/internal/platform"
)

// AppVersion 是本程序（托盘自身）的版本，与被管理的 dsh 版本无关。
// 用 var 便于构建时用 -ldflags "-X github.com/kirning/dsh-tray/internal/core.AppVersion=x.y.z" 覆盖。
var AppVersion = "2.2.2"

const (
	repoOwner = "kirning"
	repoName  = "dsh-tray"
)

// Release 是 GitHub Release 的最小投影。
type Release struct {
	TagName string  `json:"tag_name"`
	Assets  []Asset `json:"assets"`
}

type Asset struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"url"` // API 资产端点，不是 browser_download_url
}

type apiError struct {
	Message string `json:"message"`
}

// LatestRelease 查询最新 Release，按“配置的代理 → 直连”依次尝试。
func LatestRelease() (*Release, string, error) {
	c := GetConfig()
	type route struct{ addr, scheme, label string }
	var routes []route
	if c.Proxy != "" {
		routes = append(routes, route{c.Proxy, c.ProxyScheme, "代理 " + c.ProxyScheme + "://" + c.Proxy})
	}
	routes = append(routes, route{"", "", "直连"})

	var problems []string
	for _, rt := range routes {
		rel, err := fetchLatest(rt.addr, rt.scheme)
		if err == nil {
			return rel, rt.label, nil
		}
		problems = append(problems, rt.label+"："+err.Error())
	}
	return nil, "", fmt.Errorf("检查更新失败：\n%s", strings.Join(problems, "\n"))
}

func fetchLatest(addr, scheme string) (*Release, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	u := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", repoOwner, repoName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "dsh-tray/"+AppVersion)
	applyGitHubAuth(req)

	resp, err := HTTPClientFor(addr, scheme).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, apiProblem(resp, raw)
	}
	var rel Release
	if err := json.Unmarshal(raw, &rel); err != nil {
		return nil, fmt.Errorf("解析 Release 失败: %v", err)
	}
	return &rel, nil
}

// apiProblem 把 GitHub 的限流信息翻成人话，别只回 HTTP 403。
func apiProblem(resp *http.Response, body []byte) error {
	var e apiError
	_ = json.Unmarshal(body, &e)
	msg := e.Message
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	if resp.StatusCode == http.StatusForbidden && strings.Contains(strings.ToLower(msg), "rate limit") {
		note := "匿名配额已用尽"
		if r := resp.Header.Get("X-RateLimit-Remaining"); r != "" {
			note += "（" + r + "/60）"
		}
		if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
			var ts int64
			if _, err := fmt.Sscanf(reset, "%d", &ts); err == nil {
				if left := time.Until(time.Unix(ts, 0)); left > 0 {
					note += "，约 " + left.Round(time.Minute).String() + "后恢复"
				}
			}
		}
		return fmt.Errorf("%s；在「设置」里填 GitHub Token 可提到 5000 次/小时", note)
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
}

func applyGitHubAuth(req *http.Request) {
	if tok := githubToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}

func githubToken() string {
	if t := strings.TrimSpace(GetConfig().GithubToken); t != "" {
		return t
	}
	for _, k := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if t := strings.TrimSpace(os.Getenv(k)); t != "" {
			return t
		}
	}
	for _, exe := range ghCandidates() {
		if out, err := platform.RunHiddenTimeout(8*time.Second, exe, "auth", "token"); err == nil {
			if t := strings.TrimSpace(out); t != "" && !strings.Contains(t, " ") {
				return t
			}
		}
	}
	return ""
}

func ghCandidates() []string {
	var out []string
	if p, ok := platform.FindOnPath("gh"); ok {
		out = append(out, p)
	}
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		out = append(out, filepath.Join(la, "Programs", "gh", "gh.exe"))
	}
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		out = append(out, filepath.Join(pf, "GitHub CLI", "gh.exe"))
	}
	out = append(out, "/usr/bin/gh", "/usr/local/bin/gh")
	return out
}

// VerifyToken 打 /rate_limit（不占配额），返回可读结论。
func VerifyToken(token string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/rate_limit", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "dsh-tray/"+AppVersion)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	c := GetConfig()
	resp, err := HTTPClientFor(c.Proxy, c.ProxyScheme).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", apiProblem(resp, raw)
	}
	var body struct {
		Resources struct {
			Core struct {
				Limit int `json:"limit"`
			} `json:"core"`
		} `json:"resources"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Resources.Core.Limit > 0 {
		return fmt.Sprintf("令牌有效，配额 %d 次/小时", body.Resources.Core.Limit), nil
	}
	return "令牌有效", nil
}

// PickAsset 按当前平台挑选 Release 资产。
func PickAsset(rel *Release) *Asset {
	want := []string{"dsh-tray.exe"}
	if runtime.GOOS == "linux" {
		want = []string{"dsh-tray-linux-amd64", "dsh-tray"}
	}
	for i := range rel.Assets {
		for _, w := range want {
			if rel.Assets[i].Name == w {
				return &rel.Assets[i]
			}
		}
	}
	return nil
}

// DownloadAsset 下载资产到目标路径，支持 Range 续传（最多 8 轮）。
func DownloadAsset(a *Asset, dest string, progress func(done, total int64)) error {
	if a == nil {
		return fmt.Errorf("当前平台没有可用的发布资产")
	}
	total := a.Size
	have := FileSize(dest)
	const attempts = 8
	var lastErr error

	for round := 0; round < attempts; round++ {
		if total > 0 && have >= total {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.DownloadURL, nil)
		if err != nil {
			cancel()
			return err
		}
		req.Header.Set("Accept", "application/octet-stream")
		req.Header.Set("User-Agent", "dsh-tray/"+AppVersion)
		if have > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
		}
		applyGitHubAuth(req)

		c := GetConfig()
		resp, err := HTTPClientFor(c.Proxy, c.ProxyScheme).Do(req)
		if err != nil {
			cancel()
			lastErr = err
			have = FileSize(dest)
			continue
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			cancel()
			return apiProblem(resp, raw)
		}
		if resp.StatusCode == http.StatusOK {
			have = 0
		}

		flags := os.O_CREATE | os.O_WRONLY
		if have > 0 {
			flags |= os.O_APPEND
		} else {
			flags |= os.O_TRUNC
		}
		f, err := os.OpenFile(dest, flags, 0o755)
		if err != nil {
			resp.Body.Close()
			cancel()
			return err
		}
		n, copyErr := io.Copy(f, &progressReader{r: resp.Body, done: have, total: total, cb: progress})
		f.Close()
		resp.Body.Close()
		cancel()
		have += n
		if copyErr != nil {
			lastErr = copyErr
			continue
		}
		if total == 0 || have >= total {
			return nil
		}
		lastErr = fmt.Errorf("下载被中断（%d/%d 字节）", have, total)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("下载未完成（%d/%d 字节）", have, total)
	}
	return lastErr
}

type progressReader struct {
	r     io.Reader
	done  int64
	total int64
	cb    func(done, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.done += int64(n)
		if p.cb != nil {
			p.cb(p.done, p.total)
		}
	}
	return n, err
}

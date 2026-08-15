// Package api 更新检测: /api/update/check + /api/update/download-info
// - 检测 GitHub latest release (含更新日志 release notes)
// - 按用户 IP 地区判断是否走国内镜像下载 (ip-api.com → gh-proxy 前缀)
package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// GitHub 仓库 (本面板所在仓库)
const GH = "qianze0628/wechat-ai-panel-go"

// ReleaseAsset release 资产
type ReleaseAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// ReleaseInfo GitHub release 信息
type ReleaseInfo struct {
	TagName     string         `json:"tag_name"`
	Name        string         `json:"name"`
	PublishedAt string         `json:"published_at"`
	Body        string         `json:"body"`
	HTMLURL     string         `json:"html_url"`
	Assets      []ReleaseAsset `json:"assets"`
}

// UpdateCheck 更新检查结果
type UpdateCheck struct {
	HasUpdate      bool         `json:"has_update"`
	CurrentVersion string       `json:"current_version"`
	Latest         *ReleaseInfo `json:"latest,omitempty"`
	Message        string       `json:"message"`
}

// DownloadInfo 下载信息 (含镜像判断)
type DownloadInfo struct {
	Region       string `json:"region"`
	UseMirror    bool   `json:"use_mirror"`
	MirrorPrefix string `json:"mirror_prefix"`
	Asset        string `json:"asset"` // 后端算好的本平台资产名 (2026-08-11)
	DirectURL    string `json:"direct_url"`
	FinalURL     string `json:"final_url"`
}

// httpGetJSON GET JSON (带 User-Agent + 超时 + 代理感知)
// 修复 (2026-08-15 v0.6.2): 使用 newDownloadClient (环境变量代理 + Windows 系统代理),
// 用户挂 Clash 等代理时检查更新不再裸连 GitHub 超时。
func httpGetJSON(url string, out any) error {
	client := newDownloadClient()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "wechat-ai-panel-updater")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateBytes(body, 200))
	}
	return json.Unmarshal(body, out)
}

func truncateBytes(b []byte, n int) string {
	s := string(b)
	if len(s) > n {
		return s[:n]
	}
	return s
}

// githubAPIMirrors GH API 代理候选中, 仅 gh-proxy.com 支持完整 API (实测 ghfast/ghproxy.net 403)。
var githubAPIMirrors = []string{
	"https://gh-proxy.com/https://api.github.com/",
	"https://ghfast.top/https://api.github.com/",
	"https://ghproxy.net/https://api.github.com/",
	"https://mirror.ghproxy.com/https://api.github.com/",
}

// fetchLatestRelease 查 GitHub latest release, 多源回退:
//   直连 api.github.com → gh-proxy 镜像 → release 页 302 解析 tag (最兜底)。
// 修复 (2026-08-15 v0.6.2): 国内直连 api.github.com 频繁超时 → "检查更新请求超时"。
// 改进: ① 不依赖 detectRegion (额外 IP 请求拖慢检查) — 所有地区统一"直连+镜像+重定向"候选;
//       ② 单候选短超时 (12s), 失败立即换下一个, 不再被单一超时拖死。
func fetchLatestRelease() (*ReleaseInfo, error) {
	candidates := []string{fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", GH)}
	for _, m := range githubAPIMirrors {
		candidates = append(candidates, m+fmt.Sprintf("repos/%s/releases/latest", GH))
	}
	// 逐个尝试 (每个候选短超时)
	var lastErr error
	found := false
	for _, url := range candidates {
		var rel ReleaseInfo
		if err := httpGetJSONTimeout(url, &rel, 12*time.Second); err == nil && rel.TagName != "" {
			return &rel, nil
		} else if err != nil {
			lastErr = err
		}
		_ = found
	}
	// 兜底方案: release 页面 302 → Location 提取 tag (国内可达性好, 仅版本号)
	if rel, err := fetchLatestReleaseViaRedirect(); err == nil {
		return rel, nil
	}
	return nil, fmt.Errorf("检查更新失败: %v", lastErr)
}

// httpGetJSONTimeout 带指定超时的 GET JSON (代理感知)
func httpGetJSONTimeout(url string, out any, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: proxyFunc()}}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "wechat-ai-panel-updater")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateBytes(body, 200))
	}
	return json.Unmarshal(body, out)
}

// fetchLatestReleaseViaRedirect 通过 github.com/<repo>/releases/latest 的 302 Location 取 tag。
// 该入口国内可达性好 (HTML 重定向而非 API), 即使 API 全挂也能拿到版本号。
func fetchLatestReleaseViaRedirect() (*ReleaseInfo, error) {
	client := newDownloadClient()
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse // 不跟随, 取 302 Location
	}
	url := fmt.Sprintf("https://github.com/%s/releases/latest", GH)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "wechat-ai-panel-updater")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// 302 → Location: https://github.com/.../releases/tag/v0.6.1
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if loc := resp.Header.Get("Location"); loc != "" {
			if idx := strings.LastIndex(loc, "/"); idx >= 0 {
				tag := loc[idx+1:]
				tag = strings.TrimSpace(tag)
				if strings.HasPrefix(tag, "v") {
					return &ReleaseInfo{TagName: tag}, nil
				}
			}
		}
		return nil, fmt.Errorf("release 重定向缺少 Location (HTTP %d)", resp.StatusCode)
	}
	// 部分网络下直接 200 返回页面 → 尝试从 HTML 提取 version (失败也接受, 交给上层)
	return nil, fmt.Errorf("release 页面未重定向 (HTTP %d)", resp.StatusCode)
}

// versionIsNewer 语义版本比较 (v0.1.9 vs v0.2.0; 容忍 go-v0.2 这类前缀)
// 注意: /api/status 的 version 是 "go-v0.2" 内部号, 前端会把它当当前版本传入,
// 必须能正确换算成 [0,2,0] 族以与 v0.2.0 比较。
func versionIsNewer(latest, current string) bool {
	parse := func(v string) []int {
		// 提取所有数字段 (容忍任意前缀/后缀字母)
		var nums []int
		for _, part := range strings.Split(v, ".") {
			digits := ""
			for _, c := range part {
				if c >= '0' && c <= '9' {
					digits += string(c)
				} else if digits != "" {
					break
				}
			}
			if digits == "" {
				continue // 纯非数字段 (如 "go-" 前缀) 跳过
			}
			n := 0
			for _, c := range digits {
				n = n*10 + int(c-'0')
			}
			nums = append(nums, n)
		}
		// 无数字段 (如纯 build tag) 视为 0
		if len(nums) == 0 {
			nums = []int{0, 0, 0}
		}
		return nums
	}
	l, c := parse(latest), parse(current)
	for i := 0; i < len(l) || i < len(c); i++ {
		lv, cv := 0, 0
		if i < len(l) {
			lv = l[i]
		}
		if i < len(c) {
			cv = c[i]
		}
		if lv != cv {
			return lv > cv
		}
	}
	return false
}

// detectRegion 按 IP 判断地区 (免费 GeoIP, 失败回退 unknown)
func detectRegion() string {
	// ipinfo.io 免费且国内可达; 失败依次回退 (ip-api / ipapi.co)
	client := &http.Client{Timeout: 6 * time.Second}
	for _, url := range []string{
		"https://ipinfo.io/json",
		"http://ip-api.com/json/?fields=countryCode",
		"https://ipapi.co/json/",
	} {
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		var m map[string]any
		if json.Unmarshal(body, &m) == nil {
			for _, k := range []string{"countryCode", "country_code", "country"} {
				if cc, ok := m[k].(string); ok && cc != "" {
					return strings.ToUpper(cc)
				}
			}
		}
	}
	return "unknown"
}

// RegisterUpdate 注册更新 API
//   - GET /api/update-check?current=v0.1.9
//   - GET /api/update/download-info?asset=xxx.zip
func (h *Handler) RegisterUpdate() {
	h.mux.HandleFunc("/api/update-check", func(w http.ResponseWriter, r *http.Request) {
		current := r.URL.Query().Get("version")
		rel, err := fetchLatestRelease()
		if err != nil {
			jsonErr(w, 502, err.Error())
			return
		}
		has := versionIsNewer(rel.TagName, current)
		jsonOK(w, UpdateCheck{
			HasUpdate:      has || strings.TrimPrefix(rel.TagName, "v") != strings.TrimPrefix(current, "v"),
			CurrentVersion: current,
			Latest:         rel,
			Message: func() string {
				if has {
					return "发现新版本"
				}
				return "已是最新版本"
			}(),
		})
	})
	h.mux.HandleFunc("/api/update/download-info", func(w http.ResponseWriter, r *http.Request) {
		// 修复 (2026-08-11 agent 审查 P0-2): 不再依赖前端传 asset (可能取错平台/名字不匹配) →
		// 后端按当前平台自算资产名 (assetForPlatform), 版本用 latest release tag。
		// 之前用 /releases/latest/download/<asset> — asset 名不匹配 → 404。
		asset := assetForPlatform()
		if asset == "" {
			jsonErr(w, 400, "不支持当前平台")
			return
		}
		// 版本: 优先 latest release tag (前端已 check 过), 失败回退 latest/download
		ver := r.URL.Query().Get("version")
		var direct string
		if ver != "" {
			direct = fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", GH, ver, asset)
		} else {
			direct = fmt.Sprintf("https://github.com/%s/releases/latest/download/%s", GH, asset)
		}
		region := detectRegion()
		info := DownloadInfo{
			Region:    region,
			Asset:     asset,
			DirectURL: direct,
			FinalURL:  direct,
		}
		// 国内或未知地区: 用 ghproxy 镜像前缀 (公共服务不稳定, 失败时前端自动回退直连)
		// 修复 (2026-08-11 agent 审查 P1-6): unknown 也先走镜像 — 国内 GitHub 直连被墙,
		// detectRegion 失败 (ipinfo 限流) 时之前直连 → 下载卡死
		if region == "CN" || region == "unknown" {
			info.UseMirror = true
			info.MirrorPrefix = "https://gh-proxy.com/"
			info.FinalURL = info.MirrorPrefix + direct
		}
		jsonOK(w, info)
	})
}
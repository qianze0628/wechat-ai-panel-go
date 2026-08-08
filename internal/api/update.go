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
	DirectURL    string `json:"direct_url"`
	FinalURL     string `json:"final_url"`
}

// httpGetJSON GET JSON (带 User-Agent + 超时)
func httpGetJSON(url string, out any) error {
	client := &http.Client{Timeout: 15 * time.Second}
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

// fetchLatestRelease 查 GitHub latest release
func fetchLatestRelease() (*ReleaseInfo, error) {
	var rel ReleaseInfo
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", GH)
	if err := httpGetJSON(url, &rel); err != nil {
		return nil, fmt.Errorf("请求 GitHub 失败: %v", err)
	}
	return &rel, nil
}

// versionIsNewer 语义版本比较 (v0.1.9 vs v0.2.0)
func versionIsNewer(latest, current string) bool {
	parse := func(v string) []int {
		v = strings.TrimPrefix(v, "v")
		var out []int
		for _, part := range strings.Split(v, ".") {
			n := 0
			for _, c := range part {
				if c >= '0' && c <= '9' {
					n = n*10 + int(c-'0')
				} else {
					break
				}
			}
			out = append(out, n)
		}
		return out
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
		asset := r.URL.Query().Get("asset")
		direct := fmt.Sprintf("https://github.com/%s/releases/latest/download/%s", GH, asset)
		region := detectRegion()
		info := DownloadInfo{
			Region:    region,
			DirectURL: direct,
			FinalURL:  direct,
		}
		if region == "CN" {
			// 国内: 用 ghproxy 镜像前缀 (公共服务不稳定, 失败时前端自动回退直连)
			info.UseMirror = true
			info.MirrorPrefix = "https://gh-proxy.com/"
			info.FinalURL = info.MirrorPrefix + direct
		}
		jsonOK(w, info)
	})
}
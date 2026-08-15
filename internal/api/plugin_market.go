// Package api 插件市场: 已知插件源 (GitHub) 列表 + clone 安装 + 依赖安装 + 卸载
// 安装路径: {astrbot_data_dir}/plugins/<repo名>
// 安装流程: git clone (镜像加速) → pip 装 requirements → 重启 AstrBot 生效
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"wechat-ai-panel/internal/config"
)

// MarketPlugin 市场插件定义
type MarketPlugin struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Repo      string   `json:"repo"`
	Desc      string   `json:"desc"`
	Version   string   `json:"version"`
	Author    string   `json:"author"`
	Tags      []string `json:"tags"`
	Installed bool     `json:"installed"`
	LocalVer  string   `json:"local_version"`
	// AstrBot 商店增强字段 (对齐 AstrBot 市场页: 图标/Star/平台/更新时间/下载)
	Logo             string   `json:"logo"`
	Stars            int      `json:"stars"`
	AstrbotVersion   string   `json:"astrbot_version"`
	SupportPlatforms []string `json:"support_platforms"`
	UpdatedAt        string   `json:"updated_at"`
	DownloadURL      string   `json:"download_url"`
}

// 内置市场 (常用 AstrBot 插件; 后续可扩展为远程 index)
var builtinMarket = []MarketPlugin{
	{ID: "astrbot_plugin_qq_group_daily_analysis", Name: "群分析总结", Repo: "https://github.com/sxp-Simon/astrbot_plugin_qq_group_daily_analysis.git", Desc: "群聊日常分析总结, 生成精美群聊分析报告", Author: "SXP-Simon", Tags: []string{"分析", "统计"}},
	{ID: "astrbot_plugin_self_learning", Name: "自主学习", Repo: "https://github.com/NickCharlie/astrbot_plugin_self_learning.git", Desc: "对话风格学习, 群组黑话, 人格演化", Author: "NickMo", Tags: []string{"学习", "人格"}},
	{ID: "astrbot_plugin_essential", Name: "Essential 多功能", Repo: "https://github.com/Soulter/astrbot_plugin_essential.git", Desc: "随机动漫图/以图搜番/一言/今天吃什么/早晚安", Author: "Soulter", Tags: []string{"多功能"}},
	{ID: "astrbot_plugin_rss", Name: "RSS 订阅", Repo: "https://github.com/Soulter/astrbot_plugin_rss.git", Desc: "群聊/私聊 RSS 订阅推送", Author: "Soulter", Tags: []string{"订阅"}},
}

// marketState 安装状态 (并发安全)
var marketMu sync.Mutex
var marketInstalling = map[string]bool{}

// AstrBot 官方插件商店 (api.soulter.top; 与 AstrBot WebUI 同源, 1293+ 插件)
const (
	marketIndexURL    = "https://api.soulter.top/astrbot/plugins?format=json"
	marketIndexMirror = "https://api.soulter.top/astrbot/plugins?format=json&mirror=1"
)

var marketIndexCache []MarketPlugin
var marketIndexCacheTime time.Time
var marketIndexMu sync.Mutex
var marketSourceNote = "cache"

// fetchRemoteMarket 拉取远程索引 (失败回退内置), 缓存 10 分钟; 加锁防并发竞态
func fetchRemoteMarket() []MarketPlugin {
	// 1. 快速路径: 命中缓存直接返回 (锁只在读写缓存时短暂持有)
	marketIndexMu.Lock()
	if len(marketIndexCache) > 0 && time.Since(marketIndexCacheTime) < 10*time.Minute {
		marketIndexMu.Unlock()
		return marketIndexCache
	}
	// 2. 缓存失效/空 → 锁外抓取 (不阻塞其他请求), 双检避免重复抓取
	marketIndexMu.Unlock()
	var fetched []MarketPlugin
	source := "all-failed"
	// 从 AstrBot 官方商店拉取 (dict: id → plugin; 失败回退内置)
	for _, u := range []string{marketIndexURL, marketIndexMirror} {
		data, err := httpGetTimeout(u, 12000)
		if err != nil {
			continue
		}
		// 商店返回: {"plugin-id": {display_name, desc, author, repo, tags, version, logo, ...}}
		var storeMap map[string]json.RawMessage
		if err := json.Unmarshal(data, &storeMap); err != nil || len(storeMap) == 0 {
			continue
		}
		for id, raw := range storeMap {
			var p struct {
				DisplayName      string          `json:"display_name"`
				Desc             string          `json:"desc"`
				Author           string          `json:"author"`
				Repo             string          `json:"repo"`
				Tags             json.RawMessage `json:"tags"`
				Version          string          `json:"version"`
				Logo             string          `json:"logo"`
				Stars            int             `json:"stars"`
				AstrbotVersion   string          `json:"astrbot_version"`
				SupportPlatforms []string        `json:"support_platforms"`
				UpdatedAt        string          `json:"updated_at"`
				DownloadURL      string          `json:"download_url"`
			}
			if err := json.Unmarshal(raw, &p); err != nil {
				continue
			}
			name := p.DisplayName
			if name == "" {
				name = id // 商店无 display_name 时 fallback 到 id
			}
			// tags 容错: 数组或字符串 (BUG-3: 3 个插件 tags 是字符串, 原被整体丢弃)
			var tags []string
			if len(p.Tags) > 0 {
				var arr []string
				if err := json.Unmarshal(p.Tags, &arr); err == nil {
					tags = arr
				} else {
					var s string
					if err := json.Unmarshal(p.Tags, &s); err == nil && s != "" {
						tags = []string{s}
					}
				}
			}
			m := MarketPlugin{
				ID:               id,
				Name:             name,
				Repo:             p.Repo,
				Desc:             p.Desc,
				Author:           p.Author,
				Version:          p.Version,
				Tags:             tags,
				Logo:             p.Logo,
				Stars:            p.Stars,
				AstrbotVersion:   p.AstrbotVersion,
				SupportPlatforms: p.SupportPlatforms,
				UpdatedAt:        p.UpdatedAt,
				DownloadURL:      p.DownloadURL,
			}
			fetched = append(fetched, m)
		}
		if len(fetched) > 0 {
			source = "soulter-store"
			break
		}
	}
	// 3. 双检: 锁内写缓存 (若别人已写好则用之)
	marketIndexMu.Lock()
	defer marketIndexMu.Unlock()
	if len(marketIndexCache) > 0 && time.Since(marketIndexCacheTime) < 10*time.Minute {
		return marketIndexCache
	}
	if len(fetched) == 0 {
		return nil // 回退内置 (不写缓存, 下次再试)
	}
	marketIndexCache = fetched
	marketIndexCacheTime = time.Now()
	marketSourceNote = source
	return fetched
}

// httpGetTimeout 简单 HTTP GET (标准库 net/http, 支持 TLS/重定向, 超时)
func httpGetTimeout(url string, timeoutMs int) ([]byte, error) {
	client := &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "wechat-ai-panel")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	// 限制响应体大小 (防恶意大索引)
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

// candidateDirNames 可能的插件目录名集合 (id 本身/去前缀/连字符转下划线/仓库名)
func candidateDirNames(id, repo string) []string {
	names := []string{}
	if id != "" {
		names = append(names, id, strings.TrimPrefix(id, "astrbot_plugin_"), strings.ReplaceAll(id, "-", "_"))
	}
	if repo != "" {
		names = append(names, strings.TrimSuffix(filepath.Base(repo), ".git"))
	}
	return names
}

// pluginsInstalled 判断某插件是否已安装 (有目录 + metadata)
// 匹配: id 目录 / 去前缀 / 仓库 basename (商店 id 连字符 vs 目录下划线)
func pluginsInstalledByRole(cfg *config.Config, id, repo string) bool {
	names := map[string]bool{}
	// 各种可能的目录名: id 本身, 去 astrbot_plugin_ 前缀, 仓库名
	if id != "" {
		names[id] = true
		names[strings.TrimPrefix(id, "astrbot_plugin_")] = true
		names[strings.ReplaceAll(id, "-", "_")] = true
	}
	if repo != "" {
		names[strings.TrimSuffix(filepath.Base(repo), ".git")] = true
	}
	for n := range names {
		pdir := filepath.Join(pluginsDir(cfg), n)
		if _, err := os.Stat(filepath.Join(pdir, "metadata.yaml")); err == nil {
			return true
		}
		if fi, err := os.Stat(pdir); err == nil && fi.IsDir() {
			if _, err2 := os.Stat(filepath.Join(pdir, "main.py")); err2 == nil {
				return true
			}
		}
	}
	return false
}

func pluginInstalled(cfg *config.Config, id string) bool {
	return pluginsInstalledByRole(cfg, id, "")
}

// pluginLocalVersion 读本地插件版本 (metadata; 多候选目录名)
func pluginLocalVersionByRole(cfg *config.Config, id, repo string) string {
	names := []string{}
	if id != "" {
		names = append(names, id, strings.TrimPrefix(id, "astrbot_plugin_"), strings.ReplaceAll(id, "-", "_"))
	}
	if repo != "" {
		names = append(names, strings.TrimSuffix(filepath.Base(repo), ".git"))
	}
	for _, dir := range names {
		pdir := filepath.Join(pluginsDir(cfg), dir)
		if raw, err := os.ReadFile(filepath.Join(pdir, "metadata.yaml")); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "version:") {
					v := strings.TrimSpace(strings.TrimPrefix(line, "version:"))
					v = strings.Trim(v, `"'`)
					if ci := strings.Index(v, " #"); ci >= 0 {
						v = strings.TrimSpace(v[:ci])
					}
					v = strings.TrimPrefix(v, "v")
					return v
				}
			}
		}
	}
	return ""
}

func pluginLocalVersion(cfg *config.Config, id string) string {
	return pluginLocalVersionByRole(cfg, id, "")
}

// RegisterPluginMarket 插件市场 API
//   - GET   /api/market/plugins        市场列表 (+安装状态)
//   - POST  /api/market/install        {id|repo} 安装 (clone + deps)
//   - POST  /api/market/uninstall      {id} 卸载
//   - GET   /api/market/status        安装进程状态 (实现对齐注释)
func (h *Handler) RegisterPluginMarket(cfg *config.Config) {
	// 市场列表
	h.mux.HandleFunc("/api/market/plugins", func(w http.ResponseWriter, r *http.Request) {
		if h.authCheck != nil && !h.authCheck(r) {
			jsonErr(w, 401, "未认证或会话已过期")
			return
		}
		// 远程商店 + 内置, 按 repo 去重 (同一插件不同 id 只列一次)
		src := fetchRemoteMarket()
		if len(src) == 0 {
			src = builtinMarket
		}
		seenRepo := map[string]bool{}
		var merged []MarketPlugin
		for _, p := range append(append([]MarketPlugin{}, src...), builtinMarket...) {
			// 去重键小写 (BUG-2: 22 组大小写变体同名仓库避免互相覆盖)
			repoKey := strings.ToLower(strings.TrimSuffix(p.Repo, ".git"))
			if repoKey == "" || seenRepo[repoKey] {
				continue
			}
			seenRepo[repoKey] = true
			merged = append(merged, p)
		}
		list := make([]MarketPlugin, 0, len(merged))
		installedAll := 0
		for _, p := range merged {
			np := p
			np.Installed = pluginsInstalledByRole(cfg, p.ID, p.Repo)
			if np.Installed {
				np.LocalVer = pluginLocalVersionByRole(cfg, p.ID, p.Repo)
				installedAll++
			}
			list = append(list, np)
		}
		// 搜索过滤 (?q=名称/描述/作者; ?tag=标签)
		if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
			ql := strings.ToLower(q)
			filtered := list[:0]
			for _, p := range list {
				if strings.Contains(strings.ToLower(p.Name), ql) ||
					strings.Contains(strings.ToLower(p.Desc), ql) ||
					strings.Contains(strings.ToLower(p.Author), ql) ||
					strings.Contains(strings.ToLower(p.ID), ql) {
					filtered = append(filtered, p)
				}
			}
			list = filtered
		}
		jsonOK(w, map[string]any{"ok": true, "plugins": list, "source": marketSourceNote, "total": len(list), "installed_count": installedAll})
	})

	// 安装进程状态 (对齐注释: 返回正在安装的插件目录)
	h.mux.HandleFunc("/api/market/status", func(w http.ResponseWriter, r *http.Request) {
		if h.authCheck != nil && !h.authCheck(r) {
			jsonErr(w, 401, "未认证或会话已过期")
			return
		}
		marketMu.Lock()
		installing := make([]string, 0, len(marketInstalling))
		for k := range marketInstalling {
			installing = append(installing, k)
		}
		marketMu.Unlock()
		jsonOK(w, map[string]any{"ok": true, "installing": installing, "count": len(installing)})
	})

	// 安装 (clone + deps)
	h.mux.HandleFunc("/api/market/install", func(w http.ResponseWriter, r *http.Request) {
		if h.authCheck != nil && !h.authCheck(r) {
			jsonErr(w, 401, "未认证或会话已过期")
			return
		}
		if r.Method != http.MethodPost {
			jsonErr(w, 405, "仅支持 POST")
			return
		}
		var body struct {
			ID   string `json:"id"`
			Repo string `json:"repo"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || (body.ID == "" && body.Repo == "") {
			jsonErr(w, 400, "需指定 id 或 repo")
			return
		}
		// 解析目标: id → 远程/内置市场
		repo := body.Repo
		pdir := ""
		if repo == "" {
			var mp *MarketPlugin
			// 先查缓存(快速路径), 未命中再拉全量 (冷启动/未开过市场页时)
			marketIndexMu.Lock()
			cached := marketIndexCache
			marketIndexMu.Unlock()
			srcs := [][]MarketPlugin{cached}
			if len(cached) == 0 {
				srcs = append(srcs, fetchRemoteMarket())
			}
			srcs = append(srcs, builtinMarket)
			for _, src := range srcs {
				for i := range src {
					if src[i].ID == body.ID {
						mp = &src[i]
						break
					}
				}
				if mp != nil {
					break
				}
			}
			if mp == nil {
				jsonErr(w, 404, "市场无此插件: "+body.ID)
				return
			}
			if mp.Repo == "" {
				jsonErr(w, 400, "该插件无仓库地址, 无法安装")
				return
			}
			repo = mp.Repo
			// 目录用仓库 basename (与 AstrBot 插件目录/克隆一致; 商店 id 连字符 ≠ 仓库名下划线)
			repoName := strings.TrimSuffix(filepath.Base(repo), ".git")
			if repoName == "" || strings.ContainsAny(repoName, "/\\") {
				jsonErr(w, 400, "非法的仓库地址")
				return
			}
			pdir = filepath.Join(pluginsDir(cfg), repoName)
		} else {
			// 自定义 repo → 用仓库名作目录 (安全校验: 拒绝路径穿越/绝对路径/盘符)
			// 仅允许 https:// 协议
			if !strings.HasPrefix(repo, "https://") && !strings.HasPrefix(repo, "http://") {
				jsonErr(w, 400, "repo 仅支持 http(s) 地址")
				return
			}
			name := strings.TrimSuffix(filepath.Base(strings.TrimRight(repo, "/")), ".git")
			if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
				jsonErr(w, 400, "非法的仓库地址")
				return
			}
			pdir = filepath.Join(pluginsDir(cfg), name)
			// 最终仍校验路径在 plugins 内
			if rel, err := filepath.Rel(pluginsDir(cfg), pdir); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				jsonErr(w, 400, "非法的仓库地址")
				return
			}
		}
		// 并发限制
		marketMu.Lock()
		if marketInstalling[pdir] {
			marketMu.Unlock()
			jsonErr(w, 409, "该插件正在安装中")
			return
		}
		marketInstalling[pdir] = true
		marketMu.Unlock()
		defer func() {
			marketMu.Lock()
			delete(marketInstalling, pdir)
			marketMu.Unlock()
		}()

		// 执行流: clone → (有 requirements) pip install
		err := installPluginFromRepo(cfg, pdir, repo)
		if err != nil {
			_ = os.RemoveAll(pdir)
			jsonErr(w, 500, "安装失败: "+err.Error())
			return
		}
		jsonOK(w, map[string]any{"ok": true, "message": "插件已安装, 重启 AstrBot 生效", "dir": pdir})
	})

	// 卸载
	h.mux.HandleFunc("/api/market/uninstall", func(w http.ResponseWriter, r *http.Request) {
		if h.authCheck != nil && !h.authCheck(r) {
			jsonErr(w, 401, "未认证或会话已过期")
			return
		}
		if r.Method != http.MethodPost {
			jsonErr(w, 405, "仅支持 POST")
			return
		}
		var body struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
			jsonErr(w, 400, "需指定 id")
			return
		}
		// 卸载按多候选目录: 商店 id 连字符 ≠ 仓库名下划线 (BUG-1 修复: 437/1293 原无法卸载)
		removed := 0
		for _, name := range candidateDirNames(body.ID, "") {
			pdir, ok := safePluginDir(cfg, name)
			if !ok {
				continue
			}
			if err := os.RemoveAll(pdir); err == nil {
				removed++
			}
		}
		if removed == 0 {
			jsonErr(w, 400, "非法的插件 id")
			return
		}
		jsonOK(w, map[string]any{"ok": true, "message": "插件已卸载 (重启 AstrBot 生效)"})
	})
}

// errFromCmd 包装命令错误信息
func errFromCmd(err error, name string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s 失败: %v", name, err)
}

// installPluginFromRepo 获取插件源码 + 依赖安装
// (2026-08-15 第一性原理重构): git clone → HTTP zip 下载 (零外部二进制依赖, 与一键部署同源)。
func installPluginFromRepo(cfg *config.Config, pdir, repo string) error {
	_ = os.MkdirAll(pluginsDir(cfg), 0o755)
	// 重装/覆盖: 先清残留目录
	if _, err := os.Stat(pdir); err == nil {
		_ = os.RemoveAll(pdir)
	}
	// zip 下载 (用户配置的 git_clone_proxy 兼容沿用为镜像前缀)
	zipProxies := []string{}
	if cfg.Mirrors.GitCloneProxy != "" {
		zipProxies = append(zipProxies, cfg.Mirrors.GitCloneProxy)
	}
	ok, errMsg := fetchRepoZipWithMarker(repo, pdir, "", zipProxies, nil)
	if !ok {
		return fmt.Errorf("插件源码下载失败: %s", errMsg)
	}
	// requirements.txt 依赖安装: 用 astrbot 相同 Python 环境 (uv 装的), 超时 300s
	req := filepath.Join(pdir, "requirements.txt")
	if _, err := os.Stat(req); err == nil {
		var cmd *exec.Cmd
		// 优先: uv --python 指向 astrbot 环境 (Linux/macOS 与 Windows 都可用)
		py := astrbotPythonPath()
		if uvPath := which2("uv"); uvPath != "" && py != "" {
			cmd = exec.Command(uvPath, "pip", "install", "--python", py, "-r", req)
		} else if py2 := which2("python"); py2 != "" && which2("uv") == "" {
			// 无 uv 用 python -m pip
			cmd = exec.Command(py2, "-m", "pip", "install", "-r", req)
		} else {
			// 找不到 astrbot 环境 → 明确报错 (不走必失败的 uv pip 无解释器分支)
			return fmt.Errorf("依赖安装失败: 未定位 astrbot 的 Python 环境. 请确认 astrbot 用 uv/pipx 安装")
		}
		// uv 吃 UV_INDEX_URL, pip 吃 PIP_INDEX_URL — 两种都注入保证镜像生效
		cmd.Env = append(os.Environ(),
			"UV_INDEX_URL="+cfg.Mirrors.PypiIndex,
			"PIP_INDEX_URL="+cfg.Mirrors.PypiIndex,
		)
		depsCtx, cancelDeps := context.WithTimeout(context.Background(), 300*time.Second)
		defer cancelDeps()
		cmd = exec.CommandContext(depsCtx, cmd.Path, cmd.Args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("依赖安装失败: %s %v", out, err)
		}
	}
	return nil
}

// astrbotPythonPath 定位 astrbot 工具环境内的 python (跨平台)
func astrbotPythonPath() string {
	home, _ := os.UserHomeDir()
	// 平台分隔符处理: Windows 用 \ 分隔, Unix 用 /
	candidates := []string{
		// Windows: uv tool install 布局
		filepath.Join(home, "AppData", "Roaming", "uv", "tools", "astrbot", "Scripts", "python.exe"),
		// Linux/macOS: uv tool install 布局
		filepath.Join(home, ".local", "share", "uv", "tools", "astrbot", "bin", "python"),
		// pipx 布局
		filepath.Join(home, ".local", "pipx", "venvs", "astrbot", "bin", "python"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

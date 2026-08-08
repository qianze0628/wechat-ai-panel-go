// Package api 插件市场: 已知插件源 (GitHub) 列表 + clone 安装 + 依赖安装 + 卸载
// 安装路径: {astrbot_data_dir}/plugins/<repo名>
// 安装流程: git clone (镜像加速) → pip 装 requirements → 重启 AstrBot 生效
package api

import (
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
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Repo        string   `json:"repo"`
	Desc        string   `json:"desc"`
	Version     string   `json:"version"`
	Author      string   `json:"author"`
	Tags        []string `json:"tags"`
	Installed   bool     `json:"installed"`
	LocalVer    string   `json:"local_version"`
}

// 内置市场 (常用 AstrBot 插件; 后续可扩展为远程 index)
var builtinMarket = []MarketPlugin{
	{ID: "astrbot_plugin_qq_group_daily_analysis", Name: "群分析总结", Repo: "https://github.com/sxp-Simon/astrbot_plugin_qq_group_daily_analysis.git", Desc: "群聊日常分析总结, 生成精美群聊分析报告", Author: "SXP-Simon", Tags: []string{"分析", "统计"}},
	{ID: "astrbot_plugin_gitee_aiimg", Name: "Gitee AI 绘图", Repo: "https://github.com/mlzhudas/astrbot_plugin_gitee_aiimg.git", Desc: "AI 文生图/改图, 多服务商", Author: "木有知", Tags: []string{"绘图", "AI"}},
	{ID: "astrbot_plugin_self_learning", Name: "自主学习", Repo: "https://github.com/NickCharlie/astrbot_plugin_self_learning.git", Desc: "对话风格学习, 群组黑话, 人格演化", Author: "NickMo", Tags: []string{"学习", "人格"}},
	{ID: "meme_manager", Name: "表情包管理器", Repo: "https://github.com/anka-afk/astrbot_plugin_meme_manager.git", Desc: "表情包管理与自动发送", Author: "anka", Tags: []string{"表情", "趣味"}},
}

// marketState 安装状态 (并发安全)
var marketMu sync.Mutex
var marketInstalling = map[string]bool{}

// 远程市场索引 (GitHub raw, 镜像可加速; 失败回退内置)
const (
	marketIndexURL = "https://raw.githubusercontent.com/qianze0628/wechat-ai-panel-go/master/market_index.json"
	marketIndexMirror = "https://gh-proxy.com/https://raw.githubusercontent.com/qianze0628/wechat-ai-panel-go/master/market_index.json"
)

var marketIndexCache []MarketPlugin
var marketIndexCacheTime time.Time

// fetchRemoteMarket 拉取远程索引 (失败回退内置), 缓存 10 分钟
func fetchRemoteMarket() []MarketPlugin {
	if len(marketIndexCache) > 0 && time.Since(marketIndexCacheTime) < 10*time.Minute {
		return marketIndexCache
	}
	type rawIndex struct {
		Plugins []MarketPlugin `json:"plugins"`
	}
	for _, u := range []string{marketIndexMirror, marketIndexURL} {
		data, err := httpGetTimeout(u, 8000)
		if err != nil {
			continue
		}
		var idx rawIndex
		if err := json.Unmarshal(data, &idx); err == nil && len(idx.Plugins) > 0 {
			marketIndexCache = idx.Plugins
			marketIndexCacheTime = time.Now()
			return idx.Plugins
		}
	}
	return nil // 回退内置
}

// httpGetTimeout 简单 HTTP GET (标准库 net/http, 支持 TLS/重定向, 超时)
func httpGetTimeout(url string, timeoutMs int) ([]byte, error) {
	client := &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// pluginsInstalled 判断某插件是否已安装 (有目录 + metadata)
func pluginInstalled(cfg *config.Config, id string) bool {
	pdir := filepath.Join(pluginsDir(cfg), id)
	if _, err := os.Stat(filepath.Join(pdir, "metadata.yaml")); err == nil {
		return true
	}
	// 兼容: 目录存在且含 main.py
	if fi, err := os.Stat(pdir); err == nil && fi.IsDir() {
		if _, err2 := os.Stat(filepath.Join(pdir, "main.py")); err2 == nil {
			return true
		}
	}
	return false
}

// pluginLocalVersion 读本地插件版本 (metadata)
func pluginLocalVersion(cfg *config.Config, id string) string {
	pdir := filepath.Join(pluginsDir(cfg), id)
	if raw, err := os.ReadFile(filepath.Join(pdir, "metadata.yaml")); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "version:") {
				return strings.TrimSpace(strings.TrimPrefix(line, "version:"))
			}
		}
	}
	return ""
}

// RegisterPluginMarket 插件市场 API
//   - GET   /api/market/plugins        市场列表 (+安装状态)
//   - POST  /api/market/install        {id|repo} 安装 (clone + deps)
//   - POST  /api/market/uninstall      {id} 卸载
//   - GET   /api/market/status        安装进程状态
func (h *Handler) RegisterPluginMarket(cfg *config.Config) {
	// 市场列表
	h.mux.HandleFunc("/api/market/plugins", func(w http.ResponseWriter, r *http.Request) {
		if h.authCheck != nil && !h.authCheck(r) {
			jsonErr(w, 401, "未认证或会话已过期")
			return
		}
		// 远程索引 + 内置, 按 repo 去重 (同一插件不同 id 只列一次)
		src := fetchRemoteMarket()
		if len(src) == 0 {
			src = builtinMarket
		}
		seenRepo := map[string]bool{}
		var merged []MarketPlugin
		for _, p := range append(append([]MarketPlugin{}, src...), builtinMarket...) {
			repoKey := strings.TrimSuffix(p.Repo, ".git")
			if seenRepo[repoKey] {
				continue
			}
			seenRepo[repoKey] = true
			merged = append(merged, p)
		}
		list := make([]MarketPlugin, 0, len(merged))
		for _, p := range merged {
			np := p
			np.Installed = pluginInstalled(cfg, p.ID) || pluginInstalled(cfg, strings.TrimPrefix(p.ID, "astrbot_plugin_"))
			if np.Installed {
				np.LocalVer = pluginLocalVersion(cfg, p.ID)
			}
			list = append(list, np)
		}
		jsonOK(w, map[string]any{"ok": true, "plugins": list, "source": "remote"})
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
		var body struct{ ID string `json:"id"`; Repo string `json:"repo"` }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || (body.ID == "" && body.Repo == "") {
			jsonErr(w, 400, "需指定 id 或 repo")
			return
		}
		// 解析目标: id → 远程/内置市场
		repo := body.Repo
		pdir := ""
		if repo == "" {
			var mp *MarketPlugin
			// 先远程, 再内置
			for _, src := range [][]MarketPlugin{fetchRemoteMarket(), builtinMarket} {
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
			repo = mp.Repo
			pdir = filepath.Join(pluginsDir(cfg), mp.ID)
		} else {
			// 自定义 repo → 用仓库名作目录
			name := strings.TrimSuffix(filepath.Base(repo), ".git")
			pdir = filepath.Join(pluginsDir(cfg), name)
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
		var body struct{ ID string `json:"id"` }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
			jsonErr(w, 400, "需指定 id")
			return
		}
		pdir, ok := safePluginDir(cfg, body.ID)
		if !ok {
			jsonErr(w, 400, "非法的插件 id")
			return
		}
		if err := os.RemoveAll(pdir); err != nil {
			jsonErr(w, 500, "卸载失败: "+err.Error())
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

// installPluginFromRepo clone + 依赖安装
func installPluginFromRepo(cfg *config.Config, pdir, repo string) error {
	_ = os.MkdirAll(pluginsDir(cfg), 0o755)
	// git clone (depth 1; 镜像加速)
	cloneURL := repo
	if cfg.Mirrors.GitCloneProxy != "" {
		cloneURL = cfg.Mirrors.GitCloneProxy + strings.TrimPrefix(strings.TrimPrefix(repo, "https://"), "http://")
	}
	cmd := exec.Command("git", "clone", "--depth", "1", cloneURL, pdir)
	if out, err := cmd.CombinedOutput(); err != nil {
		// 若镜像失败回退直连
		cmd2 := exec.Command("git", "clone", "--depth", "1", repo, pdir)
		if out2, err2 := cmd2.CombinedOutput(); err2 != nil {
			return fmt.Errorf("clone 失败: %s / %s", out, out2)
		}
		_ = out
	}
	// requirements.txt 依赖 (循环装; AstrBot 用 uv 装, 这里用 pip)
	req := filepath.Join(pdir, "requirements.txt")
	if _, err := os.Stat(req); err == nil {
		pip := "pip"
		args := []string{"install", "-r", req}
		// uv 优先 (若可用)
		if which2("uv") != "" {
			pip = "uv"
			args = []string{"pip", "install", "-r", req}
		}
		cmd := exec.Command(pip, args...)
		cmd.Env = append(os.Environ(), "UV_INDEX_URL="+cfg.Mirrors.PypiIndex)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("依赖安装失败: %s %v", out, err)
		}
	}
	return nil
}
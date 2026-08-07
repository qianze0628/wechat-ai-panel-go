// Package api AstrBot 集成: 凭据 / 白名单对接 / OneBot 配置 / 备份恢复
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"wechat-ai-panel/internal/config"
	"wechat-ai-panel/internal/util"
)

// AstrbotClient AstrBot dashboard HTTP 客户端 (Bearer token)
type AstrbotClient struct {
	Cfg   *config.Config
	token string
	mu    chan struct{}
}

// NewAstrbotClient 创建客户端
func NewAstrbotClient(cfg *config.Config) *AstrbotClient {
	return &AstrbotClient{Cfg: cfg, mu: make(chan struct{}, 1)}
}

// webuiURL AstrBot WebUI 地址
func (c *AstrbotClient) webuiURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", c.Cfg.Services.Astrbot.WebUIPort)
}

// login 登录获取 token (缓存)
func (c *AstrbotClient) login() string {
	if c.token != "" {
		return c.token
	}
	body, _ := json.Marshal(map[string]string{
		"username": "astrbot",
		"password": "astrbot123456",
	})
	resp, err := http.Post(c.webuiURL()+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var d struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&d) != nil || d.Data.Token == "" {
		return ""
	}
	c.token = d.Data.Token
	return c.token
}

// api 调用 AstrBot 插件 API (GET/POST), 返回解码 map
func (c *AstrbotClient) api(path, method string, payload any) (map[string]any, error) {
	tok := c.login()
	if tok == "" {
		return nil, fmt.Errorf("无法登录 AstrBot")
	}
	url := c.webuiURL() + "/api/plug/whitelist_manager/" + path
	var body io.Reader
	if payload != nil {
		b, _ := json.Marshal(payload)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	// 401 → 重试一次
	if resp.StatusCode == 401 {
		c.token = ""
		tok2 := c.login()
		if tok2 == "" {
			return nil, fmt.Errorf("AstrBot 未授权")
		}
		req2, _ := http.NewRequest(method, url, body)
		req2.Header.Set("Authorization", "Bearer "+tok2)
		req2.Header.Set("Content-Type", "application/json")
		resp2, err2 := client.Do(req2)
		if err2 == nil {
			defer resp2.Body.Close()
			json.NewDecoder(resp2.Body).Decode(&out)
		}
	}
	return out, nil
}

// registerAstrbot 注册 AstrBot 相关 API
func (h *Handler) RegisterAstrbot(cfg *config.Config) {
	ac := NewAstrbotClient(cfg)

	// 凭据
	h.HandleFunc("/api/astrbot/creds", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, ExtractCreds(cfg))
	})

	// whitelist contacts
	h.HandleFunc("/api/whitelist/contacts", func(w http.ResponseWriter, r *http.Request) {
		data, err := ac.api("contacts", "GET", nil)
		if err != nil {
			jsonOK(w, map[string]any{"ok": false, "message": err.Error(), "contacts": []any{}, "rooms": []any{}})
			return
		}
		// 补全历史群 (messages.jsonl 中的群名, wechat4u 刚登录只返回部分群)
		rooms := toAnySlice(data["rooms"])
		known := map[string]bool{}
		for _, r2 := range rooms {
			if rm, ok := r2.(map[string]any); ok {
				if h, ok := rm["hashId"].(float64); ok {
					known[fmt.Sprintf("%d", int64(h))] = true
				}
			}
		}
		for _, name := range historyRoomNames(cfg) {
			hid := util.HashName(name)
			if !known[fmt.Sprintf("%d", hid)] {
				rooms = append(rooms, map[string]any{
					"name": name, "hashId": hid, "id": name, "fromHist": true,
				})
				known[fmt.Sprintf("%d", hid)] = true
			}
		}
		contacts := toAnySlice(data["contacts"])
		jsonOK(w, map[string]any{"ok": true, "contacts": contacts, "rooms": rooms})
	})

	// whitelist GET (含 nameMap)
	h.HandleFunc("/api/whitelist", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var payload struct {
				ChatIDs  []string `json:"chatIds"`
				AdminIDs []string `json:"adminIds"`
			}
			json.NewDecoder(r.Body).Decode(&payload)
			out, err := ac.api("whitelist", "POST", payload)
			if err != nil {
				jsonOK(w, map[string]any{"ok": false, "message": err.Error()})
				return
			}
			jsonOK(w, out)
			return
		}
		out, err := ac.api("whitelist", "GET", nil)
		if err != nil {
			jsonOK(w, map[string]any{"ok": false, "message": err.Error()})
			return
		}
		// nameMap: hashId(名字) → 名字
		nameMap := map[string]string{}
		if cdata, err2 := ac.api("contacts", "GET", nil); err2 == nil {
			for _, c := range toAnySlice(cdata["contacts"]) {
				if cm, ok := c.(map[string]any); ok {
					nm, _ := cm["name"].(string)
					if nm != "" {
						nameMap[fmt.Sprintf("%d", util.HashName(nm))] = nm
					}
				}
			}
			for _, r2 := range toAnySlice(cdata["rooms"]) {
				if rm, ok := r2.(map[string]any); ok {
					nm, _ := rm["name"].(string)
					if nm != "" {
						nameMap[fmt.Sprintf("%d", util.HashName(nm))] = nm
					}
				}
			}
		}
		out["nameMap"] = nameMap
		jsonOK(w, out)
	})

	// whitelist super (写 cmd_config)
	h.HandleFunc("/api/whitelist/super", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			SuperAdminIDs []string `json:"superAdminIds"`
		}
		json.NewDecoder(r.Body).Decode(&payload)
		cfgPath := cfg.Astrbot.CmdConfig
		if _, err := os.Stat(cfgPath); err != nil {
			jsonOK(w, map[string]any{"ok": false, "message": "cmd_config.json 不存在"})
			return
		}
		m, err := util.ReadJSONFile(cfgPath)
		if err != nil {
			jsonOK(w, map[string]any{"ok": false, "message": "解析失败: " + err.Error()})
			return
		}
		m["super_admins_id"] = payload.SuperAdminIDs
		if err := util.AtomicWriteJSON(cfgPath, m, true); err != nil {
			jsonOK(w, map[string]any{"ok": false, "message": "写入失败: " + err.Error()})
			return
		}
		jsonOK(w, map[string]any{"ok": true, "message": "超级管理员已更新", "superAdminIds": payload.SuperAdminIDs})
	})

	// 备份列表
	h.HandleFunc("/api/backups", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, listBackups(backupDir()))
	})

	// 变更预览
	h.HandleFunc("/api/astrbot/setup/preview", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, setupPreview(cfg))
	})

	// OneBot 一键配置
	h.HandleFunc("/api/astrbot/setup", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonErr(w, 405, "仅支持 POST")
			return
		}
		msg, err := setupOneBot(cfg)
		if err != nil {
			jsonOK(w, map[string]any{"ok": false, "message": err.Error()})
			return
		}
		jsonOK(w, map[string]any{"ok": true, "message": msg})
	})

	// 恢复
	h.HandleFunc("/api/astrbot/restore", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonErr(w, 405, "仅支持 POST")
			return
		}
		path := r.URL.Query().Get("path")
		if err := restoreConfig(cfg, path); err != nil {
			jsonOK(w, map[string]any{"ok": false, "message": err.Error()})
			return
		}
		jsonOK(w, map[string]any{"ok": true, "message": "已从备份恢复"})
	})
}

// toStringSlice 安全转 []string
func toStringSlice(v any) []string {
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// toAnySlice 安全转 []any
func toAnySlice(v any) []any {
	arr, _ := v.([]any)
	return arr
}

// AstrbotConfigured 检查 aiocqhttp 平台是否已配置且指向 ws_port (与 Python _astrbot_platform_ok 一致)
func AstrbotConfigured(cfg *config.Config) bool {
	m, err := util.ReadJSONFile(cfg.Astrbot.CmdConfig)
	if err != nil {
		return false
	}
	platforms, _ := m["platform"].([]any)
	for _, p := range platforms {
		pm, _ := p.(map[string]any)
		if pm != nil && pm["id"] == cfg.Astrbot.PlatformID {
			en, _ := pm["enable"].(bool)
			port, _ := pm["ws_reverse_port"].(float64)
			return en && int(port) == cfg.Astrbot.WSPort
		}
	}
	return false
}

// ExtractCreds 提取 AstrBot 凭据 (与 Python 一致)
func ExtractCreds(cfg *config.Config) map[string]any {
	out := map[string]any{"username": nil, "password": nil, "source": nil, "password_changed": false}
	m, err := util.ReadJSONFile(cfg.Astrbot.CmdConfig)
	if err != nil {
		return out
	}
	dash, _ := m["dashboard"].(map[string]any)
	if dash == nil {
		return out
	}
	if u, ok := dash["username"].(string); ok && u != "" {
		out["username"] = u
	}
	if upgraded, _ := dash["password_storage_upgraded"].(bool); upgraded {
		out["password_changed"] = true
		out["source"] = "cmd_config"
		return out
	}
	if p, ok := dash["password"].(string); ok && p != "" {
		out["password"] = p
		out["source"] = "cmd_config"
	}
	return out
}

// historyRoomNames 从 messages.jsonl 提取历史聊过的群名 (按条数降序)
func historyRoomNames(cfg *config.Config) []string {
	path := filepath.Join(cfg.WechatBotDir, ".data", "wechat", "messages.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	counts := map[string]int{}
	sc := newLineScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		if isRoom, _ := m["isRoom"].(bool); isRoom {
			if rm, ok := m["roomName"].(string); ok && rm != "" {
				counts[rm]++
			}
		}
	}
	// 按条数降序
	names := make([]string, 0, len(counts))
	for n := range counts {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return counts[names[i]] > counts[names[j]] })
	return names
}

// listBackups 列备份
func listBackups(dir string) map[string]any {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return map[string]any{"ok": true, "backups": []any{}}
	}
	var items []map[string]any
	for i := len(entries) - 1; i >= 0; i-- {
		d := entries[i]
		if !d.IsDir() {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(dir, d.Name()))
		for _, f := range files {
			fi, _ := f.Info()
			size := int64(0)
			if fi != nil {
				size = fi.Size()
			}
			items = append(items, map[string]any{
				"time": d.Name(), "path": filepath.Join(dir, d.Name(), f.Name()), "size": size,
			})
		}
	}
	return map[string]any{"ok": true, "backups": items}
}
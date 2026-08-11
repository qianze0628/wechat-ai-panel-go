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
		if h.authCheck != nil && !h.authCheck(r) {
			jsonErr(w, 401, "未认证或会话已过期")
			return
		}
		jsonOK(w, ExtractCreds(cfg))
	})

	// whitelist contacts
	h.HandleFunc("/api/whitelist/contacts", func(w http.ResponseWriter, r *http.Request) {
		if h.authCheck != nil && !h.authCheck(r) {
			jsonErr(w, 401, "未认证或会话已过期")
			return
		}
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
		// wechat4u 只能同步"有消息进来的群", 其余群在消息记录里但当前 session 没加载。
		// 近期(默认 3 天)有消息的群 = 仍在群里的活跃群 (标 fromUnsynced, 不标历史)
		// 更早的群 = 可能已退出 (标 fromHist 历史)
		for _, item := range historyRoomInfos(cfg) {
			name := item["name"].(string)
			hid := util.HashName(name)
			if known[fmt.Sprintf("%d", hid)] {
				continue
			}
			lastActive, _ := item["lastActive"].(int64)
			isRecent := time.Now().Unix()-lastActive <= 3*86400
			rooms = append(rooms, map[string]any{
				"name": name, "hashId": hid, "id": name,
				"fromHist":    !isRecent,
				"fromUnsynced": isRecent,
				"lastActive":  lastActive,
			})
			known[fmt.Sprintf("%d", hid)] = true
		}
		contacts := toAnySlice(data["contacts"])
		// 给群补活跃发言者 (消息记录反推真实昵称) + 构造 memberList (真实名并集)
		for _, r := range rooms {
			rm, ok := r.(map[string]any)
			if !ok {
				continue
			}
			nm, _ := rm["name"].(string)
			if nm == "" {
				continue
			}
			active := roomActiveMembers(cfg, nm)
			rm["activeNames"] = active
			ml, unk := mergeRoomMembers(rm, active, nm)
			rm["memberList"] = ml
			rm["unknownMemberCount"] = unk
			// 人数兜底: wechat4u 未同步的群 (fromUnsynced) 没有 memberCount/members,
			// 用 memberList(消息记录真实名) + 未知名成员数 估算总人数, 避免显示 0
			total := len(ml) + unk
			if cur, ok2 := rm["memberCount"].(float64); !ok2 || int(cur) <= 0 {
				rm["memberCount"] = total
			}
		}
		jsonOK(w, map[string]any{"ok": true, "contacts": contacts, "rooms": rooms})
	})

	// whitelist GET (含 nameMap)
	h.HandleFunc("/api/whitelist", func(w http.ResponseWriter, r *http.Request) {
		if h.authCheck != nil && !h.authCheck(r) {
			jsonErr(w, 401, "未认证或会话已过期")
			return
		}
		if r.Method == http.MethodPost {
			var payload struct {
				ChatIDs              []string            `json:"chatIds"`
				AdminIDs             []string            `json:"adminIds"`
				ExcludedGroupMembers map[string][]string `json:"excludedGroupMembers"`
			}
			json.NewDecoder(r.Body).Decode(&payload)
			// 修复 (2026-08-11): 面板勾选保存只传裸 hashId → AstrBot 私聊 unified_msg_origin=
			// "wechat-bridge:FriendMessage:<hashName>" 匹配不上 → 白名单拦截不回复 (王朝阳案例)。
			// 后端归一化: 用 contacts/rooms 分辨联系人(私聊)与群聊, 补齐
			// "wechat-bridge:FriendMessage:<hashName>" / "wechat-bridge:GroupMessage:<hashName>" 配套条目,
			// 与微信内 /白名单添加 (whitelist_manager) 三连格式完全一致。
			payload.ChatIDs = normalizeWhitelistIDs(ac, payload.ChatIDs)
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
		// superAdminIds/superAdminNames: 从 cmd_config.json 读 super_admins_id (与 Py 版一致)
		if cfgPath := cfg.Astrbot.CmdConfig; cfgPath != "" {
			if m, err2 := util.ReadJSONFile(cfgPath); err2 == nil {
				var supers []string
				switch v := m["super_admins_id"].(type) {
				case []any:
					for _, x := range v {
						supers = append(supers, fmt.Sprintf("%v", x))
					}
				case []string:
					supers = v
				}
				out["superAdminIds"] = supers
				supNames := make([]string, 0, len(supers))
				for _, sid := range supers {
					if n, ok2 := nameMap[sid]; ok2 {
						supNames = append(supNames, n)
					} else {
						supNames = append(supNames, sid)
					}
				}
				out["superAdminNames"] = supNames
			}
		}
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

// normalizeWhitelistIDs 归一化面板保存的白名单 ID 列表:
//   - 前端只传裸 hashId (勾选的联系人/群) → 用 contacts/rooms 分辨私聊/群聊,
//     补齐 "wechat-bridge:FriendMessage:<hashName>" / "wechat-bridge:GroupMessage:<hashName>"
//   - 已存在的 FriendMessage:/GroupMessage: 条目原样保留 (去重)
//
// 修复 (2026-08-11 王朝阳案例): AstrBot 私聊 unified_msg_origin 为
// "wechat-bridge:FriendMessage:<hashName>", 只存裸 hashId 会被 WhitelistCheckStage 拦截 → 不回复。
func normalizeWhitelistIDs(ac *AstrbotClient, chatIDs []string) []string {
	if len(chatIDs) == 0 {
		return chatIDs
	}
	// 从 wechat-bot 拉联系人/群列表, 建立 hashId → (名字, 是否群) 映射
	contactHash := map[string]string{} // hashId → 名字
	roomHash := map[string]string{}    // hashId → 群名
	if cdata, err := ac.api("contacts", "GET", nil); err == nil {
		for _, c := range toAnySlice(cdata["contacts"]) {
			if cm, ok := c.(map[string]any); ok {
				nm, _ := cm["name"].(string)
				if nm == "" {
					nm, _ = cm["rawName"].(string)
				}
				hid := fmt.Sprintf("%v", cm["hashId"])
				if hid != "" && nm != "" {
					contactHash[hid] = nm
				}
			}
		}
		for _, r := range toAnySlice(cdata["rooms"]) {
			if rm, ok := r.(map[string]any); ok {
				nm, _ := rm["name"].(string)
				hid := fmt.Sprintf("%v", rm["hashId"])
				if hid != "" && nm != "" {
					roomHash[hid] = nm
				}
			}
		}
	}

	seen := map[string]bool{}
	var out []string
	for _, id := range chatIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
		// 裸 hashId 且已知名字 → 补齐 UMO 配套条目
		if strings.Contains(id, ":") {
			continue // 已是完整条目 (FriendMessage:/GroupMessage:), 保留
		}
		if nm, ok := contactHash[id]; ok {
			h := fmt.Sprintf("%d", util.HashName(nm))
			for _, x := range []string{h, "wechat-bridge:FriendMessage:" + h} {
				if !seen[x] {
					seen[x] = true
					out = append(out, x)
				}
			}
		} else if nm, ok := roomHash[id]; ok {
			h := fmt.Sprintf("%d", util.HashName(nm))
			x := "wechat-bridge:GroupMessage:" + h
			if !seen[x] {
				seen[x] = true
				out = append(out, x)
			}
		}
	}
	return out
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

// historyRoomInfos 从 messages.jsonl 提取历史聊过的群 (按条数降序)
// 返回 [{name, count, lastActive}], 仅最近 activeDays 天内有消息的群
func historyRoomInfos(cfg *config.Config) []map[string]any {
	const activeDays = 30
	cutoff := time.Now().Add(-activeDays * 24 * time.Hour).Unix()
	path := filepath.Join(cfg.WechatBotDir, ".data", "wechat", "messages.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	counts := map[string]int{}
	lastTs := map[string]int64{}
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
				if ts, ok := m["timestamp"].(string); ok {
					if t, err := time.Parse(time.RFC3339, ts); err == nil {
						if t.Unix() > lastTs[rm] {
							lastTs[rm] = t.Unix()
						}
					}
				}
			}
		}
	}
	names := make([]string, 0, len(counts))
	for n := range counts {
		t := lastTs[n]
		if t == 0 || t >= cutoff {
			names = append(names, n)
		}
	}
	sort.Slice(names, func(i, j int) bool { return counts[names[i]] > counts[names[j]] })
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		out = append(out, map[string]any{"name": n, "count": counts[n], "lastActive": lastTs[n]})
	}
	return out
}

// historyRoomNames 兼容: 只返回群名
func historyRoomNames(cfg *config.Config) []string {
	infos := historyRoomInfos(cfg)
	out := make([]string, 0, len(infos))
	for _, it := range infos {
		if n, ok := it["name"].(string); ok {
			out = append(out, n)
		}
	}
	return out
}

// roomActiveMembers 返回某群历史活跃发言者名 (按条数降序)
func roomActiveMembers(cfg *config.Config, roomName string) []string {
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
		if isRoom, _ := m["isRoom"].(bool); !isRoom {
			continue
		}
		if rn, _ := m["roomName"].(string); rn != roomName {
			continue
		}
		t, _ := m["talkerName"].(string)
		if t != "" && t != roomName {
			counts[t]++
		}
	}
	names := make([]string, 0, len(counts))
	for n := range counts {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return counts[names[i]] > counts[names[j]] })
	if len(names) > 30 {
		names = names[:30]
	}
	return names
}

// mergeRoomMembers 合并群成员: bot 侧 members (有名字的) + 消息记录真实昵称 (activeNames),
// 去重后返回 memberList (带 hashId, 可直接勾选进白名单) 和拿不到名字的 bot 侧成员数
func mergeRoomMembers(rm map[string]any, activeNames []string, roomName string) ([]map[string]any, int) {
	existing := map[string]map[string]any{}
	var order []string
	add := func(name, rawId, source string) {
		name = strings.TrimSpace(name)
		if name == "" || name == "未知名成员" {
			return
		}
		if _, ok := existing[name]; ok {
			return
		}
		existing[name] = map[string]any{
			"rawId":  rawId,
			"name":   name,
			"hashId": util.HashName(name),
			"source": source,
		}
		order = append(order, name)
	}
	unknown := 0
	if raw, ok := rm["members"].([]any); ok {
		for _, x := range raw {
			mm, ok := x.(map[string]any)
			if !ok {
				continue
			}
			mn, _ := mm["name"].(string)
			rawID, _ := mm["rawId"].(string)
			if strings.TrimSpace(mn) == "" || mn == "未知名成员" {
				unknown++
				continue
			}
			add(mn, rawID, "wechat")
		}
	}
	for _, n := range activeNames {
		if n == roomName {
			continue
		}
		add(n, n, "messages")
	}
	out := make([]map[string]any, 0, len(order))
	for _, n := range order {
		out = append(out, existing[n])
	}
	return out, unknown
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
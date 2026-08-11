// Package api ChatUI 链路测试: 发送消息到 wechat-bot 6189 /api/chat, 走完整链路
// 链路: 信息 → wechat-bot → AstrBot → 模型 → 回复
// 回复查询: 从 bot 日志/messages.jsonl 找最近回复 (模型回复由 AstrBot 回发)
package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"wechat-ai-panel/internal/config"
	"wechat-ai-panel/internal/util"
)

// RegisterChat ChatUI API
//   - GET  /api/chat/send?text=xxx&contact=xxx&test=1        发送 (注入 wechat-bot 完整链路)
//   - GET  /api/chat/replies?user=xxx                        查询最近回复 (从日志解析)
//   - GET  /api/chat/contacts                                可发联系人 (直连 wechat-bot 6189)
func (h *Handler) RegisterChat(cfg *config.Config) {
	h.mux.HandleFunc("/api/chat/contacts", func(w http.ResponseWriter, r *http.Request) {
		if h.authCheck != nil && !h.authCheck(r) {
			jsonErr(w, 401, "未认证或会话已过期")
			return
		}
		url := fmt.Sprintf("http://127.0.0.1:%d/api/contacts", cfg.Services.Wechat.APIPort)
		resp, err := httpGetTimeout(url, 5000)
		if err != nil {
			jsonOK(w, map[string]any{"ok": false, "contacts": []any{}, "message": err.Error()})
			return
		}
		var out map[string]any
		if err := json.Unmarshal(resp, &out); err != nil {
			jsonOK(w, map[string]any{"ok": false, "contacts": []any{}})
			return
		}
		contacts := out["contacts"]
		if contacts == nil {
			contacts = []any{}
		}
		jsonOK(w, map[string]any{"ok": true, "contacts": contacts})
	})

	h.mux.HandleFunc("/api/chat/send", func(w http.ResponseWriter, r *http.Request) {
		if h.authCheck != nil && !h.authCheck(r) {
			jsonErr(w, 401, "未认证或会话已过期")
			return
		}
		text := r.URL.Query().Get("text")
		if text == "" {
			jsonErr(w, 400, "缺少 text 参数")
			return
		}
		contact := r.URL.Query().Get("contact")
		group := r.URL.Query().Get("group")
		test := r.URL.Query().Get("test")

		// 修复 (2026-08-11): ChatUI 注入联系人不在 AstrBot 白名单 → WhitelistCheckStage 拦截
		// → LLM 无响应 → 显示"发送成功"但对方收不到。注入前自动把该联系人/群加进白名单。
		if contact != "" {
			ensureWhitelistContact(cfg, contact)
		} else if group != "" {
			ensureWhitelistGroup(cfg, group)
		}

		// 调 wechat-bot /api/chat (注入消息走完整链路)
		url := fmt.Sprintf("http://127.0.0.1:%d/api/chat?text=%s", cfg.Services.Wechat.APIPort, urlEncode(text))
		if contact != "" {
			url += "&contact=" + urlEncode(contact)
		} else if group != "" {
			url += "&group=" + urlEncode(group)
		} else if test == "1" {
			url += "&test=1"
		}
		resp, err := httpGetTimeout(url, 20000)
		if err != nil {
			jsonErr(w, 502, "wechat-bot 未响应 (链路不通): "+err.Error())
			return
		}
		var out map[string]any
		if err := json.Unmarshal(resp, &out); err != nil {
			jsonErr(w, 500, "解析响应失败")
			return
		}
		ok, _ := out["ok"].(bool)
		if !ok {
			msg, _ := out["message"].(string)
			jsonErr(w, 502, "注入失败: "+msg)
			return
		}
		jsonOK(w, map[string]any{
			"ok": true, "message": "消息已发送 (信息→wechatbot→AstrBot→模型)", "t": time.Now().Unix(),
		})
	})

	// 回复查询: 从 bot 日志尾部解析 AstrBot 回复 (📤 发送消息)
	h.mux.HandleFunc("/api/chat/replies", func(w http.ResponseWriter, r *http.Request) {
		if h.authCheck != nil && !h.authCheck(r) {
			jsonErr(w, 401, "未认证或会话已过期")
			return
		}
		user := r.URL.Query().Get("user")
		content := util.ReadTail(cfg.Logs.WechatStdout, 512*1024)
		replies := []map[string]any{}
		lines := strings.Split(content, "\n")
		for i := len(lines) - 1; i >= 0 && len(replies) < 10; i-- {
			line := strings.TrimSpace(lines[i])
			if strings.Contains(line, "发送消息:") && strings.Contains(line, "→") {
				// 提取回复文本 + 目标 user (C1: 按 user 过滤, 只显示本次会话)
				idx := strings.Index(line, "发送消息:")
				if idx >= 0 {
					rest := strings.TrimSpace(line[idx+len("发送消息:"):])
					text := rest
					targetUser := ""
					if j := strings.Index(rest, " → user="); j >= 0 {
						text = rest[:j]
						rest2 := rest[j+len(" → user="):]
						if k := strings.Index(rest2, " "); k >= 0 {
							targetUser = rest2[:k]
						} else {
							targetUser = rest2
						}
					}
					// 图片 base64 摘要不显示
					if strings.Contains(text, "image=base64") || strings.Contains(text, "image=无") {
						continue
					}
					if user != "" && targetUser != user {
						continue
					}
					text = strings.TrimSpace(text)
					text = strings.Trim(text, `"'`)
					replies = append(replies, map[string]any{
						"text": text, "time": time.Now().Unix() - int64(i/2), "user": targetUser,
					})
				}
			}
		}
		jsonOK(w, map[string]any{"ok": true, "replies": replies})
	})
}

// ===== 白名单自动添加 (ChatUI 注入链路) =====
// 修复 (2026-08-11): 之前 /api/chat/send 只代理注入, 不检查 AstrBot 白名单。
// 若联系人不在 id_whitelist → WhitelistCheckStage 拦截 → LLM 无响应 → ChatUI 显示"已发送"但对方收不到。
// 现在: 注入前把联系人/群 (hashId + 会话格式 UMO) 写入 cmd_config.json, 与 whitelist_manager 插件格式一致;
// 配合 AstrBot WhitelistCheckStage 热重载补丁 (mtime 检测), 写入立即生效, 无需重启。

// ensureWhitelistContact 确保联系人已在 AstrBot 白名单 (按名字从 wechat-bot 拉 hashId)
// 返回 (是否新增, 联系人名, 错误)
func ensureWhitelistContact(cfg *config.Config, contact string) (bool, string, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/api/contacts", cfg.Services.Wechat.APIPort)
	resp, err := httpGetTimeout(url, 5000)
	if err != nil {
		return false, contact, err
	}
	var out struct {
		Contacts []struct {
			Name    string `json:"name"`
			RawName string `json:"rawName"`
			HashID  any    `json:"hashId"` // 可能是 number (32538961) 或 string, 用 any 兼容
		} `json:"contacts"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return false, contact, err
	}
	// 精确匹配名字 (alias || rawName)
	for _, c := range out.Contacts {
		if c.Name == contact || c.RawName == contact {
			added, err := ensureWhitelistAdd(cfg, hashIdString(c.HashID), c.Name, c.RawName, false)
			return added, c.Name, err
		}
	}
	return false, contact, fmt.Errorf("未在微信联系人中找到 '%s' (可能未登录或名字不匹配)", contact)
}

// ensureWhitelistGroup 确保群已在 AstrBot 白名单
func ensureWhitelistGroup(cfg *config.Config, group string) (bool, string, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/api/contacts", cfg.Services.Wechat.APIPort)
	resp, err := httpGetTimeout(url, 5000)
	if err != nil {
		return false, group, err
	}
	var out struct {
		Rooms []struct {
			Name   string `json:"name"`
			HashID any    `json:"hashId"` // 可能是 number 或 string
		} `json:"rooms"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return false, group, err
	}
	for _, r := range out.Rooms {
		if r.Name == group {
			added, err := ensureWhitelistAdd(cfg, hashIdString(r.HashID), r.Name, "", true)
			return added, r.Name, err
		}
	}
	return false, group, fmt.Errorf("未在微信群聊中找到 '%s' (可能未登录或群名不匹配)", group)
}

// ensureWhitelistAdd 把 hashId + hashId(名字) + 完整 UMO 写入 cmd_config.json id_whitelist
// 与 whitelist_manager 插件 /白名单添加 完全一致的格式, 保证 AstrBot 匹配
func ensureWhitelistAdd(cfg *config.Config, hashID, name, rawName string, isGroup bool) (bool, error) {
	// 读现有配置 (保留 admins_id 等所有字段)
	// 注意: AstrBot 写文件带 UTF-8 BOM (utf-8-sig), 需剥离后 json.Unmarshal
	raw, err := os.ReadFile(cfg.Astrbot.CmdConfig)
	if err != nil {
		return false, fmt.Errorf("读取 cmd_config.json 失败: %w", err)
	}
	raw = []byte(strings.TrimPrefix(string(raw), "\ufeff"))
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return false, fmt.Errorf("解析 cmd_config.json 失败: %w", err)
	}
	ps, _ := data["platform_settings"].(map[string]any)
	if ps == nil {
		ps = map[string]any{}
		data["platform_settings"] = ps
	}
	// 现有白名单 (可能是 []any 或 []string)
	existing := map[string]bool{}
	var wl []string
	switch v := ps["id_whitelist"].(type) {
	case []any:
		for _, x := range v {
			s := fmt.Sprint(x)
			wl = append(wl, s)
			existing[s] = true
		}
	case []string:
		for _, x := range v {
			wl = append(wl, x)
			existing[x] = true
		}
	}
	// 构造要加的 ID 形式 (与 whitelist_manager L609-618 一致)
	addIDs := []string{hashID}
	hashName := hashNameOf(name)
	if hashName != "" {
		addIDs = append(addIDs, hashName)
	}
	if !isGroup {
		if hashName != "" {
			addIDs = append(addIDs, "wechat-bridge:FriendMessage:"+hashName)
		}
		if rawName != "" && rawName != name {
			hashRaw := hashNameOf(rawName)
			if hashRaw != "" {
				addIDs = append(addIDs, hashRaw, "wechat-bridge:FriendMessage:"+hashRaw)
			}
		}
	} else if hashName != "" {
		addIDs = append(addIDs, "wechat-bridge:GroupMessage:"+hashName)
	}
	added := false
	for _, x := range addIDs {
		if x != "" && !existing[x] {
			wl = append(wl, x)
			existing[x] = true
			added = true
		}
	}
	if !added {
		return false, nil // 已在白名单, 无需写
	}
	ps["id_whitelist"] = wl
	// 保持 enable_id_white_list 开启 (ChatUI 注入依赖白名单检查)
	if _, ok := ps["enable_id_white_list"]; !ok {
		ps["enable_id_white_list"] = true
	}
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return false, err
	}
	// 原子写: 先写临时文件再替换, 避免写一半 AstrBot 热重载读到残缺配置
	tmp := cfg.Astrbot.CmdConfig + ".chatui.tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, cfg.Astrbot.CmdConfig); err != nil {
		return false, err
	}
	log.Printf("[chat] 已自动加入白名单: %s (hashId=%s) %s", name, hashID,
		map[bool]string{true: "[群]", false: "[联系人]"}[isGroup])
	return true, nil
}

// hashIdString 把 API 返回的 hashId (number 或 string) 规范成纯数字字符串
// json 数字在 Go 里是 float64, fmt.Sprint(3.2538961e+07) 会变科学计数法 → 必须转整数
func hashIdString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return fmt.Sprintf("%.0f", t)
	case float32:
		return fmt.Sprintf("%.0f", t)
	case int64:
		return fmt.Sprint(t)
	case int:
		return fmt.Sprint(t)
	case json.Number:
		return t.String()
	default:
		return fmt.Sprint(v)
	}
}

// hashNameOf 计算与 wechat-bot bridge-integration.js 一致的 hashId(name)
// JS: ((h<<5)-h+charCode)|0 循环 → Math.abs(h)+10000
func hashNameOf(s string) string {
	var h int32
	for _, ch := range s {
		h = h*31 + ch // 注意: Go rune 是 int32, 与 JS 每字符处理一致 (JS 用 UTF-16 code unit, 中文 BMP 内相同)
	}
	h32 := int32(h)
	if h32 < 0 {
		return fmt.Sprint(-int64(h32) + 10000)
	}
	return fmt.Sprint(int64(h32) + 10000)
}

// urlEncode 简单 URL 编码 (中文/空格) — 逐 UTF-8 字节编码
// 修复: 之前遍历 rune 用 %02X 只保留低 8 位, 中文 (如 秦=0x79E6) 被编码成 %E6 丢失两字节 → 转发 contact=秦晓洁 失败
func urlEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i] // 字节
		switch {
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~':
			b.WriteByte(c)
		case c == ' ':
			b.WriteString("%20")
		default:
			b.WriteString(fmt.Sprintf("%%%02X", c))
		}
	}
	return b.String()
}
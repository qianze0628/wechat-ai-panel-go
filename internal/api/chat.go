// Package api ChatUI 链路测试: 发送消息到 wechat-bot 6189 /api/chat, 走完整链路
// 链路: 信息 → wechat-bot → AstrBot → 模型 → 回复
// 回复查询: 从 bot 日志/messages.jsonl 找最近回复 (模型回复由 AstrBot 回发)
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
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
		test := r.URL.Query().Get("test")
		// 调 wechat-bot /api/chat (注入消息走完整链路)
		url := fmt.Sprintf("http://127.0.0.1:%d/api/chat?text=%s", cfg.Services.Wechat.APIPort, urlEncode(text))
		if contact != "" {
			url += "&contact=" + urlEncode(contact)
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
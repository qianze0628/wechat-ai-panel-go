// Package api wechat-bot .env 群聊配置读写 API (回复所有群聊开关等)
// 修复 (2026-08-11): 用户要求"回复所有群聊"开关 — 开启后回复所有群(黑名单除外),
// 关闭则只回复白名单中的群。该逻辑由 wechat-bot .env 的 ROOM_WHITELIST 控制
// (空 = 回复所有群, 非空 = 只回复白名单群); ROOM_MEMBER_EXCLUDE 为黑名单成员。
// 本 API 提供 .env 的读写, 并同步 AstrBot 白名单 (群聊 hashId)。
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"wechat-ai-panel/internal/config"
)

// wechatEnvPath 返回 wechat-bot .env 路径
func wechatEnvPath(cfg *config.Config) string {
	return filepath.Join(cfg.WechatBotDir, ".env")
}

// readWechatEnv 读取 .env 为 map (只读存在的 key)
func readWechatEnv(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if idx := strings.Index(line, "="); idx > 0 {
			key := strings.TrimSpace(line[:idx])
			val := strings.TrimSpace(line[idx+1:])
			// 去引号
			if len(val) >= 2 && (val[0] == '\'' && val[len(val)-1] == '\'' || val[0] == '"' && val[len(val)-1] == '"') {
				val = val[1 : len(val)-1]
			}
			out[key] = val
		}
	}
	return out, nil
}

// writeWechatEnv 更新 .env 中指定 key (保留其他行与注释)
func writeWechatEnv(path string, updates map[string]string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(raw), "\n")
	seen := map[string]bool{}
	updated := []string{}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if idx := strings.Index(line, "="); idx > 0 && !strings.HasPrefix(trimmed, "#") {
			key := strings.TrimSpace(line[:idx])
			if newVal, ok := updates[key]; ok {
				// 保留注释? .env key 行重写 (带引号)
				val := newVal
				if !strings.HasPrefix(val, "'") && !strings.HasPrefix(val, `"`) {
					// 无引号包裹 → 统一用单引号 (与现有 .env 一致)
					val = "'" + val + "'"
				}
				line = key + "=" + val
				seen[key] = true
			}
		}
		updated = append(updated, line)
	}
	// 追加缺失的 key
	for k, v := range updates {
		if seen[k] {
			continue
		}
		if !strings.HasPrefix(v, "'") && !strings.HasPrefix(v, `"`) {
			v = "'" + v + "'"
		}
		// 追加到文件末尾
		updated = append(updated, fmt.Sprintf("%s=%s", k, v))
	}
	// 保持行尾风格 (检测 CRLF)
	crlf := strings.Contains(string(raw), "\r\n")
	out := strings.Join(updated, "\n")
	if crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	return os.WriteFile(path, []byte(out+"\n"), 0o644)
}

// RegisterWechatEnv wechat-bot .env 群聊配置 API
//   - GET  /api/wechat-env           返回群聊相关配置 (replyAllGroups 等)
//   - POST /api/wechat-env          更新群聊配置 (replyAllGroups / room_whitelist / room_member_exclude)
func (h *Handler) RegisterWechatEnv(cfg *config.Config) {
	h.mux.HandleFunc("/api/wechat-env", func(w http.ResponseWriter, r *http.Request) {
		if h.authCheck != nil && !h.authCheck(r) {
			jsonErr(w, 401, "未认证或会话已过期")
			return
		}
		path := wechatEnvPath(cfg)
		switch r.Method {
		case http.MethodGet:
			env, err := readWechatEnv(path)
			if err != nil {
				jsonOK(w, map[string]any{"ok": false, "message": "读取 .env 失败: " + err.Error()})
				return
			}
			roomWL := env["ROOM_WHITELIST"]
			roomChat := env["ROOM_CHAT_ENABLED"] != "false"
			jsonOK(w, map[string]any{
				"ok": true,
				"config": map[string]any{
					// 回复所有群聊: 群白名单为空 → true
					"replyAllGroups":     roomWL == "",
					"room_whitelist":     roomWL,
					"room_member_exclude": env["ROOM_MEMBER_EXCLUDE"],
					"no_mention_rooms":   env["NO_MENTION_ROOMS"],
					"room_chat_enabled":  roomChat,
					"bot_name":           env["BOT_NAME"],
				},
				"path": path,
			})
		case http.MethodPost:
			var body struct {
				// 开启回复所有群聊 (群白名单清空 / 保留黑名单)
				ReplyAllGroups     *bool  `json:"reply_all_groups"`
				RoomWhitelist      string `json:"room_whitelist"`
				RoomMemberExclude  string `json:"room_member_exclude"`
				NoMentionRooms     string `json:"no_mention_rooms"`
				RoomChatEnabled    *bool  `json:"room_chat_enabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				jsonErr(w, 400, "请求体需为 JSON: "+err.Error())
				return
			}
			// 读当前 .env
			env, err := readWechatEnv(path)
			if err != nil {
				jsonErr(w, 500, "读取 .env 失败: "+err.Error())
				return
			}
			updates := map[string]string{}
			roomWL := env["ROOM_WHITELIST"]
			if body.ReplyAllGroups != nil {
				if *body.ReplyAllGroups {
					// 开启回复所有群: 群白名单清空 (黑名单 ROOM_MEMBER_EXCLUDE 保留)
					updates["ROOM_WHITELIST"] = ""
					roomWL = ""
				} else {
					// 关闭: 需要群白名单 (本次提交的 或 现有的)
					if strings.TrimSpace(body.RoomWhitelist) == "" && roomWL == "" {
						jsonErr(w, 400, "关闭回复所有群聊需提供群白名单 (room_whitelist)")
						return
					}
				}
			}
			// 显式设置白名单 (关闭回复所有群时的群列表; 开启时也允许显式覆盖)
			if body.RoomWhitelist != "" {
				// 防御 (2026-08-11): 编码破坏检测 — 群名全是 '?' 是编码损坏特征
				// (PowerShell/旧工具写中文变 '?'), 拒绝写入避免群聊全被白名单拦截
				trimmedWL := strings.TrimSpace(body.RoomWhitelist)
				if strings.Contains(trimmedWL, "?") {
					qCount := strings.Count(trimmedWL, "?")
					if qCount >= len(strings.ReplaceAll(trimmedWL, "?", "")) && qCount > 0 {
						jsonErr(w, 400, "群白名单含编码损坏字符 '?', 已拒绝写入。请重新选择群聊保存 (中文群名需 UTF-8 编码)")
						return
					}
				}
				updates["ROOM_WHITELIST"] = trimmedWL
				roomWL = trimmedWL
			}
			if body.RoomChatEnabled != nil {
				updates["ROOM_CHAT_ENABLED"] = map[bool]string{true: "true", false: "false"}[*body.RoomChatEnabled]
			}
			if body.RoomMemberExclude != "" {
				updates["ROOM_MEMBER_EXCLUDE"] = body.RoomMemberExclude
			}
			if body.NoMentionRooms != "" {
				updates["NO_MENTION_ROOMS"] = body.NoMentionRooms
			}
			if len(updates) == 0 {
				jsonErr(w, 400, "无更新内容")
				return
			}
			if err := writeWechatEnv(path, updates); err != nil {
				jsonErr(w, 500, "写入 .env 失败: "+err.Error())
				return
			}
			jsonOK(w, map[string]any{
				"ok":      true,
				"message": "群聊配置已保存 (重启 wechat-bot 生效)",
				"config": map[string]any{
					"replyAllGroups":  roomWL == "",
					"room_whitelist":  roomWL,
					"room_member_exclude": env["ROOM_MEMBER_EXCLUDE"],
				},
			})
		default:
			jsonErr(w, 405, "仅支持 GET/POST")
		}
	})
}
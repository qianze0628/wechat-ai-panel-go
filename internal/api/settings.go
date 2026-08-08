// Package api 面板设置路由: /api/settings (读/写面板可编辑配置项)
// 可编辑项 (写回 config.json, 原子替换): panel_password, backup_enabled
package api

import (
	"encoding/json"
	"net/http"

	"wechat-ai-panel/internal/config"
	"wechat-ai-panel/internal/util"
)

// settingsConfigPath 主 config.json 的路径 (由 RegisterSettings 注入, 解决 exe/cwd 差异)
var settingsConfigPath = ""
var settingsConfigLocalPath = ""

// SetSettingsConfigPath 设置 config.json 完整路径 (main.go 注入)
func SetSettingsConfigPath(path string) { settingsConfigPath = path }

// SetSettingsConfigLocalPath 设置 config.local.json 完整路径 (main.go 注入)
func SetSettingsConfigLocalPath(path string) { settingsConfigLocalPath = path }

// configFilePath 主 config.json (config.local.json 只读覆盖, 设置页写主文件)
func configFilePath(cfg *config.Config) string {
	if settingsConfigPath != "" {
		return settingsConfigPath
	}
	return ""
}

// configLocalFilePath config.local.json (网络设置写这里, 覆盖主文件)
func configLocalFilePath(cfg *config.Config) string {
	if settingsConfigLocalPath != "" {
		return settingsConfigLocalPath
	}
	return ""
}

// RegisterSettings 注册设置 API (需要在 main.go 里于 RegisterAuth 之后调用)
func (h *Handler) RegisterSettings(cfg *config.Config) {
	h.HandleFunc("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			authEnabled := cfg.PanelPassword != ""
			backupEnabled := true // 默认备份开启
			if p := configFilePath(cfg); p != "" {
				if m, err := util.ReadJSONFile(p); err == nil {
					if b, ok := m["backup_enabled"].(bool); ok {
						backupEnabled = b
					}
				}
			}
			jsonOK(w, map[string]any{
				"ok": true, "auth_enabled": authEnabled,
				"backup_enabled": backupEnabled,
				"config_path":    configFilePath(cfg),
			})
		case http.MethodPost:
			if h.authCheck != nil && !h.authCheck(r) {
				jsonErr(w, 401, "未认证或会话已过期")
				return
			}
			var body struct {
				PanelPassword *string `json:"panel_password"`
				BackupEnabled *bool   `json:"backup_enabled"`
				Host          *string `json:"host"`
				Port          *int    `json:"port"`
				MirrorNpm     *string `json:"mirror_npm"`
				MirrorPypi    *string `json:"mirror_pypi"`
				MirrorGit     *string `json:"mirror_git"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				jsonErr(w, 400, "请求体必须是 JSON")
				return
			}
			path := configFilePath(cfg)
			if path == "" {
				jsonErr(w, 500, "找不到 config.json")
				return
			}
			// 读现有 config.json
			m, err := util.ReadJSONFile(path)
			if err != nil {
				m = map[string]any{}
			}
			var changes []string
			if body.PanelPassword != nil {
				newPwd := *body.PanelPassword
				oldPwd, _ := m["panel_password"].(string)
				if newPwd != oldPwd {
					m["panel_password"] = newPwd
					if newPwd != "" {
						changes = append(changes, "面板认证已启用")
					} else {
						changes = append(changes, "面板认证已关闭")
					}
				}
			}
			if body.BackupEnabled != nil {
				newVal := *body.BackupEnabled
				oldVal := true
				if b, ok := m["backup_enabled"].(bool); ok {
					oldVal = b
				}
				if newVal != oldVal {
					m["backup_enabled"] = newVal
					if newVal {
						changes = append(changes, "配置备份已启用")
					} else {
						changes = append(changes, "配置备份已关闭")
					}
				}
			}
			// 网络: host / port / 镜像 (写 config.local.json)
			if body.Host != nil || body.Port != nil || body.MirrorNpm != nil || body.MirrorPypi != nil || body.MirrorGit != nil {
				localPath := configLocalFilePath(cfg)
				var lm map[string]any
				if localPath != "" {
					lm, _ = util.ReadJSONFile(localPath)
				}
				if lm == nil {
					lm = map[string]any{}
				}
				// mirrors 子对象
				if body.MirrorNpm != nil || body.MirrorPypi != nil || body.MirrorGit != nil {
					mir, _ := lm["mirrors"].(map[string]any)
					if mir == nil {
						mir = map[string]any{}
					}
					if body.MirrorNpm != nil {
						mir["npm_registry"] = *body.MirrorNpm
					}
					if body.MirrorPypi != nil {
						mir["pypi_index"] = *body.MirrorPypi
					}
					if body.MirrorGit != nil {
						mir["git_clone_proxy"] = *body.MirrorGit
					}
					lm["mirrors"] = mir
					changes = append(changes, "镜像配置已更新")
				}
				if body.Host != nil {
					lm["host"] = *body.Host
					changes = append(changes, "监听地址已更新 (重启生效)")
				}
				if body.Port != nil {
					lm["port"] = *body.Port
					changes = append(changes, "端口已更新 (重启生效)")
				}
				if localPath != "" {
					if err := util.AtomicWriteJSON(localPath, lm, true); err != nil {
						jsonErr(w, 500, "写入 config.local.json 失败: "+err.Error())
						return
					}
				}
			}
			if len(changes) == 0 {
				jsonOK(w, map[string]any{"ok": true, "message": "无变更", "changes": []string{}})
				return
			}
			if err := util.AtomicWriteJSON(path, m, true); err != nil {
				jsonErr(w, 500, "写入 config.json 失败: "+err.Error())
				return
			}
			// 同步内存配置
			if body.PanelPassword != nil {
				cfg.PanelPassword = *body.PanelPassword
				if h.setAuth != nil {
					h.setAuth.SetPassword(*body.PanelPassword)
				}
			}
			if body.BackupEnabled != nil {
				cfg.BackupEnabled = *body.BackupEnabled
				SetBackupEnabled(*body.BackupEnabled)
			}
			// 认证变更标记: 开启/修改密码 → auth_changed (前端跳登录页输新密码);
			// 关闭认证 → auth_disabled (前端免登录直接进, 不跳登录页)
			out := map[string]any{"ok": true, "message": "设置已保存: " + joinStr(changes), "changes": changes}
			if body.PanelPassword != nil {
				if *body.PanelPassword != "" {
					out["auth_changed"] = true
				} else {
					out["auth_disabled"] = true
				}
			}
			jsonOK(w, out)
		default:
			jsonErr(w, 405, "仅支持 GET/POST")
		}
	})
}
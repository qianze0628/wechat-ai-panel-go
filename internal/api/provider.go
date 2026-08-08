// Package api 模型提供商管理: 读/写 cmd_config.json 的 provider 列表 + provider_settings
// 数据源: cmd_config.json { provider: [{id, key, enable, ...}], provider_settings: {...} }
// 保存时原子写 cmd_config (自动备份)
package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"wechat-ai-panel/internal/config"
	"wechat-ai-panel/internal/util"
)

// RegisterProvider 模型提供商 API
//   - GET  /api/providers            读 provider 列表 + provider_settings
//   - POST /api/providers            保存完整列表+settings (原子写, 备份)
func (h *Handler) RegisterProvider(cfg *config.Config) {
	h.mux.HandleFunc("/api/providers", func(w http.ResponseWriter, r *http.Request) {
		if h.authCheck != nil && !h.authCheck(r) {
			jsonErr(w, 401, "未认证或会话已过期")
			return
		}
		cfgPath := cfg.Astrbot.CmdConfig
		if _, err := os.Stat(cfgPath); err != nil {
			jsonErr(w, 404, "cmd_config.json 不存在: "+cfgPath)
			return
		}
		switch r.Method {
		case http.MethodGet:
			m, err := util.ReadJSONFile(cfgPath)
			if err != nil {
				jsonErr(w, 500, "读取 cmd_config 失败: "+err.Error())
				return
			}
			providers, _ := m["provider"].([]any)
			ps, _ := m["provider_settings"].(map[string]any)
			jsonOK(w, map[string]any{"ok": true, "providers": providers, "provider_settings": ps})
		case http.MethodPost:
			var body struct {
				Providers []json.RawMessage `json:"providers"`
				Settings  map[string]any     `json:"provider_settings"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				jsonErr(w, 400, "请求体需为 JSON")
				return
			}
			m, err := util.ReadJSONFile(cfgPath)
			if err != nil {
				jsonErr(w, 500, "读取 cmd_config 失败: "+err.Error())
				return
			}
			// 组装 providers (校验每项是对象)
			var provArr []any
			for _, raw := range body.Providers {
				var one map[string]any
				single := json.RawMessage(raw)
				if len(single) > 0 && single[0] == '"' {
					var s string
					if err := json.Unmarshal(single, &s); err == nil {
						single = json.RawMessage(s)
					}
				}
				if err := json.Unmarshal(single, &one); err != nil {
					jsonErr(w, 400, "providers 项必须是对象")
					return
				}
				provArr = append(provArr, one)
			}
			m["provider"] = provArr
			if body.Settings != nil {
				m["provider_settings"] = body.Settings
			}
			// 原子写 + 备份
			if err := writeJSONAtomicBackup(cfgPath, cfg.AstrbotDataDir, m); err != nil {
				jsonErr(w, 500, "保存失败: "+err.Error())
				return
			}
			jsonOK(w, map[string]any{"ok": true, "message": "模型提供商已保存 (重启 AstrBot 生效)"})
		default:
			jsonErr(w, 405, "仅支持 GET/POST")
		}
	})
}

// writeJSONAtomicBackup 原子写 JSON + 备份 (通用)
func writeJSONAtomicBackup(cfgPath, dataDir string, obj map[string]any) error {
	// 备份
	backupDir := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return err
	}
	ts := time.Now().Format("20060102-150405.000")
	if raw, err := os.ReadFile(cfgPath); err == nil {
		_ = os.WriteFile(filepath.Join(backupDir, "cmd_config."+ts+".json"), raw, 0o644)
	}
	raw, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(cfgPath), ".cmd-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	if err := os.WriteFile(tmpPath, raw, 0o644); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, cfgPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

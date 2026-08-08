// Package api 知识库管理: 读/写 cmd_config 的 kb 相关字段 + 扫描 kb.db 同目录文件
// 不直接操作 SQLite (避免与 AstrBot 写入冲突), 只读文件清单 + 配置字段
// 集合实体由 AstrBot 创建 (WebUI), 此处管理配置项 kb_names/default_kb_collection
package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"wechat-ai-panel/internal/config"
	"wechat-ai-panel/internal/util"
)

// RegisterKB 知识库 API
//   - GET  /api/kb            读配置字段 + 知识库文件清单
//   - POST /api/kb           保存配置字段 (kb_names/default_kb_collection/kb_fusion_top_k/kb_final_top_k/kb_agentic_mode)
func (h *Handler) RegisterKB(cfg *config.Config) {
	h.mux.HandleFunc("/api/kb", func(w http.ResponseWriter, r *http.Request) {
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
			// 读 kb 配置 (AstrBot 的 kb_* 字段在 cmd_config 顶层, 不在 provider_settings)
			kbNames, _ := m["kb_names"].([]any)
			var names []string
			for _, n := range kbNames {
				if s, ok := n.(string); ok {
					names = append(names, s)
				}
			}
			if names == nil {
				names = []string{}
			}
			kbCfg := map[string]any{
				"kb_names":              names,
				"default_kb_collection": m["default_kb_collection"],
				"kb_fusion_top_k":       m["kb_fusion_top_k"],
				"kb_final_top_k":        m["kb_final_top_k"],
				"kb_agentic_mode":       m["kb_agentic_mode"],
			}
			// 文件清单 (data/knowledge_base/)
			files := []map[string]any{}
			kbDir := filepath.Join(cfg.AstrbotDataDir, "knowledge_base")
			if entries, err := os.ReadDir(kbDir); err == nil {
				for _, e := range entries {
					fi, _ := e.Info()
					files = append(files, map[string]any{
						"name": e.Name(), "size": fi.Size(), "dir": e.IsDir(),
					})
				}
			}
			sort.Slice(files, func(i, j int) bool { return files[i]["name"].(string) < files[j]["name"].(string) })
			jsonOK(w, map[string]any{"ok": true, "config": kbCfg, "files": files})
		case http.MethodPost:
			var body struct {
				KbNames               []string `json:"kb_names"`
				DefaultCollection     string   `json:"default_kb_collection"`
				FusionTopK            *int     `json:"kb_fusion_top_k"`
				FinalTopK             *int     `json:"kb_final_top_k"`
				AgenticMode           *bool    `json:"kb_agentic_mode"`
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
			// AstrBot 的 kb_* 在顶层; 保存也写顶层 (对齐 default schema)
			m["kb_names"] = body.KbNames
			m["default_kb_collection"] = body.DefaultCollection
			if body.FusionTopK != nil {
				m["kb_fusion_top_k"] = *body.FusionTopK
			}
			if body.FinalTopK != nil {
				m["kb_final_top_k"] = *body.FinalTopK
			}
			if body.AgenticMode != nil {
				m["kb_agentic_mode"] = *body.AgenticMode
			}
			if err := writeJSONAtomicBackup(cfgPath, cfg.AstrbotDataDir, m); err != nil {
				jsonErr(w, 500, "保存失败: "+err.Error())
				return
			}
			jsonOK(w, map[string]any{"ok": true, "message": "知识库配置已保存 (重启 AstrBot 生效)"})
		default:
			jsonErr(w, 405, "仅支持 GET/POST")
		}
	})
}

// listKBFiles 列出 knowledge_base 目录文件 (供前端展示)
func listKBFiles(dataDir string) []map[string]any {
	kbDir := filepath.Join(dataDir, "knowledge_base")
	var files []map[string]any
	entries, err := os.ReadDir(kbDir)
	if err != nil {
		return files
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".db-wal") || strings.HasSuffix(e.Name(), ".db-shm") {
			continue
		}
		fi, _ := e.Info()
		files = append(files, map[string]any{"name": e.Name(), "size": fi.Size(), "dir": e.IsDir()})
	}
	return files
}
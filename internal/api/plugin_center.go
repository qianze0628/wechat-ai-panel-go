// Package api 插件中心: 扫描 AstrBot 插件 + 配置读写 + 连通状态
// 数据源: {astrbot_data_dir}/plugins/<name>/
//   - metadata.yaml  插件元数据 (name/display_name/desc/version/author/support_platforms)
//   - _conf_schema.json 配置 schema (自动渲染表单)
//   - config.json    当前配置值
// 适配状态: support_platforms 含 aiocqhttp 视为"适配 wechat-bot"
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"wechat-ai-panel/internal/config"
)

// PluginInfo 插件信息 (前端展示)
type PluginInfo struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	DisplayName     string   `json:"display_name"`
	Desc            string   `json:"desc"`
	Version         string   `json:"version"`
	Author          string   `json:"author"`
	Repo            string   `json:"repo"`
	SupportPlatforms []string `json:"support_platforms"`
	Enabled         bool     `json:"enabled"`
	// 连通状态: 适配 wechat-bot (aiocqhttp) 与否
	Compatible      bool     `json:"compatible"`
	CompatibleNote  string   `json:"compatible_note"`
	HasConfig       bool     `json:"has_config"`
	ConfigPath      string   `json:"config_path"`
	ConfSchemaPath  string   `json:"conf_schema_path"`
}

// pluginsDir 返回 AstrBot 插件目录
func pluginsDir(cfg *config.Config) string {
	return filepath.Join(cfg.AstrbotDataDir, "plugins")
}

// parseMetadataYaml 极简 metadata.yaml KV 解析 (缩进 2 空格结构)
// 解析顶层字段 + support_platforms 列表
func parseMetadataYaml(content string) map[string]any {
	out := map[string]any{}
	var lastKey string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// support_platforms: 下的 "- aiocqhttp" 列表项
		if strings.HasPrefix(line, "  - ") || strings.HasPrefix(line, "    - ") {
			item := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			item = strings.Trim(item, `"'`)
			if lastKey == "support_platforms" {
				arr, _ := out["support_platforms"].([]string)
				out["support_platforms"] = append(arr, item)
			}
			continue
		}
		// 顶层 key: value (兼容 "key: >" 多行块首行)
		if idx := strings.Index(line, ":"); idx >= 0 && !strings.HasPrefix(trimmed, "-") {
			key := strings.TrimSpace(line[:idx])
			val := strings.TrimSpace(line[idx+1:])
			lastKey = key
			// 多行块 (> 开头) 只取第一行, 其余忽略
			if strings.HasPrefix(val, ">") {
				val = strings.TrimSpace(strings.TrimPrefix(val, ">"))
			}
			// 去掉行内注释 (value 中的 " # xxx")
			if ci := strings.Index(val, " #"); ci >= 0 {
				val = strings.TrimSpace(val[:ci])
			}
			// 去掉首尾引号
			val = strings.Trim(val, `"'`)
			// 版本号去重前缀 v (metadata 里常是 v1.0.0)
			if key == "version" {
				val = strings.TrimPrefix(val, "v")
			}
			out[key] = val
		}
	}
	return out
}


// safePluginDir 安全拼接插件目录: 拒绝路径穿越 (id 只能是插件子目录名)
func safePluginDir(cfg *config.Config, id string) (string, bool) {
	base := pluginsDir(cfg)
	if id == "" || strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return "", false
	}
	pdir := filepath.Join(base, id)
	rel, err := filepath.Rel(base, pdir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	if fi, err := os.Stat(pdir); err != nil || !fi.IsDir() {
		return "", false
	}
	return pdir, true
}

// scanPlugins 扫描插件目录, 返回插件列表
func scanPlugins(cfg *config.Config) []PluginInfo {
	dir := pluginsDir(cfg)
	var result []PluginInfo
	entries, err := os.ReadDir(dir)
	if err != nil {
		return result
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		pdir := filepath.Join(dir, name)

		// 元数据
		meta := map[string]any{}
		metaBytes, err := os.ReadFile(filepath.Join(pdir, "metadata.yaml"))
		if err == nil {
			meta = parseMetadataYaml(string(metaBytes))
		}
		// 显示名/描述兜底
		displayName, _ := meta["display_name"].(string)
		if displayName == "" {
			displayName, _ = meta["name"].(string)
		}
		if displayName == "" {
			displayName = name
		}
		desc, _ := meta["desc"].(string)
		version, _ := meta["version"].(string)
		author, _ := meta["author"].(string)
		repo, _ := meta["repo"].(string)
		platforms, _ := meta["support_platforms"].([]string)

		// 启用状态: 目录下存在 .disabled 文件 = 禁用
		enabled := true
		if _, err := os.Stat(filepath.Join(pdir, ".disabled")); err == nil {
			enabled = false
		}

		// 连通状态: aiocqhttp 适配
		compatible := false
		note := ""
		for _, p := range platforms {
			if strings.Contains(strings.ToLower(p), "aiocqhttp") || strings.Contains(strings.ToLower(p), "onebot") {
				compatible = true
				break
			}
		}
		if len(platforms) == 0 {
			// 无 platforms 声明: 默认按兼容处理 (多数插件通用)
			compatible = true
			note = "未声明平台, 按通用插件处理"
		} else if !compatible {
			note = "声明平台: " + strings.Join(platforms, ", ") + " (非 OneBot, 可能不适配微信)"
		} else {
			note = "适配 OneBot 协议, 可与 wechat-bot 协同"
		}

		hasConfig := false
		cfgPath := ""
		schemaPath := ""
		if _, err := os.Stat(filepath.Join(pdir, "config.json")); err == nil {
			hasConfig = true
			cfgPath = filepath.Join(pdir, "config.json")
		}
		if _, err := os.Stat(filepath.Join(pdir, "_conf_schema.json")); err == nil {
			schemaPath = filepath.Join(pdir, "_conf_schema.json")
		} else if _, err := os.Stat(filepath.Join(pdir, "_conf_schema.yaml")); err == nil {
			schemaPath = filepath.Join(pdir, "_conf_schema.yaml")
		}

		result = append(result, PluginInfo{
			ID: name, Name: name, DisplayName: displayName, Desc: desc,
			Version: version, Author: author, Repo: repo,
			SupportPlatforms: platforms, Enabled: enabled,
			Compatible: compatible, CompatibleNote: note,
			HasConfig: hasConfig, ConfigPath: cfgPath, ConfSchemaPath: schemaPath,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// RegisterPluginCenter 注册插件中心 API
//   - GET  /api/plugin-center         插件列表
//   - GET  /api/plugin-center/config?id=x   插件配置 (值 + schema)
//   - POST /api/plugin-center/config?id=x   保存配置
//   - POST /api/plugin-center/toggle?id=x&enabled=true  启用/禁用 (写 .disabled 标记)
func (h *Handler) RegisterPluginCenter(cfg *config.Config) {
	// 插件列表
	h.mux.HandleFunc("/api/plugin-center", func(w http.ResponseWriter, r *http.Request) {
		if h.authCheck != nil && !h.authCheck(r) {
			jsonErr(w, 401, "未认证或会话已过期")
			return
		}
		jsonOK(w, map[string]any{"ok": true, "plugins": scanPlugins(cfg)})
	})

	// 插件配置 (GET 读值+schema; POST 保存)
	h.mux.HandleFunc("/api/plugin-center/config", func(w http.ResponseWriter, r *http.Request) {
		if h.authCheck != nil && !h.authCheck(r) {
			jsonErr(w, 401, "未认证或会话已过期")
			return
		}
		id := r.URL.Query().Get("id")
		pdir, ok := safePluginDir(cfg, id)
		if !ok {
			jsonErr(w, 400, "非法的插件 id")
			return
		}
		switch r.Method {
		case http.MethodGet:
			// 读 config.json (不存在返回空对象)
			cfgVal := map[string]any{}
			raw, err := os.ReadFile(filepath.Join(pdir, "config.json"))
			if err == nil {
				_ = json.Unmarshal(raw, &cfgVal)
			}
			// 读 schema
			var schema any
			if _, err := os.Stat(filepath.Join(pdir, "_conf_schema.json")); err == nil {
				rawS, _ := os.ReadFile(filepath.Join(pdir, "_conf_schema.json"))
				_ = json.Unmarshal(rawS, &schema)
			} else if _, err := os.Stat(filepath.Join(pdir, "_conf_schema.yaml")); err == nil {
				rawS, _ := os.ReadFile(filepath.Join(pdir, "_conf_schema.yaml"))
				_ = json.Unmarshal(rawS, &schema)
			}
			jsonOK(w, map[string]any{"ok": true, "config": cfgVal, "schema": schema})
		case http.MethodPost:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				jsonErr(w, 400, "请求体需为 JSON")
				return
			}
			val, _ := body["config"].(map[string]any)
			// 写 config.json (原子: 先写临时再 rename)
			raw, _ := json.MarshalIndent(val, "", "  ")
			tmp := filepath.Join(pdir, "config.json.tmp")
			if err := os.WriteFile(tmp, raw, 0o644); err != nil {
				jsonErr(w, 500, "写入失败: "+err.Error())
				return
			}
			if err := os.Rename(tmp, filepath.Join(pdir, "config.json")); err != nil {
				jsonErr(w, 500, "替换失败: "+err.Error())
				return
			}
			jsonOK(w, map[string]any{"ok": true, "message": "配置已保存 (重启 AstrBot 生效)"})
		default:
			jsonErr(w, 405, "仅支持 GET/POST")
		}
	})

	// 启用/禁用 (写 .disabled 标记)
	h.mux.HandleFunc("/api/plugin-center/toggle", func(w http.ResponseWriter, r *http.Request) {
		if h.authCheck != nil && !h.authCheck(r) {
			jsonErr(w, 401, "未认证或会话已过期")
			return
		}
		if r.Method != http.MethodPost {
			jsonErr(w, 405, "仅支持 POST")
			return
		}
		id := r.URL.Query().Get("id")
		enabledStr := r.URL.Query().Get("enabled")
		pdir, ok := safePluginDir(cfg, id)
		if !ok {
			jsonErr(w, 400, "非法的插件 id")
			return
		}
		disabledFile := filepath.Join(pdir, ".disabled")
		enabled, perr := strconv.ParseBool(enabledStr)
		if perr != nil {
			jsonErr(w, 400, "enabled 需为 true/false")
			return
		}
		if enabled {
			_ = os.Remove(disabledFile)
		} else {
			_ = os.WriteFile(disabledFile, []byte("disabled by panel\n"), 0o644)
		}
		jsonOK(w, map[string]any{
			"ok": true, "message": fmt.Sprintf("插件已%s (重启 AstrBot 生效)", map[bool]string{true: "启用", false: "禁用"}[enabled]),
		})
	})
}

// RegisterCmdConfig 注册 AstrBot 配置文件读写 API (仿 AstrBot 配置文件页)
//   - GET  /api/cmd-config   读取 cmd_config.json (备份后)
//   - POST /api/cmd-config   保存 (原子写 + 自动备份)
func (h *Handler) RegisterCmdConfig(cfg *config.Config) {
	h.mux.HandleFunc("/api/cmd-config", func(w http.ResponseWriter, r *http.Request) {
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
			data, err := os.ReadFile(cfgPath)
			if err != nil {
				jsonErr(w, 500, "读取失败: "+err.Error())
				return
			}
			// 兼容 BOM
			raw := strings.TrimPrefix(string(data), "")
			jsonOK(w, map[string]any{"ok": true, "config": json.RawMessage(raw), "path": cfgPath})
		case http.MethodPost:
			var body struct {
				// 前端可能传 对象 或 JSON字符串, 统一用 json.RawMessage 接住再双重解析
				Config json.RawMessage `json:"config"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Config) == 0 {
				jsonErr(w, 400, "请求体需含 config")
				return
			}
			// 关键: 兼容前端传 JSON字符串 的场景 (先当字符串解, 再当对象解)
			cfgRaw := body.Config
			// 若整体是带引号的字符串字面量, 先解出内层 JSON
			if len(cfgRaw) > 0 && cfgRaw[0] == '"' {
				var s string
				if err := json.Unmarshal(cfgRaw, &s); err == nil {
					cfgRaw = json.RawMessage(s)
				}
			}
			// 必须是 JSON 对象 (命令空间: map)
			var m map[string]any
			if err := json.Unmarshal(cfgRaw, &m); err != nil {
				jsonErr(w, 400, "config 必须是 JSON 对象: "+err.Error())
				return
			}
			// 备份当前配置
			backupDir := filepath.Join(cfg.AstrbotDataDir, "backups")
			if err := os.MkdirAll(backupDir, 0o755); err != nil {
				jsonErr(w, 500, "备份目录创建失败: "+err.Error())
				return
			}
			ts := time.Now().Format("20060102-150405.000")
			if raw, err := os.ReadFile(cfgPath); err == nil {
				if err := os.WriteFile(filepath.Join(backupDir, "cmd_config."+ts+".json"), raw, 0o644); err != nil {
					jsonOK(w, map[string]any{"ok": false, "message": "备份失败, 已取消保存: " + err.Error()})
					return
				}
			}
			// 合法校验 + 美化写回 (原子: 临时文件带随机后缀 + rename)
			var pretty bytes.Buffer
			if err := json.Indent(&pretty, cfgRaw, "", "  "); err != nil {
				jsonErr(w, 400, "配置格式化失败")
				return
			}
			tmp, err := os.CreateTemp(filepath.Dir(cfgPath), ".cmd-config-*.tmp")
			if err != nil {
				jsonErr(w, 500, "创建临时文件失败: "+err.Error())
				return
			}
			tmpPath := tmp.Name()
			_ = tmp.Close()
			if err := os.WriteFile(tmpPath, pretty.Bytes(), 0o644); err != nil {
				jsonErr(w, 500, "写入失败: "+err.Error())
				return
			}
			if err := os.Rename(tmpPath, cfgPath); err != nil {
				_ = os.Remove(tmpPath)
				jsonErr(w, 500, "替换失败: "+err.Error())
				return
			}
			// 备份文件保留最近 10 份
			cleanupOldBackups(backupDir, 10)
			jsonOK(w, map[string]any{"ok": true, "message": "配置已保存 (重启 AstrBot 生效)", "backup": true})
		default:
			jsonErr(w, 405, "仅支持 GET/POST")
		}
	})
}

// cleanupOldBackups 保留最近 N 份备份, 删除更早的
func cleanupOldBackups(dir string, keep int) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "cmd_config.") && strings.HasSuffix(f.Name(), ".json") {
			names = append(names, f.Name())
		}
	}
	sort.Strings(names) // 时间戳命名 → 字典序 = 时间序
	for i := 0; i < len(names)-keep; i++ {
		_ = os.Remove(filepath.Join(dir, names[i]))
	}
}
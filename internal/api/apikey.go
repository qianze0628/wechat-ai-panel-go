// Package api API 密钥管理 (面板 OpenAPI): 生成/撤销面板 API Key
// 密钥存 {dataDir}/.panel_api_keys.json; 只有面板自己用 (HTTP 请求带 X-Api-Key)
// 用途: 自动化脚本/外部服务调用面板 API (等同 OpenAPI)
package api

import (
	"encoding/json"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"wechat-ai-panel/internal/config"
)

const apiKeysFile = "api_keys.json"

// apiKeyStore 读密钥文件
func readAPIKeys(dataDir string) (map[string]time.Time, error) {
	keys := map[string]time.Time{}
	raw, err := os.ReadFile(filepath.Join(dataDir, apiKeysFile))
	if err != nil {
		return keys, nil // 无文件 = 空
	}
	var m map[string]string // key → created RFC3339
	if err := json.Unmarshal(raw, &m); err != nil {
		return keys, nil
	}
	for k, v := range m {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			keys[k] = t
		}
	}
	return keys, nil
}

// writeAPIKeys 写密钥文件
func writeAPIKeys(dataDir string, keys map[string]time.Time) error {
	m := map[string]string{}
	for k, t := range keys {
		m[k] = t.Format(time.RFC3339)
	}
	raw, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(filepath.Join(dataDir, apiKeysFile), raw, 0o600)
}

// genAPIKey 生成随机 32 字节 hex key
func genAPIKey() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return "pan_" + hex.EncodeToString(b)
}

// RegisterAPIKey 面板 API 密钥管理
//   - GET   /api/apikeys    列出密钥 (仅显示前缀+时间)
//   - POST  /api/apikeys    生成新密钥
//   - POST  /api/apikeys/del {key} 撤销
func (h *Handler) RegisterAPIKey(cfg *config.Config) {
	h.mux.HandleFunc("/api/apikeys", func(w http.ResponseWriter, r *http.Request) {
		if h.authCheck != nil && !h.authCheck(r) {
			jsonErr(w, 401, "未认证或会话已过期")
			return
		}
		dataDir := filepath.Join(cfg.ProjectRoot, ".panel_keys")
		switch r.Method {
		case http.MethodGet:
			_ = os.MkdirAll(dataDir, 0o700)
			keys, _ := readAPIKeys(dataDir)
			list := []map[string]any{}
			for k, t := range keys {
				list = append(list, map[string]any{
					"key": k,
					"created": t.Format("2006-01-02 15:04"),
					"prefix": k[:12] + "…",
				})
			}
			jsonOK(w, map[string]any{"ok": true, "keys": list, "count": len(list)})
		case http.MethodPost:
			// 语义: body.key 非空 → 撤销; 否则生成
			var body struct{ Key string `json:"key"` }
			_ = json.NewDecoder(r.Body).Decode(&body)
			_ = os.MkdirAll(dataDir, 0o700)
			keys, _ := readAPIKeys(dataDir)
			if body.Key != "" {
				// 撤销
				if _, ok := keys[body.Key]; !ok {
					jsonErr(w, 404, "密钥不存在")
					return
				}
				delete(keys, body.Key)
				_ = writeAPIKeys(dataDir, keys)
				jsonOK(w, map[string]any{"ok": true, "message": "密钥已撤销"})
				return
			}
			// 生成
			key := genAPIKey()
			keys[key] = time.Now()
			if err := writeAPIKeys(dataDir, keys); err != nil {
				jsonErr(w, 500, "保存失败: "+err.Error())
				return
			}
			jsonOK(w, map[string]any{"ok": true, "key": key, "message": "密钥已生成 (仅显示一次, 请妥善保存)"})
		default:
			jsonErr(w, 405, "仅支持 GET/POST")
		}
	})
}

// apiKeyValid 校验请求是否携带有效 API Key (供中间件)
func apiKeyValid(cfg *config.Config, r *http.Request) bool {
	key := r.Header.Get("X-Api-Key")
	if key == "" {
		return false
	}
	dirK := filepath.Join(cfg.ProjectRoot, ".panel_keys")
	keys, _ := readAPIKeys(dirK)
	_, ok := keys[key]
	return ok
}
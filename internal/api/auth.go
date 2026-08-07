// Package api 面板认证: /api/auth/login /api/auth/status (panel_password)
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"wechat-ai-panel/internal/config"
)

// AuthStore 认证状态
type AuthStore struct {
	mu      sync.Mutex
	enabled bool
	passwd  string
	tokens  map[string]int64 // token → expire epoch
	ttl     int64
}

// NewAuth 创建认证 (panel_password 非空时启用)
func NewAuth(cfg *config.Config) *AuthStore {
	return &AuthStore{
		enabled: cfg.PanelPassword != "",
		passwd:  cfg.PanelPassword,
		tokens:  map[string]int64{},
		ttl:     12 * 3600,
	}
}

// Check 校验请求是否已认证
func (a *AuthStore) Check(r *http.Request) bool {
	if !a.enabled {
		return true
	}
	tok := r.Header.Get("X-Auth-Token")
	if tok == "" {
		if c, err := r.Cookie("panel_token"); err == nil {
			tok = c.Value
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.tokens[tok]
	return ok && exp > time.Now().Unix()
}

// RegisterAuth 注册认证 API
func (h *Handler) RegisterAuth(cfg *config.Config) {
	auth := NewAuth(cfg)
	// 注入写操作认证检查
	h.authCheck = func(r *http.Request) bool { return auth.Check(r) }

	h.HandleFunc("/api/auth/status", func(w http.ResponseWriter, r *http.Request) {
		if !auth.enabled {
			jsonOK(w, map[string]any{"enabled": false, "authed": true})
			return
		}
		jsonOK(w, map[string]any{"enabled": true, "authed": auth.Check(r)})
	})

	h.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonErr(w, 405, "仅支持 POST")
			return
		}
		if !auth.enabled {
			jsonOK(w, map[string]any{"ok": true, "message": "面板未启用认证"})
			return
		}
		var body struct {
			Password string `json:"password"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Password != auth.passwd {
			jsonErr(w, 401, "密码错误")
			return
		}
		// 生成 token
		b := make([]byte, 16)
		rand.Read(b)
		token := hex.EncodeToString(b)
		auth.mu.Lock()
		auth.tokens[token] = time.Now().Unix() + auth.ttl
		auth.mu.Unlock()
		// 返回 JSON + cookie
		jsonOK(w, map[string]any{"ok": true, "message": "登录成功"})
		http.SetCookie(w, &http.Cookie{
			Name: "panel_token", Value: token, Path: "/",
			HttpOnly: true, MaxAge: int(auth.ttl), SameSite: http.SameSiteLaxMode,
		})
	})
}
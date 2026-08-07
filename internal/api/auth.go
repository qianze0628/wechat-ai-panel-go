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
	a.mu.Lock()
	enabled := a.enabled
	a.mu.Unlock()
	if !enabled {
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

// SetPassword 运行时更新密码/开关 (设置页保存后热生效, 无需重启)
func (a *AuthStore) SetPassword(pwd string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	changed := pwd != a.passwd
	a.passwd = pwd
	a.enabled = pwd != ""
	// 密码不变/关闭时都清空会话: 改密后旧 token 立即失效, 关闭认证时也清空
	if changed {
		a.tokens = map[string]int64{}
	}
}

// IssueToken 发放 token (登录/开启认证后调用)
func (a *AuthStore) IssueToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	token := hex.EncodeToString(b)
	a.mu.Lock()
	a.tokens[token] = time.Now().Unix() + a.ttl
	a.mu.Unlock()
	return token
}

// RegisterAuth 注册认证 API
func (h *Handler) RegisterAuth(cfg *config.Config) {
	auth := NewAuth(cfg)
	// 注入写操作认证检查
	h.authCheck = func(r *http.Request) bool { return auth.Check(r) }

	h.HandleFunc("/api/auth/status", func(w http.ResponseWriter, r *http.Request) {
		auth.mu.Lock()
		enabled := auth.enabled
		auth.mu.Unlock()
		if !enabled {
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
		auth.mu.Lock()
		enabled := auth.enabled
		passwd := auth.passwd
		ttl := auth.ttl
		auth.mu.Unlock()
		if !enabled {
			jsonOK(w, map[string]any{"ok": true, "message": "面板未启用认证"})
			return
		}
		var body struct {
			Password string `json:"password"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Password != passwd {
			jsonErr(w, 401, "密码错误")
			return
		}
		token := auth.IssueToken()
		// 先写 cookie 头, 再写 JSON 响应 (jsonOK 会写 header, 顺序不能反)
		http.SetCookie(w, &http.Cookie{
			Name: "panel_token", Value: token, Path: "/",
			HttpOnly: true, MaxAge: int(ttl), SameSite: http.SameSiteLaxMode,
		})
		jsonOK(w, map[string]any{"ok": true, "message": "登录成功"})
	})

	// 供 settings 路由热更新认证
	h.setAuth = auth
}
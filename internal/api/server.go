// Package api HTTP 路由层 (标准库 net/http, 轻量)
package api

import (
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"strings"
)

// Handler 聚合路由
type Handler struct {
	webFS fs.FS   // 嵌入的前端静态资源 (dist)
	upFn  func() any // 应用状态聚合 (由上层注入, 简单解耦)
	auth  func(r *http.Request) bool
}

// New 创建 HTTP handler
func New(webFS fs.FS) *Handler {
	return &Handler{webFS: webFS}
}

// SetStatusHandler 设置 /api/status 的聚合函数 (来自上层)
func (h *Handler) SetStatusHandler(fn func() any) { h.upFn = fn }

// Handler 实现 http.Handler
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// API 路由
	if strings.HasPrefix(path, "/api/") {
		h.handleAPI(w, r)
		return
	}

	// 静态: /static/* 从嵌入 FS 提供
	if strings.HasPrefix(path, "/static/") {
		serveFS(w, r, h.webFS, strings.TrimPrefix(path, "/"))
		return
	}

	// 根/SPA: 返回 index.html (deep link 回退)
	serveIndex(w, h.webFS)
}

func (h *Handler) handleAPI(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/status":
		if h.upFn != nil {
			jsonOK(w, h.upFn())
		} else {
			jsonOK(w, map[string]any{"ok": false, "message": "status 未初始化"})
		}
	case "/api/health":
		jsonOK(w, map[string]any{"ok": true})
	default:
		http.NotFound(w, r)
	}
}

// jsonOK 返回 JSON 200
func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// serveFS 从 fs.FS 服务单个文件
func serveFS(w http.ResponseWriter, r *http.Request, root fs.FS, name string) {
	if name == "" {
		name = "index.html"
	}
	data, err := fs.ReadFile(root, name)
	if err != nil {
		// 静态资源缺失 → 退回 index.html (SPA deep link)
		data, err = fs.ReadFile(root, "index.html")
		if err != nil {
			http.Error(w, "index.html 缺失", http.StatusInternalServerError)
			return
		}
		name = "index.html"
	}
	ct := contentTypeFor(name)
	w.Header().Set("Content-Type", ct)
	w.Write(data)
}

// serveIndex 服务 SPA 首页
func serveIndex(w http.ResponseWriter, root fs.FS) {
	data, err := fs.ReadFile(root, "index.html")
	if err != nil {
		http.Error(w, "index.html 缺失", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func contentTypeFor(name string) string {
	switch {
	case strings.HasSuffix(name, ".js"):
		return "application/javascript"
	case strings.HasSuffix(name, ".css"):
		return "text/css"
	case strings.HasSuffix(name, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".svg"):
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}

// LogServe helper
func LogServe(addr string) {
	log.Printf("面板: http://localhost%s", addr)
}
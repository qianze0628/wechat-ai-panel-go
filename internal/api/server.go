// Package api HTTP 路由层 (标准库 net/http, 轻量)
package api

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"
)

// ServiceController 服务控制接口 (由 process 实现, 隔离依赖)
type ServiceController interface {
	Start(name string) (bool, string)
	Stop(name string) (bool, string)
	Restart(name string) (bool, string)
	HealthCheck(name string) (bool, map[string]any)
	WaitHealth(name string, timeout time.Duration) bool
}

// Handler 聚合路由 (ServeMux 模式)
type Handler struct {
	webFS       fs.FS // 嵌入的前端静态资源 (dist)
	mux         *http.ServeMux
	upFn        func() any // 应用状态聚合
	svc         ServiceController
	authCheck   func(r *http.Request) bool
	setAuth     *AuthStore // 认证存储 (settings 热更新用)
	astrbotPort int        // AstrBot WebUI 端口 (供 /astrbot 跳转)
}

// New 创建 HTTP handler
func New(webFS fs.FS) *Handler {
	h := &Handler{webFS: webFS, mux: http.NewServeMux()}
	// 静态 + SPA
	h.mux.HandleFunc("/static/", func(w http.ResponseWriter, r *http.Request) {
		// 去掉 /static/ 前缀 (webSub 内路径是 assets/xxx)
		name := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/"), "static/")
		serveFS(w, r, h.webFS, name)
	})
	h.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// API 未匹配则 SPA
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		serveIndex(w, h.webFS)
	})
	// /astrbot 跳转 AstrBot WebUI (与 Python RedirectResponse 一致; 在 SPA 兜底前注册)
	h.mux.HandleFunc("/astrbot", func(w http.ResponseWriter, r *http.Request) {
		port := 6185 // 默认 webui 端口; 由 SetAstrbotWebUIPort 覆盖
		if h.astrbotPort > 0 {
			port = h.astrbotPort
		}
		http.Redirect(w, r, fmt.Sprintf("http://localhost:%d", port), http.StatusFound)
	})
	// 基础 API
	h.mux.HandleFunc("/api/status", h.handleStatus)
	// /api/services 服务运行状态 (与 Python service_status 一致; 复用 status 聚合的 services)
	h.mux.HandleFunc("/api/services", func(w http.ResponseWriter, r *http.Request) {
		if h.upFn == nil {
			jsonOK(w, map[string]any{"ok": false, "message": "status 未初始化"})
			return
		}
		st := h.upFn()
		m, ok := st.(map[string]any)
		if !ok {
			jsonOK(w, map[string]any{"ok": false, "message": "服务状态不可用"})
			return
		}
		if svc, ok2 := m["services"]; ok2 {
			jsonOK(w, svc)
			return
		}
		jsonOK(w, map[string]any{"ok": false, "message": "services 字段缺失"})
	})
	h.mux.HandleFunc("/api/start", h.handleServiceControl)
	h.mux.HandleFunc("/api/stop", h.handleServiceControl)
	h.mux.HandleFunc("/api/restart", h.handleServiceControl)
	// 插件列表 (Go 版内置插件元数据; 对应 Py 版 install/messages 插件,
	// 让前端侧边栏显示"插件"分区; Go 后端已实现对应 API)
	h.mux.HandleFunc("/api/plugins", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]any{"ok": true, "plugins": []any{
			map[string]any{
				"id": "install", "name": "依赖安装", "description": "多平台依赖安装引擎",
				"version": "1.0.0", "enabled": true,
				"nav": map[string]any{"to": "/plugin/install", "label": "依赖安装（插件）", "icon": "Puzzle"},
			},
			map[string]any{
				"id": "messages", "name": "消息记录", "description": "微信消息记录",
				"version": "1.0.0", "enabled": true,
				"nav": map[string]any{"to": "/plugin/messages", "label": "插件示例：消息记录", "icon": "Puzzle"},
			},
		}})
	})
	return h
}

// HandleFunc 注册 API 路由 (精确匹配)
func (h *Handler) HandleFunc(pattern string, fn http.HandlerFunc) {
	h.mux.HandleFunc(pattern, fn)
}

// SetStatusHandler 设置 /api/status 的聚合函数
func (h *Handler) SetStatusHandler(fn func() any) { h.upFn = fn }

// SetAstrbotWebUIPort 设置 AstrBot WebUI 端口 (供 /astrbot 跳转)
func (h *Handler) SetAstrbotWebUIPort(port int) { h.astrbotPort = port }

// SetServiceController 注入服务控制
func (h *Handler) SetServiceController(svc ServiceController) { h.svc = svc }

// Handler 实现 http.Handler
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// handleStatus /api/status
func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if h.upFn != nil {
		jsonOK(w, h.upFn())
	} else {
		jsonOK(w, map[string]any{"ok": false, "message": "status 未初始化"})
	}
}

// handleServiceControl /api/start|stop|restart?service=astrbot|wechat|qr|all
func (h *Handler) handleServiceControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, 405, "仅支持 POST")
		return
	}
	if h.authCheck != nil && !h.authCheck(r) {
		jsonErr(w, 401, "未认证或会话已过期")
		return
	}
	if h.svc == nil {
		jsonErr(w, 500, "服务控制器未初始化")
		return
	}
	svc := r.URL.Query().Get("service")
	if svc == "" {
		svc = "all"
	}
	action := ""
	switch r.URL.Path {
	case "/api/start":
		action = "start"
	case "/api/stop":
		action = "stop"
	case "/api/restart":
		action = "restart"
	}

	names := []string{}
	if svc == "all" {
		names = []string{"astrbot", "wechat", "qr"}
	} else {
		names = []string{svc}
	}

	messages := []string{}
	steps := []map[string]any{}
	failed := false
	for _, n := range names {
		var ok bool
		var msg string
		switch action {
		case "start":
			ok, msg = h.startOne(n)
		case "stop":
			ok, msg = h.svc.Stop(n)
		default:
			ok, msg = h.svc.Restart(n)
		}
		if !ok {
			failed = true
		}
		messages = append(messages, msg)
		steps = append(steps, map[string]any{"service": n, "ok": ok, "message": msg})
	}
	jsonOK(w, map[string]any{"ok": !failed, "message": strings.Join(messages, " | "), "steps": steps})
}

// startOne 启动单个服务 (all 模式下带健康门控)
func (h *Handler) startOne(name string) (bool, string) {
	ok, msg := h.svc.Start(name)
	if !ok {
		return false, msg
	}
	// 健康等待 (astrbot 60s, wechat 30s, qr 20s)
	timeout := 30
	switch name {
	case "astrbot":
		timeout = 60
	case "qr":
		timeout = 20
	}
	if !h.svc.WaitHealth(name, time.Duration(timeout)*time.Second) {
		return false, msg + " (健康检查超时)"
	}
	return true, msg + " (健康通过)"
}

// jsonErr 错误响应
func jsonErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "message": msg})
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